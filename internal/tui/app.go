package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"fm-cli/internal/api"
	"fm-cli/internal/model"
	"fm-cli/internal/storage"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
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
	viewSearch // Global email search
	viewAbout
)

// MainMenuItem represents an option in the main menu
type MainMenuItem struct {
	Name     string
	Shortcut string
	State    sessionState
}

// msg types
type mailboxesLoadedMsg []model.Mailbox
type emailsLoadedMsg []model.Email
type emailsRefreshedMsg []model.Email // For refresh without appending
type searchResultsMsg []model.Email   // For global search results
type emailBodyLoadedMsg struct {
	body     string
	htmlBody string
}
type editorFinishedMsg struct{ err error }
type emailSentMsg struct{}
type draftSavedMsg struct{}
type emailDeletedMsg struct{}
type identitiesLoadedMsg struct {
	emails     []string
	defaultIdx int // index of the preferred sending identity
}
type calendarsLoadedMsg []model.Calendar
type eventsLoadedMsg []model.CalendarEvent
type addressBooksLoadedMsg []model.AddressBook
type contactsLoadedMsg []model.Contact
type eventCreatedMsg struct{}
type eventDeletedMsg struct{}
type contactCreatedMsg struct{}
type contactDeletedMsg struct{}
type browserOpenedMsg struct{}
type errorMsg error

// OpenEmailMsg asks the TUI to show one email: by email id, or the newest
// email of a thread when only the thread id is known. main sends it for the
// --thread/--email flags and for hand-offs from a second `fm-cli tui --remote`.
type OpenEmailMsg struct {
	ThreadID string
	EmailID  string
}

type emailTargetLoadedMsg struct {
	emails []model.Email
	cursor int
}

// Main menu items
var mainMenuItems = []MainMenuItem{
	{Name: "Mail", Shortcut: "1", State: viewMailboxes},
	{Name: "Search Mail", Shortcut: "/", State: viewSearch},
	{Name: "Calendar", Shortcut: "2", State: viewCalendar},
	{Name: "Contacts", Shortcut: "3", State: viewContacts},
	{Name: "Settings", Shortcut: "4", State: viewSettings},
	{Name: "About", Shortcut: "5", State: viewAbout},
}

// Model implementation
type Model struct {
	client    *api.Client
	davClient *api.DAVClient
	db        *storage.DB
	state     sessionState

	// Offline mode
	offlineMode bool

	// A thread or email to open on start (from --thread / --email).
	initialTarget *OpenEmailMsg

	// Main Menu
	menuCursor int

	// Mailbox View Data
	mailboxes []model.Mailbox
	mbCursor  int
	mbOffset  int

	// Email View Data
	emails      []model.Email
	emailCursor int
	emailOffset int
	loading     bool
	canLoadMore bool // If true, hitting bottom loads more

	// Body View Data
	openEmail     model.Email  // The email shown in the reader
	bodyReturn    sessionState // Where Esc goes from the reader
	bodyContent   string
	htmlBody      string   // Raw HTML for image rendering
	bodyLines     []string // Rendered reader lines, rebuilt on load/resize
	showDetails   bool     // Toggle expanded headers
	bodyScrollPos int

	// Composition Data
	inputTo         textinput.Model
	inputSubject    textinput.Model
	composeBody     string
	tempFile        string
	draftID         string          // If editing a draft
	composeReturn   sessionState    // Where cancelling the composer goes
	identities      []string        // Available sending identities (email addresses)
	identityIdx     int             // Currently selected identity index
	toSuggestions   []model.Contact // Autocomplete suggestions for To field
	toSuggestionIdx int             // Selected suggestion index
	showSuggestions bool            // Whether to show suggestions dropdown

	// Calendar Data
	calendars       []model.Calendar
	events          []model.CalendarEvent
	eventCursor     int
	eventOffset     int                  // Scroll offset (in agenda lines)
	agendaStart     time.Time            // Start of agenda view (usually today)
	agendaDays      int                  // Number of days to show (default 7)
	viewEventDetail bool                 // Viewing event details
	editingEvent    *model.CalendarEvent // Event being created/edited
	eventInput      textinput.Model

	// Contacts Data
	addressBooks      []model.AddressBook
	contacts          []model.Contact
	contactCursor     int
	contactOffset     int            // Scroll offset for contacts
	viewContactDetail bool           // Viewing contact details
	editingContact    *model.Contact // Contact being created/edited
	contactInput      textinput.Model
	contactEditField  int // Which field is being edited

	// Search
	searchInput   textinput.Model
	searchActive  bool          // Whether search mode is active
	searchQuery   string        // Current search filter
	searchResults []model.Email // Global search results
	searchCursor  int           // Cursor for search results

	// Settings
	settingsCursor int

	// About
	aboutShowLicense bool
	aboutScroll      int
	aboutStatus      string

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

	tiSearch := textinput.New()
	tiSearch.Placeholder = "Search…"
	tiSearch.Prompt = "/ "

	// A textinput with no width shows only the first rune of its placeholder.
	for _, ti := range []*textinput.Model{&tiTo, &tiSubj, &tiEvent, &tiContact, &tiSearch} {
		ti.Width = 60
	}

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
		searchInput:  tiSearch,
		loading:      false,
		agendaStart:  startOfDay(time.Now()),
		agendaDays:   14,
	}
}

