package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"fm-cli/internal/api"
	"fm-cli/internal/images"
	"fm-cli/internal/model"
	"fm-cli/internal/storage"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/net/html"
)

// SessionState indicates the current view
type sessionState int

const (
	viewMainMenu sessionState = iota
	viewMailboxes
	viewEmails
	viewBody
	viewComposeTo
	viewComposeSubject
	viewComposeConfirm
	viewCalendar
	viewContacts
	viewSettings
)

// MainMenuItem represents an option in the main menu
type MainMenuItem struct {
	Name     string
	Shortcut string
	State    sessionState
}

// Styles
var (
	appStyle = lipgloss.NewStyle().Padding(1, 2)

	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFDF5")).
			Background(lipgloss.Color("#25A065")).
			Padding(0, 1)

	// Mailbox Styles
	mailboxStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			PaddingLeft(1)

	selectedMailboxStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("229")).
				Background(lipgloss.Color("57")).
				PaddingLeft(1)

	// Email Styles
	emailItemStyle = lipgloss.NewStyle().
			PaddingLeft(1).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(lipgloss.Color("240"))

	selectedEmailItemStyle = lipgloss.NewStyle().
				PaddingLeft(1).
				Foreground(lipgloss.Color("229")).
				Background(lipgloss.Color("57")).
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(lipgloss.Color("57"))

	unreadStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#04B575")).
			Bold(true)
)

// msg types
type mailboxesLoadedMsg []model.Mailbox
type emailsLoadedMsg []model.Email
type emailsRefreshedMsg []model.Email // For refresh without appending
type emailBodyLoadedMsg struct {
	body     string
	htmlBody string
}
type editorFinishedMsg struct{ err error }
type emailSentMsg struct{}
type draftSavedMsg struct{}
type emailDeletedMsg struct{}
type identitiesLoadedMsg []string
type calendarsLoadedMsg []model.Calendar
type eventsLoadedMsg []model.CalendarEvent
type addressBooksLoadedMsg []model.AddressBook
type contactsLoadedMsg []model.Contact
type eventCreatedMsg struct{}
type eventDeletedMsg struct{}
type contactCreatedMsg struct{}
type contactDeletedMsg struct{}
type htmlBodyLoadedMsg string
type browserOpenedMsg struct{}
type errorMsg error

// Main menu items
var mainMenuItems = []MainMenuItem{
	{Name: "Mail", Shortcut: "m", State: viewMailboxes},
	{Name: "Calendar", Shortcut: "c", State: viewCalendar},
	{Name: "Contacts", Shortcut: "o", State: viewContacts},
	{Name: "Settings", Shortcut: "s", State: viewSettings},
}

// Model implementation
type Model struct {
	client    *api.Client
	davClient *api.DAVClient
	db        *storage.DB
	state     sessionState

	// Offline mode
	offlineMode bool

	// Main Menu
	menuCursor int

	// Mailbox View Data
	mailboxes []model.Mailbox
	mbCursor  int

	// Email View Data
	emails      []model.Email
	emailCursor int
	emailOffset int
	loading     bool
	canLoadMore bool // If true, hitting bottom loads more

	// Body View Data
	bodyContent string
	htmlBody    string // Raw HTML for image rendering
	showDetails bool   // Toggle expanded headers

	// Composition Data
	inputTo          textinput.Model
	inputSubject     textinput.Model
	composeBody      string
	tempFile         string
	draftID          string   // If editing a draft
	identities       []string // Available sending identities (email addresses)
	identityIdx      int      // Currently selected identity index
	toSuggestions    []model.Contact // Autocomplete suggestions for To field
	toSuggestionIdx  int             // Selected suggestion index
	showSuggestions  bool            // Whether to show suggestions dropdown

	// Calendar Data
	calendars       []model.Calendar
	calendarCursor  int
	events          []model.CalendarEvent
	eventCursor     int
	agendaStart     time.Time // Start of agenda view (usually today)
	agendaDays      int       // Number of days to show (default 7)
	viewEventDetail bool      // Viewing event details
	editingEvent    *model.CalendarEvent // Event being created/edited
	eventInput      textinput.Model

	// Contacts Data
	addressBooks      []model.AddressBook
	addressBookCursor int
	contacts          []model.Contact
	contactCursor     int
	viewContactDetail bool       // Viewing contact details
	editingContact    *model.Contact // Contact being created/edited
	contactInput      textinput.Model
	contactEditField  int // Which field is being edited

	// Settings
	settingsCursor int

	err    error
	width  int
	height int
}

func NewModel(client *api.Client) Model {
	return NewModelWithStorage(client, nil, nil, false)
}

func NewModelWithStorage(client *api.Client, davClient *api.DAVClient, db *storage.DB, offlineMode bool) Model {
	tiTo := textinput.New()
	tiTo.Placeholder = "recipient@example.com"
	tiTo.Focus()

	tiSubj := textinput.New()
	tiSubj.Placeholder = "Subject"

	tiEvent := textinput.New()
	tiEvent.Placeholder = "Event title"

	tiContact := textinput.New()
	tiContact.Placeholder = "Contact name"

	return Model{
		client:       client,
		davClient:    davClient,
		db:           db,
		offlineMode:  offlineMode,
		state:        viewMainMenu,
		inputTo:      tiTo,
		inputSubject: tiSubj,
		eventInput:   tiEvent,
		contactInput: tiContact,
		loading:      false,
		agendaStart:  time.Now().Truncate(24 * time.Hour),
		agendaDays:   14,
	}
}

