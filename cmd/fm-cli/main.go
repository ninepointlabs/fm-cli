package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fm-cli/internal/api"
	"fm-cli/internal/auth"
	"fm-cli/internal/cli"
	"fm-cli/internal/storage"
	"fm-cli/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

// Set by goreleaser through -ldflags "-X main.version=...".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

var store = auth.NewStore()

func main() {
	cli.Version = version
	argv := os.Args[1:]

	if cli.Handled(argv) {
		os.Exit(cli.NewApp().Run(context.Background(), argv))
	}
	if len(argv) > 0 {
		switch argv[0] {
		case "settings":
			settings()
			return
		case "sync":
			syncNow()
			return
		case "debug":
			debugSession()
			return
		case "tui":
			// fall through to the TUI
		}
	}

	opts, err := parseTUIArgs(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fm-cli: %v\n", err)
		os.Exit(2)
	}
	if opts.remote {
		if err := sendToRunningTUI(opts.thread, opts.email); err != nil {
			fmt.Fprintf(os.Stderr, "fm-cli: %v\n", err)
			os.Exit(1)
		}
		return
	}
	runTUI(opts)
}

// tuiOptions are the flags `fm-cli tui` accepts.
type tuiOptions struct {
	thread string
	email  string
	remote bool
}

func parseTUIArgs(argv []string) (tuiOptions, error) {
	var opts tuiOptions
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "tui":
		case arg == "--remote":
			opts.remote = true
		case arg == "--thread" || arg == "--email":
			if i+1 >= len(argv) {
				return opts, fmt.Errorf("flag %q needs a value", arg)
			}
			i++
			if arg == "--thread" {
				opts.thread = argv[i]
			} else {
				opts.email = argv[i]
			}
		case strings.HasPrefix(arg, "--thread="):
			opts.thread = strings.TrimPrefix(arg, "--thread=")
		case strings.HasPrefix(arg, "--email="):
			opts.email = strings.TrimPrefix(arg, "--email=")
		default:
			return opts, fmt.Errorf("unknown flag %q", arg)
		}
	}
	if opts.remote && opts.thread == "" && opts.email == "" {
		return opts, errors.New("--remote needs --thread or --email")
	}
	return opts, nil
}

// tuiSocketPath is where a running TUI listens for hand-offs.
func tuiSocketPath() (string, error) {
	dir, err := auth.RuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tui.sock"), nil
}

type tuiHandoff struct {
	Thread string `json:"thread,omitempty"`
	Email  string `json:"email,omitempty"`
}

// sendToRunningTUI hands a target to a TUI already listening on the socket.
// It fails when none is, so the caller can start one instead.
func sendToRunningTUI(thread, email string) error {
	path, err := tuiSocketPath()
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return errors.New("no running fm-cli TUI to hand off to")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := json.NewEncoder(conn).Encode(tuiHandoff{Thread: thread, Email: email}); err != nil {
		return err
	}
	ack := make([]byte, 3)
	if _, err := conn.Read(ack); err != nil || string(ack) != "ok\n" {
		return errors.New("the running TUI did not accept the hand-off")
	}
	return nil
}

// listenForHandoffs serves the TUI socket until the program exits. A stale
// socket file from a crashed TUI is replaced; a live one (another TUI) means
// this instance simply does not listen.
func listenForHandoffs(p *tea.Program) (cleanup func()) {
	path, err := tuiSocketPath()
	if err != nil {
		return func() {}
	}
	if conn, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
		conn.Close()
		return func() {}
	}
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return func() {}
	}
	_ = os.Chmod(path, 0o600)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				var handoff tuiHandoff
				if err := json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&handoff); err != nil {
					return
				}
				if handoff.Thread == "" && handoff.Email == "" || len(handoff.Thread) > 64 || len(handoff.Email) > 64 {
					return
				}
				p.Send(tui.OpenEmailMsg{ThreadID: handoff.Thread, EmailID: handoff.Email})
				_, _ = conn.Write([]byte("ok\n"))
			}(conn)
		}
	}()
	return func() {
		listener.Close()
		_ = os.Remove(path)
	}
}

