package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"fm-cli/internal/api"
	"fm-cli/internal/auth"

	"golang.org/x/term"
)

func (app *App) authStatus(ctx context.Context) (*Result, error) {
	data := map[string]any{
		"authenticated":     false,
		"auth_type":         string(auth.KindNone),
		"username":          "",
		"expires_at":        nil,
		"refresh_available": false,
		"storage":           "keyring",
		"base_url":          auth.DefaultIssuer,
	}
	creds, err := app.Store.Load()
	if err != nil {
		if errors.Is(err, auth.ErrSignedOut) {
			return &Result{Data: data, Summary: "Not signed in", Text: "Not signed in. Run `fm-cli auth login`."}, nil
		}
		return nil, err
	}
	data["auth_type"] = string(creds.Kind)
	data["username"] = creds.Username
	data["storage"] = creds.Storage
	if creds.Issuer != "" {
		data["base_url"] = creds.Issuer
	}
	if creds.Kind == auth.KindOAuth {
		data["refresh_available"] = creds.RefreshToken != "" && !creds.Revoked
		if !creds.ExpiresAt.IsZero() {
			data["expires_at"] = creds.ExpiresAt.UTC().Format(time.RFC3339)
		}
	}
	authenticated := creds.Authenticated()
	data["authenticated"] = authenticated
	summary := "Not signed in"
	if authenticated {
		summary = "Signed in"
		if creds.Username != "" {
			summary += " as " + creds.Username
		}
		if creds.Kind == auth.KindToken {
			summary += " (API token)"
		}
	} else if creds.Kind == auth.KindOAuth && creds.Revoked {
		summary = "Signed out: the Fastmail authorization was revoked"
	}
	return &Result{Data: data, Summary: summary, Text: summary}, nil
}

func (app *App) authLogin(ctx context.Context, a *Args) (*Result, error) {
	var creds *auth.Credentials
	if a.Token {
		token, err := app.readSecret("Fastmail API token: ")
		if err != nil {
			return nil, err
		}
		if token == "" {
			return nil, usageError("no API token given")
		}
		creds = &auth.Credentials{Kind: auth.KindToken, APIToken: token, Issuer: auth.DefaultIssuer}
	} else {
		previous, _ := app.Store.Load()
		opts := auth.LoginOptions{NoBrowser: a.NoBrowser, Out: app.Stderr}
		if previous != nil && previous.Kind == auth.KindOAuth && previous.ClientID != "" && previous.Issuer == auth.DefaultIssuer {
			opts.ClientID = previous.ClientID
		}
		var err error
		creds, err = auth.Login(ctx, app.Plain, auth.DefaultIssuer, opts)
		if err != nil {
			var te interface{ Error() string }
			_ = te
			// A stale cached client id makes the server refuse the exchange;
			// register a fresh client once and retry.
			if opts.ClientID != "" && strings.Contains(err.Error(), "invalid_client") {
				opts.ClientID = ""
				creds, err = auth.Login(ctx, app.Plain, auth.DefaultIssuer, opts)
			}
			if err != nil {
				return nil, err
			}
		}
	}
	creds.Storage = "keyring"

	// Verify the credential and learn the username before storing it.
	source := auth.NewSource(app.Store, creds, app.Plain)
	verifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := api.NewClientWithHTTP(verifyCtx, source.HTTPClient(), strings.TrimRight(creds.Issuer, "/")+"/jmap/session")
	if err != nil {
		if errors.Is(err, api.ErrUnauthorized) {
			return nil, &Error{Code: "auth", Message: "Fastmail did not accept the credential", Hint: "For API tokens, the token needs at least the Email scope."}
		}
		return nil, err
	}
	creds = source.Credentials()
	creds.Username = client.Username()
	creds.Storage = "keyring"
	if err := app.Store.Save(creds); err != nil {
		return nil, err
	}
	summary := "Signed in as " + creds.Username
	if a.SilentSuccess {
		return &Result{Data: map[string]any{"username": creds.Username, "auth_type": string(creds.Kind)}, Summary: summary, Text: ""}, nil
	}
	return &Result{Data: map[string]any{"username": creds.Username, "auth_type": string(creds.Kind)}, Summary: summary, Text: summary}, nil
}

// authToken prints a live bearer token, refreshing first when needed.
func (app *App) authToken(ctx context.Context) (*Result, error) {
	creds, err := app.Store.Load()
	if err != nil {
		return nil, err
	}
	if !creds.Authenticated() {
		return nil, auth.ErrSignedOut
	}
	source := auth.NewSource(app.Store, creds, app.Plain)
	token, err := source.Token()
	if err != nil {
		return nil, err
	}
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(app.Stderr, "note: this bearer token reads and changes your mail until it expires; keep it out of shell history and logs.")
	}
	return &Result{Data: map[string]any{"token": token.AccessToken, "expires_at": token.Expiry}, Text: token.AccessToken}, nil
}