func (m Model) Init() tea.Cmd {
	// Pre-fetch identities on startup if online
	if !m.offlineMode && m.client != nil {
		return fetchIdentitiesCmd(m.client)
	}
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	// Global / Async Message Handling (Higher Priority)
	switch msg := msg.(type) {
	case editorFinishedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		content, err := ioutil.ReadFile(m.tempFile)
		if err != nil {
			m.err = err
			return m, nil
		}
		m.composeBody = string(content)
		m.state = viewComposeConfirm
		return m, nil

	case mailboxesLoadedMsg:
		m.mailboxes = msg
		m.loading = false
		return m, nil

	case emailsLoadedMsg:
		newEmails := []model.Email(msg)
		if len(newEmails) < 20 {
			m.canLoadMore = false
		} else {
			m.canLoadMore = true
		}
		m.emails = append(m.emails, newEmails...)
		m.loading = false
		return m, nil

	case emailsRefreshedMsg:
		// Replace emails instead of appending (for refresh)
		m.emails = []model.Email(msg)
		m.emailOffset = 0
		m.emailCursor = 0
		if len(m.emails) < 20 {
			m.canLoadMore = false
		} else {
			m.canLoadMore = true
		}
		m.loading = false
		return m, nil

	case emailBodyLoadedMsg:
		m.bodyContent = msg.body
		m.htmlBody = msg.htmlBody
		m.loading = false
		return m, nil

	case identitiesLoadedMsg:
		m.identities = msg
		return m, nil

	case draftSavedMsg:
		m.loading = false
		m.state = viewMailboxes
		os.Remove(m.tempFile)
		if m.offlineMode || m.client == nil {
			return m, fetchMailboxesOfflineCmd(m.db)
		}
		return m, fetchMailboxesCmd(m.client, m.db)

	case emailSentMsg:
		m.loading = false
		m.state = viewMailboxes
		os.Remove(m.tempFile)
		if m.offlineMode || m.client == nil {
			return m, fetchMailboxesOfflineCmd(m.db)
		}
		return m, fetchMailboxesCmd(m.client, m.db)

	case emailDeletedMsg:
		m.loading = false
		// Refresh mailbox counts after delete
		if m.offlineMode || m.client == nil {
			return m, fetchMailboxesOfflineCmd(m.db)
		}
		return m, fetchMailboxesCmd(m.client, m.db)

	case calendarsLoadedMsg:
		m.calendars = msg
		m.loading = false
		// Auto-fetch events for visible calendars
		if len(m.calendars) > 0 && m.client != nil && m.davClient != nil {
			var calIDs []string
			for _, cal := range m.calendars {
				if cal.IsVisible && cal.MayReadItems {
					calIDs = append(calIDs, cal.ID)
				}
			}
			if len(calIDs) > 0 {
				return m, fetchEventsCmd(m.davClient, calIDs, m.agendaStart, m.agendaStart.AddDate(0, 0, m.agendaDays))
			}
		}
		return m, nil

	case eventsLoadedMsg:
		m.events = msg
		m.loading = false
		return m, nil

	case addressBooksLoadedMsg:
		m.addressBooks = msg
		m.loading = false
		// Auto-fetch contacts for default address book
		if len(m.addressBooks) > 0 && m.client != nil && m.davClient != nil {
			defaultAB := ""
			for _, ab := range m.addressBooks {
				if ab.IsDefault && ab.MayReadItems {
					defaultAB = ab.ID
					break
				}
			}
			if defaultAB == "" && len(m.addressBooks) > 0 && m.addressBooks[0].MayReadItems {
				defaultAB = m.addressBooks[0].ID
			}
			if defaultAB != "" {
				return m, fetchContactsCmd(m.davClient, defaultAB, 100)
			}
		}
		return m, nil

	case contactsLoadedMsg:
		m.contacts = msg
		m.loading = false
		return m, nil

	case eventCreatedMsg:
		m.editingEvent = nil
		m.loading = false
		// Refresh events
		if len(m.calendars) > 0 && m.client != nil && m.davClient != nil {
			var calIDs []string
			for _, cal := range m.calendars {
				if cal.IsVisible && cal.MayReadItems {
					calIDs = append(calIDs, cal.ID)
				}
			}
			return m, fetchEventsCmd(m.davClient, calIDs, m.agendaStart, m.agendaStart.AddDate(0, 0, m.agendaDays))
		}
		return m, nil

	case eventDeletedMsg:
		m.loading = false
		m.viewEventDetail = false
		// Refresh events
		if len(m.calendars) > 0 && m.client != nil && m.davClient != nil {
			var calIDs []string
			for _, cal := range m.calendars {
				if cal.IsVisible && cal.MayReadItems {
					calIDs = append(calIDs, cal.ID)
				}
			}
			return m, fetchEventsCmd(m.davClient, calIDs, m.agendaStart, m.agendaStart.AddDate(0, 0, m.agendaDays))
		}
		return m, nil

	case contactCreatedMsg:
		m.editingContact = nil
		m.loading = false
		// Refresh contacts
		if len(m.addressBooks) > 0 && m.client != nil && m.davClient != nil {
			abID := ""
			if m.addressBookCursor < len(m.addressBooks) {
				abID = m.addressBooks[m.addressBookCursor].ID
			}
			return m, fetchContactsCmd(m.davClient, abID, 100)
		}
		return m, nil

	case contactDeletedMsg:
		m.loading = false
		m.viewContactDetail = false
		// Refresh contacts
		if len(m.addressBooks) > 0 && m.client != nil && m.davClient != nil {
			abID := ""
			if m.addressBookCursor < len(m.addressBooks) {
				abID = m.addressBooks[m.addressBookCursor].ID
			}
			return m, fetchContactsCmd(m.davClient, abID, 100)
		}
		return m, nil

	case errorMsg:
		m.err = msg
		m.loading = false
		return m, nil
	
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Don't return, let UI resize if needed (though mostly static)
	}

	// Handle Calendar Event Editing
	if m.state == viewCalendar && m.editingEvent != nil {
		m.eventInput, cmd = m.eventInput.Update(msg)
		
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.Type {
			case tea.KeyEnter:
				// Save the event
				m.editingEvent.Title = m.eventInput.Value()
				if m.editingEvent.Title == "" {
					m.err = fmt.Errorf("event title cannot be empty")
					return m, nil
				}
				if m.editingEvent.Duration == "" {
					m.editingEvent.Duration = "PT1H"
				}
				m.loading = true
				if m.editingEvent.ID == "" && m.davClient != nil {
					return m, createEventCmd(m.davClient, *m.editingEvent)
				} else if m.davClient != nil {
					return m, updateEventCmd(m.davClient, *m.editingEvent)
				}
			case tea.KeyEsc:
				m.editingEvent = nil
				m.eventInput.Blur()
				return m, nil
			case tea.KeyCtrlC:
				return m, tea.Quit
			}
		}
		return m, cmd
	}

	// Handle Contact Editing
	if m.state == viewContacts && m.editingContact != nil {
		m.contactInput, cmd = m.contactInput.Update(msg)
		
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.Type {
			case tea.KeyTab:
				// Save current field and move to next
				switch m.contactEditField {
				case 0: // Full Name
					m.editingContact.FullName = m.contactInput.Value()
				case 1: // Email
					if m.contactInput.Value() != "" {
						if len(m.editingContact.Emails) == 0 {
							m.editingContact.Emails = []model.ContactEmail{{Type: "home"}}
						}
						m.editingContact.Emails[0].Email = m.contactInput.Value()
					}
				case 2: // Phone
					if m.contactInput.Value() != "" {
						if len(m.editingContact.Phones) == 0 {
							m.editingContact.Phones = []model.ContactPhone{{Type: "mobile"}}
						}
						m.editingContact.Phones[0].Number = m.contactInput.Value()
					}
				case 3: // Company
					m.editingContact.Company = m.contactInput.Value()
				case 4: // Notes
					m.editingContact.Notes = m.contactInput.Value()
				}
				
				// Move to next field
				m.contactEditField = (m.contactEditField + 1) % 5
				
				// Set input value for new field
				switch m.contactEditField {
				case 0:
					m.contactInput.SetValue(m.editingContact.FullName)
					m.contactInput.Placeholder = "Full Name"
				case 1:
					email := ""
					if len(m.editingContact.Emails) > 0 {
						email = m.editingContact.Emails[0].Email
					}
					m.contactInput.SetValue(email)
					m.contactInput.Placeholder = "Email"
				case 2:
					phone := ""
					if len(m.editingContact.Phones) > 0 {
						phone = m.editingContact.Phones[0].Number
					}
					m.contactInput.SetValue(phone)
					m.contactInput.Placeholder = "Phone"
				case 3:
					m.contactInput.SetValue(m.editingContact.Company)
					m.contactInput.Placeholder = "Company"
				case 4:
					m.contactInput.SetValue(m.editingContact.Notes)
					m.contactInput.Placeholder = "Notes"
				}
				return m, nil
			case tea.KeyEnter:
				// Save the current field value first
				switch m.contactEditField {
				case 0:
					m.editingContact.FullName = m.contactInput.Value()
				case 1:
					if m.contactInput.Value() != "" {
						if len(m.editingContact.Emails) == 0 {
							m.editingContact.Emails = []model.ContactEmail{{Type: "home"}}
						}
						m.editingContact.Emails[0].Email = m.contactInput.Value()
					}
				case 2:
					if m.contactInput.Value() != "" {
						if len(m.editingContact.Phones) == 0 {
							m.editingContact.Phones = []model.ContactPhone{{Type: "mobile"}}
						}
						m.editingContact.Phones[0].Number = m.contactInput.Value()
					}
				case 3:
					m.editingContact.Company = m.contactInput.Value()
				case 4:
					m.editingContact.Notes = m.contactInput.Value()
				}
				
				// Save the contact
				if m.editingContact.FullName == "" {
					m.err = fmt.Errorf("contact name cannot be empty")
					return m, nil
				}
				m.loading = true
				if m.editingContact.ID == "" && m.davClient != nil {
					return m, createContactCmd(m.davClient, *m.editingContact)
				} else if m.davClient != nil {
					return m, updateContactCmd(m.davClient, *m.editingContact)
				}
			case tea.KeyEsc:
				m.editingContact = nil
				m.contactInput.Blur()
				return m, nil
			case tea.KeyCtrlC:
				return m, tea.Quit
			}
		}
		return m, cmd
	}

	// Handle Composition States
	if m.state == viewComposeTo {
		oldValue := m.inputTo.Value()
		m.inputTo, cmd = m.inputTo.Update(msg)
		newValue := m.inputTo.Value()
		
		// Update suggestions when input changes
		if oldValue != newValue && len(newValue) >= 1 {
			m.toSuggestions = filterContacts(m.contacts, newValue)
			m.showSuggestions = len(m.toSuggestions) > 0
			m.toSuggestionIdx = 0
		} else if newValue == "" {
			m.toSuggestions = nil
			m.showSuggestions = false
		}
		
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.Type {
			case tea.KeyDown:
				if m.showSuggestions && m.toSuggestionIdx < len(m.toSuggestions)-1 {
					m.toSuggestionIdx++
					return m, nil
				}
			case tea.KeyUp:
				if m.showSuggestions && m.toSuggestionIdx > 0 {
					m.toSuggestionIdx--
					return m, nil
				}
			case tea.KeyEnter:
				if m.showSuggestions && len(m.toSuggestions) > 0 {
					// Select the suggestion
					selected := m.toSuggestions[m.toSuggestionIdx]
					if len(selected.Emails) > 0 {
						if selected.FullName != "" {
							m.inputTo.SetValue(fmt.Sprintf("%s <%s>", selected.FullName, selected.Emails[0].Email))
						} else {
							m.inputTo.SetValue(selected.Emails[0].Email)
						}
					}
					m.showSuggestions = false
					m.toSuggestions = nil
					return m, nil
				}
				m.state = viewComposeSubject
				m.inputTo.Blur()
				m.inputSubject.Focus()
				m.showSuggestions = false
				return m, textinput.Blink
			case tea.KeyTab:
				if m.showSuggestions && len(m.toSuggestions) > 0 {
					// Tab also selects suggestion
					selected := m.toSuggestions[m.toSuggestionIdx]
					if len(selected.Emails) > 0 {
						if selected.FullName != "" {
							m.inputTo.SetValue(fmt.Sprintf("%s <%s>", selected.FullName, selected.Emails[0].Email))
						} else {
							m.inputTo.SetValue(selected.Emails[0].Email)
						}
					}
					m.showSuggestions = false
					m.toSuggestions = nil
					return m, nil
				}
				if len(m.identities) > 1 {
					m.identityIdx = (m.identityIdx + 1) % len(m.identities)
				}
				return m, nil
			case tea.KeyEsc:
				if m.showSuggestions {
					m.showSuggestions = false
					return m, nil
				}
				m.state = viewMailboxes
				m.inputTo.Blur()
				return m, nil
			case tea.KeyCtrlC:
				return m, tea.Quit
			}
		}
		return m, cmd
	}

	if m.state == viewComposeSubject {
		m.inputSubject, cmd = m.inputSubject.Update(msg)
		
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.Type {
			case tea.KeyEnter:
				// Create Temp File
				f, err := ioutil.TempFile("", "fm-cli-*.txt")
				if err != nil {
					m.err = err
					return m, nil
				}
				
				// Write existing body content to file if available
				if m.composeBody != "" {
					if _, err := f.WriteString(m.composeBody); err != nil {
						f.Close()
						m.err = err
						return m, nil
					}
				}
				
				m.tempFile = f.Name()
				f.Close()

				editor := os.Getenv("EDITOR")
				if editor == "" {
					editor = "nano"
				}
				c := exec.Command(editor, m.tempFile)
				return m, tea.ExecProcess(c, func(err error) tea.Msg {
					return editorFinishedMsg{err}
				})
			case tea.KeyTab:
				if len(m.identities) > 1 {
					m.identityIdx = (m.identityIdx + 1) % len(m.identities)
				}
				return m, nil
			case tea.KeyEsc:
				m.state = viewComposeTo
				m.inputSubject.Blur()
				m.inputTo.Focus()
				return m, textinput.Blink
			case tea.KeyCtrlC:
				return m, tea.Quit
			}
		}
		return m, cmd
	}

	if m.state == viewComposeConfirm {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "y", "Y":
				m.loading = true
				fromAddr := ""
				if len(m.identities) > 0 {
					fromAddr = m.identities[m.identityIdx]
				}
				return m, sendEmailCmd(m.client, m.draftID, fromAddr, m.inputTo.Value(), m.inputSubject.Value(), m.composeBody)
			case "s", "S":
				m.loading = true
				fromAddr := ""
				if len(m.identities) > 0 {
					fromAddr = m.identities[m.identityIdx]
				}
				return m, saveDraftCmd(m.client, m.draftID, fromAddr, m.inputTo.Value(), m.inputSubject.Value(), m.composeBody)
			case "n", "N":
				m.state = viewMailboxes
				m.composeBody = ""
				os.Remove(m.tempFile)
				return m, nil
			case "e", "E":
				editor := os.Getenv("EDITOR")
				if editor == "" {
					editor = "nano"
				}
				c := exec.Command(editor, m.tempFile)
				return m, tea.ExecProcess(c, func(err error) tea.Msg {
					return editorFinishedMsg{err}
				})
			case "tab":
				if len(m.identities) > 1 {
					m.identityIdx = (m.identityIdx + 1) % len(m.identities)
				}
				return m, nil
			case "ctrl+c":
				return m, tea.Quit
			}
		}
		return m, nil
	}

	// Normal Navigation States
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Clear error on any key press
		if m.err != nil {
			m.err = nil
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		
		case "q":
			// Only quit from main menu
			if m.state == viewMainMenu {
				return m, tea.Quit
			}

		// Global navigation shortcuts (number keys)
		case "0":
			// Back to main menu
			m.state = viewMainMenu
			return m, nil
		case "1":
			// Go to Mail
			if m.state != viewComposeTo && m.state != viewComposeSubject && m.state != viewComposeConfirm {
				m.state = viewMailboxes
				m.loading = true
				if m.offlineMode || m.client == nil {
					return m, fetchMailboxesOfflineCmd(m.db)
				}
				return m, fetchMailboxesCmd(m.client, m.db)
			}
		case "2":
			// Go to Calendar
			if m.state != viewComposeTo && m.state != viewComposeSubject && m.state != viewComposeConfirm {
				m.state = viewCalendar
				if len(m.calendars) == 0 && m.client != nil && !m.offlineMode && m.davClient != nil {
					m.loading = true
					return m, fetchCalendarsCmd(m.davClient)
				}
				return m, nil
			}
		case "3":
			// Go to Contacts
			if m.state != viewComposeTo && m.state != viewComposeSubject && m.state != viewComposeConfirm {
				m.state = viewContacts
				m.contactCursor = 0 // Reset cursor
				if len(m.addressBooks) == 0 && m.client != nil && !m.offlineMode && m.davClient != nil {
					m.loading = true
					return m, fetchAddressBooksCmd(m.davClient)
				} else if len(m.addressBooks) > 0 && len(m.contacts) == 0 && m.davClient != nil && !m.offlineMode {
					// Address books loaded but no contacts yet - fetch them
					m.loading = true
					defaultAB := m.addressBooks[0].ID
					for _, ab := range m.addressBooks {
						if ab.IsDefault {
							defaultAB = ab.ID
							break
						}
					}
					return m, fetchContactsCmd(m.davClient, defaultAB, 100)
				}
				return m, nil
			}
		case "4":
			// Go to Settings
			if m.state != viewComposeTo && m.state != viewComposeSubject && m.state != viewComposeConfirm {
				m.state = viewSettings
				return m, nil
			}

		case "d", "backspace":
			if m.state == viewEmails && len(m.emails) > 0 {
				if m.offlineMode {
					m.err = fmt.Errorf("cannot delete emails in offline mode")
					return m, nil
				}
				if m.client == nil {
					m.err = fmt.Errorf("not connected")
					return m, nil
				}
				m.loading = true
				selectedEmail := m.emails[m.emailCursor]
				// Optimistic UI update
				if m.emailCursor < len(m.emails)-1 {
					m.emails = append(m.emails[:m.emailCursor], m.emails[m.emailCursor+1:]...)
				} else {
					m.emails = m.emails[:m.emailCursor]
					if m.emailCursor > 0 {
						m.emailCursor--
					}
				}
				return m, deleteEmailCmd(m.client, selectedEmail.ID)
			} else if m.state == viewCalendar && len(m.events) > 0 && !m.offlineMode && m.davClient != nil {
				if m.viewEventDetail || m.editingEvent == nil {
					m.loading = true
					eventID := m.events[m.eventCursor].ID
					// Optimistic UI update
					if m.eventCursor < len(m.events)-1 {
						m.events = append(m.events[:m.eventCursor], m.events[m.eventCursor+1:]...)
					} else {
						m.events = m.events[:m.eventCursor]
						if m.eventCursor > 0 {
							m.eventCursor--
						}
					}
					m.viewEventDetail = false
					return m, deleteEventCmd(m.davClient, eventID)
				}
			} else if m.state == viewContacts && len(m.contacts) > 0 && !m.offlineMode && m.davClient != nil {
				if m.viewContactDetail || m.editingContact == nil {
					m.loading = true
					contactID := m.contacts[m.contactCursor].ID
					// Optimistic UI update
					if m.contactCursor < len(m.contacts)-1 {
						m.contacts = append(m.contacts[:m.contactCursor], m.contacts[m.contactCursor+1:]...)
					} else {
						m.contacts = m.contacts[:m.contactCursor]
						if m.contactCursor > 0 {
							m.contactCursor--
						}
					}
					m.viewContactDetail = false
					return m, deleteContactCmd(m.davClient, contactID)
				}
			}

		case "u":
			if m.state == viewEmails && len(m.emails) > 0 {
				selectedEmail := m.emails[m.emailCursor]
				newState := !selectedEmail.IsUnread
				m.emails[m.emailCursor].IsUnread = newState
				return m, toggleUnreadCmd(m.client, selectedEmail.ID, newState)
			}

		case "f":
			if m.state == viewEmails && len(m.emails) > 0 {
				selectedEmail := m.emails[m.emailCursor]
				newState := !selectedEmail.IsFlagged
				m.emails[m.emailCursor].IsFlagged = newState
				return m, toggleFlaggedCmd(m.client, selectedEmail.ID, newState)
			}
		
		case "e":
			if m.state == viewEmails && len(m.emails) > 0 {
				targetMBID := ""
				// Find Archive Mailbox ID
				for _, mb := range m.mailboxes {
					if mb.Role == "archive" {
						targetMBID = mb.ID
						break
					}
				}

				if targetMBID != "" {
					m.loading = true
					selectedEmail := m.emails[m.emailCursor]
					currentMBID := m.mailboxes[m.mbCursor].ID
					
					// Optimistic UI update
					if m.emailCursor < len(m.emails)-1 {
						m.emails = append(m.emails[:m.emailCursor], m.emails[m.emailCursor+1:]...)
					} else {
						m.emails = m.emails[:m.emailCursor]
						if m.emailCursor > 0 {
							m.emailCursor--
						}
					}
					
					return m, moveEmailCmd(m.client, selectedEmail.ID, currentMBID, targetMBID)
				}
			} else if m.state == viewBody {
				// If viewing a draft, 'e' edits it
				if len(m.emails) > m.emailCursor {
					selectedEmail := m.emails[m.emailCursor]
					if selectedEmail.IsDraft {
						m.state = viewComposeTo
						m.draftID = selectedEmail.ID
						m.inputTo.SetValue(selectedEmail.To)
						m.inputSubject.SetValue(selectedEmail.Subject)
						
						// Prepare body
						body := m.bodyContent
						if strings.HasPrefix(body, "[Converted HTML]\n") {
							body = strings.TrimPrefix(body, "[Converted HTML]\n")
						}
						m.composeBody = body
						
						// Determine focus
						if m.inputTo.Value() == "" {
							m.state = viewComposeTo
							m.inputTo.Focus()
						} else {
							m.state = viewComposeSubject
							m.inputTo.Blur()
							m.inputSubject.Focus()
						}
						return m, textinput.Blink
					}
				}
			} else if m.state == viewCalendar && m.viewEventDetail && len(m.events) > 0 && !m.offlineMode {
				// Edit event
				event := m.events[m.eventCursor]
				m.editingEvent = &event
				m.viewEventDetail = false
				m.eventInput.SetValue(event.Title)
				m.eventInput.Focus()
				return m, textinput.Blink
			} else if m.state == viewContacts && m.viewContactDetail && len(m.contacts) > 0 && !m.offlineMode {
				// Edit contact
				contact := m.contacts[m.contactCursor]
				m.editingContact = &contact
				m.viewContactDetail = false
				m.contactInput.SetValue(contact.FullName)
				m.contactInput.Focus()
				m.contactEditField = 0
				return m, textinput.Blink
			}

		case "c":
			m.state = viewComposeTo
			m.draftID = "" // New email
			m.inputTo.SetValue("")
			m.inputSubject.SetValue("")
			m.composeBody = ""
			m.showSuggestions = false
			m.toSuggestions = nil
			m.inputTo.Focus()
			// Fetch contacts for autocomplete if not already loaded
			if m.davClient != nil && !m.offlineMode {
				if len(m.addressBooks) == 0 {
					// Need to fetch address books first, then contacts
					return m, tea.Batch(textinput.Blink, fetchAddressBooksCmd(m.davClient))
				} else if len(m.contacts) == 0 {
					defaultAB := m.addressBooks[0].ID
					for _, ab := range m.addressBooks {
						if ab.IsDefault {
							defaultAB = ab.ID
							break
						}
					}
					return m, tea.Batch(textinput.Blink, fetchContactsCmd(m.davClient, defaultAB, 100))
				}
			}
			return m, textinput.Blink

		case "R": // Reply to sender
			if m.state == viewBody && len(m.emails) > 0 {
				selectedEmail := m.emails[m.emailCursor]
				m.state = viewComposeTo
				m.draftID = ""
				// Use ReplyTo if available, otherwise From
				replyTo := selectedEmail.From
				if selectedEmail.ReplyTo != "" {
					replyTo = selectedEmail.ReplyTo
				}
				m.inputTo.SetValue(replyTo)
				// Add Re: prefix if not already present
				subject := selectedEmail.Subject
				if !strings.HasPrefix(strings.ToLower(subject), "re:") {
					subject = "Re: " + subject
				}
				m.inputSubject.SetValue(subject)
				// Quote original message
				m.composeBody = fmt.Sprintf("\n\n--- Original Message ---\nFrom: %s\nDate: %s\nSubject: %s\n\n%s",
					selectedEmail.From, selectedEmail.Date, selectedEmail.Subject, m.bodyContent)
				m.inputTo.Focus()
				return m, textinput.Blink
			}

		case "A": // Reply all
			if m.state == viewBody && len(m.emails) > 0 {
				selectedEmail := m.emails[m.emailCursor]
				m.state = viewComposeTo
				m.draftID = ""
				// Combine From (or ReplyTo), To, and Cc for reply-all
				var recipients []string
				replyTo := selectedEmail.From
				if selectedEmail.ReplyTo != "" {
					replyTo = selectedEmail.ReplyTo
				}
				recipients = append(recipients, replyTo)
				if selectedEmail.To != "" {
					recipients = append(recipients, selectedEmail.To)
				}
				if selectedEmail.Cc != "" {
					recipients = append(recipients, selectedEmail.Cc)
				}
				m.inputTo.SetValue(strings.Join(recipients, ", "))
				// Add Re: prefix if not already present
				subject := selectedEmail.Subject
				if !strings.HasPrefix(strings.ToLower(subject), "re:") {
					subject = "Re: " + subject
				}
				m.inputSubject.SetValue(subject)
				// Quote original message
				m.composeBody = fmt.Sprintf("\n\n--- Original Message ---\nFrom: %s\nDate: %s\nSubject: %s\n\n%s",
					selectedEmail.From, selectedEmail.Date, selectedEmail.Subject, m.bodyContent)
				m.inputTo.Focus()
				return m, textinput.Blink
			}

		case "F": // Forward
			if m.state == viewBody && len(m.emails) > 0 {
				selectedEmail := m.emails[m.emailCursor]
				m.state = viewComposeTo
				m.draftID = ""
				m.inputTo.SetValue("") // User needs to enter recipient
				// Add Fwd: prefix if not already present
				subject := selectedEmail.Subject
				if !strings.HasPrefix(strings.ToLower(subject), "fwd:") && !strings.HasPrefix(strings.ToLower(subject), "fw:") {
					subject = "Fwd: " + subject
				}
				m.inputSubject.SetValue(subject)
				// Include forwarded message
				m.composeBody = fmt.Sprintf("\n\n--- Forwarded Message ---\nFrom: %s\nTo: %s\nDate: %s\nSubject: %s\n\n%s",
					selectedEmail.From, selectedEmail.To, selectedEmail.Date, selectedEmail.Subject, m.bodyContent)
				m.inputTo.Focus()
				return m, textinput.Blink
			}

		case "m":
			if m.state == viewBody {
				m.showDetails = !m.showDetails
				return m, nil
			}
			// 'm' also goes to Mail from main menu
			if m.state == viewMainMenu {
				m.state = viewMailboxes
				m.loading = true
				if m.offlineMode || m.client == nil {
					return m, fetchMailboxesOfflineCmd(m.db)
				}
				return m, fetchMailboxesCmd(m.client, m.db)
			}

		case "b":
			// Open email in browser
			if m.state == viewBody && m.htmlBody != "" {
				return m, openInBrowserCmd(m.htmlBody)
			}

		case "i":
			// Render images inline (Sixel/Kitty/iTerm2)
			if m.state == viewBody && m.htmlBody != "" {
				return m, renderImagesCmd(m.htmlBody)
			}

		case "up", "k":
			if m.state == viewMainMenu {
				if m.menuCursor > 0 {
					m.menuCursor--
				}
				return m, nil
			} else if m.state == viewMailboxes {
				if m.mbCursor > 0 {
					m.mbCursor--
				}
				return m, nil
			} else if m.state == viewEmails {
				if m.emailCursor > 0 {
					m.emailCursor--
					if m.emailCursor < m.emailOffset {
						m.emailOffset = m.emailCursor
					}
				}
				return m, nil
			} else if m.state == viewCalendar && !m.viewEventDetail && m.editingEvent == nil {
				if m.eventCursor > 0 {
					m.eventCursor--
				}
				return m, nil
			} else if m.state == viewContacts && !m.viewContactDetail && m.editingContact == nil {
				if m.contactCursor > 0 {
					m.contactCursor--
				}
				return m, nil
			} else if m.state == viewSettings {
				if m.settingsCursor > 0 {
					m.settingsCursor--
				}
				return m, nil
			}

		case "down", "j":
			if m.state == viewMainMenu {
				if m.menuCursor < len(mainMenuItems)-1 {
					m.menuCursor++
				}
				return m, nil
			} else if m.state == viewMailboxes {
				if m.mbCursor < len(m.mailboxes)-1 {
					m.mbCursor++
				}
				return m, nil
			} else if m.state == viewEmails {
				// Dynamic page height
				headerHeight := 5
				footerHeight := 2
				pageHeight := m.height - headerHeight - footerHeight
				if pageHeight < 5 {
					pageHeight = 5
				}

				if m.emailCursor < len(m.emails)-1 {
					m.emailCursor++
					if m.emailCursor >= m.emailOffset+pageHeight {
						m.emailOffset++
					}
				} else if m.canLoadMore && !m.loading {
					m.loading = true
					selectedMB := m.mailboxes[m.mbCursor]
					if m.offlineMode || m.client == nil {
						return m, fetchEmailsOfflineCmd(m.db, selectedMB.ID, len(m.emails))
					}
					return m, fetchEmailsCmd(m.client, m.db, selectedMB.ID, len(m.emails))
				}
				return m, nil
			} else if m.state == viewCalendar && !m.viewEventDetail && m.editingEvent == nil {
				if m.eventCursor < len(m.events)-1 {
					m.eventCursor++
				}
				return m, nil
			} else if m.state == viewContacts && !m.viewContactDetail && m.editingContact == nil {
				if m.contactCursor < len(m.contacts)-1 {
					m.contactCursor++
				}
				return m, nil
			} else if m.state == viewSettings {
				if m.settingsCursor < 1 { // Only 1 setting currently
					m.settingsCursor++
				}
				return m, nil
			}

		case "enter", "right", "l":
			if m.state == viewMainMenu {
				// Navigate to selected menu item
				selectedItem := mainMenuItems[m.menuCursor]
				m.state = selectedItem.State
				if selectedItem.State == viewMailboxes {
					m.loading = true
					if m.offlineMode || m.client == nil {
						return m, fetchMailboxesOfflineCmd(m.db)
					}
					return m, fetchMailboxesCmd(m.client, m.db)
				} else if selectedItem.State == viewCalendar && !m.offlineMode && m.client != nil && m.davClient != nil {
					m.loading = true
					if len(m.calendars) == 0 {
						return m, fetchCalendarsCmd(m.davClient)
					}
					// Calendars already loaded, fetch events
					var calIDs []string
					for _, cal := range m.calendars {
						if cal.IsVisible && cal.MayReadItems {
							calIDs = append(calIDs, cal.ID)
						}
					}
					if len(calIDs) > 0 {
						start := time.Now()
						end := start.AddDate(0, 0, m.agendaDays)
						return m, fetchEventsCmd(m.davClient, calIDs, start, end)
					}
				} else if selectedItem.State == viewContacts && !m.offlineMode && m.client != nil && m.davClient != nil {
					m.loading = true
					m.contactCursor = 0
					if len(m.addressBooks) == 0 {
						return m, fetchAddressBooksCmd(m.davClient)
					}
					// Address books loaded, fetch contacts if needed
					if len(m.contacts) == 0 {
						defaultAB := m.addressBooks[0].ID
						for _, ab := range m.addressBooks {
							if ab.IsDefault {
								defaultAB = ab.ID
								break
							}
						}
						return m, fetchContactsCmd(m.davClient, defaultAB, 100)
					}
					m.loading = false
				}
				return m, nil
			} else if m.state == viewMailboxes && len(m.mailboxes) > 0 {
				m.state = viewEmails
				m.emailCursor = 0 // reset cursor
				m.emailOffset = 0 // reset offset
				m.emails = nil    // clear previous
				m.loading = true
				m.canLoadMore = true
				selectedMB := m.mailboxes[m.mbCursor]
				if m.offlineMode || m.client == nil {
					return m, fetchEmailsOfflineCmd(m.db, selectedMB.ID, 0)
				}
				return m, fetchEmailsCmd(m.client, m.db, selectedMB.ID, 0)
			} else if m.state == viewEmails && len(m.emails) > 0 {
				// Always go to preview first, even for drafts
				m.state = viewBody
				m.loading = true
				selectedEmail := m.emails[m.emailCursor]
				if m.offlineMode || m.client == nil {
					return m, fetchEmailBodyOfflineCmd(m.db, selectedEmail.ID)
				}
				return m, fetchEmailBodyCmd(m.client, m.db, selectedEmail.ID)
			} else if m.state == viewCalendar && !m.viewEventDetail && m.editingEvent == nil && len(m.events) > 0 {
				// View event details
				m.viewEventDetail = true
				return m, nil
			} else if m.state == viewContacts && !m.viewContactDetail && m.editingContact == nil && len(m.contacts) > 0 {
				// View contact details
				m.viewContactDetail = true
				return m, nil
			} else if m.state == viewSettings {
				// Toggle offline mode
				if m.settingsCursor == 0 {
					m.offlineMode = !m.offlineMode
					if m.db != nil {
						if m.offlineMode {
							m.db.SetConfig("offline_mode", "true")
						} else {
							m.db.SetConfig("offline_mode", "false")
						}
					}
				}
				return m, nil
			}

		case "esc", "left", "h":
			if m.state == viewMailboxes {
				m.state = viewMainMenu
				return m, nil
			} else if m.state == viewEmails {
				m.state = viewMailboxes
				m.emails = nil
				// Refresh mailbox counts when returning
				if m.offlineMode || m.client == nil {
					return m, fetchMailboxesOfflineCmd(m.db)
				}
				return m, fetchMailboxesCmd(m.client, m.db)
			} else if m.state == viewBody {
				m.state = viewEmails
				m.bodyContent = ""
				m.htmlBody = ""
			} else if m.state == viewCalendar {
				if m.viewEventDetail {
					m.viewEventDetail = false
				} else if m.editingEvent != nil {
					m.editingEvent = nil
				} else {
					m.state = viewMainMenu
				}
				return m, nil
			} else if m.state == viewContacts {
				if m.viewContactDetail {
					m.viewContactDetail = false
				} else if m.editingContact != nil {
					m.editingContact = nil
				} else {
					m.state = viewMainMenu
				}
				return m, nil
			} else if m.state == viewSettings {
				m.state = viewMainMenu
				return m, nil
			}

		case "r":
			// Manual refresh
			if m.state == viewMailboxes {
				m.loading = true
				if m.offlineMode || m.client == nil {
					return m, fetchMailboxesOfflineCmd(m.db)
				}
				return m, fetchMailboxesCmd(m.client, m.db)
			} else if m.state == viewEmails && len(m.mailboxes) > 0 {
				m.loading = true
				selectedMB := m.mailboxes[m.mbCursor]
				if m.offlineMode || m.client == nil {
					return m, tea.Batch(fetchMailboxesOfflineCmd(m.db), fetchEmailsOfflineCmd(m.db, selectedMB.ID, 0))
				}
				return m, tea.Batch(fetchMailboxesCmd(m.client, m.db), refreshEmailsCmd(m.client, m.db, selectedMB.ID))
			} else if m.state == viewCalendar && !m.offlineMode && m.client != nil && m.davClient != nil {
				m.loading = true
				var calIDs []string
				for _, cal := range m.calendars {
					if cal.IsVisible && cal.MayReadItems {
						calIDs = append(calIDs, cal.ID)
					}
				}
				return m, fetchEventsCmd(m.davClient, calIDs, m.agendaStart, m.agendaStart.AddDate(0, 0, m.agendaDays))
			} else if m.state == viewContacts && !m.offlineMode && m.client != nil && m.davClient != nil {
				m.loading = true
				abID := ""
				if m.addressBookCursor < len(m.addressBooks) {
					abID = m.addressBooks[m.addressBookCursor].ID
				}
				return m, fetchContactsCmd(m.davClient, abID, 100)
			}

		// Calendar-specific keys
		case "n":
			if m.state == viewCalendar && !m.viewEventDetail && m.editingEvent == nil && !m.offlineMode {
				// Create new event
				m.editingEvent = &model.CalendarEvent{
					Start: time.Now().Truncate(time.Hour).Add(time.Hour),
				}
				// Set default calendar
				for _, cal := range m.calendars {
					if cal.IsDefault && cal.MayAddItems {
						m.editingEvent.CalendarID = cal.ID
						break
					}
				}
				if m.editingEvent.CalendarID == "" && len(m.calendars) > 0 {
					for _, cal := range m.calendars {
						if cal.MayAddItems {
							m.editingEvent.CalendarID = cal.ID
							break
						}
					}
				}
				m.eventInput.SetValue("")
				m.eventInput.Focus()
				return m, textinput.Blink
			} else if m.state == viewContacts && !m.viewContactDetail && m.editingContact == nil && !m.offlineMode {
				// Create new contact
				m.editingContact = &model.Contact{}
				// Set default address book
				for _, ab := range m.addressBooks {
					if ab.IsDefault && ab.MayAddItems {
						m.editingContact.AddressBookID = ab.ID
						break
					}
				}
				if m.editingContact.AddressBookID == "" && len(m.addressBooks) > 0 {
					for _, ab := range m.addressBooks {
						if ab.MayAddItems {
							m.editingContact.AddressBookID = ab.ID
							break
						}
					}
				}
				m.contactInput.SetValue("")
				m.contactInput.Focus()
				m.contactEditField = 0
				return m, textinput.Blink
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case mailboxesLoadedMsg:
		m.mailboxes = msg
		m.loading = false

	case emailsLoadedMsg:
		newEmails := []model.Email(msg)
		if len(newEmails) < 20 {
			m.canLoadMore = false
		} else {
			m.canLoadMore = true
		}
		m.emails = append(m.emails, newEmails...)
		m.loading = false

	case emailBodyLoadedMsg:
		m.bodyContent = msg.body
		m.htmlBody = msg.htmlBody
		m.loading = false
		
		// If we are loading a draft to edit:
		if m.draftID != "" && (m.state == viewComposeTo || m.state == viewEmails) {
			// We came here from selecting a draft
			// Clean up "To" field (remove Name <Email> format to just Email if possible, or leave it)
			// JMAP usually handles Name <Email> in To field ok on sending? 
			// Actually our SendEmail uses Email struct which parses it or expects raw.
			// Ideally we should parse it. For now, leave as is.
			
			// Clean body: Remove [Converted HTML] header if present?
			// Since we want to edit the raw text.
			// The fetchEmailBody returns converted text.
			// Ideally we want raw textBody from API.
			// Current API `FetchEmailBody` tries text then html->text.
			body := msg.body
			if strings.HasPrefix(body, "[Converted HTML]\n") {
				body = strings.TrimPrefix(body, "[Converted HTML]\n")
			}
			m.composeBody = body
			
			// Determine where to focus
			if m.inputTo.Value() == "" {
				m.state = viewComposeTo
				m.inputTo.Focus()
			} else {
				m.state = viewComposeSubject
				m.inputTo.Blur()
				m.inputSubject.Focus()
			}
		}

	case editorFinishedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		content, err := ioutil.ReadFile(m.tempFile)
		if err != nil {
			m.err = err
			return m, nil
		}
		m.composeBody = string(content)
		m.state = viewComposeConfirm
		return m, nil

	case emailDeletedMsg:
		m.loading = false
		return m, nil

	case errorMsg:
		m.err = msg
		m.loading = false
	}

	return m, nil
}

