package auth

import (
	"errors"
	"testing"

	"github.com/99designs/keyring"
)

func TestNoKeyring(t *testing.T) {
	t.Setenv("FM_API_TOKEN", "")
	s := &Store{open: func() (keyring.Keyring, error) { return nil, keyring.ErrNoAvailImpl }}
	if _, err := s.Load(); !errors.Is(err, ErrKeyringUnavailable) {
		t.Errorf("Load: got %v", err)
	}
	if err := s.Clear(); !errors.Is(err, ErrKeyringUnavailable) {
		t.Errorf("Clear: got %v", err)
	}
	t.Setenv("FM_API_TOKEN", "fake")
	if creds, err := s.Load(); err != nil || creds.Storage != "env" {
		t.Errorf("Load with FM_API_TOKEN: got %+v, %v", creds, err)
	}
}