func (app *App) authLogout(ctx context.Context) (*Result, error) {
	creds, err := app.Store.Load()
	if err != nil && !errors.Is(err, auth.ErrSignedOut) {
		return nil, err
	}
	if creds != nil && creds.Kind == auth.KindOAuth {
		revokeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := auth.Revoke(revokeCtx, app.Plain, creds); err != nil {
			fmt.Fprintf(app.Stderr, "note: could not revoke the grant at Fastmail: %v\n", err)
		}
	}
	if err := app.Store.Clear(); err != nil {
		return nil, err
	}
	return &Result{Data: map[string]any{"signed_out": true}, Summary: "Signed out", Text: "Signed out. The credential was removed from the keyring."}, nil
}

func (app *App) authDAV(ctx context.Context, a *Args) (*Result, error) {
	if a.Remove {
		if err := app.Store.ClearDAVCredentials(); err != nil {
			return nil, err
		}
		return &Result{Data: map[string]any{"removed": true}, Summary: "App password removed", Text: "App password removed."}, nil
	}
	fmt.Fprintln(app.Stderr, "Calendar and contacts use CalDAV/CardDAV, which needs a Fastmail app password")
	fmt.Fprintln(app.Stderr, "(Settings > Privacy & Security > Integrations > App passwords, with Mail, Contacts & Calendars access).")
	email, err := app.readLine("Fastmail email address: ")
	if err != nil {
		return nil, err
	}
	password, err := app.readSecret("App password: ")
	if err != nil {
		return nil, err
	}
	if email == "" || password == "" {
		return nil, usageError("both the email address and the app password are needed")
	}
	if err := app.Store.SaveDAVCredentials(password, email); err != nil {
		return nil, err
	}
	return &Result{Data: map[string]any{"email": email}, Summary: "App password stored", Text: "App password stored for " + email + "."}, nil
}

// setup is the plugin's guided path: sign in only when signed out.
func (app *App) setup(ctx context.Context, a *Args) (*Result, error) {
	creds, err := app.Store.Load()
	if err == nil && creds.Authenticated() {
		if a.SilentSuccess {
			return &Result{Data: map[string]any{"username": creds.Username}, Summary: "Already signed in"}, nil
		}
		summary := "Already signed in"
		if creds.Username != "" {
			summary += " as " + creds.Username
		}
		return &Result{Data: map[string]any{"username": creds.Username}, Summary: summary, Text: summary}, nil
	}
	if err != nil && !errors.Is(err, auth.ErrSignedOut) {
		return nil, err
	}
	result, err := app.authLogin(ctx, a)
	if err != nil {
		return nil, err
	}
	if a.SilentSuccess {
		result.Text = ""
	}
	return result, nil
}

func (app *App) accountList(ctx context.Context) (*Result, error) {
	client, _, err := app.connect(ctx)
	if err != nil {
		return nil, err
	}
	accounts := client.MailAccounts()
	var text strings.Builder
	for _, account := range accounts {
		marker := " "
		if account.Active {
			marker = "*"
		}
		fmt.Fprintf(&text, "%s %s  %s\n", marker, account.ID, account.Email)
	}
	return &Result{Data: accounts, Summary: fmt.Sprintf("%d mail account%s", len(accounts), plural(len(accounts))), Text: text.String()}, nil
}