// filterContacts returns contacts matching the search query
func filterContacts(contacts []model.Contact, query string) []model.Contact {
	if query == "" {
		return nil
	}
	query = strings.ToLower(query)
	var matches []model.Contact
	for _, c := range contacts {
		// Match against name or email
		if strings.Contains(strings.ToLower(c.FullName), query) {
			matches = append(matches, c)
			continue
		}
		for _, e := range c.Emails {
			if strings.Contains(strings.ToLower(e.Email), query) {
				matches = append(matches, c)
				break
			}
		}
		if len(matches) >= 5 { // Limit suggestions
			break
		}
	}
	return matches
}

// Helper to make links clickable (OSC 8)
// shortenURL returns a display-friendly shortened URL
func shortenURL(url string, maxLen int) string {
	if len(url) <= maxLen {
		return url
	}
	
	// Try to extract domain and show domain + "..."
	// e.g., https://click.redditmail.com/CL0/https%3A... -> click.redditmail.com/...
	re := regexp.MustCompile(`^(https?://)([^/]+)(.*)$`)
	matches := re.FindStringSubmatch(url)
	if matches != nil {
		domain := matches[2]
		path := matches[3]
		
		// If domain alone is short enough, show domain + truncated path
		if len(domain) < maxLen-4 {
			remaining := maxLen - len(domain) - 4 // 4 for "..." and "/"
			if remaining > 0 && len(path) > 0 {
				if len(path) > remaining {
					return domain + path[:remaining] + "..."
				}
				return domain + path
			}
			return domain + "/..."
		}
		return domain[:maxLen-3] + "..."
	}
	
	return url[:maxLen-3] + "..."
}

