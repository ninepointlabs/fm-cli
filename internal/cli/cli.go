// Package cli is fm-cli's scripting surface: the JSON commands the Omarchy
// plugin drives, plus sign-in. See docs/omarchy-contract.md.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"fm-cli/internal/api"
	"fm-cli/internal/auth"
)

// Version is set by main from the build's ldflags.
var Version = "dev"

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// Error is a failure the envelope can carry.
type Error struct {
	Code    string
	Message string
	Hint    string
}

func (e *Error) Error() string { return e.Message }

func usageError(format string, args ...any) *Error {
	return &Error{Code: "usage", Message: fmt.Sprintf(format, args...)}
}

// classify maps an arbitrary error onto an envelope error.
func classify(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, auth.ErrSignedOut):
		return &Error{Code: "auth", Message: "Not signed in", Hint: "Run `fm-cli auth login`."}
	case errors.Is(err, auth.ErrReauthenticate), errors.Is(err, api.ErrUnauthorized):
		return &Error{Code: "auth", Message: "Fastmail rejected the stored credential", Hint: "Run `fm-cli auth login` to sign in again."}
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Code: "network", Message: "Fastmail did not answer in time"}
	}
	text := err.Error()
	lower := strings.ToLower(text)
	if strings.Contains(lower, "could not reach") || strings.Contains(lower, "dial tcp") || strings.Contains(lower, "no such host") || strings.Contains(lower, "connection refused") || strings.Contains(lower, "tls") {
		return &Error{Code: "network", Message: text}
	}
	return &Error{Code: "remote", Message: text}
}

// Args is the parsed command line. Flags may appear anywhere.
type Args struct {
	Positional    []string
	JSON          bool
	Account       string
	Limit         int
	Events        string
	Boxes         []string
	Excludes      []string
	Token         bool
	NoBrowser     bool
	SilentSuccess bool
	Remove        bool
	Version       bool
	Help          bool
}

// Parse reads argv (without the program name).
func Parse(argv []string) (*Args, error) {
	a := &Args{}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			a.Positional = append(a.Positional, argv[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			a.Positional = append(a.Positional, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		need := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i+1 >= len(argv) {
				return "", usageError("flag %q needs a value", name)
			}
			i++
			return argv[i], nil
		}
		switch name {
		case "--json":
			a.JSON = true
		case "--account":
			v, err := need()
			if err != nil {
				return nil, err
			}
			a.Account = v
		case "--limit", "-n":
			v, err := need()
			if err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, usageError("--limit needs a positive number")
			}
			a.Limit = n
		case "--events":
			v, err := need()
			if err != nil {
				return nil, err
			}
			a.Events = v
		case "--box":
			v, err := need()
			if err != nil {
				return nil, err
			}
			a.Boxes = append(a.Boxes, v)
		case "--exclude":
			v, err := need()
			if err != nil {
				return nil, err
			}
			a.Excludes = append(a.Excludes, v)
		case "--token":
			a.Token = true
		case "--no-browser":
			a.NoBrowser = true
		case "--silent-success":
			a.SilentSuccess = true
		case "--remove":
			a.Remove = true
		case "--version", "-v":
			a.Version = true
		case "--help", "-h":
			a.Help = true
		default:
			return nil, usageError("unknown flag %q", name)
		}
	}
	return a, nil
}

// Handled says whether Run owns argv; anything else is the TUI or a legacy
// command handled by main.
func Handled(argv []string) bool {
	// The TUI and the legacy commands own their own flags, so they are picked
	// out by their first word before the shared parser sees anything.
	for _, arg := range argv {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		switch arg {
		case "tui", "settings", "sync", "debug":
			return false
		}
		break
	}
	a, err := Parse(argv)
	if err != nil {
		return true
	}
	if a.Version || a.Help {
		return true
	}
	return len(a.Positional) > 0
}

// App carries the dependencies commands use.
type App struct {
	Store  *auth.Store
	Stdout io.Writer
	Stderr io.Writer
	// Plain is the http client for unauthenticated calls (discovery, login).
	Plain *http.Client
}

