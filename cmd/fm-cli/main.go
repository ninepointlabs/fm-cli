package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"

	"fm-cli/internal/api"
	"fm-cli/internal/storage"
	"fm-cli/internal/tui"

	"github.com/99designs/keyring"
	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

const (
	serviceName       = "fm-cli"
	keyringUser       = "fastmail-api-token" // The key for JMAP API token
	keyringAppPwd     = "fastmail-app-password" // The key for CalDAV/CardDAV app password
	keyringEmail      = "fastmail-email"     // The key for email address
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			login()
			return
		case "logout":
			logout()
			return
		case "settings":
			settings()
			return
		case "sync":
			syncNow()
			return
		case "debug":
			debugSession()
			return
		case "help":
			printHelp()
			return
		}
	}

	// Security: Fetch Token from Keyring
	token, err := getToken()
	if err != nil || token == "" {
		// Fallback to env var for development
		token = os.Getenv("FM_API_TOKEN")
	}

	if token == "" {
		fmt.Println("No API token found.")
		fmt.Println("Please run 'fm-cli login' to store your Fastmail API token, or set FM_API_TOKEN.")
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
		client, err = api.NewClient(token)
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
	appPwd, email := getAppPassword()
	if appPwd != "" && email != "" {
		davClient, err = api.NewDAVClient(email, appPwd)
		if err != nil {
			fmt.Printf("Note: CalDAV/CardDAV not available: %v\n", err)
			// Continue without DAV - calendar/contacts won't work
		}
	}

	// Initialize Bubble Tea Program
	p := tea.NewProgram(tui.NewModelWithStorage(client, davClient, db, offlineMode), tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println("fm-cli - Fastmail TUI Client")
	fmt.Println("\nUsage:")
	fmt.Println("  fm-cli [command]")
	fmt.Println("\nCommands:")
	fmt.Println("  login     Store Fastmail API token in system keychain")
	fmt.Println("  logout    Remove Fastmail API token from system keychain")
	fmt.Println("  settings  Configure offline mode and other settings")
	fmt.Println("  sync      Sync pending offline changes with server")
	fmt.Println("  help      Show this help message")
	fmt.Println("\nIf no command is provided, the TUI will start.")
}

func login() {
	fmt.Println("FM-CLI Login Setup")
	fmt.Println("==================")
	fmt.Println()

	// Email address
	fmt.Print("Enter your Fastmail email address: ")
	var email string
	fmt.Scanln(&email)
	email = strings.TrimSpace(email)

	// JMAP API Token
	fmt.Println()
	fmt.Println("JMAP API Token (for email access):")
	fmt.Println("Get one from: Settings > Privacy & Security > Integrations > API Tokens")
	fmt.Print("Enter your Fastmail API Token: ")
	byteToken, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		fmt.Printf("\nError reading token: %v\n", err)
		return
	}
	token := strings.TrimSpace(string(byteToken))
	fmt.Println()

	// App password for CalDAV/CardDAV (optional)
	fmt.Println()
	fmt.Println("App Password (for calendar/contacts access - optional):")
	fmt.Println("Get one from: Settings > Privacy & Security > Integrations > App Passwords")
	fmt.Println("Create one with 'Mail, Contacts & Calendars' access")
	fmt.Print("Enter your App Password (or press Enter to skip): ")
	byteAppPwd, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		fmt.Printf("\nError reading app password: %v\n", err)
		return
	}
	appPwd := strings.TrimSpace(string(byteAppPwd))
	fmt.Println()

	ring, err := keyring.Open(keyring.Config{
		ServiceName: serviceName,
	})
	if err != nil {
		fmt.Printf("Error opening keyring: %v\n", err)
		return
	}

	// Store email
	err = ring.Set(keyring.Item{
		Key:  keyringEmail,
		Data: []byte(email),
	})
	if err != nil {
		fmt.Printf("Error storing email: %v\n", err)
		return
	}

	// Store JMAP token
	err = ring.Set(keyring.Item{
		Key:  keyringUser,
		Data: []byte(token),
	})
	if err != nil {
		fmt.Printf("Error storing token: %v\n", err)
		return
	}

	// Store app password if provided
	if appPwd != "" {
		err = ring.Set(keyring.Item{
			Key:  keyringAppPwd,
			Data: []byte(appPwd),
		})
		if err != nil {
			fmt.Printf("Error storing app password: %v\n", err)
			return
		}
		fmt.Println("Credentials saved successfully (email, API token, and app password).")
	} else {
		fmt.Println("Credentials saved successfully (email and API token).")
		fmt.Println("Note: Calendar and contacts features require an app password.")
	}
}

func logout() {
	ring, err := keyring.Open(keyring.Config{
		ServiceName: serviceName,
	})
	if err != nil {
		fmt.Printf("Error opening keyring: %v\n", err)
		return
	}

	err = ring.Remove(keyringUser)
	if err != nil {
		fmt.Printf("Error removing token: %v\n", err)
		return
	}
	fmt.Println("Token removed from system keyring.")
}

func getToken() (string, error) {
	ring, err := keyring.Open(keyring.Config{
		ServiceName: serviceName,
	})
	if err != nil {
		return "", err
	}

	item, err := ring.Get(keyringUser)
	if err != nil {
		return "", err
	}

	return string(item.Data), nil
}

func getAppPassword() (appPwd, email string) {
	ring, err := keyring.Open(keyring.Config{
		ServiceName: serviceName,
	})
	if err != nil {
		return "", ""
	}

	emailItem, err := ring.Get(keyringEmail)
	if err != nil {
		// Try environment variable fallback
		email = os.Getenv("FM_EMAIL")
	} else {
		email = string(emailItem.Data)
	}

	appPwdItem, err := ring.Get(keyringAppPwd)
	if err != nil {
		// Try environment variable fallback
		appPwd = os.Getenv("FM_APP_PASSWORD")
	} else {
		appPwd = string(appPwdItem.Data)
	}

	return appPwd, email
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

	// Get token
	token, err := getToken()
	if err != nil || token == "" {
		token = os.Getenv("FM_API_TOKEN")
	}
	if token == "" {
		fmt.Println("No API token found. Please run 'fm-cli login' first.")
		return
	}

	// Connect to server
	fmt.Println("Connecting to Fastmail JMAP...")
	client, err := api.NewClient(token)
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
	token, err := getToken()
	if err != nil || token == "" {
		token = os.Getenv("FM_API_TOKEN")
	}
	if token == "" {
		fmt.Println("No API token found. Please run 'fm-cli login' first.")
		return
	}

	client, err := api.NewClient(token)
	if err != nil {
		fmt.Printf("Error connecting to Fastmail: %v\n", err)
		return
	}

	fmt.Println("JMAP Session:")
	fmt.Println(client.DebugSession())

	// Also test CalDAV/CardDAV
	appPwd, email := getAppPassword()
	if appPwd != "" && email != "" {
		fmt.Println("\nCalDAV/CardDAV Connection:")
		fmt.Printf("Email: %s\n", email)
		fmt.Printf("App Password: %s***\n", appPwd[:min(3, len(appPwd))])

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
		fmt.Println("Run 'fm-cli login' with an app password to enable calendar/contacts.")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