func linkify(text string) string {
	// 1. Convert Markdown links: [Title](URL) -> OSC 8 link
	reMD := regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)]+)\)`)
	text = reMD.ReplaceAllString(text, "\x1b]8;;$2\x1b\\$1\x1b]8;;\x1b\\")

	// 2. Convert Bare URLs: https://google.com -> OSC 8 link with shortened display
	if strings.Contains(text, "[Converted HTML]") {
		return text
	}

	// Plain text mode: Wrap all bare URLs with shortened display text
	reURL := regexp.MustCompile(`(https?://[^\s()<>"]+)`)
	text = reURL.ReplaceAllStringFunc(text, func(url string) string {
		display := shortenURL(url, 50)
		return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, display)
	})
	return text
}

// htmlToText converts HTML to readable plain text using a proper parser
func htmlToText(htmlContent string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return stripHTMLFallback(htmlContent)
	}
	
	var buf strings.Builder
	var extractText func(*html.Node)
	
	// Helper to get attribute value
	getAttr := func(n *html.Node, key string) string {
		for _, attr := range n.Attr {
			if attr.Key == key {
				return attr.Val
			}
		}
		return ""
	}
	
	extractText = func(n *html.Node) {
		// Skip style, script, head elements entirely
		if n.Type == html.ElementNode {
			switch n.Data {
			case "style", "script", "head", "noscript":
				return
			case "br":
				buf.WriteString("\n")
				return
			case "p", "div", "tr", "li", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote":
				buf.WriteString("\n")
			case "td", "th":
				buf.WriteString(" ")
			case "a":
				// Handle links - extract text and href
				href := getAttr(n, "href")
				if href != "" && strings.HasPrefix(href, "http") {
					// Get link text
					var linkText strings.Builder
					var extractLinkText func(*html.Node)
					extractLinkText = func(ln *html.Node) {
						if ln.Type == html.TextNode {
							text := strings.TrimSpace(ln.Data)
							if text != "" {
								linkText.WriteString(text)
							}
						}
						for c := ln.FirstChild; c != nil; c = c.NextSibling {
							extractLinkText(c)
						}
					}
					extractLinkText(n)
					
					text := strings.TrimSpace(linkText.String())
					if text != "" && text != href && !strings.HasPrefix(text, "http") {
						// Show as "text (shortened_url)"
						buf.WriteString(text)
						buf.WriteString(" (")
						buf.WriteString(shortenURL(href, 40))
						buf.WriteString(") ")
					} else {
						// Just show shortened URL
						buf.WriteString(shortenURL(href, 50))
						buf.WriteString(" ")
					}
					return // Don't process children again
				}
			}
		}
		
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				buf.WriteString(text)
				buf.WriteString(" ")
			}
		}
		
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extractText(c)
		}
		
		// Add newline after block elements
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "tr", "li", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote":
				buf.WriteString("\n")
			}
		}
	}
	
	extractText(doc)
	
	result := buf.String()
	
	// Clean up whitespace
	reSpaces := regexp.MustCompile(`[ \t]+`)
	result = reSpaces.ReplaceAllString(result, " ")
	reNewlines := regexp.MustCompile(`\n[ \t]+`)
	result = reNewlines.ReplaceAllString(result, "\n")
	reMultiNewlines := regexp.MustCompile(`\n{3,}`)
	result = reMultiNewlines.ReplaceAllString(result, "\n\n")
	
	// Remove zero-width characters often used in spam
	result = strings.ReplaceAll(result, "\u200b", "") // zero-width space
	result = strings.ReplaceAll(result, "\u200c", "") // zero-width non-joiner
	result = strings.ReplaceAll(result, "\u200d", "") // zero-width joiner
	result = strings.ReplaceAll(result, "\ufeff", "") // BOM
	
	return strings.TrimSpace(result)
}

