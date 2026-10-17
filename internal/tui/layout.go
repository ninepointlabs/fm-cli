package tui

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"fm-cli/internal/model"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The list cursors (emailCursor, eventCursor, contactCursor) index the list
// as displayed — after the local "/" filter — so what is highlighted is what
// Enter opens and d deletes. These helpers resolve them.

func (m Model) visibleEmails() []model.Email {
	return filterEmails(m.emails, m.searchQuery)
}

func (m Model) visibleEvents() []model.CalendarEvent {
	return filterEvents(m.events, m.searchQuery)
}

func (m Model) visibleContacts() []model.Contact {
	return filterContactsAll(m.contacts, m.searchQuery)
}

func (m Model) selectedEmail() (model.Email, bool) {
	list := m.visibleEmails()
	if m.emailCursor < 0 || m.emailCursor >= len(list) {
		return model.Email{}, false
	}
	return list[m.emailCursor], true
}

func (m Model) selectedEvent() (model.CalendarEvent, bool) {
	list := m.visibleEvents()
	if m.eventCursor < 0 || m.eventCursor >= len(list) {
		return model.CalendarEvent{}, false
	}
	return list[m.eventCursor], true
}

func (m Model) selectedContact() (model.Contact, bool) {
	list := m.visibleContacts()
	if m.contactCursor < 0 || m.contactCursor >= len(list) {
		return model.Contact{}, false
	}
	return list[m.contactCursor], true
}

// removeEmail drops an email from the loaded list (an optimistic update
// after archive/delete) and keeps the cursor on a valid row.
func (m *Model) removeEmail(id string) {
	for i, e := range m.emails {
		if e.ID == id {
			m.emails = append(m.emails[:i], m.emails[i+1:]...)
			break
		}
	}
	m.emailCursor = clampIndex(m.emailCursor, len(m.visibleEmails()))
}

func (m *Model) removeEvent(id string) {
	for i, e := range m.events {
		if e.ID == id {
			m.events = append(m.events[:i], m.events[i+1:]...)
			break
		}
	}
	m.eventCursor = clampIndex(m.eventCursor, len(m.visibleEvents()))
}

func (m *Model) removeContact(id string) {
	for i, c := range m.contacts {
		if c.ID == id {
			m.contacts = append(m.contacts[:i], m.contacts[i+1:]...)
			break
		}
	}
	m.contactCursor = clampIndex(m.contactCursor, len(m.visibleContacts()))
}

// updateEmail applies f to the loaded email with the given id.
func (m *Model) updateEmail(id string, f func(*model.Email)) {
	for i := range m.emails {
		if m.emails[i].ID == id {
			f(&m.emails[i])
			return
		}
	}
}

// resetListCursors puts every list back at the top, as after the filter
// changes.
func (m *Model) resetListCursors() {
	m.emailCursor, m.emailOffset = 0, 0
	m.eventCursor, m.eventOffset = 0, 0
	m.contactCursor, m.contactOffset = 0, 0
}

func clampIndex(i, n int) int {
	if i >= n {
		i = n - 1
	}
	return max(i, 0)
}

// keepVisible returns a scroll offset that shows rows top..bottom inside a
// window of page rows, moving the current offset as little as possible.
func keepVisible(top, bottom, offset, page int) int {
	if page < 1 {
		page = 1
	}
	if bottom >= offset+page {
		offset = bottom - page + 1
	}
	if top < offset {
		offset = top
	}
	return max(offset, 0)
}

// visibleCalendarIDs lists the calendars whose events the agenda shows.
func (m Model) visibleCalendarIDs() []string {
	var ids []string
	for _, cal := range m.calendars {
		if cal.IsVisible && cal.MayReadItems {
			ids = append(ids, cal.ID)
		}
	}
	return ids
}

// fetchAgendaCmd reloads the agenda, or returns nil when there is nothing to
// load it from.
func (m Model) fetchAgendaCmd() tea.Cmd {
	ids := m.visibleCalendarIDs()
	if m.davClient == nil || len(ids) == 0 {
		return nil
	}
	return fetchEventsCmd(m.davClient, ids, m.agendaStart, m.agendaStart.AddDate(0, 0, m.agendaDays))
}

// defaultAddressBookID picks the default readable address book, else the
// first readable one.
func (m Model) defaultAddressBookID() string {
	for _, ab := range m.addressBooks {
		if ab.IsDefault && ab.MayReadItems {
			return ab.ID
		}
	}
	for _, ab := range m.addressBooks {
		if ab.MayReadItems {
			return ab.ID
		}
	}
	return ""
}

// startOfDay is local midnight on t's calendar day. (time.Truncate works in
// UTC, which files evening events under the wrong day west of Greenwich.)
func startOfDay(t time.Time) time.Time {
	y, mo, d := t.Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, time.Local)
}

// contentWidth is the usable width inside the app's padding.
func (m Model) contentWidth() int {
	if m.width <= 0 {
		return 80
	}
	return max(m.width-appStyle.GetHorizontalFrameSize(), 20)
}