// WithTarget returns the model set to open a thread or email once started.
func (m Model) WithTarget(threadID, emailID string) Model {
	if threadID == "" && emailID == "" {
		return m
	}
	target := OpenEmailMsg{ThreadID: threadID, EmailID: emailID}
	m.initialTarget = &target
	return m
}

func (m Model) Init() tea.Cmd {
	var cmds []tea.Cmd
	// Pre-fetch identities on startup if online
	if !m.offlineMode && m.client != nil {
		cmds = append(cmds, fetchIdentitiesCmd(m.client))
	}
	if m.initialTarget != nil {
		target := *m.initialTarget
		cmds = append(cmds, func() tea.Msg { return target })
	}
	return tea.Batch(cmds...)
}

// selectMailboxOfCurrentEmail points the folder cursor at the folder the
// open email sits in — the Inbox when it is there — so the breadcrumb and a
// later Esc land somewhere sensible after a deep-linked open.
func (m *Model) selectMailboxOfCurrentEmail() {
	if len(m.mailboxes) == 0 {
		return
	}
	inBoxes := map[string]bool{}
	for _, id := range m.openEmail.MailboxIDs {
		inBoxes[id] = true
	}
	choice := -1
	for i, mb := range m.mailboxes {
		if !inBoxes[mb.ID] {
			continue
		}
		if mb.Role == "inbox" {
			choice = i
			break
		}
		if choice == -1 {
			choice = i
		}
	}
	if choice >= 0 {
		m.mbCursor = choice
	}
}

// openTarget resolves an OpenEmailMsg: the thread's emails (or the one email)
// are loaded and the reader opens on the chosen one, with the mailbox list
// fetched alongside so Esc has somewhere to go back to.
func (m Model) openTarget(target OpenEmailMsg) (tea.Model, tea.Cmd) {
	if m.offlineMode || m.client == nil {
		m.err = fmt.Errorf("opening a specific email needs a connection")
		return m, nil
	}
	m.loading = true
	m.err = nil
	cmds := []tea.Cmd{openTargetCmd(m.client, target)}
	if len(m.mailboxes) == 0 {
		cmds = append(cmds, fetchMailboxesCmd(m.client, m.db))
	}
	return m, tea.Batch(cmds...)
}

func openTargetCmd(client *api.Client, target OpenEmailMsg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var emails []model.Email
		var err error
		if target.ThreadID != "" {
			emails, err = client.FetchThreadEmails(ctx, target.ThreadID)
		}
		if err != nil || len(emails) == 0 {
			if target.EmailID == "" {
				if err == nil {
					err = fmt.Errorf("thread %s has no emails", target.ThreadID)
				}
				return errorMsg(err)
			}
			emails, err = client.FetchEmailsByIDs(ctx, []string{target.EmailID})
			if err != nil {
				return errorMsg(err)
			}
			if len(emails) == 0 {
				return errorMsg(fmt.Errorf("email %s not found", target.EmailID))
			}
		}
		cursor := 0
		for i, e := range emails {
			if e.ID == target.EmailID {
				cursor = i
				break
			}
		}
		return emailTargetLoadedMsg{emails: emails, cursor: cursor}
	}
}

