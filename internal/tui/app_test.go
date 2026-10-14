package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"fm-cli/internal/model"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func key(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func send(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func sampleEmails(n int) []model.Email {
	var out []model.Email
	for i := range n {
		out = append(out, model.Email{
			ID:       fmt.Sprintf("e%d", i),
			From:     fmt.Sprintf("Sender Number %d <sender%d@example.com>", i, i),
			Subject:  fmt.Sprintf("Subject line %d that is long enough to need truncating on a narrow terminal", i),
			Date:     []string{"2026-01-02 15:04", "2026-01-02T15:04:00Z", "not a date at all, and long"}[i%3],
			IsUnread: i%3 == 0,
		})
	}
	return out
}

func testModel(width, height int) Model {
	m := NewModelWithStorage(nil, nil, nil, false)
	m.width, m.height = width, height
	for i := range 40 {
		m.mailboxes = append(m.mailboxes, model.Mailbox{ID: fmt.Sprintf("mb%d", i), Name: fmt.Sprintf("Folder %d", i), UnreadCount: i})
	}
	m.emails = sampleEmails(100)
	m.searchResults = sampleEmails(60)
	m.searchQuery = ""
	day := startOfDay(time.Now())
	for i := range 40 {
		m.events = append(m.events, model.CalendarEvent{
			ID: fmt.Sprintf("ev%d", i), Title: fmt.Sprintf("Event %d", i),
			Start: day.AddDate(0, 0, i/3).Add(time.Duration(9+i%3) * time.Hour), Location: "Room 1",
		})
	}
	m.calendars = []model.Calendar{{ID: "cal", IsVisible: true, MayReadItems: true}}
	m.addressBooks = []model.AddressBook{{ID: "ab", IsDefault: true, MayReadItems: true}}
	for i := range 200 {
		m.contacts = append(m.contacts, model.Contact{ID: fmt.Sprintf("c%d", i), FullName: fmt.Sprintf("Person %d", i),
			Emails: []model.ContactEmail{{Email: fmt.Sprintf("p%d@example.com", i)}}})
	}
	return m
}

// TestViewsFitTheTerminal renders every view at a few sizes and checks the
// frame never overflows — in the alt screen an overflow scrolls the header
// off the top.
func TestViewsFitTheTerminal(t *testing.T) {
	var longBody strings.Builder
	for i := range 300 {
		fmt.Fprintf(&longBody, "Body line %d with some words in it to wrap around the reader width.\n", i)
	}
	setups := map[string]func(Model) Model{
		"menu":      func(m Model) Model { m.state = viewMainMenu; return m },
		"mailboxes": func(m Model) Model { m.state = viewMailboxes; return m },
		"emails":    func(m Model) Model { m.state = viewEmails; return m },
		"emails-filtered": func(m Model) Model {
			m.state = viewEmails
			m.searchQuery = "1"
			return m
		},
		"reader": func(m Model) Model {
			m, _ = m.openReader(m.emails[0], viewEmails)
			return send(t, m, emailBodyLoadedMsg{body: longBody.String()})
		},
		"calendar": func(m Model) Model { m.state = viewCalendar; return m },
		"contacts": func(m Model) Model { m.state = viewContacts; return m },
		"search": func(m Model) Model {
			m.state = viewSearch
			m.searchQuery = "hello"
			return m
		},
		"settings": func(m Model) Model { m.state = viewSettings; return m },
		"filter-typing": func(m Model) Model {
			m.state = viewEmails
			m.searchActive = true
			return m
		},
		"compose-to": func(m Model) Model {
			m, _ = m.startCompose("", "", "")
			return m
		},
		"compose": func(m Model) Model {
			m.state = viewComposeConfirm
			m.composeBody = longBody.String()
			return m
		},
	}
	sizes := [][2]int{{80, 24}, {120, 40}, {60, 16}}
	for name, setup := range setups {
		for _, size := range sizes {
			for _, withErr := range []bool{false, true} {
				m := setup(send(t, testModel(0, 0), tea.WindowSizeMsg{Width: size[0], Height: size[1]}))
				if withErr {
					m.err = errors.New("something went wrong while talking to the server, and the message is long")
				}
				// Walk the cursor down past the first page.
				for range 50 {
					m.moveCursor(1)
				}
				view := m.View()
				if h := lipgloss.Height(view); h > size[1] {
					t.Errorf("%s at %dx%d (err=%v): %d lines, want <= %d", name, size[0], size[1], withErr, h, size[1])
				}
				if w := lipgloss.Width(view); w > size[0] {
					t.Errorf("%s at %dx%d (err=%v): %d columns, want <= %d", name, size[0], size[1], withErr, w, size[0])
				}
			}
		}
	}
}

// TestSelectionFollowsFilter checks that with a filter active, Enter opens
// and d deletes the highlighted email — not the one at the same index in the
// unfiltered list.
func TestSelectionFollowsFilter(t *testing.T) {
	m := testModel(100, 30)
	m.state = viewEmails
	m.emails = []model.Email{
		{ID: "a", Subject: "alpha"},
		{ID: "b", Subject: "beta"},
		{ID: "c", Subject: "gamma beta"},
	}
	m.searchQuery = "beta"

	m = send(t, m, key("j"))
	if e, _ := m.selectedEmail(); e.ID != "c" {
		t.Fatalf("selected %q, want c", e.ID)
	}
	opened := send(t, m, key("enter"))
	if opened.state != viewBody || opened.openEmail.ID != "c" {
		t.Fatalf("opened %q in state %v, want c in the reader", opened.openEmail.ID, opened.state)
	}
	back := send(t, opened, key("esc"))
	if back.state != viewEmails {
		t.Fatalf("esc from reader went to %v, want the email list", back.state)
	}

	m.removeEmail("c")
	if len(m.emails) != 2 || m.emails[0].ID != "a" || m.emails[1].ID != "b" {
		t.Fatalf("after removing c: %+v", m.emails)
	}
	if e, _ := m.selectedEmail(); e.ID != "b" {
		t.Fatalf("cursor on %q after delete, want b", e.ID)
	}
}

func TestReaderEscReturnsToSearch(t *testing.T) {
	m := testModel(100, 30)
	m.state = viewSearch
	m.searchQuery = "x"
	m = send(t, m, key("j"), key("enter"))
	if m.state != viewBody || m.openEmail.ID != "e1" {
		t.Fatalf("opened %q in state %v", m.openEmail.ID, m.state)
	}
	if m = send(t, m, key("esc")); m.state != viewSearch {
		t.Fatalf("esc went to %v, want search results", m.state)
	}
}

func TestReaderScrollIsClamped(t *testing.T) {
	m := testModel(80, 24)
	m, _ = m.openReader(m.emails[0], viewEmails)
	m = send(t, m, emailBodyLoadedMsg{body: "short body"})
	for range 20 {
		m = send(t, m, key("j"))
	}
	if m.bodyScrollPos != 0 {
		t.Fatalf("scrolled to %d in a body that fits", m.bodyScrollPos)
	}
}

func TestStartOfDayIsLocal(t *testing.T) {
	loc := time.FixedZone("UTC-7", -7*3600)
	old := time.Local
	time.Local = loc
	defer func() { time.Local = old }()

	evening := time.Date(2026, 3, 10, 20, 0, 0, 0, loc) // 03:00 UTC the next day
	got := startOfDay(evening)
	if want := time.Date(2026, 3, 10, 0, 0, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("startOfDay = %v, want %v", got, want)
	}
}

func TestKeepVisible(t *testing.T) {
	cases := []struct{ top, bottom, offset, page, want int }{
		{0, 0, 0, 10, 0},
		{9, 9, 0, 10, 0},
		{10, 10, 0, 10, 1},
		{3, 3, 5, 10, 3},
		{4, 5, 0, 5, 1},
	}
	for _, c := range cases {
		if got := keepVisible(c.top, c.bottom, c.offset, c.page); got != c.want {
			t.Errorf("keepVisible(%d,%d,%d,%d) = %d, want %d", c.top, c.bottom, c.offset, c.page, got, c.want)
		}
	}
}

func TestQuoteReply(t *testing.T) {
	got := quoteReply(model.Email{From: "Ann", Date: "2026-01-02 15:04"}, "[Converted HTML]\nhello\n\nbye\n")
	want := "\n\nOn 2026-01-02 15:04, Ann wrote:\n> hello\n>\n> bye\n"
	if got != want {
		t.Fatalf("quoteReply = %q, want %q", got, want)
	}
}

func TestSenderName(t *testing.T) {
	cases := map[string]string{
		`Ann Lee <ann@example.com>`:                  "Ann Lee",
		`Lee, Ann <ann@example.com>`:                 "Lee, Ann",
		`ann@example.com, Bob <b@example.com>`:       "ann@example.com",
		`ann@example.com`:                            "ann@example.com",
		`<ann@example.com>`:                          "ann@example.com",
		`Ann <ann@example.com>, Bob <b@example.com>`: "Ann",
	}
	for in, want := range cases {
		if got := senderName(in); got != want {
			t.Errorf("senderName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIdentitiesLoadedSelectsPreferred(t *testing.T) {
	m := testModel(100, 30)
	m = send(t, m, identitiesLoadedMsg{emails: []string{"a@example.com", "b@example.com", "c@example.com"}, defaultIdx: 2})
	if m.identityIdx != 2 || m.identities[m.identityIdx] != "c@example.com" {
		t.Errorf("identityIdx = %d, identities = %v", m.identityIdx, m.identities)
	}
}
