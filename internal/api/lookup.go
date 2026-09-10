package api

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"fm-cli/internal/model"

	"git.sr.ht/~rockorager/go-jmap"
	"git.sr.ht/~rockorager/go-jmap/mail/email"
	"git.sr.ht/~rockorager/go-jmap/mail/thread"
)

var modelEmailProperties = []string{"id", "subject", "from", "to", "cc", "bcc", "replyTo", "preview", "receivedAt", "mailboxIds", "threadId", "keywords"}

// toModelEmail converts a JMAP email into the TUI's model.
func toModelEmail(e *email.Email) model.Email {
	var boxIDs []string
	for k := range e.MailboxIDs {
		boxIDs = append(boxIDs, string(k))
	}
	sort.Strings(boxIDs)
	dateStr := ""
	if e.ReceivedAt != nil {
		dateStr = e.ReceivedAt.Format("2006-01-02 15:04")
	}
	return model.Email{
		ID:         string(e.ID),
		Subject:    CleanLine(e.Subject),
		From:       CleanLine(formatAddresses(e.From)),
		To:         CleanLine(formatAddresses(e.To)),
		Cc:         CleanLine(formatAddresses(e.CC)),
		Bcc:        CleanLine(formatAddresses(e.BCC)),
		ReplyTo:    CleanLine(formatAddresses(e.ReplyTo)),
		Preview:    CleanLine(e.Preview),
		Date:       dateStr,
		IsUnread:   !e.Keywords["$seen"],
		IsFlagged:  e.Keywords["$flagged"],
		IsDraft:    e.Keywords["$draft"],
		ThreadID:   string(e.ThreadID),
		MailboxIDs: boxIDs,
	}
}

// FetchEmailsByIDs loads model emails for the given ids, newest first.
func (c *Client) FetchEmailsByIDs(ctx context.Context, ids []string) ([]model.Email, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	jids := make([]jmap.ID, 0, len(ids))
	for _, id := range ids {
		jids = append(jids, jmap.ID(id))
	}
	req := &jmap.Request{}
	call := req.Invoke(&email.Get{Account: c.getMailAccountID(), IDs: jids, Properties: modelEmailProperties})
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	args, err := responseFor(resp, call)
	if err != nil {
		return nil, err
	}
	got, ok := args.(*email.GetResponse)
	if !ok {
		return nil, errors.New("unexpected Email/get response")
	}
	sort.Slice(got.List, func(i, j int) bool {
		a, b := got.List[i].ReceivedAt, got.List[j].ReceivedAt
		if a == nil || b == nil {
			return a != nil
		}
		return a.After(*b)
	})
	out := make([]model.Email, 0, len(got.List))
	for _, e := range got.List {
		out = append(out, toModelEmail(e))
	}
	return out, nil
}

// FetchThreadEmails loads every email of a thread, newest first.
func (c *Client) FetchThreadEmails(ctx context.Context, threadID string) ([]model.Email, error) {
	req := &jmap.Request{}
	call := req.Invoke(&thread.Get{Account: c.getMailAccountID(), IDs: []jmap.ID{jmap.ID(threadID)}})
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	args, err := responseFor(resp, call)
	if err != nil {
		return nil, err
	}
	got, ok := args.(*thread.GetResponse)
	if !ok || len(got.List) == 0 {
		return nil, fmt.Errorf("thread %s not found", threadID)
	}
	ids := make([]string, 0, len(got.List[0].EmailIDs))
	for _, id := range got.List[0].EmailIDs {
		ids = append(ids, string(id))
	}
	return c.FetchEmailsByIDs(ctx, ids)
}