// stripHTMLFallback is a simple regex-based fallback
func stripHTMLFallback(htmlContent string) string {
	// Remove style and script blocks entirely
	reStyle := regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	htmlContent = reStyle.ReplaceAllString(htmlContent, "")
	reScript := regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	htmlContent = reScript.ReplaceAllString(htmlContent, "")
	
	// Replace common block elements with newlines
	reBlock := regexp.MustCompile(`(?i)</(p|div|tr|li|h[1-6])>`)
	htmlContent = reBlock.ReplaceAllString(htmlContent, "\n")
	reBr := regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlContent = reBr.ReplaceAllString(htmlContent, "\n")
	
	// Remove all remaining tags
	reTags := regexp.MustCompile(`<[^>]+>`)
	htmlContent = reTags.ReplaceAllString(htmlContent, "")
	
	// Decode common HTML entities
	htmlContent = html.UnescapeString(htmlContent)
	
	// Collapse whitespace
	reSpaces := regexp.MustCompile(`[ \t]+`)
	htmlContent = reSpaces.ReplaceAllString(htmlContent, " ")
	reNewlines := regexp.MustCompile(`\n{3,}`)
	htmlContent = reNewlines.ReplaceAllString(htmlContent, "\n\n")
	
	return strings.TrimSpace(htmlContent)
}