// NewApp builds the default app.
func NewApp() *App {
	return &App{
		Store:  auth.NewStore(),
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Plain:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Run executes argv and returns the exit code.
func (app *App) Run(ctx context.Context, argv []string) int {
	a, err := Parse(argv)
	if err != nil {
		return app.fail(a != nil && a.JSON, err)
	}
	if a.Version {
		fmt.Fprintf(app.Stdout, "fm-cli version %s\n", Version)
		return ExitOK
	}
	if a.Help || len(a.Positional) == 0 || a.Positional[0] == "help" {
		app.printHelp()
		return ExitOK
	}
	result, err := app.dispatch(ctx, a)
	if err != nil {
		return app.fail(a.JSON, err)
	}
	if result == nil {
		return ExitOK
	}
	if a.JSON {
		env := map[string]any{"ok": true, "data": result.Data}
		if result.Summary != "" {
			env["summary"] = result.Summary
		}
		enc := json.NewEncoder(app.Stdout)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(env)
		return ExitOK
	}
	if result.Text != "" {
		fmt.Fprint(app.Stdout, result.Text)
		if !strings.HasSuffix(result.Text, "\n") {
			fmt.Fprintln(app.Stdout)
		}
	} else if result.Summary != "" {
		fmt.Fprintln(app.Stdout, result.Summary)
	}
	return ExitOK
}

// Result is a command's outcome; Data feeds the JSON envelope, Text the
// human rendering.
type Result struct {
	Data    any
	Summary string
	Text    string
}

func (app *App) fail(asJSON bool, err error) int {
	e := classify(err)
	if asJSON {
		env := map[string]any{"ok": false, "error": e.Message, "code": e.Code}
		if e.Hint != "" {
			env["hint"] = e.Hint
		}
		enc := json.NewEncoder(app.Stderr)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(env)
	} else {
		fmt.Fprintf(app.Stderr, "fm-cli: %s\n", e.Message)
		if e.Hint != "" {
			fmt.Fprintf(app.Stderr, "%s\n", e.Hint)
		}
	}
	if e.Code == "usage" {
		return ExitUsage
	}
	return ExitError
}

func (app *App) dispatch(ctx context.Context, a *Args) (*Result, error) {
	cmd := a.Positional[0]
	rest := a.Positional[1:]
	sub := ""
	if len(rest) > 0 {
		sub = rest[0]
	}
	switch cmd {
	case "version":
		return &Result{Data: map[string]string{"version": Version}, Text: "fm-cli version " + Version}, nil
	case "auth":
		switch sub {
		case "status":
			return app.authStatus(ctx)
		case "login":
			return app.authLogin(ctx, a)
		case "logout":
			return app.authLogout(ctx)
		case "dav":
			return app.authDAV(ctx, a)
		case "token":
			return app.authToken(ctx)
		case "":
			return nil, usageError("auth needs one of: status, login, logout, token, dav")
		}
		return nil, usageError("unknown command %q", "auth "+sub)
	case "login":
		return app.authLogin(ctx, a)
	case "logout":
		return app.authLogout(ctx)
	case "setup":
		return app.setup(ctx, a)
	case "account", "accounts":
		if sub == "list" || sub == "" {
			return app.accountList(ctx)
		}
		return nil, usageError("unknown command %q", cmd+" "+sub)
	case "box":
		switch sub {
		case "list":
			return app.boxList(ctx, a)
		case "view":
			selector := "inbox"
			if len(rest) > 1 {
				selector = rest[1]
			}
			return app.boxView(ctx, a, selector)
		case "":
			return nil, usageError("box needs one of: list, view")
		}
		return nil, usageError("unknown command %q", "box "+sub)
	case "inbox":
		return app.boxView(ctx, a, "inbox")
	case "seen":
		return app.setSeen(ctx, a, rest, true)
	case "unseen":
		return app.setSeen(ctx, a, rest, false)
	case "watch":
		return app.watch(ctx, a)
	}
	return nil, usageError("unknown command %q", cmd)
}

func (app *App) printHelp() {
	fmt.Fprint(app.Stdout, `fm-cli - Fastmail from the terminal

USAGE
  fm-cli                      Open the TUI
  fm-cli <command> [flags]

AUTH
  auth login [--token] [--no-browser]   Sign in (OAuth in the browser, or paste an API token)
  auth status [--json]                  Report sign-in state without touching the network
  auth logout                           Forget the stored credential
  auth token                            Print a current bearer token (for curl and scripts)
  auth dav [--remove]                   Store an app password for calendar and contacts (CalDAV/CardDAV)
  setup [--silent-success]              Sign in only when signed out

MAIL
  account list [--json]                 List mail accounts
  box list [--json]                     List mailboxes
  box view <inbox|all|role|id> [--limit N] [--account ID] [--exclude FOLDER]... [--json]
                                        "all" = newest Inbox threads plus unseen mail in every other folder
  inbox [--limit N] [--json]            Same as box view inbox
  seen <id>... [--json]                 Mark threads or emails seen
  unseen <id>... [--json]               Mark threads or emails unseen
  watch [--box NAME]... [--events LIST] Print a JSON line per change, live

OTHER
  settings | sync | debug               Offline mode, pending actions, session dump
  version                               Print the version

FLAGS
  --account ID|all   Select a mail account (default: the primary account)
  --json             Machine-readable output
`)
}

// connect loads the credential and opens an authenticated JMAP client.
func (app *App) connect(ctx context.Context) (*api.Client, *auth.Source, error) {
	creds, err := app.Store.Load()
	if err != nil {
		return nil, nil, err
	}
	if !creds.Authenticated() {
		if creds.Kind == auth.KindOAuth && creds.Revoked {
			return nil, nil, auth.ErrReauthenticate
		}
		return nil, nil, auth.ErrSignedOut
	}
	source := auth.NewSource(app.Store, creds, app.Plain)
	sessionURL := strings.TrimRight(creds.Issuer, "/") + "/jmap/session"
	client, err := api.NewClientWithHTTP(ctx, source.HTTPClient(), sessionURL)
	if err != nil {
		return nil, nil, err
	}
	return client, source, nil
}

// selectAccounts resolves --account against the session.
func selectAccounts(client *api.Client, selector string) ([]api.AccountInfo, error) {
	accounts := client.MailAccounts()
	if len(accounts) == 0 {
		return nil, errors.New("the credential has no mail account")
	}
	switch selector {
	case "", "default", "primary":
		return accounts[:1], nil
	case "all":
		return accounts, nil
	}
	for _, account := range accounts {
		if account.ID == selector || strings.EqualFold(account.Email, selector) || strings.EqualFold(account.Name, selector) {
			return []api.AccountInfo{account}, nil
		}
	}
	return nil, &Error{Code: "usage", Message: fmt.Sprintf("unknown account %q", selector)}
}