// openReader shows e in the reader and starts loading its body; Esc goes
// back to returnTo.
func (m Model) openReader(e model.Email, returnTo sessionState) (Model, tea.Cmd) {
	m.state = viewBody
	m.bodyReturn = returnTo
	m.openEmail = e
	m.loading = true
	m.bodyScrollPos = 0
	m.bodyContent = ""
	m.htmlBody = ""
	m.bodyLines = nil
	if m.offlineMode || m.client == nil {
		return m, fetchEmailBodyOfflineCmd(m.db, e.ID)
	}
	cmds := []tea.Cmd{fetchEmailBodyCmd(m.client, m.db, e.ID)}
	if e.IsUnread {
		// Opening an email marks it read, as in the web client.
		m.openEmail.IsUnread = false
		m.markRead(e.ID)
		cmds = append(cmds, toggleUnreadCmd(m.client, e.ID, false))
	}
	return m, tea.Batch(cmds...)
}

// markRead clears the unread dot on an email wherever it is listed.
func (m *Model) markRead(id string) {
	m.updateEmail(id, func(e *model.Email) { e.IsUnread = false })
	for i := range m.searchResults {
		if m.searchResults[i].ID == id {
			m.searchResults[i].IsUnread = false
		}
	}
}

// refreshMailboxesCmd reloads the folder list from the server, or from the
// local cache when offline.
func (m Model) refreshMailboxesCmd() tea.Cmd {
	if m.offlineMode || m.client == nil {
		return fetchMailboxesOfflineCmd(m.db)
	}
	return fetchMailboxesCmd(m.client, m.db)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	// Global / Async Message Handling (Higher Priority)
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// An error banner is dismissed by the next key press.
		if m.err != nil && msg.Type != tea.KeyCtrlC {
			m.err = nil
			return m, nil
		}

	case editorFinishedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		content, err := os.ReadFile(m.tempFile)
		if err != nil {
			m.err = err
			return m, nil
		}
		m.composeBody = string(content)
		m.state = viewComposeConfirm
		return m, nil

	case mailboxesLoadedMsg:
		m.mailboxes = sortMailboxes(msg)
		m.mbCursor = clampIndex(m.mbCursor, len(m.mailboxes))
		m.loading = false
		if m.state == viewBody {
			m.selectMailboxOfCurrentEmail()
		}
		return m, nil

	case emailsLoadedMsg:
		newEmails := []model.Email(msg)
		m.canLoadMore = len(newEmails) >= 20
		m.emails = append(m.emails, newEmails...)
		m.loading = false
		return m, nil

	case emailsRefreshedMsg:
		// Replace emails instead of appending (for refresh)
		m.emails = []model.Email(msg)
		m.emailOffset = 0
		m.emailCursor = 0
		m.canLoadMore = len(m.emails) >= 20
		m.loading = false
		return m, nil

	case searchResultsMsg:
		m.searchResults = []model.Email(msg)
		m.searchCursor = 0
		m.loading = false
		return m, nil

	case emailBodyLoadedMsg:
		m.bodyContent = msg.body
		m.htmlBody = msg.htmlBody
		m.loading = false
		m.renderBody()
		return m, nil

	case OpenEmailMsg:
		return m.openTarget(msg)

	case emailTargetLoadedMsg:
		m.emails = msg.emails
		m.emailCursor = msg.cursor
		m.emailOffset = 0
		m.canLoadMore = false
		m.searchQuery = ""
		m, cmd = m.openReader(m.emails[m.emailCursor], viewEmails)
		m.selectMailboxOfCurrentEmail()
		return m, cmd

	case identitiesLoadedMsg:
		m.identities = msg.emails
		m.identityIdx = msg.defaultIdx
		return m, nil

	case draftSavedMsg, emailSentMsg:
		m.loading = false
		m.state = viewMailboxes
		os.Remove(m.tempFile)
		return m, m.refreshMailboxesCmd()

	case emailDeletedMsg:
		m.loading = false
		// Refresh mailbox counts after delete
		return m, m.refreshMailboxesCmd()

	case calendarsLoadedMsg:
		m.calendars = msg
		m.loading = false
		// Auto-fetch events for visible calendars
		if cmd := m.fetchAgendaCmd(); cmd != nil {
			m.loading = true
			return m, cmd
		}
		return m, nil

	case eventsLoadedMsg:
		m.events = msg
		m.eventCursor = clampIndex(m.eventCursor, len(m.visibleEvents()))
		m.loading = false
		return m, nil

	case addressBooksLoadedMsg:
		m.addressBooks = msg
		m.loading = false
		// Auto-fetch contacts for default address book
		if ab := m.defaultAddressBookID(); ab != "" && m.davClient != nil {
			m.loading = true
			return m, fetchContactsCmd(m.davClient, ab, 500)
		}
		return m, nil

	case contactsLoadedMsg:
		m.contacts = msg
		m.contactCursor = clampIndex(m.contactCursor, len(m.visibleContacts()))
		m.loading = false
		return m, nil

	case eventCreatedMsg, eventDeletedMsg:
		m.editingEvent = nil
		m.viewEventDetail = false
		m.loading = false
		if cmd := m.fetchAgendaCmd(); cmd != nil {
			m.loading = true
			return m, cmd
		}
		return m, nil

	case contactCreatedMsg, contactDeletedMsg:
		m.editingContact = nil
		m.viewContactDetail = false
		m.loading = false
		if ab := m.defaultAddressBookID(); ab != "" && m.davClient != nil {
			m.loading = true
			return m, fetchContactsCmd(m.davClient, ab, 500)
		}
		return m, nil

	case aboutStatusMsg:
		m.aboutStatus = string(msg)
		return m, nil

	case errorMsg:
		m.err = msg
		m.loading = false
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		inputWidth := min(m.contentWidth()-12, 60)
		for _, ti := range []*textinput.Model{&m.inputTo, &m.inputSubject, &m.eventInput, &m.contactInput, &m.searchInput} {
			ti.Width = inputWidth
		}
		m.renderBody()
		return m, nil
	}

	// Handle Search Mode
	if m.searchActive {
		m.searchInput, cmd = m.searchInput.Update(msg)

		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.Type {
			case tea.KeyEnter:
				query := m.searchInput.Value()
				m.searchActive = false
				m.searchInput.Blur()
				// For viewSearch, perform server-side search
				if m.state == viewSearch && query != "" && m.client != nil {
					m.loading = true
					m.searchQuery = query
					return m, searchEmailsCmd(m.client, query)
				}
				// For other views, just set the local filter
				m.searchQuery = query
				m.resetListCursors()
				return m, nil
			case tea.KeyEsc:
				m.searchActive = false
				m.searchInput.Blur()
				// If in search view with no results, go back to menu
				if m.state == viewSearch && len(m.searchResults) == 0 {
					m.state = viewMainMenu
					m.searchQuery = ""
					m.searchInput.SetValue("")
				}
				return m, nil
			case tea.KeyCtrlC:
				return m, tea.Quit
			}
			// Update filter in real-time as user types (for local filter views only)
			if m.state != viewSearch && m.searchQuery != m.searchInput.Value() {
				m.searchQuery = m.searchInput.Value()
				m.resetListCursors()
			}
		}
		return m, cmd
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
				m.state = m.composeReturn
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
				f, err := os.CreateTemp("", "fm-cli-*.txt")
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
				m.state = m.composeReturn
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
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	if m.state == viewAbout {
		if next, cmd, ok := m.updateAbout(keyMsg); ok {
			return next, cmd
		}
	}

	switch keyMsg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "q":
		// Only quit from main menu
		if m.state == viewMainMenu {
			return m, tea.Quit
		}

	// Global navigation shortcuts (number keys)
	case "0":
		m.state = viewMainMenu
		m.clearFilter()
		return m, nil
	case "1":
		return m.goMail()
	case "2":
		return m.goCalendar()
	case "3":
		return m.goContacts()
	case "4":
		m.state = viewSettings
		m.clearFilter()
		return m, nil
	case "5":
		return m.goAbout()

	case "d", "backspace":
		switch m.state {
		case viewEmails:
			e, ok := m.selectedEmail()
			if !ok {
				break
			}
			if m.offlineMode {
				m.err = fmt.Errorf("cannot delete emails in offline mode")
				return m, nil
			}
			if m.client == nil {
				m.err = fmt.Errorf("not connected")
				return m, nil
			}
			m.loading = true
			m.removeEmail(e.ID) // Optimistic UI update
			return m, deleteEmailCmd(m.client, e.ID)
		case viewCalendar:
			e, ok := m.selectedEvent()
			if ok && !m.offlineMode && m.davClient != nil {
				m.loading = true
				m.removeEvent(e.ID)
				m.viewEventDetail = false
				return m, deleteEventCmd(m.davClient, e.ID)
			}
		case viewContacts:
			c, ok := m.selectedContact()
			if ok && !m.offlineMode && m.davClient != nil {
				m.loading = true
				m.removeContact(c.ID)
				m.viewContactDetail = false
				return m, deleteContactCmd(m.davClient, c.ID)
			}
		}

	case "u":
		if e, ok := m.selectedEmail(); ok && m.state == viewEmails && m.client != nil {
			m.updateEmail(e.ID, func(e *model.Email) { e.IsUnread = !e.IsUnread })
			return m, toggleUnreadCmd(m.client, e.ID, !e.IsUnread)
		}

	case "f":
		if e, ok := m.selectedEmail(); ok && m.state == viewEmails && m.client != nil {
			m.updateEmail(e.ID, func(e *model.Email) { e.IsFlagged = !e.IsFlagged })
			return m, toggleFlaggedCmd(m.client, e.ID, !e.IsFlagged)
		}

	case "e":
		switch {
		case m.state == viewEmails:
			e, ok := m.selectedEmail()
			if !ok || m.client == nil || m.mbCursor >= len(m.mailboxes) {
				break
			}
			archiveID := ""
			for _, mb := range m.mailboxes {
				if mb.Role == "archive" {
					archiveID = mb.ID
					break
				}
			}
			if archiveID == "" {
				m.err = fmt.Errorf("no Archive folder found")
				return m, nil
			}
			m.loading = true
			m.removeEmail(e.ID) // Optimistic UI update
			return m, moveEmailCmd(m.client, e.ID, m.mailboxes[m.mbCursor].ID, archiveID)

		case m.state == viewBody && m.openEmail.IsDraft:
			// Edit the draft being read
			e := m.openEmail
			m, cmd = m.startCompose(e.To, e.Subject, strings.TrimPrefix(m.bodyContent, "[Converted HTML]\n"))
			m.draftID = e.ID
			if m.inputTo.Value() != "" {
				m.state = viewComposeSubject
				m.inputTo.Blur()
				m.inputSubject.Focus()
			}
			return m, cmd

		case m.state == viewCalendar && m.viewEventDetail && !m.offlineMode:
			if event, ok := m.selectedEvent(); ok {
				m.editingEvent = &event
				m.viewEventDetail = false
				m.eventInput.SetValue(event.Title)
				m.eventInput.Focus()
				return m, textinput.Blink
			}

		case m.state == viewContacts && m.viewContactDetail && !m.offlineMode:
			if contact, ok := m.selectedContact(); ok {
				m.editingContact = &contact
				m.viewContactDetail = false
				m.contactInput.SetValue(contact.FullName)
				m.contactInput.Placeholder = "Full Name"
				m.contactInput.Focus()
				m.contactEditField = 0
				return m, textinput.Blink
			}
		}

	case "c":
		return m.startCompose("", "", "")

	case "R", "A": // Reply to sender / reply all
		if m.state == viewBody && m.openEmail.ID != "" {
			e := m.openEmail
			// Use ReplyTo if available, otherwise From
			recipients := []string{e.From}
			if e.ReplyTo != "" {
				recipients[0] = e.ReplyTo
			}
			if keyMsg.String() == "A" {
				if e.To != "" {
					recipients = append(recipients, e.To)
				}
				if e.Cc != "" {
					recipients = append(recipients, e.Cc)
				}
			}
			subject := e.Subject
			if !strings.HasPrefix(strings.ToLower(subject), "re:") {
				subject = "Re: " + subject
			}
			return m.startCompose(strings.Join(recipients, ", "), subject, quoteReply(e, m.bodyContent))
		}

	case "F": // Forward
		if m.state == viewBody && m.openEmail.ID != "" {
			e := m.openEmail
			subject := e.Subject
			lower := strings.ToLower(subject)
			if !strings.HasPrefix(lower, "fwd:") && !strings.HasPrefix(lower, "fw:") {
				subject = "Fwd: " + subject
			}
			body := fmt.Sprintf("\n\n--- Forwarded Message ---\nFrom: %s\nTo: %s\nDate: %s\nSubject: %s\n\n%s",
				e.From, e.To, e.Date, e.Subject, strings.TrimPrefix(m.bodyContent, "[Converted HTML]\n"))
			return m.startCompose("", subject, body)
		}

	case "m":
		if m.state == viewBody {
			m.showDetails = !m.showDetails
			m.renderBody()
			return m, nil
		}
		if m.state == viewMainMenu {
			return m.goMail()
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
		m.moveCursor(-1)
		return m, nil

	case "down", "j":
		return m.moveDown()

	case "pgup", "ctrl+u":
		if m.state == viewBody {
			m.scrollBody(-max(m.bodyViewHeight()-2, 1))
			return m, nil
		}
		// Clear search filter in list views
		if m.searchQuery != "" && (m.state == viewEmails || m.state == viewCalendar || m.state == viewContacts) {
			m.clearFilter()
			return m, nil
		}

	case "pgdown", "ctrl+d", " ":
		if m.state == viewBody {
			m.scrollBody(max(m.bodyViewHeight()-2, 1))
			return m, nil
		}

	case "home", "g":
		if m.state == viewBody {
			m.bodyScrollPos = 0
			return m, nil
		}

	case "end", "G":
		if m.state == viewBody {
			m.scrollBody(len(m.bodyLines))
			return m, nil
		}

	case "enter", "right", "l":
		switch {
		case m.state == viewMainMenu:
			switch mainMenuItems[m.menuCursor].State {
			case viewMailboxes:
				return m.goMail()
			case viewSearch:
				return m.goSearch()
			case viewCalendar:
				return m.goCalendar()
			case viewContacts:
				return m.goContacts()
			case viewSettings:
				m.state = viewSettings
			case viewAbout:
				return m.goAbout()
			}
			return m, nil

		case m.state == viewMailboxes && len(m.mailboxes) > 0:
			m.state = viewEmails
			m.emails = nil
			m.clearFilter()
			m.loading = true
			m.canLoadMore = true
			selectedMB := m.mailboxes[m.mbCursor]
			if m.offlineMode || m.client == nil {
				return m, fetchEmailsOfflineCmd(m.db, selectedMB.ID, 0)
			}
			return m, fetchEmailsCmd(m.client, m.db, selectedMB.ID, 0)

		case m.state == viewEmails:
			// Always go to preview first, even for drafts
			if e, ok := m.selectedEmail(); ok {
				return m.openReader(e, viewEmails)
			}

		case m.state == viewSearch && !m.searchActive && m.searchCursor < len(m.searchResults):
			return m.openReader(m.searchResults[m.searchCursor], viewSearch)

		case m.state == viewCalendar && !m.viewEventDetail && m.editingEvent == nil:
			if _, ok := m.selectedEvent(); ok {
				m.viewEventDetail = true
			}
			return m, nil

		case m.state == viewContacts && !m.viewContactDetail && m.editingContact == nil:
			if _, ok := m.selectedContact(); ok {
				m.viewContactDetail = true
			}
			return m, nil

		case m.state == viewSettings && m.settingsCursor == 0:
			// Toggle offline mode
			m.offlineMode = !m.offlineMode
			if m.db != nil {
				m.db.SetConfig("offline_mode", fmt.Sprint(m.offlineMode))
			}
			return m, nil
		}

	case "esc", "left", "h":
		switch m.state {
		case viewMailboxes, viewSettings:
			m.state = viewMainMenu
		case viewEmails:
			m.state = viewMailboxes
			m.emails = nil
			m.clearFilter()
			// Refresh mailbox counts when returning
			return m, m.refreshMailboxesCmd()
		case viewBody:
			m.state = m.bodyReturn
			m.bodyContent = ""
			m.htmlBody = ""
			m.bodyLines = nil
		case viewCalendar:
			if m.viewEventDetail {
				m.viewEventDetail = false
			} else {
				m.state = viewMainMenu
				m.clearFilter()
			}
		case viewContacts:
			if m.viewContactDetail {
				m.viewContactDetail = false
			} else {
				m.state = viewMainMenu
				m.clearFilter()
			}
		case viewSearch:
			m.state = viewMainMenu
			m.searchResults = nil
			m.searchQuery = ""
		}
		return m, nil

	case "r":
		// Manual refresh
		switch {
		case m.state == viewMailboxes:
			m.loading = true
			return m, m.refreshMailboxesCmd()
		case m.state == viewEmails && len(m.mailboxes) > 0:
			m.loading = true
			selectedMB := m.mailboxes[m.mbCursor]
			if m.offlineMode || m.client == nil {
				m.emails = nil
				return m, tea.Batch(fetchMailboxesOfflineCmd(m.db), fetchEmailsOfflineCmd(m.db, selectedMB.ID, 0))
			}
			return m, tea.Batch(fetchMailboxesCmd(m.client, m.db), refreshEmailsCmd(m.client, m.db, selectedMB.ID))
		case m.state == viewCalendar && !m.offlineMode:
			if len(m.calendars) == 0 && m.davClient != nil {
				m.loading = true
				return m, fetchCalendarsCmd(m.davClient)
			}
			if cmd := m.fetchAgendaCmd(); cmd != nil {
				m.loading = true
				return m, cmd
			}
		case m.state == viewContacts && !m.offlineMode && m.davClient != nil:
			m.loading = true
			if ab := m.defaultAddressBookID(); ab != "" {
				return m, fetchContactsCmd(m.davClient, ab, 500)
			}
			return m, fetchAddressBooksCmd(m.davClient)
		}

	// Search (available in list views)
	case "/":
		switch {
		case m.state == viewMainMenu:
			return m.goSearch()
		case m.state == viewEmails,
			m.state == viewCalendar && !m.viewEventDetail,
			m.state == viewContacts && !m.viewContactDetail:
			m.searchActive = true
			m.searchInput.SetValue(m.searchQuery)
			m.searchInput.CursorEnd()
			m.searchInput.Focus()
			return m, textinput.Blink
		case m.state == viewSearch:
			m.searchActive = true
			m.searchInput.SetValue("")
			m.searchInput.Focus()
			return m, textinput.Blink
		}

	// Calendar/contacts: new item
	case "n":
		if m.state == viewCalendar && !m.viewEventDetail && !m.offlineMode {
			m.editingEvent = &model.CalendarEvent{
				Start: time.Now().Truncate(time.Hour).Add(time.Hour),
			}
			m.editingEvent.CalendarID = pickWritable(m.calendars,
				func(c model.Calendar) bool { return c.IsDefault && c.MayAddItems },
				func(c model.Calendar) bool { return c.MayAddItems },
				func(c model.Calendar) string { return c.ID })
			m.eventInput.SetValue("")
			m.eventInput.Focus()
			return m, textinput.Blink
		} else if m.state == viewContacts && !m.viewContactDetail && !m.offlineMode {
			m.editingContact = &model.Contact{}
			m.editingContact.AddressBookID = pickWritable(m.addressBooks,
				func(ab model.AddressBook) bool { return ab.IsDefault && ab.MayAddItems },
				func(ab model.AddressBook) bool { return ab.MayAddItems },
				func(ab model.AddressBook) string { return ab.ID })
			m.contactInput.SetValue("")
			m.contactInput.Placeholder = "Full Name"
			m.contactInput.Focus()
			m.contactEditField = 0
			return m, textinput.Blink
		}
	}

	return m, nil
}