// renderEmailBody renders the email body, converting HTML to plain text
func renderEmailBody(textBody, htmlBody string, width int) string {
	// Check if textBody looks like HTML (contains HTML tags)
	isHTMLBody := strings.Contains(textBody, "<html") || 
		strings.Contains(textBody, "<table") || 
		strings.Contains(textBody, "<div") ||
		strings.Contains(textBody, "<td") ||
		strings.Contains(textBody, "<!DOCTYPE")
	
	// If we have clean text body (not HTML), prefer it
	if textBody != "" && !strings.HasPrefix(textBody, "[Converted HTML]") && !isHTMLBody {
		return linkify(textBody)
	}
	
	// For HTML content, convert to plain text
	// Try htmlBody first, then textBody if it's HTML
	contentToConvert := htmlBody
	if contentToConvert == "" && isHTMLBody {
		contentToConvert = textBody
	}
	
	if contentToConvert != "" {
		text := htmlToText(contentToConvert)
		if text != "" {
			return linkify(text)
		}
	}
	
	// Fall back to text body with linkify (even if it's converted HTML)
	if textBody != "" {
		return linkify(textBody)
	}
	
	return "(No content)"
}

func (m Model) View() string {
	if m.err != nil {
		return fmt.Sprintf("Error: %v\n\nPress any key to continue...", m.err)
	}

	s := strings.Builder{}
	s.WriteString(titleStyle.Render("FM-CLI"))
	s.WriteString(" ")

	// Show offline indicator
	if m.offlineMode {
		s.WriteString("[OFFLINE] ")
	}

	// Breadcrumbs based on state
	switch m.state {
	case viewMailboxes, viewEmails, viewBody, viewComposeTo, viewComposeSubject, viewComposeConfirm:
		s.WriteString("> Mail")
		if (m.state == viewEmails || m.state == viewBody) && len(m.mailboxes) > 0 {
			mb := m.mailboxes[m.mbCursor]
			s.WriteString(fmt.Sprintf(" > %s", mb.Name))
		}
	case viewCalendar:
		s.WriteString("> Calendar")
	case viewContacts:
		s.WriteString("> Contacts")
	case viewSettings:
		s.WriteString("> Settings")
	}
	s.WriteString("\n\n")

	// Global shortcuts hint
	if m.state != viewMainMenu && m.state != viewComposeTo && m.state != viewComposeSubject && m.state != viewComposeConfirm {
		s.WriteString("(1: Mail  2: Calendar  3: Contacts  4: Settings  0: Menu)\n\n")
	}

	if m.state == viewMainMenu {
		s.WriteString("Welcome to FM-CLI\n\n")
		for i, item := range mainMenuItems {
			cursor := " "
			style := mailboxStyle
			if i == m.menuCursor {
				cursor = ">"
				style = selectedMailboxStyle
			}
			label := fmt.Sprintf("%s [%s] %s", cursor, item.Shortcut, item.Name)
			s.WriteString(style.Render(label) + "\n")
		}
		s.WriteString("\n(j/k navigate, enter to select, q to quit)")

	} else if m.state == viewMailboxes {
		if m.loading {
			s.WriteString("Loading mailboxes...")
		} else if len(m.mailboxes) == 0 {
			s.WriteString("No mailboxes found.")
		}
		for i, mb := range m.mailboxes {
			cursor := " "
			style := mailboxStyle

			if i == m.mbCursor {
				cursor = ">"
				style = selectedMailboxStyle
			}

			label := fmt.Sprintf("%s %s (%d)", cursor, mb.Name, mb.UnreadCount)
			s.WriteString(style.Render(label) + "\n")
		}
		s.WriteString("\n(j/k navigate, enter/l open, r: refresh, c: compose)")

	} else if m.state == viewEmails {
		if m.loading {
			s.WriteString("Loading emails using JMAP...\n")
		} else if len(m.emails) == 0 {
			s.WriteString("No emails found.")
		} else {
			// Basic Render Loop for Emails
			headerHeight := 5
			footerHeight := 2
			pageHeight := m.height - headerHeight - footerHeight
			if pageHeight < 5 {
				pageHeight = 5
			}

			start := m.emailOffset
			end := start + pageHeight
			if end > len(m.emails) {
				end = len(m.emails)
			}

			for i := start; i < end; i++ {
				e := m.emails[i]
				style := emailItemStyle
				if i == m.emailCursor {
					style = selectedEmailItemStyle
				}

				unreadMarker := " "
				if e.IsUnread {
					unreadMarker = "*"
				}
				
				flagMarker := " "
				if e.IsFlagged {
					flagMarker = "!"
				}

				// Format: * ! [Date] From: Subject
				line := fmt.Sprintf("%s%s [%s] %-20s %s", unreadMarker, flagMarker, e.Date, e.From, e.Subject)

				if e.IsUnread {
					line = unreadStyle.Render(line)
				}

				s.WriteString(style.Render(line) + "\n")
			}
		}
		s.WriteString("\n(h/esc back, j/k navigate, r: refresh, u: read/unread, f: flag, e: archive, d: delete, c: compose)")
	
	} else if m.state == viewBody {
		if m.loading {
			s.WriteString("Loading content...\n")
		} else {
			if len(m.emails) > m.emailCursor {
				e := m.emails[m.emailCursor]
				s.WriteString(fmt.Sprintf("Subject: %s\nFrom:    %s\nDate:    %s\n", e.Subject, e.From, e.Date))

				if m.showDetails {
					if e.To != "" {
						s.WriteString(fmt.Sprintf("To:      %s\n", e.To))
					}
					if e.Cc != "" {
						s.WriteString(fmt.Sprintf("Cc:      %s\n", e.Cc))
					}
					if e.Bcc != "" {
						s.WriteString(fmt.Sprintf("Bcc:     %s\n", e.Bcc))
					}
					if e.ReplyTo != "" {
						s.WriteString(fmt.Sprintf("ReplyTo: %s\n", e.ReplyTo))
					}
					s.WriteString(fmt.Sprintf("ID:      %s\n", e.ID))
					s.WriteString(fmt.Sprintf("Mailboxes: %v\n", e.MailboxIDs))
				}

				s.WriteString("--------------------------------------------------\n\n")
				
				// Render body with glamour for HTML or linkify for plain text
				content := renderEmailBody(m.bodyContent, m.htmlBody, 80)
				s.WriteString(content)
			}
		}
		
		help := "\n\n(h/esc: back, R: reply, A: reply all, F: forward, m: toggle details, b: browser"
		if images.HasGraphicsSupport() {
			help += ", i: images)"
		} else {
			help += ")"
		}
		if len(m.emails) > m.emailCursor && m.emails[m.emailCursor].IsDraft {
			help = "\n\n(h/esc: back, e: edit draft, m: toggle details, b: browser)"
		}
		s.WriteString(help)

	} else if m.state == viewComposeTo {
		s.WriteString("Compose New Email\n\n")
		fromAddr := "(loading...)"
		if len(m.identities) > 0 {
			fromAddr = m.identities[m.identityIdx]
		}
		s.WriteString("From: " + fromAddr + "  [Tab to change]\n")
		s.WriteString("To: " + m.inputTo.View() + "\n")
		
		// Show autocomplete suggestions
		if m.showSuggestions && len(m.toSuggestions) > 0 {
			s.WriteString("\n")
			for i, c := range m.toSuggestions {
				cursor := "  "
				if i == m.toSuggestionIdx {
					cursor = "> "
				}
				email := ""
				if len(c.Emails) > 0 {
					email = c.Emails[0].Email
				}
				if c.FullName != "" {
					s.WriteString(fmt.Sprintf("%s%s <%s>\n", cursor, c.FullName, email))
				} else {
					s.WriteString(fmt.Sprintf("%s%s\n", cursor, email))
				}
			}
			s.WriteString("\n(↑/↓ select, Tab/Enter to use, Esc to dismiss)")
		} else {
			s.WriteString("\n(Enter to continue, Tab to cycle From, Esc to cancel)")
		}

	} else if m.state == viewComposeSubject {
		s.WriteString("Compose New Email\n\n")
		fromAddr := ""
		if len(m.identities) > 0 {
			fromAddr = m.identities[m.identityIdx]
		}
		s.WriteString("From: " + fromAddr + "\n")
		s.WriteString("To: " + m.inputTo.Value() + "\n")
		s.WriteString("Subject: " + m.inputSubject.View() + "\n")
		s.WriteString("\n(Enter to write body in $EDITOR, Tab to cycle From, Esc to back)")

	} else if m.state == viewComposeConfirm {
		s.WriteString("Confirm Send?\n\n")
		fromAddr := ""
		if len(m.identities) > 0 {
			fromAddr = m.identities[m.identityIdx]
		}
		s.WriteString("From: " + fromAddr + "\n")
		s.WriteString("To: " + m.inputTo.Value() + "\n")
		s.WriteString("Subject: " + m.inputSubject.Value() + "\n")
		s.WriteString("Body Preview:\n")
		
		preview := m.composeBody
		if len(preview) > 100 {
			preview = preview[:100] + "..."
		}
		s.WriteString(preview + "\n")
		
		if m.loading {
			s.WriteString("\nSENDING...\n")
		} else {
			s.WriteString("\n(y) Send  (s) Save Draft  (n) Cancel  (e) Edit Body  (Tab) Change From")
		}

	} else if m.state == viewCalendar {
		s.WriteString("Calendar - Agenda View\n\n")
		
		if m.loading {
			s.WriteString("Loading calendar...")
		} else if m.editingEvent != nil {
			// Editing/Creating event
			if m.editingEvent.ID == "" {
				s.WriteString("Create New Event\n\n")
			} else {
				s.WriteString("Edit Event\n\n")
			}
			s.WriteString(fmt.Sprintf("Title: %s\n", m.eventInput.View()))
			s.WriteString(fmt.Sprintf("Date: %s\n", m.editingEvent.Start.Format("2006-01-02")))
			s.WriteString(fmt.Sprintf("Time: %s\n", m.editingEvent.Start.Format("15:04")))
			if m.editingEvent.Duration != "" {
				s.WriteString(fmt.Sprintf("Duration: %s\n", m.editingEvent.Duration))
			}
			s.WriteString(fmt.Sprintf("Location: %s\n", m.editingEvent.Location))
			s.WriteString("\n(enter: save, esc: cancel)")
		} else if m.viewEventDetail && m.eventCursor < len(m.events) {
			// Viewing event details
			e := m.events[m.eventCursor]
			s.WriteString(fmt.Sprintf("Title: %s\n\n", e.Title))
			s.WriteString(fmt.Sprintf("Date: %s\n", e.Start.Format("Monday, January 2, 2006")))
			if e.IsAllDay {
				s.WriteString("Time: All Day\n")
			} else {
				s.WriteString(fmt.Sprintf("Time: %s - %s\n", e.Start.Format("15:04"), e.End.Format("15:04")))
			}
			if e.Location != "" {
				s.WriteString(fmt.Sprintf("Location: %s\n", e.Location))
			}
			if e.Description != "" {
				s.WriteString(fmt.Sprintf("\nDescription:\n%s\n", e.Description))
			}
			if len(e.Participants) > 0 {
				s.WriteString("\nParticipants:\n")
				for _, p := range e.Participants {
					status := ""
					if p.Status != "" {
						status = fmt.Sprintf(" (%s)", p.Status)
					}
					s.WriteString(fmt.Sprintf("  - %s <%s>%s\n", p.Name, p.Email, status))
				}
			}
			s.WriteString("\n(e: edit, d: delete, esc: back)")
		} else if len(m.events) == 0 && len(m.calendars) > 0 {
			s.WriteString("No events in the next " + fmt.Sprintf("%d", m.agendaDays) + " days.\n")
			s.WriteString("\n(n: new event, r: refresh, esc: back)")
		} else if len(m.calendars) == 0 {
			if m.offlineMode {
				s.WriteString("Calendar is not available in offline mode.\n")
			} else if m.davClient == nil {
				s.WriteString("Calendar requires an app password to be configured.\n")
				s.WriteString("Set FM_APP_PASSWORD environment variable.\n")
			} else {
				s.WriteString("No calendars found. Make sure you have calendars in Fastmail.\n")
			}
			s.WriteString("\n(r: refresh, esc: back)")
		} else {
			// Agenda view
			today := time.Now().Truncate(24 * time.Hour)
			currentDate := time.Time{}
			
			for i, e := range m.events {
				eventDate := e.Start.Truncate(24 * time.Hour)
				
				// Print date header if new day
				if eventDate != currentDate {
					currentDate = eventDate
					dateStr := eventDate.Format("Monday, January 2")
					if eventDate.Equal(today) {
						dateStr += " (Today)"
					} else if eventDate.Equal(today.AddDate(0, 0, 1)) {
						dateStr += " (Tomorrow)"
					}
					s.WriteString("\n" + dateStr + "\n")
					s.WriteString(strings.Repeat("-", len(dateStr)) + "\n")
				}
				
				// Event line
				cursor := " "
				style := emailItemStyle
				if i == m.eventCursor {
					cursor = ">"
					style = selectedEmailItemStyle
				}
				
				timeStr := e.Start.Format("15:04")
				if e.IsAllDay {
					timeStr = "All Day"
				}
				
				line := fmt.Sprintf("%s %s  %s", cursor, timeStr, e.Title)
				if e.Location != "" {
					line += fmt.Sprintf(" @ %s", e.Location)
				}
				s.WriteString(style.Render(line) + "\n")
			}
			s.WriteString("\n(j/k navigate, enter: view, n: new, d: delete, r: refresh)")
		}

	} else if m.state == viewContacts {
		s.WriteString("Contacts\n\n")
		
		if m.loading {
			s.WriteString("Loading contacts...")
		} else if m.editingContact != nil {
			// Editing/Creating contact
			if m.editingContact.ID == "" {
				s.WriteString("Create New Contact\n\n")
			} else {
				s.WriteString("Edit Contact\n\n")
			}
			
			fields := []struct {
				label string
				value string
			}{
				{"Full Name", m.editingContact.FullName},
				{"Email", ""},
				{"Phone", ""},
				{"Company", m.editingContact.Company},
				{"Notes", m.editingContact.Notes},
			}
			if len(m.editingContact.Emails) > 0 {
				fields[1].value = m.editingContact.Emails[0].Email
			}
			if len(m.editingContact.Phones) > 0 {
				fields[2].value = m.editingContact.Phones[0].Number
			}
			
			for i, f := range fields {
				marker := " "
				if i == m.contactEditField {
					marker = ">"
					s.WriteString(fmt.Sprintf("%s %s: %s\n", marker, f.label, m.contactInput.View()))
				} else {
					s.WriteString(fmt.Sprintf("%s %s: %s\n", marker, f.label, f.value))
				}
			}
			s.WriteString("\n(tab: next field, enter: save, esc: cancel)")
		} else if m.viewContactDetail && m.contactCursor < len(m.contacts) {
			// Viewing contact details
			c := m.contacts[m.contactCursor]
			s.WriteString(fmt.Sprintf("Name: %s\n\n", c.FullName))
			if c.Nickname != "" {
				s.WriteString(fmt.Sprintf("Nickname: %s\n", c.Nickname))
			}
			if c.Company != "" || c.JobTitle != "" {
				s.WriteString(fmt.Sprintf("Work: %s - %s\n", c.Company, c.JobTitle))
			}
			
			if len(c.Emails) > 0 {
				s.WriteString("\nEmails:\n")
				for _, e := range c.Emails {
					s.WriteString(fmt.Sprintf("  %s: %s\n", e.Type, e.Email))
				}
			}
			
			if len(c.Phones) > 0 {
				s.WriteString("\nPhones:\n")
				for _, p := range c.Phones {
					s.WriteString(fmt.Sprintf("  %s: %s\n", p.Type, p.Number))
				}
			}
			
			if len(c.Addresses) > 0 {
				s.WriteString("\nAddresses:\n")
				for _, a := range c.Addresses {
					addr := strings.Join([]string{a.Street, a.City, a.State, a.PostalCode, a.Country}, ", ")
					addr = strings.Trim(strings.ReplaceAll(addr, ", , ", ", "), ", ")
					s.WriteString(fmt.Sprintf("  %s: %s\n", a.Type, addr))
				}
			}
			
			if c.Birthday != "" {
				s.WriteString(fmt.Sprintf("\nBirthday: %s\n", c.Birthday))
			}
			
			if c.Notes != "" {
				s.WriteString(fmt.Sprintf("\nNotes:\n%s\n", c.Notes))
			}
			s.WriteString("\n(e: edit, d: delete, esc: back)")
		} else if len(m.contacts) == 0 && len(m.addressBooks) > 0 {
			s.WriteString("No contacts found.\n")
			s.WriteString("\n(n: new contact, r: refresh, esc: back)")
		} else if len(m.addressBooks) == 0 {
			if m.offlineMode {
				s.WriteString("Contacts are not available in offline mode.\n")
			} else if m.davClient == nil {
				s.WriteString("Contacts require an app password to be configured.\n")
				s.WriteString("Set FM_APP_PASSWORD environment variable.\n")
			} else {
				s.WriteString("No address books found.\n")
			}
			s.WriteString("\n(r: refresh, esc: back)")
		} else {
			// Contact list with count
			s.WriteString(fmt.Sprintf("Showing %d contacts\n\n", len(m.contacts)))
			
			// Calculate visible window
			headerHeight := 4
			footerHeight := 2
			pageHeight := m.height - headerHeight - footerHeight
			if pageHeight < 5 {
				pageHeight = 10
			}
			
			startIdx := 0
			if m.contactCursor >= pageHeight {
				startIdx = m.contactCursor - pageHeight + 1
			}
			endIdx := startIdx + pageHeight
			if endIdx > len(m.contacts) {
				endIdx = len(m.contacts)
			}
			
			for i := startIdx; i < endIdx; i++ {
				c := m.contacts[i]
				cursor := "  "
				style := emailItemStyle
				if i == m.contactCursor {
					cursor = "> "
					style = selectedEmailItemStyle
				}
				
				// Format: Name <email> | phone
				line := c.FullName
				if line == "" {
					line = "(No name)"
				}
				if len(c.Emails) > 0 {
					line += fmt.Sprintf("  <%s>", c.Emails[0].Email)
				}
				if len(c.Phones) > 0 {
					line += fmt.Sprintf("  %s", c.Phones[0].Number)
				}
				
				s.WriteString(style.Render(cursor + line) + "\n")
			}
			s.WriteString("\n(j/k: navigate, enter: view, n: new, d: delete, r: refresh, esc: back)")
		}

	} else if m.state == viewSettings {
		s.WriteString("Settings\n\n")
		
		offlineStatus := "OFF"
		if m.offlineMode {
			offlineStatus = "ON"
		}
		
		settings := []string{
			fmt.Sprintf("  Offline Mode: %s", offlineStatus),
		}
		
		for i, setting := range settings {
			cursor := " "
			if i == m.settingsCursor {
				cursor = ">"
			}
			s.WriteString(fmt.Sprintf("%s%s\n", cursor, setting))
		}
		
		s.WriteString("\n(enter to toggle, 0: back to menu)")
	}

	return appStyle.Render(s.String())
}