// headerView is the top line — app name, where you are, status — plus an
// error banner when there is one.
func (m Model) headerView() string {
	left := titleStyle.Render("✉ fm-cli")
	if crumb := m.breadcrumb(); crumb != "" {
		left += " " + breadcrumbStyle.Render(crumb)
	}
	if m.offlineMode {
		left += " " + errorBadgeStyle.Render("OFFLINE")
	}
	if m.loading {
		left += " " + helpStyle.Render("loading…")
	}

	line := left
	if m.state != viewMainMenu && !m.composing() {
		right := helpStyle.Render(keyStyle.Render("1") + " mail  " + keyStyle.Render("2") + " calendar  " +
			keyStyle.Render("3") + " contacts  " + keyStyle.Render("4") + " settings  " + keyStyle.Render("0") + " menu")
		if gap := m.contentWidth() - lipgloss.Width(left) - lipgloss.Width(right); gap >= 2 {
			line = left + strings.Repeat(" ", gap) + right
		}
	}

	if m.err != nil {
		banner := errorBadgeStyle.Render("Error") + " " + m.err.Error() + "  " + helpStyle.Render("(any key to dismiss)")
		line += "\n" + lipgloss.NewStyle().Width(m.contentWidth()).Render(banner)
	}
	return line
}

func (m Model) breadcrumb() string {
	switch m.state {
	case viewMailboxes, viewComposeTo, viewComposeSubject, viewComposeConfirm:
		return "Mail"
	case viewEmails:
		return "Mail › " + m.currentMailboxName()
	case viewBody:
		if m.bodyReturn == viewSearch {
			return "Search › Message"
		}
		return "Mail › " + m.currentMailboxName()
	case viewSearch:
		return "Search"
	case viewCalendar:
		return "Calendar"
	case viewContacts:
		return "Contacts"
	case viewSettings:
		return "Settings"
	case viewAbout:
		return "About"
	}
	return ""
}

func (m Model) currentMailboxName() string {
	if m.mbCursor >= 0 && m.mbCursor < len(m.mailboxes) {
		return m.mailboxes[m.mbCursor].Name
	}
	return ""
}

func (m Model) composing() bool {
	return m.state == viewComposeTo || m.state == viewComposeSubject || m.state == viewComposeConfirm
}

// filterLines is how many lines the filter bar takes in a list view.
func (m Model) filterLines() int {
	switch m.state {
	case viewEmails, viewCalendar, viewContacts:
		if m.searchActive || m.searchQuery != "" {
			return 2
		}
	}
	return 0
}

// listPageHeight is how many rows a list view can show: the terminal height
// less the padding, header, a blank line, the filter bar, the list's title
// line, and the blank-plus-help footer.
func (m Model) listPageHeight() int {
	if m.height <= 0 {
		return 10
	}
	chrome := appStyle.GetVerticalFrameSize() + lipgloss.Height(m.headerView()) + 1 + m.filterLines() + 1 + 2
	return max(m.height-chrome, 3)
}

// bodyViewHeight is how many lines of the reader fit on screen.
func (m Model) bodyViewHeight() int {
	if m.height <= 0 {
		return 20
	}
	chrome := appStyle.GetVerticalFrameSize() + lipgloss.Height(m.headerView()) + 1 + 2
	return max(m.height-chrome, 3)
}

// renderHelp joins key/label pairs into a footer line that fits the width.
func (m Model) renderHelp(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, keyStyle.Render(pairs[i])+" "+pairs[i+1])
	}
	return helpStyle.MaxWidth(m.contentWidth()).Render(strings.Join(parts, "  "))
}

// agendaRow is one line of the agenda: a blank spacer, a day heading, or an
// event (event is its index in the displayed list).
type agendaRow struct {
	day   time.Time
	event int // -1 for a heading or spacer
	blank bool
}

// agendaRows lays out events under a heading per day.
func agendaRows(events []model.CalendarEvent) []agendaRow {
	var rows []agendaRow
	var current time.Time
	for i, e := range events {
		day := startOfDay(e.Start)
		if !day.Equal(current) {
			if len(rows) > 0 {
				rows = append(rows, agendaRow{event: -1, blank: true})
			}
			rows = append(rows, agendaRow{day: day, event: -1})
			current = day
		}
		rows = append(rows, agendaRow{day: day, event: i})
	}
	return rows
}

// agendaRowSpan is the rows that must be on screen for the selected event:
// the event itself, and its day heading when it is the first of the day.
func agendaRowSpan(events []model.CalendarEvent, cursor int) (top, bottom int) {
	rows := agendaRows(events)
	for i, r := range rows {
		if r.event == cursor {
			top = i
			if i > 0 && rows[i-1].event == -1 && !rows[i-1].blank {
				top = i - 1
			}
			return top, i
		}
	}
	return 0, 0
}

// mailboxRoleOrder puts the standard folders first, in the order Fastmail's
// web client lists them.
var mailboxRoleOrder = map[string]int{
	"inbox": 0, "snoozed": 1, "scheduled": 2, "drafts": 3, "sent": 4, "archive": 5, "junk": 6, "trash": 7,
}

// sortMailboxes orders folders: standard folders first, then the rest by
// their sort order and name.
func sortMailboxes(mbs []model.Mailbox) []model.Mailbox {
	rank := func(mb model.Mailbox) int {
		if r, ok := mailboxRoleOrder[mb.Role]; ok {
			return r
		}
		return len(mailboxRoleOrder)
	}
	sorted := slices.Clone(mbs)
	slices.SortStableFunc(sorted, func(a, b model.Mailbox) int {
		return cmp.Or(
			cmp.Compare(rank(a), rank(b)),
			cmp.Compare(a.SortOrder, b.SortOrder),
			cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)),
		)
	})
	return sorted
}
