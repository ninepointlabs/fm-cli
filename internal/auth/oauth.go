package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// Scope is what fm-cli asks Fastmail for: JMAP mail plus a refresh token.
const Scope = "urn:ietf:params:oauth:scope:mail offline_access"

// ResourceFor is the RFC 8707 resource indicator Fastmail requires on the
// authorization and token requests: the JMAP session URL. Without it the
// authorize endpoint bounces straight back with invalid_target.
func ResourceFor(issuer string) string {
	return strings.TrimRight(issuer, "/") + "/jmap/session"
}

const (
	clientName = "fm-cli"
	clientURI  = "https://github.com/ninepointlabs/fm-cli"
	// loginTimeout bounds how long the loopback listener waits for the
	// browser to come back.
	loginTimeout = 5 * time.Minute
	// refreshLeeway refreshes an access token this long before it expires.
	refreshLeeway = 90 * time.Second
)

// ServerMetadata is the subset of RFC 8414 authorization-server metadata used.
type ServerMetadata struct {
	Issuer                string `json:"issuer"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
}

// Discover fetches the authorization server's metadata.
func Discover(ctx context.Context, hc *http.Client, issuer string) (*ServerMetadata, error) {
	endpoint := strings.TrimRight(issuer, "/") + "/.well-known/oauth-authorization-server"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", issuer, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s does not publish OAuth metadata (HTTP %d)", issuer, resp.StatusCode)
	}
	var meta ServerMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta); err != nil {
		return nil, fmt.Errorf("unreadable OAuth metadata: %w", err)
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return nil, errors.New("OAuth metadata is missing its endpoints")
	}
	return &meta, nil
}

// RegisterClient performs dynamic client registration (RFC 7591) for a public
// native client with loopback redirects and returns the client id.
func RegisterClient(ctx context.Context, hc *http.Client, meta *ServerMetadata) (string, error) {
	if meta.RegistrationEndpoint == "" {
		return "", errors.New("the server does not offer client registration")
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                clientName,
		"client_uri":                 clientURI,
		"software_id":                clientName,
		"redirect_uris":              []string{"http://127.0.0.1/callback", "http://localhost/callback"},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"application_type":           "native",
		"scope":                      Scope,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RegistrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("client registration failed: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("client registration refused (HTTP %d): %s", resp.StatusCode, oauthErrorText(data))
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.ClientID == "" {
		return "", errors.New("client registration returned no client id")
	}
	return out.ClientID, nil
}

// LoginOptions tunes the browser flow.
type LoginOptions struct {
	// NoBrowser prints the URL instead of launching a browser.
	NoBrowser bool
	// Out receives progress text for the user; nil discards it.
	Out io.Writer
	// ClientID reuses a registration; empty registers a new client.
	ClientID string
}

// Login runs the authorization-code flow with PKCE against the issuer and
// returns OAuth credentials (without Username; the caller fills that from the
// JMAP session).
func Login(ctx context.Context, hc *http.Client, issuer string, opts LoginOptions) (*Credentials, error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	meta, err := Discover(ctx, hc, issuer)
	if err != nil {
		return nil, err
	}
	clientID := opts.ClientID
	if clientID == "" {
		clientID, err = RegisterClient(ctx, hc, meta)
		if err != nil {
			return nil, err
		}
	}

	verifier, challenge, err := pkcePair()
	if err != nil {
		return nil, err
	}
	state, err := randomToken(24)
	if err != nil {
		return nil, err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("could not open a loopback port: %w", err)
	}
	defer listener.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)

	authorize, _ := url.Parse(meta.AuthorizationEndpoint)
	q := authorize.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", Scope)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("resource", ResourceFor(issuer))
	authorize.RawQuery = q.Encode()

	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	// The first genuine result wins; anything after it is dropped rather
	// than blocking a handler goroutine forever.
	deliver := func(r result) {
		select {
		case results <- r:
		default:
		}
	}
	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			params := r.URL.Query()
			// A request that does not carry this attempt's state is not the
			// browser's redirect — a stray page probing the port, say — so it
			// is refused and the flow keeps waiting for the real one.
			if params.Get("state") != state {
				writePage(w, http.StatusBadRequest, "Sign-in failed", "The response did not match this sign-in attempt. Return to the terminal and try again.")
				return
			}
			if e := params.Get("error"); e != "" {
				desc := cleanLine(params.Get("error_description"))
				writePage(w, http.StatusBadRequest, "Sign-in cancelled", "Fastmail reported: "+cleanLine(e)+". You can close this tab.")
				if desc == "" {
					desc = cleanLine(e)
				}
				deliver(result{err: fmt.Errorf("authorization refused: %s", desc)})
				return
			}
			code := params.Get("code")
			if code == "" {
				writePage(w, http.StatusBadRequest, "Sign-in failed", "No authorization code was returned. Return to the terminal and try again.")
				deliver(result{err: errors.New("the authorization response carried no code")})
				return
			}
			writePage(w, http.StatusOK, "fm-cli is signed in", "You can close this tab and return to the terminal.")
			deliver(result{code: code})
		}),
	}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Fprintln(out, "Sign in to Fastmail in your browser to authorize fm-cli.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, authorize.String())
	fmt.Fprintln(out)
	if !opts.NoBrowser {
		if err := openBrowser(authorize.String()); err != nil {
			fmt.Fprintln(out, "Could not open a browser automatically; open the link above by hand.")
		} else {
			fmt.Fprintln(out, "Waiting for the browser…")
		}
	}

	timer := time.NewTimer(loginTimeout)
	defer timer.Stop()
	var code string
	select {
	case res := <-results:
		if res.err != nil {
			return nil, res.err
		}
		code = res.code
	case <-timer.C:
		return nil, errors.New("timed out waiting for the browser; run the command again")
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	form.Set("resource", ResourceFor(issuer))
	token, err := postToken(ctx, hc, meta.TokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	creds := &Credentials{
		Kind:          KindOAuth,
		AccessToken:   token.AccessToken,
		RefreshToken:  token.RefreshToken,
		ExpiresAt:     token.expiresAt(),
		ClientID:      clientID,
		TokenEndpoint: meta.TokenEndpoint,
		Scope:         token.Scope,
		Issuer:        strings.TrimRight(issuer, "/"),
	}
	if creds.Scope == "" {
		creds.Scope = Scope
	}
	return creds, nil
}

// Revoke tells the server to drop the grant. Failures are reported but the
// caller clears the local credential regardless.
func Revoke(ctx context.Context, hc *http.Client, creds *Credentials) error {
	if creds == nil || creds.Kind != KindOAuth || creds.RefreshToken == "" {
		return nil
	}
	meta, err := Discover(ctx, hc, creds.Issuer)
	if err != nil || meta.RevocationEndpoint == "" {
		return err
	}
	form := url.Values{}
	form.Set("token", creds.RefreshToken)
	form.Set("token_type_hint", "refresh_token")
	form.Set("client_id", creds.ClientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

func (t *tokenResponse) expiresAt() time.Time {
	if t.ExpiresIn <= 0 {
		// No expiry given: assume an hour so the refresh path still runs.
		return time.Now().Add(time.Hour)
	}
	return time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
}

// tokenError is an RFC 6749 error response from the token endpoint.
type tokenError struct {
	Code        string
	Description string
	Status      int
}

func (e *tokenError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Description)
	}
	return e.Code
}

// permanent says the grant is gone for good and a new sign-in is needed.
func (e *tokenError) permanent() bool {
	switch e.Code {
	case "invalid_grant", "invalid_client", "unauthorized_client":
		return true
	}
	return false
}

func postToken(ctx context.Context, hc *http.Client, endpoint string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(data, &body)
		if body.Error == "" {
			body.Error = fmt.Sprintf("http_%d", resp.StatusCode)
		}
		return nil, &tokenError{Code: body.Error, Description: body.Description, Status: resp.StatusCode}
	}
	var token tokenResponse
	if err := json.Unmarshal(data, &token); err != nil || token.AccessToken == "" {
		return nil, errors.New("the token response carried no access token")
	}
	return &token, nil
}

func oauthErrorText(data []byte) string {
	var body struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(data, &body) == nil && body.Error != "" {
		if body.Description != "" {
			return body.Error + ": " + body.Description
		}
		return body.Error
	}
	text := strings.TrimSpace(string(data))
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}

func pkcePair() (verifier, challenge string, err error) {
	verifier, err = randomToken(48)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func openBrowser(target string) error {
	for _, candidate := range [][]string{{"xdg-open", target}, {"open", target}} {
		if _, err := exec.LookPath(candidate[0]); err != nil {
			continue
		}
		cmd := exec.Command(candidate[0], candidate[1:]...)
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	return errors.New("no browser opener found")
}

// cleanLine keeps server-supplied text out of the terminal's control set.
func cleanLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if len(out) > 200 {
		out = out[:200] + "…"
	}
	return out
}

func writePage(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>%s</title>
<style>body{font-family:system-ui,sans-serif;background:#111;color:#eee;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
main{max-width:28rem;padding:2rem;text-align:center}h1{font-size:1.4rem;margin:0 0 .5rem}p{opacity:.8;margin:0}</style></head>
<body><main><h1>%s</h1><p>%s</p></main></body></html>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(message))
}