// Commands
func saveDraftCmd(client *api.Client, draftID, from, to, subject, body string) tea.Cmd {
	return func() tea.Msg {
		err := client.SaveDraft(draftID, from, to, subject, body)
		if err != nil {
			return errorMsg(err)
		}
		return draftSavedMsg{}
	}
}

func sendEmailCmd(client *api.Client, draftID, from, to, subject, body string) tea.Cmd {
	return func() tea.Msg {
		err := client.SendEmail(draftID, from, to, subject, body)
		if err != nil {
			return errorMsg(err)
		}
		return emailSentMsg{}
	}
}

func moveEmailCmd(client *api.Client, emailID, fromMBID, toMBID string) tea.Cmd {
	return func() tea.Msg {
		err := client.MoveEmail(emailID, fromMBID, toMBID)
		if err != nil {
			return errorMsg(err)
		}
		return emailDeletedMsg{} // Reuse deleted msg to clear loading state
	}
}

func deleteEmailCmd(client *api.Client, emailID string) tea.Cmd {
	return func() tea.Msg {
		err := client.DeleteEmail(emailID)
		if err != nil {
			return errorMsg(err)
		}
		return emailDeletedMsg{}
	}
}

func toggleUnreadCmd(client *api.Client, emailID string, isUnread bool) tea.Cmd {
	return func() tea.Msg {
		err := client.SetUnread(emailID, isUnread)
		if err != nil {
			return errorMsg(err)
		}
		return nil
	}
}

func toggleFlaggedCmd(client *api.Client, emailID string, isFlagged bool) tea.Cmd {
	return func() tea.Msg {
		err := client.SetFlagged(emailID, isFlagged)
		if err != nil {
			return errorMsg(err)
		}
		return nil
	}
}

