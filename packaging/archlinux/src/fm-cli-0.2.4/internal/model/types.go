package model

import "time"

// Mailbox represents a simplified JMAP mailbox for the TUI.
type Mailbox struct {
	ID          string
	Name        string
	UnreadCount int
	Role        string // e.g., "inbox", "archive", "snoozed"
	ParentID    string
	SortOrder   int
}

// AppConfig holds configuration details like the API token.
type AppConfig struct {
	APIToken string
	JMAPCore string // URL for the JMAP session resource
}

// Settings holds user preferences
type Settings struct {
	OfflineMode    bool // Store emails locally for offline access
	SyncOnStartup  bool // Sync with server on startup
	AutoSync       bool // Auto-sync after actions
}

// DefaultSettings returns the default settings
func DefaultSettings() Settings {
	return Settings{
		OfflineMode:   false,
		SyncOnStartup: true,
		AutoSync:      true,
	}
}

// Email represents a simplified email message for the TUI.
type Email struct {
	ID         string
	Subject    string
	From       string
	To         string
	Cc         string
	Bcc        string
	ReplyTo    string
	Preview    string
	Date       string
	IsUnread   bool
	IsFlagged  bool
	IsDraft    bool
	IsLocal    bool // True if this is a local-only draft not yet synced
	ThreadID   string
	MailboxIDs []string
	Body       string
}

// Calendar represents a JMAP calendar
type Calendar struct {
	ID                string
	Name              string
	Color             string
	IsVisible         bool
	IsDefault         bool
	MayReadItems      bool
	MayAddItems       bool
	MayModifyItems    bool
	MayRemoveItems    bool
}

// CalendarEvent represents a JMAP calendar event (JSCalendar format)
type CalendarEvent struct {
	ID           string
	CalendarID   string
	Title        string
	Description  string
	Location     string
	Start        time.Time
	End          time.Time
	Duration     string    // ISO 8601 duration (e.g., "PT1H")
	IsAllDay     bool
	Status       string    // confirmed, tentative, cancelled
	ShowWithoutTime bool
	Recurrence   string    // RRULE string if recurring
	Alerts       []EventAlert
	Participants []EventParticipant
	Created      time.Time
	Updated      time.Time
}

// EventAlert represents a reminder for a calendar event
type EventAlert struct {
	ID       string
	Trigger  string // e.g., "-PT15M" (15 minutes before)
	Action   string // display, email
}

// EventParticipant represents an attendee of an event
type EventParticipant struct {
	Name   string
	Email  string
	Kind   string // individual, group, resource
	Role   string // owner, attendee, optional
	Status string // needs-action, accepted, declined, tentative
}

// AddressBook represents a JMAP address book
type AddressBook struct {
	ID               string
	Name             string
	IsDefault        bool
	MayReadItems     bool
	MayAddItems      bool
	MayModifyItems   bool
	MayRemoveItems   bool
}

// Contact represents a JMAP contact card (JSContact format)
type Contact struct {
	ID             string
	AddressBookID  string
	FullName       string
	Prefix         string
	FirstName      string
	LastName       string
	Suffix         string
	Nickname       string
	Company        string
	JobTitle       string
	Emails         []ContactEmail
	Phones         []ContactPhone
	Addresses      []ContactAddress
	Notes          string
	Birthday       string
	Anniversary    string
	Created        time.Time
	Updated        time.Time
}

// ContactEmail represents an email address for a contact
type ContactEmail struct {
	Type    string // home, work, other
	Email   string
	IsDefault bool
}

// ContactPhone represents a phone number for a contact
type ContactPhone struct {
	Type   string // home, work, mobile, fax, other
	Number string
	IsDefault bool
}

// ContactAddress represents a physical address for a contact
type ContactAddress struct {
	Type       string // home, work, other
	Street     string
	City       string
	State      string
	PostalCode string
	Country    string
}
