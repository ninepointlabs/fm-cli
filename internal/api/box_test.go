package api

import (
	"testing"
	"time"

	"git.sr.ht/~rockorager/go-jmap"
	"git.sr.ht/~rockorager/go-jmap/mail"
	"git.sr.ht/~rockorager/go-jmap/mail/email"
)

func TestFindBox(t *testing.T) {
	boxes := []Box{{ID: "P-F", Kind: "inbox", Name: "Inbox"}, {ID: "P6-", Kind: "archive", Name: "Archive"}, {ID: "Pibo", Name: "Finance"}}
	for _, tc := range []struct{ sel, id string }{{"inbox", "P-F"}, {"INBOX", "P-F"}, {"", "P-F"}, {"P6-", "P6-"}, {"finance", "Pibo"}} {
		box, ok := FindBox(boxes, tc.sel)
		if !ok || box.ID != tc.id {
			t.Errorf("%q -> %v %v", tc.sel, box, ok)
		}
	}
	if _, ok := FindBox(boxes, "nope"); ok {
		t.Error("nope should not match")
	}
}

func TestInitials(t *testing.T) {
	for in, want := range map[string]string{"The HEY Team": "TH", "support@hey.com": "S", "": "?", "éric d": "ÉD", "  --  ": "?"} {
		if got := initials(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestURLs(t *testing.T) {
	if got := boxURL("Inbox", "inbox", "u5afe405d"); got != "https://app.fastmail.com/mail/Inbox?u=5afe405d" {
		t.Error(got)
	}
	if got := boxURL("Baha'i/Fargo", "", ""); got != "https://app.fastmail.com/mail/Baha%27i%2FFargo" {
		t.Error(got)
	}
	if got := messageURL("Inbox", "u5afe405d", "AB5kIMdwqots", "StmlKEsTVdXB"); got != "https://app.fastmail.com/mail/Inbox/AB5kIMdwqots.StmlKEsTVdXB?u=5afe405d" {
		t.Error(got)
	}
	if got := messageURL("Inbox", "u5afe405d", "AB5kIMdwqots", ""); got != "https://app.fastmail.com/mail/Inbox/AB5kIMdwqots?u=5afe405d" {
		t.Error(got)
	}
	if got := messageURL("", "", "", ""); got != "https://app.fastmail.com/mail/Inbox" {
		t.Error(got)
	}
}

func TestBuildPostingCountsUnseenInBox(t *testing.T) {
	box := &Box{ID: "P-F", Name: "Inbox", Kind: "inbox", AccountID: "u1"}
	older := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	head := &email.Email{ID: "m2", ThreadID: "t1", Subject: " Hello ", Preview: "hi", ReceivedAt: &newer,
		From:       []*mail.Address{{Name: "Ada Lovelace", Email: "ada@example.com"}},
		Keywords:   map[string]bool{"$seen": true},
		MailboxIDs: map[jmap.ID]bool{"P-F": true}}
	unseenInBox := &email.Email{ID: "m1", ThreadID: "t1", ReceivedAt: &older, Keywords: map[string]bool{}, MailboxIDs: map[jmap.ID]bool{"P-F": true}}
	unseenElsewhere := &email.Email{ID: "m0", ThreadID: "t1", ReceivedAt: &older, Keywords: map[string]bool{}, MailboxIDs: map[jmap.ID]bool{"P6-": true}}
	p := buildPosting(box, head, []*email.Email{head, unseenInBox, unseenElsewhere}, nil)
	if p.ID != "t1" || p.EmailID != "m2" || p.AccountID != "u1" || p.Name != "Hello" {
		t.Fatalf("%+v", p)
	}
	if p.Seen || p.UnseenCount != 1 || p.VisibleEntryCount != 2 {
		t.Fatalf("seen=%v unseen=%d visible=%d", p.Seen, p.UnseenCount, p.VisibleEntryCount)
	}
	if p.ActiveAt != "2026-09-01T13:00:00Z" || p.Creator.Initials != "AL" {
		t.Fatalf("%+v", p)
	}
	if p.BoxID != "P-F" || p.BoxKind != "inbox" || p.BoxName != "Inbox" {
		t.Fatalf("box fields: %+v", p)
	}
	// Scoped across included folders: the Archive copy now counts too.
	scoped := buildPosting(box, head, []*email.Email{head, unseenInBox, unseenElsewhere}, map[string]bool{"P-F": true, "P6-": true})
	if scoped.UnseenCount != 2 || scoped.VisibleEntryCount != 3 {
		t.Fatalf("scoped unseen=%d visible=%d", scoped.UnseenCount, scoped.VisibleEntryCount)
	}
}

func TestIncludedFolders(t *testing.T) {
	boxes := []Box{
		{ID: "P-F", Kind: "inbox", Name: "Inbox", Path: "Inbox"},
		{ID: "P4k", Kind: "junk", Name: "Spam", Path: "Spam"},
		{ID: "P1-", Kind: "trash", Name: "Trash", Path: "Trash"},
		{ID: "Pibo", Name: "FInance", Path: "FInance"},
		{ID: "P-Tdi", Name: "Other Services", Path: "Other Services"},
		{ID: "Pvdk", Name: "Gmail", Path: "Other Services/Gmail", ParentID: "P-Tdi"},
		{ID: "P-TTF", Name: "Family", Path: "Family"},
	}
	included, excluded := includedFolders(boxes, []string{" other services ", "family"})
	for _, id := range []string{"P-F", "Pibo"} {
		if !included[id] {
			t.Errorf("%s should be included", id)
		}
	}
	for _, id := range []string{"P4k", "P1-", "P-Tdi", "Pvdk", "P-TTF"} {
		if included[id] {
			t.Errorf("%s should be excluded", id)
		}
	}
	if len(excluded) != 5 {
		t.Errorf("excluded: %v", excluded)
	}
	included, _ = includedFolders(boxes, []string{"Pibo"})
	if included["Pibo"] {
		t.Error("exclusion by id failed")
	}
}

func TestEventSourceURL(t *testing.T) {
	got, err := eventSourceURL("https://api.fastmail.com/jmap/event/?types={types}&closeafter={closeafter}&ping={ping}", []string{"Email", "Mailbox"}, 60)
	if err != nil || got != "https://api.fastmail.com/jmap/event/?types=Email%2CMailbox&closeafter=no&ping=60" {
		t.Fatal(got, err)
	}
	got, err = eventSourceURL("https://example.com/events", nil, 30)
	if err != nil || got != "https://example.com/events?closeafter=no&ping=30&types=%2A" {
		t.Fatal(got, err)
	}
}
