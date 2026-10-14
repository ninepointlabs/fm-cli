package api

import (
	"testing"

	"git.sr.ht/~rockorager/go-jmap/mail/identity"
)

// Cases adapted from fsnoah's FM_DEFAULT_FROM patch (issue #2).
func TestMatchIdentity(t *testing.T) {
	ids := []*identity.Identity{
		{ID: "0", Email: "noah.hopping@example.com"},
		nil,
		{ID: "3", Email: "noah@example.com"},
	}
	for _, tc := range []struct{ addr, id string }{
		{"  Noah@Example.com ", "3"},
		{"noah.hopping@example.com", "0"},
		{"nobody@example.com", ""},
		{"", ""},
		{"   ", ""},
	} {
		got := MatchIdentity(ids, tc.addr)
		if (got == nil && tc.id != "") || (got != nil && string(got.ID) != tc.id) {
			t.Errorf("%q -> %+v, want %q", tc.addr, got, tc.id)
		}
	}
}

func TestPreferredIdentity(t *testing.T) {
	ids := []*identity.Identity{
		{ID: "0", Email: "first@example.com"},
		{ID: "1", Email: "setting@example.com"},
		{ID: "2", Email: "env@example.com"},
	}
	for _, tc := range []struct {
		name, env, setting, id string
	}{
		{"unset falls back to first", "", "", "0"},
		{"setting picks identity", "", "Setting@example.com", "1"},
		{"env wins over setting", "env@example.com", "setting@example.com", "2"},
		{"unmatched env falls back to first, not setting", "nobody@example.com", "setting@example.com", "0"},
		{"blank env defers to setting", "  ", "setting@example.com", "1"},
		{"unmatched setting falls back to first", "", "nobody@example.com", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(DefaultFromEnv, tc.env)
			c := &Client{DefaultFrom: tc.setting}
			if got := c.PreferredIdentity(ids); got == nil || string(got.ID) != tc.id {
				t.Errorf("got %+v, want %q", got, tc.id)
			}
		})
	}

	t.Run("no identities", func(t *testing.T) {
		t.Setenv(DefaultFromEnv, "env@example.com")
		if got := (&Client{}).PreferredIdentity(nil); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})
}