func (app *App) boxList(ctx context.Context, a *Args) (*Result, error) {
	client, _, err := app.connect(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := selectAccounts(client, a.Account)
	if err != nil {
		return nil, err
	}
	var all []api.Box
	for _, account := range accounts {
		boxes, err := client.Mailboxes(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		all = append(all, boxes...)
	}
	var text strings.Builder
	for _, box := range all {
		kind := box.Kind
		if kind == "" {
			kind = "-"
		}
		fmt.Fprintf(&text, "%-8s %-10s %4d unread  %s\n", box.ID, kind, box.UnreadCount, box.Name)
	}
	return &Result{Data: all, Summary: fmt.Sprintf("%d mailbox%s", len(all), pluralEs(len(all))), Text: text.String()}, nil
}

func (app *App) boxView(ctx context.Context, a *Args, selector string) (*Result, error) {
	client, _, err := app.connect(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := selectAccounts(client, a.Account)
	if err != nil {
		return nil, err
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > api.MaxPostings {
		limit = api.MaxPostings
	}
	wantAll := strings.EqualFold(strings.TrimSpace(selector), "all")

	var views []*api.BoxView
	for _, account := range accounts {
		boxes, err := client.Mailboxes(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		var view *api.BoxView
		if wantAll {
			view, err = client.ViewAll(ctx, account.ID, boxes, limit, a.Excludes)
			if err != nil {
				return nil, err
			}
		} else {
			box, ok := api.FindBox(boxes, selector)
			if !ok {
				if len(accounts) > 1 {
					continue
				}
				return nil, &Error{Code: "usage", Message: fmt.Sprintf("no mailbox matches %q", selector)}
			}
			view, err = client.ViewBox(ctx, box, limit)
			if err != nil {
				return nil, err
			}
		}
		views = append(views, view)
	}
	if len(views) == 0 {
		return nil, &Error{Code: "usage", Message: fmt.Sprintf("no mailbox matches %q", selector)}
	}

	merged := *views[0]
	if len(views) > 1 {
		// Several accounts: one merged view keyed on the first, newest first.
		merged.Postings = nil
		merged.Folders = nil
		merged.UnreadCount = 0
		for _, v := range views {
			merged.Postings = append(merged.Postings, v.Postings...)
			merged.Folders = append(merged.Folders, v.Folders...)
			merged.UnreadCount += v.UnreadCount
		}
		sortPostings(merged.Postings)
		if len(merged.Postings) > limit {
			merged.Postings = merged.Postings[:limit]
		}
		merged.AccountID = "all"
	}

	var summary string
	switch {
	case wantAll && len(views) > 1:
		summary = fmt.Sprintf("%d unread across %d folders in %d accounts", merged.UnreadCount, len(merged.Folders), len(views))
	case wantAll:
		summary = fmt.Sprintf("%d unread across %d folder%s", merged.UnreadCount, len(merged.Folders), plural(len(merged.Folders)))
	case len(views) > 1:
		summary = fmt.Sprintf("%d unread in %s across %d accounts", merged.UnreadCount, merged.Name, len(views))
	default:
		summary = fmt.Sprintf("%d unread in %s", merged.UnreadCount, merged.Name)
	}

	var text strings.Builder
	for _, p := range merged.Postings {
		mark := " "
		if !p.Seen {
			mark = "•"
		}
		when := p.ActiveAt
		if t, err := time.Parse(time.RFC3339, p.ActiveAt); err == nil {
			when = t.Local().Format("Jan 02 15:04")
		}
		folder := ""
		if wantAll && p.BoxKind != "inbox" {
			folder = "  [" + p.BoxName + "]"
		}
		fmt.Fprintf(&text, "%s %s  %-24.24s  %s%s\n", mark, when, p.Creator.Name, p.Name, folder)
	}
	return &Result{Data: merged, Summary: summary, Text: text.String()}, nil
}

func sortPostings(postings []api.Posting) {
	for i := 1; i < len(postings); i++ {
		for j := i; j > 0 && postings[j].ActiveAt > postings[j-1].ActiveAt; j-- {
			postings[j], postings[j-1] = postings[j-1], postings[j]
		}
	}
}

func (app *App) setSeen(ctx context.Context, a *Args, ids []string, seen bool) (*Result, error) {
	if len(ids) == 0 {
		return nil, usageError("give at least one thread or email id")
	}
	client, _, err := app.connect(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := selectAccounts(client, a.Account)
	if err != nil {
		return nil, err
	}
	var patched []string
	var lastErr error
	for _, account := range accounts {
		list, err := client.SetSeen(ctx, account.ID, ids, seen)
		if err != nil {
			lastErr = err
			continue
		}
		patched = append(patched, list...)
	}
	if len(patched) == 0 && lastErr != nil {
		return nil, lastErr
	}
	word := "seen"
	if !seen {
		word = "unseen"
	}
	summary := fmt.Sprintf("Marked %d email%s %s", len(patched), plural(len(patched)), word)
	return &Result{Data: map[string]any{"ids": patched, "seen": seen}, Summary: summary, Text: summary}, nil
}

func (app *App) readLine(prompt string) (string, error) {
	fmt.Fprint(app.Stderr, prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readSecret reads a token from stdin, hidden when stdin is a terminal.
func (app *App) readSecret(prompt string) (string, error) {
	if term.IsTerminal(int(syscall.Stdin)) {
		fmt.Fprint(app.Stderr, prompt)
		data, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(app.Stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 64*1024))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func pluralEs(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}