// connect opens an authenticated JMAP client from the stored credential.
func connect(ctx context.Context) (*api.Client, error) {
	creds, err := store.Load()
	if err != nil {
		return nil, err
	}
	if !creds.Authenticated() {
		return nil, auth.ErrSignedOut
	}
	source := auth.NewSource(store, creds, nil)
	return api.NewClientWithHTTP(ctx, source.HTTPClient(), creds.Issuer+"/jmap/session")
}

func runTUI(opts tuiOptions) {
	if _, err := store.Load(); err != nil {
		if errors.Is(err, auth.ErrSignedOut) {
			fmt.Println("Not signed in.")
			fmt.Println("Run 'fm-cli auth login' to sign in with your browser, or 'fm-cli auth login --token' to paste an API token.")
			os.Exit(1)
		}
		fmt.Printf("Could not read credentials: %v\n", err)
		os.Exit(1)
	}

	// Open local storage
	db, err := storage.Open()
	if err != nil {
		fmt.Printf("Warning: Could not open local storage: %v\n", err)
		// Continue without local storage
	}
	defer func() {
		if db != nil {
			db.Close()
		}
	}()

	// Check offline mode setting
	offlineMode := false
	if db != nil {
		if val, _ := db.GetConfig("offline_mode"); val == "true" {
			offlineMode = true
		}
	}

	// Initialize JMAP Client
	var client *api.Client
	if !offlineMode {
		fmt.Println("Connecting to Fastmail JMAP...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		client, err = connect(ctx)
		cancel()
		if err != nil {
			fmt.Printf("Failed to connect to server: %v\n", err)
			if db != nil {
				fmt.Println("Starting in offline mode with cached data...")
				offlineMode = true
			} else {
				os.Exit(1)
			}
		}
	} else {
		fmt.Println("Starting in offline mode...")
	}

	// Initialize CalDAV/CardDAV Client (optional, for calendar/contacts)
	var davClient *api.DAVClient
	appPwd, email := store.DAVCredentials()
	if appPwd != "" && email != "" {
		davClient, err = api.NewDAVClient(email, appPwd)
		if err != nil {
			fmt.Printf("Note: CalDAV/CardDAV not available: %v\n", err)
			// Continue without DAV - calendar/contacts won't work
		}
	}

	// Initialize Bubble Tea Program
	model := tui.NewModelWithStorage(client, davClient, db, offlineMode).WithTarget(opts.thread, opts.email)
	p := tea.NewProgram(model, tea.WithAltScreen())
	stopListening := listenForHandoffs(p)
	defer stopListening()

	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

func settings() {
	db, err := storage.Open()
	if err != nil {
		fmt.Printf("Error opening storage: %v\n", err)
		return
	}
	defer db.Close()

	if len(os.Args) < 3 {
		// Show current settings
		offlineMode, _ := db.GetConfig("offline_mode")
		fmt.Println("Current Settings:")
		fmt.Printf("  offline_mode: %s\n", boolStr(offlineMode))
		fmt.Println("\nUsage:")
		fmt.Println("  fm-cli settings offline on   - Enable offline mode (store emails locally)")
		fmt.Println("  fm-cli settings offline off  - Disable offline mode")
		return
	}

	switch os.Args[2] {
	case "offline":
		if len(os.Args) < 4 {
			fmt.Println("Usage: fm-cli settings offline [on|off]")
			return
		}
		switch os.Args[3] {
		case "on", "true", "1":
			db.SetConfig("offline_mode", "true")
			fmt.Println("Offline mode enabled. Emails will be stored locally.")
		case "off", "false", "0":
			db.SetConfig("offline_mode", "false")
			fmt.Println("Offline mode disabled.")
		default:
			fmt.Println("Usage: fm-cli settings offline [on|off]")
		}
	default:
		fmt.Printf("Unknown setting: %s\n", os.Args[2])
	}
}

func boolStr(val string) string {
	if val == "true" {
		return "on"
	}
	return "off"
}

func syncNow() {
	db, err := storage.Open()
	if err != nil {
		fmt.Printf("Error opening storage: %v\n", err)
		return
	}
	defer db.Close()

	// Get pending actions
	actions, err := db.GetPendingActions()
	if err != nil {
		fmt.Printf("Error getting pending actions: %v\n", err)
		return
	}

	if len(actions) == 0 {
		fmt.Println("No pending actions to sync.")
		return
	}

	// Connect to server
	fmt.Println("Connecting to Fastmail JMAP...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := connect(ctx)
	if err != nil {
		fmt.Printf("Failed to connect: %v\n", err)
		return
	}

	fmt.Printf("Syncing %d pending action(s)...\n", len(actions))

	for _, action := range actions {
		fmt.Printf("  Syncing %s...", action.Type)
		err := syncAction(client, db, action)
		if err != nil {
			fmt.Printf(" FAILED: %v\n", err)
		} else {
			fmt.Println(" OK")
			db.RemovePendingAction(action.ID)
		}
	}

	fmt.Println("Sync complete.")
}

func syncAction(client *api.Client, db *storage.DB, action storage.PendingAction) error {
	switch action.Type {
	case "save_draft":
		// Parse draft data and save to server
		// Data format: {"from":"...", "to":"...", "subject":"...", "body":"..."}
		var data map[string]string
		if err := json.Unmarshal([]byte(action.Data), &data); err != nil {
			return err
		}
		return client.SaveDraft("", data["from"], data["to"], data["subject"], data["body"])

	case "send_email":
		var data map[string]string
		if err := json.Unmarshal([]byte(action.Data), &data); err != nil {
			return err
		}
		return client.SendEmail("", data["from"], data["to"], data["subject"], data["body"])

	case "delete":
		return client.DeleteEmail(action.EmailID)

	case "set_unread":
		var data map[string]bool
		if err := json.Unmarshal([]byte(action.Data), &data); err != nil {
			return err
		}
		return client.SetUnread(action.EmailID, data["is_unread"])

	case "set_flagged":
		var data map[string]bool
		if err := json.Unmarshal([]byte(action.Data), &data); err != nil {
			return err
		}
		return client.SetFlagged(action.EmailID, data["is_flagged"])

	default:
		return fmt.Errorf("unknown action type: %s", action.Type)
	}
}

func debugSession() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := connect(ctx)
	if err != nil {
		fmt.Printf("Error connecting to Fastmail: %v\n", err)
		return
	}

	fmt.Printf("fm-cli %s", version)
	if commit != "" {
		fmt.Printf(" (%s %s)", commit, date)
	}
	fmt.Println()
	fmt.Println("JMAP Session:")
	fmt.Println(client.DebugSession())

	// Also test CalDAV/CardDAV
	appPwd, email := store.DAVCredentials()
	if appPwd != "" && email != "" {
		fmt.Println("\nCalDAV/CardDAV Connection:")
		fmt.Printf("Email: %s\n", email)
		fmt.Printf("App Password: set (%d characters)\n", len(appPwd))

		davClient, err := api.NewDAVClient(email, appPwd)
		if err != nil {
			fmt.Printf("Error creating DAV client: %v\n", err)
			return
		}

		fmt.Println("\nFetching calendars...")
		calendars, err := davClient.FetchCalendars(context.Background())
		if err != nil {
			fmt.Printf("Error fetching calendars: %v\n", err)
		} else {
			fmt.Printf("Found %d calendar(s):\n", len(calendars))
			for _, c := range calendars {
				fmt.Printf("  - %s (Path: %s)\n", c.Name, c.ID)
			}
		}

		fmt.Println("\nFetching address books...")
		addressBooks, err := davClient.FetchAddressBooks(context.Background())
		if err != nil {
			fmt.Printf("Error fetching address books: %v\n", err)
		} else {
			fmt.Printf("Found %d address book(s):\n", len(addressBooks))
			for _, ab := range addressBooks {
				fmt.Printf("  - %s (Path: %s)\n", ab.Name, ab.ID)
			}
		}
	} else {
		fmt.Println("\nNo CalDAV/CardDAV credentials configured.")
		fmt.Println("Run 'fm-cli auth dav' to store an app password for calendar/contacts.")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
