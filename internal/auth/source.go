package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// ErrReauthenticate reports that the OAuth grant is no longer valid and the
// user has to sign in again.
var ErrReauthenticate = errors.New("the Fastmail authorization has expired; run `fm-cli auth login`")

// Source hands out bearer tokens for a stored credential, refreshing OAuth
// access tokens under the cross-process lock and persisting the rotated
// refresh token. It implements oauth2.TokenSource.
type Source struct {
	store *Store
	hc    *http.Client

	mu    sync.Mutex
	creds *Credentials
}

// NewSource wraps a loaded credential. hc is used for refresh calls and must
// not itself authenticate.
func NewSource(store *Store, creds *Credentials, hc *http.Client) *Source {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Source{store: store, hc: hc, creds: creds}
}

// Credentials returns the current credential snapshot.
func (s *Source) Credentials() *Credentials {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *s.creds
	return &copy
}

// Token implements oauth2.TokenSource.
func (s *Source) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch s.creds.Kind {
	case KindToken:
		return &oauth2.Token{AccessToken: s.creds.APIToken, TokenType: "Bearer"}, nil
	case KindOAuth:
	default:
		return nil, ErrSignedOut
	}
	if s.creds.Revoked {
		return nil, ErrReauthenticate
	}
	if s.creds.AccessToken != "" && time.Until(s.creds.ExpiresAt) > refreshLeeway {
		return s.bearer(), nil
	}
	if err := s.refreshLocked(); err != nil {
		return nil, err
	}
	return s.bearer(), nil
}

func (s *Source) bearer() *oauth2.Token {
	return &oauth2.Token{AccessToken: s.creds.AccessToken, TokenType: "Bearer", Expiry: s.creds.ExpiresAt}
}

// refreshLocked refreshes the access token. The caller holds s.mu. Another
// process may have refreshed first, so the stored credential is re-read under
// the file lock before any network call.
func (s *Source) refreshLocked() error {
	if s.creds.RefreshToken == "" {
		return ErrReauthenticate
	}
	lock, err := acquireRefreshLock()
	if err != nil {
		return fmt.Errorf("could not take the refresh lock: %w", err)
	}
	defer lock.release()

	if s.creds.Storage == "keyring" {
		if fresh, err := s.store.Load(); err == nil && fresh.Kind == KindOAuth {
			if fresh.Revoked {
				s.creds = fresh
				return ErrReauthenticate
			}
			if fresh.AccessToken != "" && time.Until(fresh.ExpiresAt) > refreshLeeway && fresh.RefreshToken != s.creds.RefreshToken {
				s.creds = fresh
				return nil
			}
			if fresh.RefreshToken != "" {
				s.creds = fresh
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	endpoint := s.creds.TokenEndpoint
	if endpoint == "" {
		meta, err := Discover(ctx, s.hc, s.creds.Issuer)
		if err != nil {
			return err
		}
		endpoint = meta.TokenEndpoint
		s.creds.TokenEndpoint = endpoint
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", s.creds.RefreshToken)
	form.Set("client_id", s.creds.ClientID)
	form.Set("resource", ResourceFor(s.creds.Issuer))
	token, err := postToken(ctx, s.hc, endpoint, form)
	if err != nil {
		var te *tokenError
		if errors.As(err, &te) && te.permanent() {
			// The grant is gone: keep nothing usable in the keyring.
			s.creds.Revoked = true
			s.creds.AccessToken = ""
			s.creds.RefreshToken = ""
			s.creds.ExpiresAt = time.Time{}
			if s.creds.Storage == "keyring" {
				_ = s.store.Save(s.creds)
			}
			return ErrReauthenticate
		}
		return err
	}
	s.creds.AccessToken = token.AccessToken
	s.creds.ExpiresAt = token.expiresAt()
	if token.RefreshToken != "" {
		s.creds.RefreshToken = token.RefreshToken
	}
	if token.Scope != "" {
		s.creds.Scope = token.Scope
	}
	if s.creds.Storage == "keyring" {
		// The rotated refresh token must land in the keyring: the old one is
		// spent, and replaying it later would revoke the grant.
		if err := s.store.Save(s.creds); err != nil {
			time.Sleep(200 * time.Millisecond)
			if err2 := s.store.Save(s.creds); err2 != nil {
				return fmt.Errorf("the refreshed token could not be stored in the keyring (%w); run `fm-cli auth login` again", err2)
			}
		}
	}
	return nil
}

// HTTPClient returns an http.Client that authenticates every request with the
// source. It has no overall timeout, so callers bound requests with contexts;
// the EventSource connection relies on that.
func (s *Source) HTTPClient() *http.Client {
	return &http.Client{
		Transport: &oauth2.Transport{Source: s, Base: http.DefaultTransport},
	}
}