// pickWritable returns the id of the first item matching preferred, else
// the first matching fallback, else "".
func pickWritable[T any](items []T, preferred, fallback func(T) bool, id func(T) string) string {
	for _, it := range items {
		if preferred(it) {
			return id(it)
		}
	}
	for _, it := range items {
		if fallback(it) {
			return id(it)
		}
	}
	return ""
}

// quoteReply builds the body of a reply: an attribution line and the
// original message quoted with "> ".
func quoteReply(e model.Email, body string) string {
	body = strings.TrimRight(strings.TrimPrefix(body, "[Converted HTML]\n"), "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nOn %s, %s wrote:\n", e.Date, e.From)
	for line := range strings.SplitSeq(body, "\n") {
		if line == "" {
			b.WriteString(">\n")
		} else {
			b.WriteString("> " + line + "\n")
		}
	}
	return b.String()
}

// clearFilter drops the local list filter.
func (m *Model) clearFilter() {
	if m.searchQuery == "" {
		return
	}
	m.searchQuery = ""
	m.searchInput.SetValue("")
	m.resetListCursors()
}

func (m Model) goMail() (tea.Model, tea.Cmd) {
	m.state = viewMailboxes
	m.clearFilter()
	m.loading = true
	return m, m.refreshMailboxesCmd()
}