func fetchMailboxesCmd(client *api.Client, db *storage.DB) tea.Cmd {
	return func() tea.Msg {
		mbs, err := client.FetchMailboxes()
		if err != nil {
			return errorMsg(err)
		}
		// Save to local storage if available
		if db != nil {
			db.SaveMailboxes(mbs)
		}
		return mailboxesLoadedMsg(mbs)
	}
}

func fetchMailboxesOfflineCmd(db *storage.DB) tea.Cmd {
	return func() tea.Msg {
		if db == nil {
			return errorMsg(fmt.Errorf("no local storage available"))
		}
		mbs, err := db.GetMailboxes()
		if err != nil {
			return errorMsg(err)
		}
		return mailboxesLoadedMsg(mbs)
	}
}

func fetchIdentitiesCmd(client *api.Client) tea.Cmd {
	return func() tea.Msg {
		identities, err := client.GetIdentities()
		if err != nil {
			return errorMsg(err)
		}
		var emails []string
		for _, id := range identities {
			emails = append(emails, id.Email)
		}
		return identitiesLoadedMsg(emails)
	}
}

func fetchEmailsCmd(client *api.Client, db *storage.DB, mailboxID string, offset int) tea.Cmd {
	return func() tea.Msg {
		emails, err := client.FetchEmails(mailboxID, offset)
		if err != nil {
			return errorMsg(err)
		}
		// Save to local storage if available
		if db != nil {
			db.SaveEmails(emails)
			// Pre-fetch and cache email bodies in background for offline access
			go func() {
				for _, email := range emails {
					// Check if body already cached
					existingBody, _ := db.GetEmailBody(email.ID)
					if existingBody == "" || strings.HasPrefix(existingBody, "[Full email body not cached") || strings.HasPrefix(existingBody, "[Email body not available") {
						body, err := client.FetchEmailBody(email.ID)
						if err == nil && body != "" {
							db.SaveEmailBody(email.ID, body)
						}
					}
				}
			}()
		}
		return emailsLoadedMsg(emails)
	}
}

func fetchEmailsOfflineCmd(db *storage.DB, mailboxID string, offset int) tea.Cmd {
	return func() tea.Msg {
		if db == nil {
			return errorMsg(fmt.Errorf("no local storage available"))
		}
		emails, err := db.GetEmails(mailboxID, offset, 20)
		if err != nil {
			return errorMsg(err)
		}
		return emailsLoadedMsg(emails)
	}
}

func refreshEmailsCmd(client *api.Client, db *storage.DB, mailboxID string) tea.Cmd {
	return func() tea.Msg {
		emails, err := client.FetchEmails(mailboxID, 0)
		if err != nil {
			return errorMsg(err)
		}
		if db != nil {
			db.SaveEmails(emails)
			// Pre-fetch and cache email bodies in background for offline access
			go func() {
				for _, email := range emails {
					// Check if body already cached
					existingBody, _ := db.GetEmailBody(email.ID)
					if existingBody == "" || strings.HasPrefix(existingBody, "[Full email body not cached") || strings.HasPrefix(existingBody, "[Email body not available") {
						body, err := client.FetchEmailBody(email.ID)
						if err == nil && body != "" {
							db.SaveEmailBody(email.ID, body)
						}
					}
				}
			}()
		}
		return emailsRefreshedMsg(emails)
	}
}

func fetchEmailBodyCmd(client *api.Client, db *storage.DB, emailID string) tea.Cmd {
	return func() tea.Msg {
		body, err := client.FetchEmailBody(emailID)
		if err != nil {
			return errorMsg(err)
		}
		// Also fetch HTML body for image rendering
		htmlBody, _ := client.FetchEmailHTMLBody(emailID)
		// Save body to local storage
		if db != nil {
			db.SaveEmailBody(emailID, body)
			if htmlBody != "" {
				db.SaveEmailHTMLBody(emailID, htmlBody)
			}
		}
		return emailBodyLoadedMsg{body: body, htmlBody: htmlBody}
	}
}

func fetchEmailBodyOfflineCmd(db *storage.DB, emailID string) tea.Cmd {
	return func() tea.Msg {
		if db == nil {
			return errorMsg(fmt.Errorf("no local storage available"))
		}
		body, err := db.GetEmailBody(emailID)
		if err != nil {
			return errorMsg(err)
		}
		htmlBody, _ := db.GetEmailHTMLBody(emailID)
		return emailBodyLoadedMsg{body: body, htmlBody: htmlBody}
	}
}

func openInBrowserCmd(htmlBody string) tea.Cmd {
	return func() tea.Msg {
		err := images.OpenHTMLInBrowser(htmlBody)
		if err != nil {
			return errorMsg(err)
		}
		return browserOpenedMsg{}
	}
}

func renderImagesCmd(htmlBody string) tea.Cmd {
	return func() tea.Msg {
		// Check for graphics support
		if !images.HasGraphicsSupport() {
			// Fall back to browser
			err := images.OpenHTMLInBrowser(htmlBody)
			if err != nil {
				return errorMsg(err)
			}
			return browserOpenedMsg{}
		}

		// Extract and render images
		imgs := images.ExtractImagesFromHTML(htmlBody)
		if len(imgs) == 0 {
			return errorMsg(fmt.Errorf("no images found in email"))
		}

		// Render each image
		for _, img := range imgs {
			if img.URL != "" && !strings.HasPrefix(img.URL, "cid:") {
				rendered, err := images.RenderImageFromURL(img.URL, 80, 40)
				if err == nil && rendered != "" {
					fmt.Print(rendered)
				}
			}
		}
		return browserOpenedMsg{}
	}
}

func saveDraftOfflineCmd(db *storage.DB, from, to, subject, body string) tea.Cmd {
	return func() tea.Msg {
		if db == nil {
			return errorMsg(fmt.Errorf("no local storage available"))
		}
		// Generate a local ID
		localID := fmt.Sprintf("local-%d", time.Now().UnixNano())
		err := db.SaveLocalDraft(localID, from, to, subject, body)
		if err != nil {
			return errorMsg(err)
		}
		// Queue for sync
		data, _ := json.Marshal(map[string]string{
			"from": from, "to": to, "subject": subject, "body": body,
		})
		db.AddPendingAction("save_draft", localID, string(data))
		return draftSavedMsg{}
	}
}

// Calendar Commands (using CalDAV)
func fetchCalendarsCmd(davClient *api.DAVClient) tea.Cmd {
	return func() tea.Msg {
		if davClient == nil {
			return errorMsg(fmt.Errorf("CalDAV not configured. Run 'fm-cli login' with app password"))
		}
		calendars, err := davClient.FetchCalendars(context.Background())
		if err != nil {
			return errorMsg(err)
		}
		return calendarsLoadedMsg(calendars)
	}
}

func fetchEventsCmd(davClient *api.DAVClient, calendarPaths []string, start, end time.Time) tea.Cmd {
	return func() tea.Msg {
		if davClient == nil {
			return errorMsg(fmt.Errorf("CalDAV not configured"))
		}
		events, err := davClient.FetchEvents(context.Background(), calendarPaths, start, end)
		if err != nil {
			return errorMsg(err)
		}
		return eventsLoadedMsg(events)
	}
}

func createEventCmd(davClient *api.DAVClient, event model.CalendarEvent) tea.Cmd {
	return func() tea.Msg {
		if davClient == nil {
			return errorMsg(fmt.Errorf("CalDAV not configured"))
		}
		_, err := davClient.CreateEvent(context.Background(), event)
		if err != nil {
			return errorMsg(err)
		}
		return eventCreatedMsg{}
	}
}

func updateEventCmd(davClient *api.DAVClient, event model.CalendarEvent) tea.Cmd {
	return func() tea.Msg {
		if davClient == nil {
			return errorMsg(fmt.Errorf("CalDAV not configured"))
		}
		err := davClient.UpdateEvent(context.Background(), event)
		if err != nil {
			return errorMsg(err)
		}
		return eventCreatedMsg{} // Reuse created msg to trigger refresh
	}
}

func deleteEventCmd(davClient *api.DAVClient, eventPath string) tea.Cmd {
	return func() tea.Msg {
		if davClient == nil {
			return errorMsg(fmt.Errorf("CalDAV not configured"))
		}
		err := davClient.DeleteEvent(context.Background(), eventPath)
		if err != nil {
			return errorMsg(err)
		}
		return eventDeletedMsg{}
	}
}

// Contacts Commands (using CardDAV)
func fetchAddressBooksCmd(davClient *api.DAVClient) tea.Cmd {
	return func() tea.Msg {
		if davClient == nil {
			return errorMsg(fmt.Errorf("CardDAV not configured. Run 'fm-cli login' with app password"))
		}
		addressBooks, err := davClient.FetchAddressBooks(context.Background())
		if err != nil {
			return errorMsg(err)
		}
		return addressBooksLoadedMsg(addressBooks)
	}
}

func fetchContactsCmd(davClient *api.DAVClient, addressBookPath string, limit int) tea.Cmd {
	return func() tea.Msg {
		if davClient == nil {
			return errorMsg(fmt.Errorf("CardDAV not configured"))
		}
		contacts, err := davClient.FetchContacts(context.Background(), addressBookPath, limit)
		if err != nil {
			return errorMsg(err)
		}
		return contactsLoadedMsg(contacts)
	}
}

func createContactCmd(davClient *api.DAVClient, contact model.Contact) tea.Cmd {
	return func() tea.Msg {
		if davClient == nil {
			return errorMsg(fmt.Errorf("CardDAV not configured"))
		}
		_, err := davClient.CreateContact(context.Background(), contact)
		if err != nil {
			return errorMsg(err)
		}
		return contactCreatedMsg{}
	}
}

func updateContactCmd(davClient *api.DAVClient, contact model.Contact) tea.Cmd {
	return func() tea.Msg {
		if davClient == nil {
			return errorMsg(fmt.Errorf("CardDAV not configured"))
		}
		err := davClient.UpdateContact(context.Background(), contact)
		if err != nil {
			return errorMsg(err)
		}
		return contactCreatedMsg{} // Reuse created msg to trigger refresh
	}
}

func deleteContactCmd(davClient *api.DAVClient, contactPath string) tea.Cmd {
	return func() tea.Msg {
		if davClient == nil {
			return errorMsg(fmt.Errorf("CardDAV not configured"))
		}
		err := davClient.DeleteContact(context.Background(), contactPath)
		if err != nil {
			return errorMsg(err)
		}
		return contactDeletedMsg{}
	}
}
