package tui

import (
	"fmt"
	"strings"
	"time"

	"fm-cli/internal/api"
	"fm-cli/internal/images"
	"fm-cli/internal/model"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) View() string {
	var s strings.Builder
	s.WriteString(m.headerView())
	s.WriteString("\n\n")

	switch m.state {
	case viewMainMenu:
		s.WriteString(m.mainMenuView())
	case viewMailboxes:
		s.WriteString(m.mailboxesView())
	case viewEmails:
		s.WriteString(m.emailsView())
	case viewBody:
		s.WriteString(m.readerView())
	case viewComposeTo, viewComposeSubject, viewComposeConfirm:
		s.WriteString(m.composeView())
	case viewCalendar:
		s.WriteString(m.calendarView())
	case viewContacts:
		s.WriteString(m.contactsView())
	case viewSearch:
		s.WriteString(m.searchView())
	case viewSettings:
		s.WriteString(m.settingsView())
	case viewAbout:
		s.WriteString(m.aboutView())
	}

	return appStyle.Render(s.String())
}

// filterBar is the "/" filter line of a list view, with its trailing blank
// line (filterLines counts both).
func (m Model) filterBar() string {
	if m.searchActive {
		return m.searchInput.View() + "\n\n"
	}
	if m.searchQuery != "" {
		return badgeStyle.UnsetMarginLeft().Render("Filter: "+m.searchQuery) + " " + m.renderHelp("ctrl+u", "clear", "/", "edit") + "\n\n"
	}
	return ""
}

// listTitle is the line above a list: its name and which rows are showing.
func listTitle(name string, start, end, total, unfiltered int) string {
	count := ""
	if total > 0 {
		count = fmt.Sprintf("%d–%d of %d", start+1, end, total)
		if unfiltered != total {
			count += fmt.Sprintf(" (filtered from %d)", unfiltered)
		}
	}
	return subtitleStyle.Render(name) + "  " + helpStyle.Render(count) + "\n"
}

// selectableRow renders one list row, highlighted when selected. plain is
// used for the highlighted form so the selection color is not broken up by
// the row's own colors.
func (m Model) selectableRow(selected bool, styled, plain string) string {
	width := m.contentWidth()
	if selected {
		return selectedEmailItemStyle.Width(width).MaxWidth(width).Render(plain) + "\n"
	}
	return emailItemStyle.MaxWidth(width).Render(styled) + "\n"
}

func (m Model) mainMenuView() string {
	var s strings.Builder
	s.WriteString(subtitleStyle.Render("Main Menu") + "\n\n")
	icons := map[sessionState]string{
		viewMailboxes: "📧", viewSearch: "🔍", viewCalendar: "📅", viewContacts: "👤", viewSettings: "🔧", viewAbout: "💬",
	}
	for i, item := range mainMenuItems {
		label := icons[item.State] + " " + item.Name
		if i == m.menuCursor {
			label = selectedMailboxStyle.Width(20).Render("▶ " + label)
		} else {
			label = mailboxStyle.Width(20).Render("  " + label)
		}
		s.WriteString(label + "  " + keyStyle.Render(item.Shortcut) + "\n")
	}
	s.WriteString("\n" + m.renderHelp("↑↓", "navigate", "⏎", "select", "c", "compose", "q", "quit"))
	return s.String()
}