func (m Model) goCalendar() (tea.Model, tea.Cmd) {
	m.state = viewCalendar
	m.viewEventDetail = false
	m.clearFilter()
	m.agendaStart = startOfDay(time.Now())
	if m.offlineMode || m.davClient == nil {
		return m, nil
	}
	if len(m.calendars) == 0 {
		m.loading = true
		return m, fetchCalendarsCmd(m.davClient)
	}
	if cmd := m.fetchAgendaCmd(); cmd != nil {
		m.loading = true
		return m, cmd
	}
	return m, nil
}

func (m Model) goContacts() (tea.Model, tea.Cmd) {
	m.state = viewContacts
	m.viewContactDetail = false
	m.clearFilter()
	m.contactCursor, m.contactOffset = 0, 0
	if m.offlineMode || m.davClient == nil {
		return m, nil
	}
	m.loading = true
	if ab := m.defaultAddressBookID(); ab != "" {
		return m, fetchContactsCmd(m.davClient, ab, 500)
	}
	return m, fetchAddressBooksCmd(m.davClient)
}

func (m Model) goSearch() (tea.Model, tea.Cmd) {
	if m.offlineMode || m.client == nil {
		m.err = fmt.Errorf("search requires online mode")
		return m, nil
	}
	m.state = viewSearch
	m.clearFilter()
	m.searchActive = true
	m.searchResults = nil
	m.searchCursor = 0
	m.searchInput.SetValue("")
	m.searchInput.Focus()
	return m, textinput.Blink
}

