package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"git.sr.ht/~rockorager/go-jmap"
	"git.sr.ht/~rockorager/go-jmap/mail"
)

// ErrUnauthorized reports that Fastmail rejected the credential.
var ErrUnauthorized = errors.New("Fastmail rejected the credential")

// SessionURL is Fastmail's JMAP session resource.
const SessionURL = "https://api.fastmail.com/jmap/session"

// NewClientWithHTTP builds a JMAP client over an http.Client that already
// authenticates its requests, and fetches the session. A 401 becomes
// ErrUnauthorized; other failures are returned as they are.
func NewClientWithHTTP(ctx context.Context, hc *http.Client, sessionURL string) (*Client, error) {
	if sessionURL == "" {
		sessionURL = SessionURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sessionURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach Fastmail: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrUnauthorized
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("Fastmail session request failed (HTTP %d)", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	session := &jmap.Session{}
	if err := json.Unmarshal(data, session); err != nil {
		return nil, fmt.Errorf("unreadable JMAP session: %w", err)
	}
	for id, account := range session.Accounts {
		account.ID = string(id)
		session.Accounts[id] = account
	}
	c := &jmap.Client{SessionEndpoint: sessionURL, HttpClient: hc, Session: session}
	return &Client{Client: c, Session: session}, nil
}

// AccountInfo is one JMAP account the credential can read mail in.
type AccountInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Active   bool   `json:"active"`
	Personal bool   `json:"personal"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// MailAccounts lists accounts with the mail capability, primary first.
func (c *Client) MailAccounts() []AccountInfo {
	if c.Session == nil {
		return nil
	}
	primary := string(c.Session.PrimaryAccounts[mail.URI])
	var out []AccountInfo
	for id, account := range c.Session.Accounts {
		if _, ok := account.RawCapabilities[mail.URI]; !ok {
			continue
		}
		info := AccountInfo{
			ID:       string(id),
			Name:     account.Name,
			Email:    account.Name,
			Active:   string(id) == primary,
			Personal: account.IsPersonal,
			ReadOnly: account.IsReadOnly,
		}
		if info.Active && c.Session.Username != "" {
			info.Email = c.Session.Username
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Username is the session's signed-in address.
func (c *Client) Username() string {
	if c.Session == nil {
		return ""
	}
	return c.Session.Username
}

// do runs a request with a timeout and maps HTTP-level auth failures.
func (c *Client) do(ctx context.Context, req *jmap.Request) (*jmap.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
	}
	req.Context = ctx
	resp, err := c.Client.Do(req)
	if err != nil {
		var re *jmap.RequestError
		if errors.As(err, &re) && (re.Status == http.StatusUnauthorized || re.Status == http.StatusForbidden) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	return resp, nil
}

// responseFor finds the invocation answering callID, or the method error it
// produced.
func responseFor(resp *jmap.Response, callID string) (any, error) {
	for _, inv := range resp.Responses {
		if inv.CallID != callID {
			continue
		}
		if me, ok := inv.Args.(*jmap.MethodError); ok {
			return nil, me
		}
		return inv.Args, nil
	}
	return nil, fmt.Errorf("the server did not answer call %s", callID)
}