func (m Model) mailboxesView() string {
	var s strings.Builder
	if m.loading && len(m.mailboxes) == 0 {
		return statusStyle.Render(" Loading folders… ")
	}
	if len(m.mailboxes) == 0 {
		s.WriteString(helpStyle.Render("No folders found.") + "\n")
	}
	page := m.listPageHeight()
	offset := keepVisible(m.mbCursor, m.mbCursor, m.mbOffset, page)
	end := min(offset+page, len(m.mailboxes))
	s.WriteString(listTitle("Folders", offset, end, len(m.mailboxes), len(m.mailboxes)))

	nameWidth := 0
	for _, mb := range m.mailboxes {
		nameWidth = max(nameWidth, lipgloss.Width(mb.Name))
	}
	nameWidth = min(nameWidth, m.contentWidth()-12)
	for i := offset; i < end; i++ {
		mb := m.mailboxes[i]
		name := lipgloss.NewStyle().Width(nameWidth).MaxWidth(nameWidth).Render(mb.Name)
		count := ""
		if mb.UnreadCount > 0 {
			count = fmt.Sprintf("%d", mb.UnreadCount)
		}
		styled := "  " + name + "  " + unreadStyle.Render(count)
		plain := "▶ " + name + "  " + count
		s.WriteString(m.selectableRow(i == m.mbCursor, styled, plain))
	}
	s.WriteString("\n" + m.renderHelp("↑↓", "navigate", "⏎", "open", "r", "refresh", "c", "compose", "esc", "back"))
	return s.String()
}

// shortDate formats an email's "2006-01-02 15:04" date for a list: the time
// for today, the day and month this year, the full date otherwise.
func shortDate(date string) string {
	t, err := time.ParseInLocation("2006-01-02 15:04", date, time.Local)
	if err != nil {
		return date
	}
	now := time.Now()
	switch {
	case startOfDay(t).Equal(startOfDay(now)):
		return t.Format("15:04")
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	default:
		return t.Format("2006-01-02")
	}
}

// senderName shortens "Name <addr>, ..." to the first sender's display name,
// or their address when there is no name.
func senderName(from string) string {
	from = api.CleanLine(from)
	// Names are not quoted here, so a name may itself contain ", ".
	if name, _, ok := strings.Cut(from, " <"); ok && name != "" && !strings.Contains(name, "@") {
		return name
	}
	first, _, _ := strings.Cut(from, ", ")
	if name, _, ok := strings.Cut(first, " <"); ok && name != "" {
		return name
	}
	return strings.Trim(first, "<>")
}