// startCompose opens the composer with the given fields, loading contacts
// for To: autocomplete if they are not loaded yet.
func (m Model) startCompose(to, subject, body string) (Model, tea.Cmd) {
	m.composeReturn = m.state
	m.state = viewComposeTo
	m.draftID = ""
	m.inputTo.SetValue(to)
	m.inputSubject.SetValue(subject)
	m.composeBody = body
	m.showSuggestions = false
	m.toSuggestions = nil
	m.inputSubject.Blur()
	m.inputTo.Focus()
	cmds := []tea.Cmd{textinput.Blink}
	if m.davClient != nil && !m.offlineMode && len(m.contacts) == 0 {
		if ab := m.defaultAddressBookID(); ab != "" {
			cmds = append(cmds, fetchContactsCmd(m.davClient, ab, 500))
		} else if len(m.addressBooks) == 0 {
			cmds = append(cmds, fetchAddressBooksCmd(m.davClient))
		}
	}
	return m, tea.Batch(cmds...)
}

// moveCursor moves the current view's cursor up or down one row.
func (m *Model) moveCursor(delta int) {
	page := m.listPageHeight()
	switch {
	case m.state == viewMainMenu:
		m.menuCursor = clampIndex(m.menuCursor+delta, len(mainMenuItems))
	case m.state == viewBody:
		m.scrollBody(delta)
	case m.state == viewMailboxes:
		m.mbCursor = clampIndex(m.mbCursor+delta, len(m.mailboxes))
		m.mbOffset = keepVisible(m.mbCursor, m.mbCursor, m.mbOffset, page)
	case m.state == viewEmails:
		m.emailCursor = clampIndex(m.emailCursor+delta, len(m.visibleEmails()))
		m.emailOffset = keepVisible(m.emailCursor, m.emailCursor, m.emailOffset, page)
	case m.state == viewCalendar && !m.viewEventDetail:
		events := m.visibleEvents()
		m.eventCursor = clampIndex(m.eventCursor+delta, len(events))
		top, bottom := agendaRowSpan(events, m.eventCursor)
		m.eventOffset = keepVisible(top, bottom, m.eventOffset, page)
	case m.state == viewContacts && !m.viewContactDetail:
		m.contactCursor = clampIndex(m.contactCursor+delta, len(m.visibleContacts()))
		m.contactOffset = keepVisible(m.contactCursor, m.contactCursor, m.contactOffset, page)
	case m.state == viewSearch && !m.searchActive:
		m.searchCursor = clampIndex(m.searchCursor+delta, len(m.searchResults))
	case m.state == viewSettings:
		m.settingsCursor = clampIndex(m.settingsCursor+delta, 1)
	}
}

// moveDown moves the cursor down, loading the next page of emails when it
// is already on the last one.
func (m Model) moveDown() (tea.Model, tea.Cmd) {
	if m.state == viewEmails && m.emailCursor >= len(m.visibleEmails())-1 &&
		m.canLoadMore && !m.loading && m.mbCursor < len(m.mailboxes) {
		m.loading = true
		selectedMB := m.mailboxes[m.mbCursor]
		if m.offlineMode || m.client == nil {
			return m, fetchEmailsOfflineCmd(m.db, selectedMB.ID, len(m.emails))
		}
		return m, fetchEmailsCmd(m.client, m.db, selectedMB.ID, len(m.emails))
	}
	m.moveCursor(1)
	return m, nil
}

// scrollBody scrolls the reader, keeping at least a page of text on screen.
func (m *Model) scrollBody(delta int) {
	maxScroll := max(len(m.bodyLines)-m.bodyViewHeight(), 0)
	m.bodyScrollPos = min(max(m.bodyScrollPos+delta, 0), maxScroll)
}
