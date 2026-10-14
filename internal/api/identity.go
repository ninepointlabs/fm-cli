package api

import (
	"os"
	"strings"

	"git.sr.ht/~rockorager/go-jmap/mail/identity"
)

// DefaultFromEnv names the environment variable that picks the default
// sending identity. It wins over the stored `settings from` value.
const DefaultFromEnv = "FM_DEFAULT_FROM"

// MatchIdentity returns the identity whose email equals addr, trimmed and
// compared case-insensitively, or nil when addr is empty or matches none.
func MatchIdentity(identities []*identity.Identity, addr string) *identity.Identity {
	want := strings.TrimSpace(addr)
	if want == "" {
		return nil
	}
	for _, id := range identities {
		if id != nil && strings.EqualFold(strings.TrimSpace(id.Email), want) {
			return id
		}
	}
	return nil
}

// defaultFrom returns the configured default sending address: FM_DEFAULT_FROM
// when set, otherwise the client's DefaultFrom setting.
func (c *Client) defaultFrom() string {
	if v := strings.TrimSpace(os.Getenv(DefaultFromEnv)); v != "" {
		return v
	}
	return strings.TrimSpace(c.DefaultFrom)
}

// PreferredIdentity returns the identity named by the configured default
// address, falling back to the first identity. Fastmail's web-UI default
// does not reorder Identity/get, so the first one is only a guess. It returns
// nil when identities is empty.
func (c *Client) PreferredIdentity(identities []*identity.Identity) *identity.Identity {
	if id := MatchIdentity(identities, c.defaultFrom()); id != nil {
		return id
	}
	if len(identities) == 0 {
		return nil
	}
	return identities[0]
}