// emailRow renders one line of an email list: unread dot, flag, sender,
// subject and date, fitted to the width.
func (m Model) emailRow(e model.Email, selected bool) string {
	width := m.contentWidth() - 1 // row padding
	date := api.TruncateRunes(shortDate(e.Date), 10)
	dateWidth := 10
	fromWidth := min(24, max(width/4, 10))
	subjectWidth := max(width-2-2-fromWidth-2-dateWidth-1, 5)

	marker := "  "
	if e.IsUnread {
		marker = "● "
	}
	flag := "  "
	if e.IsFlagged {
		flag = "★ "
	}
	from := api.TruncateRunes(senderName(e.From), fromWidth)
	subject := api.CleanLine(e.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	if e.IsDraft {
		subject = "[draft] " + subject
	}
	subject = api.TruncateRunes(subject, subjectWidth)

	pad := func(s string, w int) string { return lipgloss.NewStyle().Width(w).Render(s) }
	dateCol := lipgloss.NewStyle().Width(dateWidth).Align(lipgloss.Right).Render(date)
	plain := marker + flag + pad(from, fromWidth) + "  " + pad(subject, subjectWidth) + " " + dateCol

	fromStyle, subjectStyle := readStyle, readStyle
	if e.IsUnread {
		fromStyle, subjectStyle = emailFromStyle, emailSubjectStyle.Bold(true)
	}
	styled := unreadStyle.Render(marker) + warningStyle.Render(flag) +
		fromStyle.Render(pad(from, fromWidth)) + "  " + subjectStyle.Render(pad(subject, subjectWidth)) + " " +
		emailDateStyle.Render(dateCol)
	return m.selectableRow(selected, styled, plain)
}

func (m Model) emailsView() string {
	var s strings.Builder
	s.WriteString(m.filterBar())
	displayEmails := m.visibleEmails()

	switch {
	case m.loading && len(m.emails) == 0:
		s.WriteString(statusStyle.Render(" Loading emails… ") + "\n")
	case len(displayEmails) == 0:
		s.WriteString(listTitle(m.currentMailboxName(), 0, 0, 0, 0))
		if m.searchQuery != "" {
			s.WriteString(helpStyle.Render("No emails match the filter.") + "\n")
		} else {
			s.WriteString(helpStyle.Render("No emails here.") + "\n")
		}
	default:
		page := m.listPageHeight()
		offset := keepVisible(m.emailCursor, m.emailCursor, m.emailOffset, page)
		end := min(offset+page, len(displayEmails))
		total := fmt.Sprint(len(m.emails))
		if m.canLoadMore {
			total += "+"
		}
		title := fmt.Sprintf("%d–%d of %s", offset+1, end, total)
		if m.searchQuery != "" {
			title = fmt.Sprintf("%d–%d of %d matching", offset+1, end, len(displayEmails))
		}
		s.WriteString(subtitleStyle.Render(m.currentMailboxName()) + "  " + helpStyle.Render(title) + "\n")
		for i := offset; i < end; i++ {
			s.WriteString(m.emailRow(displayEmails[i], i == m.emailCursor))
		}
	}

	s.WriteString("\n" + m.renderHelp("⏎", "read", "/", "filter", "r", "refresh", "u", "read/unread",
		"f", "flag", "e", "archive", "d", "delete", "c", "compose", "esc", "back"))
	return s.String()
}

func (m Model) readerView() string {
	if m.bodyLines == nil {
		return statusStyle.Render(" Loading message… ")
	}
	var s strings.Builder
	height := m.bodyViewHeight()
	maxScroll := max(len(m.bodyLines)-height, 0)
	pos := min(m.bodyScrollPos, maxScroll)
	end := min(pos+height, len(m.bodyLines))
	for _, line := range m.bodyLines[pos:end] {
		s.WriteString(line + "\n")
	}

	scroll := ""
	if maxScroll > 0 {
		scroll = helpStyle.Render(fmt.Sprintf("%d%%  ", pos*100/maxScroll))
	}
	pairs := []string{"esc", "back", "j/k", "scroll", "space", "page"}
	if m.openEmail.IsDraft {
		pairs = append(pairs, "e", "edit draft")
	} else {
		pairs = append(pairs, "R", "reply", "A", "reply all", "F", "forward")
	}
	pairs = append(pairs, "m", "details")
	if m.htmlBody != "" {
		pairs = append(pairs, "b", "browser")
		if images.HasGraphicsSupport() {
			pairs = append(pairs, "i", "images")
		}
	}
	s.WriteString("\n" + lipgloss.NewStyle().MaxWidth(m.contentWidth()).Render(scroll+m.renderHelp(pairs...)))
	return s.String()
}

func (m Model) composeView() string {
	var s strings.Builder
	title := "New Message"
	if m.draftID != "" {
		title = "Edit Draft"
	}
	s.WriteString(subtitleStyle.Render(title) + "\n\n")

	label := contactFieldLabelStyle.Width(9)
	fromAddr := "(loading…)"
	if len(m.identities) > 0 {
		fromAddr = m.identities[m.identityIdx]
	}
	from := contactFieldValueStyle.Render(fromAddr)
	if len(m.identities) > 1 {
		from += "  " + helpStyle.Render("(tab to change)")
	}
	s.WriteString(label.Render("From") + from + "\n")

	switch m.state {
	case viewComposeTo:
		s.WriteString(label.Render("To") + m.inputTo.View() + "\n")
		if m.showSuggestions && len(m.toSuggestions) > 0 {
			for i, c := range m.toSuggestions {
				email := ""
				if len(c.Emails) > 0 {
					email = c.Emails[0].Email
				}
				text := email
				if c.FullName != "" {
					text = fmt.Sprintf("%s <%s>", c.FullName, email)
				}
				if i == m.toSuggestionIdx {
					s.WriteString(strings.Repeat(" ", 9) + selectedEmailItemStyle.Render(text) + "\n")
				} else {
					s.WriteString(strings.Repeat(" ", 9) + emailItemStyle.Render(text) + "\n")
				}
			}
			s.WriteString("\n" + m.renderHelp("↑↓", "choose", "tab/⏎", "use", "esc", "dismiss"))
		} else {
			s.WriteString("\n" + m.renderHelp("⏎", "next", "tab", "change from", "esc", "cancel"))
		}

	case viewComposeSubject:
		s.WriteString(label.Render("To") + contactFieldValueStyle.Render(m.inputTo.Value()) + "\n")
		s.WriteString(label.Render("Subject") + m.inputSubject.View() + "\n")
		s.WriteString("\n" + m.renderHelp("⏎", "write body in $EDITOR", "tab", "change from", "esc", "back"))

	case viewComposeConfirm:
		s.WriteString(label.Render("To") + contactFieldValueStyle.Render(m.inputTo.Value()) + "\n")
		s.WriteString(label.Render("Subject") + contactFieldValueStyle.Render(m.inputSubject.Value()) + "\n\n")

		// Show as much of the start of the body as fits
		lines := strings.Split(strings.TrimSpace(m.composeBody), "\n")
		room := 10
		if m.height > 0 {
			room = min(m.height-appStyle.GetVerticalFrameSize()-lipgloss.Height(m.headerView())-11, 10)
		}
		shown := min(len(lines), max(room, 1))
		width := m.contentWidth() - 2
		bar := emailDateStyle.Render("│ ")
		for _, line := range lines[:shown] {
			s.WriteString(bar + api.TruncateRunes(line, width) + "\n")
		}
		if len(lines) > shown {
			s.WriteString(bar + helpStyle.Render(fmt.Sprintf("… %d more lines", len(lines)-shown)) + "\n")
		}
		s.WriteString("\n")

		if m.loading {
			s.WriteString(statusStyle.Render(" Working… "))
		} else {
			s.WriteString(m.renderHelp("y", "send", "s", "save draft", "e", "edit body", "tab", "change from", "n", "discard"))
		}
	}
	return s.String()
}

func (m Model) calendarView() string {
	var s strings.Builder
	s.WriteString(m.filterBar())
	displayEvents := m.visibleEvents()

	switch {
	case m.loading && len(m.events) == 0 && m.editingEvent == nil:
		s.WriteString(statusStyle.Render(" Loading calendar… "))

	case m.editingEvent != nil:
		title := "New Event"
		if m.editingEvent.ID != "" {
			title = "Edit Event"
		}
		s.WriteString(subtitleStyle.Render(title) + "\n\n")
		label := contactFieldLabelStyle.Width(10)
		s.WriteString(label.Render("Title") + m.eventInput.View() + "\n")
		s.WriteString(label.Render("Date") + contactFieldValueStyle.Render(m.editingEvent.Start.Format("Mon, Jan 2 2006")) + "\n")
		s.WriteString(label.Render("Time") + contactFieldValueStyle.Render(m.editingEvent.Start.Format("15:04")) + "\n")
		if m.editingEvent.Duration != "" {
			s.WriteString(label.Render("Duration") + contactFieldValueStyle.Render(m.editingEvent.Duration) + "\n")
		}
		if m.editingEvent.Location != "" {
			s.WriteString(label.Render("Location") + contactFieldValueStyle.Render(m.editingEvent.Location) + "\n")
		}
		s.WriteString("\n" + m.renderHelp("⏎", "save", "esc", "cancel"))

	case m.viewEventDetail:
		e, ok := m.selectedEvent()
		if !ok {
			break
		}
		s.WriteString(boxStyle.UnsetMarginTop().Render(eventTitleStyle.Render(e.Title)) + "\n\n")
		label := contactFieldLabelStyle.Width(10)
		s.WriteString(label.Render("Date") + contactFieldValueStyle.Render(e.Start.Format("Monday, January 2, 2006")) + "\n")
		if e.IsAllDay {
			s.WriteString(label.Render("Time") + badgeStyle.UnsetMarginLeft().Render("All day") + "\n")
		} else {
			s.WriteString(label.Render("Time") + eventTimeStyle.Render(e.Start.Format("15:04")+" – "+e.End.Format("15:04")) + "\n")
		}
		if e.Location != "" {
			s.WriteString(label.Render("Location") + contactFieldValueStyle.Render(e.Location) + "\n")
		}
		if e.Description != "" {
			s.WriteString("\n" + lipgloss.NewStyle().Width(m.contentWidth()).Render(e.Description) + "\n")
		}
		if len(e.Participants) > 0 {
			s.WriteString("\n" + contactFieldLabelStyle.Render("Participants") + "\n")
			for _, p := range e.Participants {
				status := ""
				if p.Status != "" {
					status = " " + helpStyle.Render(p.Status)
				}
				s.WriteString("  • " + contactNameStyle.Render(p.Name) + " " + contactEmailStyle.Render("<"+p.Email+">") + status + "\n")
			}
		}
		s.WriteString("\n" + m.renderHelp("e", "edit", "d", "delete", "esc", "back"))

	case len(m.calendars) == 0:
		switch {
		case m.offlineMode:
			s.WriteString(helpStyle.Render("Calendar is not available in offline mode.") + "\n")
		case m.davClient == nil:
			s.WriteString(warningStyle.Render("⚠ Calendar needs an app password.") + "\n")
			s.WriteString(helpStyle.Render("Run `fm-cli auth dav` to store one.") + "\n")
		default:
			s.WriteString(helpStyle.Render("No calendars found.") + "\n")
		}
		s.WriteString("\n" + m.renderHelp("r", "refresh", "esc", "back"))

	case len(displayEvents) == 0:
		s.WriteString(listTitle("Agenda", 0, 0, 0, 0))
		if m.searchQuery != "" {
			s.WriteString(helpStyle.Render("No events match the filter.") + "\n")
		} else {
			s.WriteString(helpStyle.Render(fmt.Sprintf("Nothing in the next %d days.", m.agendaDays)) + "\n")
		}
		s.WriteString("\n" + m.renderHelp("n", "new event", "r", "refresh", "esc", "back"))

	default:
		rows := agendaRows(displayEvents)
		page := m.listPageHeight()
		top, bottom := agendaRowSpan(displayEvents, m.eventCursor)
		offset := keepVisible(top, bottom, m.eventOffset, page)
		end := min(offset+page, len(rows))
		s.WriteString(subtitleStyle.Render("Agenda") + "  " +
			helpStyle.Render(fmt.Sprintf("next %d days · %d events", m.agendaDays, len(displayEvents))) + "\n")

		today := startOfDay(time.Now())
		for _, r := range rows[offset:end] {
			switch {
			case r.blank:
				s.WriteString("\n")
			case r.event < 0:
				label := r.day.Format("Monday, January 2")
				style := eventDateHeaderStyle.UnsetMarginTop()
				if r.day.Equal(today) {
					label += " · Today"
					style = todayBadgeStyle
				} else if r.day.Equal(today.AddDate(0, 0, 1)) {
					label += " · Tomorrow"
				}
				s.WriteString(style.Render(label) + "\n")
			default:
				e := displayEvents[r.event]
				when := e.Start.Format("15:04")
				if e.IsAllDay {
					when = "all day"
				}
				when = fmt.Sprintf("%-7s", when)
				plain := "  " + when + "  " + e.Title
				styled := "  " + eventTimeStyle.Render(when) + "  " + eventTitleStyle.Render(e.Title)
				if e.Location != "" {
					plain += "  @ " + e.Location
					styled += "  " + helpStyle.Render("@ "+e.Location)
				}
				s.WriteString(m.selectableRow(r.event == m.eventCursor, styled, plain))
			}
		}
		s.WriteString("\n" + m.renderHelp("⏎", "view", "/", "filter", "n", "new", "d", "delete", "r", "refresh", "esc", "back"))
	}
	return s.String()
}

func (m Model) contactsView() string {
	var s strings.Builder
	s.WriteString(m.filterBar())
	displayContacts := m.visibleContacts()

	switch {
	case m.loading && len(m.contacts) == 0 && m.editingContact == nil:
		s.WriteString(statusStyle.Render(" Loading contacts… "))

	case m.editingContact != nil:
		title := "New Contact"
		if m.editingContact.ID != "" {
			title = "Edit Contact"
		}
		s.WriteString(subtitleStyle.Render(title) + "\n\n")

		fields := []struct{ label, value string }{
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
		label := contactFieldLabelStyle.Width(11)
		for i, f := range fields {
			if i == m.contactEditField {
				s.WriteString(keyStyle.Render("▶ ") + label.Render(f.label) + m.contactInput.View() + "\n")
			} else {
				s.WriteString("  " + label.Render(f.label) + contactFieldValueStyle.Render(f.value) + "\n")
			}
		}
		s.WriteString("\n" + m.renderHelp("tab", "next field", "⏎", "save", "esc", "cancel"))

	case m.viewContactDetail:
		c, ok := m.selectedContact()
		if !ok {
			break
		}
		s.WriteString(boxStyle.UnsetMarginTop().Render(contactNameStyle.Render(c.FullName)) + "\n\n")
		label := contactFieldLabelStyle.Width(10)
		if c.Nickname != "" {
			s.WriteString(label.Render("Nickname") + contactFieldValueStyle.Render(c.Nickname) + "\n")
		}
		if c.Company != "" || c.JobTitle != "" {
			work := strings.Trim(c.Company+" – "+c.JobTitle, " –")
			s.WriteString(label.Render("Work") + contactFieldValueStyle.Render(work) + "\n")
		}
		for _, e := range c.Emails {
			s.WriteString(label.Render("Email") + contactEmailStyle.Render(e.Email) + " " + helpStyle.Render(e.Type) + "\n")
		}
		for _, p := range c.Phones {
			s.WriteString(label.Render("Phone") + contactFieldValueStyle.Render(p.Number) + " " + helpStyle.Render(p.Type) + "\n")
		}
		for _, a := range c.Addresses {
			var parts []string
			for _, part := range []string{a.Street, a.City, a.State, a.PostalCode, a.Country} {
				if part != "" {
					parts = append(parts, part)
				}
			}
			s.WriteString(label.Render("Address") + contactFieldValueStyle.Render(strings.Join(parts, ", ")) + " " + helpStyle.Render(a.Type) + "\n")
		}
		if c.Birthday != "" {
			s.WriteString(label.Render("Birthday") + contactFieldValueStyle.Render(c.Birthday) + "\n")
		}
		if c.Notes != "" {
			s.WriteString("\n" + lipgloss.NewStyle().Width(m.contentWidth()).Render(c.Notes) + "\n")
		}
		s.WriteString("\n" + m.renderHelp("e", "edit", "d", "delete", "esc", "back"))

	case len(m.addressBooks) == 0:
		switch {
		case m.offlineMode:
			s.WriteString(helpStyle.Render("Contacts are not available in offline mode.") + "\n")
		case m.davClient == nil:
			s.WriteString(warningStyle.Render("⚠ Contacts need an app password.") + "\n")
			s.WriteString(helpStyle.Render("Run `fm-cli auth dav` to store one.") + "\n")
		default:
			s.WriteString(helpStyle.Render("No address books found.") + "\n")
		}
		s.WriteString("\n" + m.renderHelp("r", "refresh", "esc", "back"))

	case len(displayContacts) == 0:
		s.WriteString(listTitle("Contacts", 0, 0, 0, 0))
		if m.searchQuery != "" {
			s.WriteString(helpStyle.Render("No contacts match the filter.") + "\n")
		} else {
			s.WriteString(helpStyle.Render("No contacts yet.") + "\n")
		}
		s.WriteString("\n" + m.renderHelp("n", "new contact", "r", "refresh", "esc", "back"))

	default:
		page := m.listPageHeight()
		offset := keepVisible(m.contactCursor, m.contactCursor, m.contactOffset, page)
		end := min(offset+page, len(displayContacts))
		s.WriteString(listTitle("Contacts", offset, end, len(displayContacts), len(m.contacts)))

		nameWidth := min(30, max(m.contentWidth()/3, 12))
		for i := offset; i < end; i++ {
			c := displayContacts[i]
			name := c.FullName
			if name == "" {
				name = "(no name)"
			}
			name = lipgloss.NewStyle().Width(nameWidth).Render(api.TruncateRunes(name, nameWidth-1))
			email, phone := "", ""
			if len(c.Emails) > 0 {
				email = c.Emails[0].Email
			}
			if len(c.Phones) > 0 {
				phone = "  " + c.Phones[0].Number
			}
			plain := "  " + name + email + phone
			styled := "  " + contactNameStyle.Render(name) + contactEmailStyle.Render(email) + helpStyle.Render(phone)
			s.WriteString(m.selectableRow(i == m.contactCursor, styled, plain))
		}
		s.WriteString("\n" + m.renderHelp("⏎", "view", "/", "filter", "n", "new", "d", "delete", "r", "refresh", "esc", "back"))
	}
	return s.String()
}

func (m Model) searchView() string {
	var s strings.Builder
	switch {
	case m.searchActive:
		s.WriteString(subtitleStyle.Render("Search all mail") + "\n\n")
		s.WriteString(m.searchInput.View() + "\n\n")
		s.WriteString(m.renderHelp("⏎", "search", "esc", "cancel"))
	case m.loading:
		s.WriteString(statusStyle.Render(" Searching for “" + m.searchQuery + "”… "))
	case m.searchQuery == "":
		s.WriteString(subtitleStyle.Render("Search all mail") + "\n\n")
		s.WriteString(m.renderHelp("/", "search", "esc", "back"))
	case len(m.searchResults) == 0:
		s.WriteString(listTitle("No results for “"+m.searchQuery+"”", 0, 0, 0, 0))
		s.WriteString("\n" + m.renderHelp("/", "search again", "esc", "back"))
	default:
		page := m.listPageHeight()
		offset := keepVisible(m.searchCursor, m.searchCursor, 0, page)
		end := min(offset+page, len(m.searchResults))
		s.WriteString(listTitle("“"+m.searchQuery+"”", offset, end, len(m.searchResults), len(m.searchResults)))
		for i := offset; i < end; i++ {
			s.WriteString(m.emailRow(m.searchResults[i], i == m.searchCursor))
		}
		s.WriteString("\n" + m.renderHelp("⏎", "read", "/", "new search", "esc", "back"))
	}
	return s.String()
}

func (m Model) settingsView() string {
	var s strings.Builder
	s.WriteString(subtitleStyle.Render("Settings") + "\n\n")
	offline := "off"
	if m.offlineMode {
		offline = "on"
	}
	settings := []string{"Offline mode: " + offline}
	for i, setting := range settings {
		s.WriteString(m.selectableRow(i == m.settingsCursor, "  "+setting, "▶ "+setting))
	}
	s.WriteString("\n" + helpStyle.Width(m.contentWidth()).Render("Offline mode reads mail from the local cache instead of the server.") + "\n")
	s.WriteString("\n" + m.renderHelp("⏎", "toggle", "esc", "back"))
	return s.String()
}
