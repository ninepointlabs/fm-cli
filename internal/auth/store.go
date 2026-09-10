// Package auth holds the credential store and the OAuth flow. Credentials live
// in the system keyring under one JSON blob; an API token from the environment
// or the pre-0.3 keyring entry is still honored so existing installs keep
// working.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/99designs/keyring"
)

const (
	ServiceName = "fm-cli"

	credentialsKey = "fastmail-credentials"
	legacyTokenKey = "fastmail-api-token"
	appPasswordKey = "fastmail-app-password"
	emailKey       = "fastmail-email"

	// DefaultIssuer is Fastmail's OAuth authorization server and JMAP host.
	DefaultIssuer = "https://api.fastmail.com"
)

// ErrSignedOut reports that no usable credential is stored.
var ErrSignedOut = errors.New("not signed in")

// Kind is how a credential authenticates.
type Kind string

const (
	KindNone  Kind = "none"
	KindOAuth Kind = "oauth"
	KindToken Kind = "token"
)

// Credentials is the stored credential blob. Exactly one of APIToken or the
// OAuth fields is populated, according to Kind.
type Credentials struct {
	Kind Kind `json:"kind"`

	APIToken string `json:"api_token,omitempty"`

	AccessToken   string    `json:"access_token,omitempty"`
	RefreshToken  string    `json:"refresh_token,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	ClientID      string    `json:"client_id,omitempty"`
	TokenEndpoint string    `json:"token_endpoint,omitempty"`
	Scope         string    `json:"scope,omitempty"`
	// Revoked is set when a refresh was refused for good (invalid_grant), so
	// `auth status` can say signed out without a network round trip.
	Revoked bool `json:"revoked,omitempty"`

	Issuer   string `json:"issuer,omitempty"`
	Username string `json:"username,omitempty"`

	// Storage says where the credential was read from: "keyring" or "env".
	Storage string `json:"-"`
}

// Authenticated says whether the credential can be used as it stands, without
// contacting the server.
func (c *Credentials) Authenticated() bool {
	if c == nil {
		return false
	}
	switch c.Kind {
	case KindToken:
		return c.APIToken != ""
	case KindOAuth:
		if c.Revoked {
			return false
		}
		if c.RefreshToken != "" {
			return true
		}
		return c.AccessToken != "" && time.Now().Before(c.ExpiresAt)
	}
	return false
}

// Store reads and writes credentials.
type Store struct {
	open func() (keyring.Keyring, error)
}

// NewStore returns the default keyring-backed store.
func NewStore() *Store {
	return &Store{open: openKeyring}
}

func openKeyring() (keyring.Keyring, error) {
	return keyring.Open(keyring.Config{
		ServiceName: ServiceName,
		// Backends that prompt on a terminal (file, pass) would hang a shell
		// plugin, so only the desktop secret stores are allowed.
		AllowedBackends: []keyring.BackendType{
			keyring.SecretServiceBackend,
			keyring.KWalletBackend,
			keyring.KeychainBackend,
			keyring.WinCredBackend,
		},
		LibSecretCollectionName: "login",
	})
}

// Load returns the stored credential, an environment token when FM_API_TOKEN
// is set and nothing is stored, or ErrSignedOut.
func (s *Store) Load() (*Credentials, error) {
	ring, err := s.open()
	if err != nil {
		if token := strings.TrimSpace(os.Getenv("FM_API_TOKEN")); token != "" {
			return &Credentials{Kind: KindToken, APIToken: token, Issuer: DefaultIssuer, Storage: "env"}, nil
		}
		return nil, fmt.Errorf("keyring unavailable: %w", err)
	}

	if item, err := ring.Get(credentialsKey); err == nil {
		var creds Credentials
		if err := json.Unmarshal(item.Data, &creds); err != nil {
			return nil, fmt.Errorf("stored credentials are unreadable: %w", err)
		}
		if creds.Issuer == "" {
			creds.Issuer = DefaultIssuer
		}
		creds.Storage = "keyring"
		return &creds, nil
	}

	if item, err := ring.Get(legacyTokenKey); err == nil {
		token := strings.TrimSpace(string(item.Data))
		if token != "" {
			creds := &Credentials{Kind: KindToken, APIToken: token, Issuer: DefaultIssuer, Storage: "keyring"}
			if emailItem, err := ring.Get(emailKey); err == nil {
				creds.Username = strings.TrimSpace(string(emailItem.Data))
			}
			return creds, nil
		}
	}

	if token := strings.TrimSpace(os.Getenv("FM_API_TOKEN")); token != "" {
		return &Credentials{Kind: KindToken, APIToken: token, Issuer: DefaultIssuer, Storage: "env"}, nil
	}
	return nil, ErrSignedOut
}

// Save writes the credential blob, replacing any earlier one and the legacy
// token entry.
func (s *Store) Save(creds *Credentials) error {
	ring, err := s.open()
	if err != nil {
		return fmt.Errorf("keyring unavailable: %w", err)
	}
	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	if err := ring.Set(keyring.Item{Key: credentialsKey, Data: data, Label: "fm-cli Fastmail credentials"}); err != nil {
		return fmt.Errorf("could not store credentials: %w", err)
	}
	_ = ring.Remove(legacyTokenKey)
	return nil
}

// Clear removes the mail credential. The DAV app password and email address
// are left alone; `auth dav --remove` clears those.
func (s *Store) Clear() error {
	ring, err := s.open()
	if err != nil {
		return fmt.Errorf("keyring unavailable: %w", err)
	}
	var first error
	for _, key := range []string{credentialsKey, legacyTokenKey} {
		if err := ring.Remove(key); err != nil && first == nil && !errors.Is(err, keyring.ErrKeyNotFound) {
			first = err
		}
	}
	return first
}

// DAVCredentials returns the app password and email used for CalDAV/CardDAV,
// from the keyring or the FM_APP_PASSWORD / FM_EMAIL environment.
func (s *Store) DAVCredentials() (appPassword, email string) {
	email = strings.TrimSpace(os.Getenv("FM_EMAIL"))
	appPassword = strings.TrimSpace(os.Getenv("FM_APP_PASSWORD"))
	ring, err := s.open()
	if err != nil {
		return appPassword, email
	}
	if item, err := ring.Get(emailKey); err == nil {
		email = strings.TrimSpace(string(item.Data))
	}
	if item, err := ring.Get(appPasswordKey); err == nil {
		appPassword = strings.TrimSpace(string(item.Data))
	}
	return appPassword, email
}

// SaveDAVCredentials stores the app password and email for CalDAV/CardDAV.
func (s *Store) SaveDAVCredentials(appPassword, email string) error {
	ring, err := s.open()
	if err != nil {
		return fmt.Errorf("keyring unavailable: %w", err)
	}
	if err := ring.Set(keyring.Item{Key: emailKey, Data: []byte(email), Label: "fm-cli Fastmail email"}); err != nil {
		return err
	}
	if appPassword == "" {
		return nil
	}
	return ring.Set(keyring.Item{Key: appPasswordKey, Data: []byte(appPassword), Label: "fm-cli Fastmail app password"})
}

// ClearDAVCredentials removes the app password and email.
func (s *Store) ClearDAVCredentials() error {
	ring, err := s.open()
	if err != nil {
		return fmt.Errorf("keyring unavailable: %w", err)
	}
	_ = ring.Remove(appPasswordKey)
	_ = ring.Remove(emailKey)
	return nil
}
