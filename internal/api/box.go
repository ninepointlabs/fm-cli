package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"git.sr.ht/~rockorager/go-jmap"
	"git.sr.ht/~rockorager/go-jmap/mail"
	"git.sr.ht/~rockorager/go-jmap/mail/email"
	"git.sr.ht/~rockorager/go-jmap/mail/mailbox"
	"git.sr.ht/~rockorager/go-jmap/mail/thread"
)

// WebOrigin is the Fastmail web app.
const WebOrigin = "https://app.fastmail.com"

// MaxPostings caps a box read.
const MaxPostings = 200

var postingProperties = []string{"id", "threadId", "subject", "from", "preview", "receivedAt", "keywords", "mailboxIds"}
var threadMemberProperties = []string{"id", "threadId", "keywords", "mailboxIds", "receivedAt"}

// Box is a mailbox as the scripting surface reports it.
type Box struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	AccountID   string `json:"account_id"`
	AppURL      string `json:"app_url"`
	UnreadCount uint64 `json:"unread_count"`
	TotalCount  uint64 `json:"total_count"`
	SortOrder   uint64 `json:"-"`
	ParentID    string `json:"-"`
	// Path is the slash-joined folder path, e.g. "Other Services/Gmail".
	Path string `json:"-"`
}

// BoxView is a box read: the box, the folders worth showing, and the postings.
type BoxView struct {
	Box
	Folders  []Box     `json:"folders"`
	Postings []Posting `json:"postings"`
}

// Roles never included in the virtual all box.
var excludedRoles = map[string]bool{"junk": true, "trash": true, "drafts": true, "sent": true, "snoozed": true, "scheduled": true, "archive": true}

// Posting is one thread as seen from a box.
type Posting struct {
	ID                    string  `json:"id"`
	ThreadID              string  `json:"thread_id"`
	EmailID               string  `json:"email_id"`
	AccountID             string  `json:"account_id"`
	BoxID                 string  `json:"box_id"`
	BoxKind               string  `json:"box_kind"`
	BoxName               string  `json:"box_name"`
	Name                  string  `json:"name"`
	Summary               string  `json:"summary"`
	ActiveAt              string  `json:"active_at"`
	Seen                  bool    `json:"seen"`
	UnseenCount           int     `json:"unseen_count"`
	VisibleEntryCount     int     `json:"visible_entry_count"`
	Flagged               bool    `json:"flagged"`
	AppURL                string  `json:"app_url"`
	AlternativeSenderName string  `json:"alternative_sender_name"`
	Creator               Creator `json:"creator"`
}

// Creator is the sender of a posting's newest message.
type Creator struct {
	Name         string `json:"name"`
	EmailAddress string `json:"email_address"`
	Initials     string `json:"initials"`
}

// Mailboxes lists an account's mailboxes as boxes.
func (c *Client) Mailboxes(ctx context.Context, account string) ([]Box, error) {
	req := &jmap.Request{}
	id := req.Invoke(&mailbox.Get{
		Account:    jmap.ID(account),
		Properties: []string{"id", "name", "role", "sortOrder", "totalEmails", "unreadEmails", "parentId"},
	})
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	args, err := responseFor(resp, id)
	if err != nil {
		return nil, err
	}
	got, ok := args.(*mailbox.GetResponse)
	if !ok {
		return nil, errors.New("unexpected Mailbox/get response")
	}
	boxes := make([]Box, 0, len(got.List))
	for _, m := range got.List {
		boxes = append(boxes, Box{
			ID:          string(m.ID),
			Kind:        string(m.Role),
			Name:        CleanLine(m.Name),
			AccountID:   account,
			AppURL:      boxURL(m.Name, string(m.Role), account),
			UnreadCount: m.UnreadEmails,
			TotalCount:  m.TotalEmails,
			SortOrder:   m.SortOrder,
			ParentID:    string(m.ParentID),
		})
	}
	names := map[string]*Box{}
	for i := range boxes {
		names[boxes[i].ID] = &boxes[i]
	}
	for i := range boxes {
		boxes[i].Path = folderPath(names, &boxes[i], 0)
	}
	sort.Slice(boxes, func(i, j int) bool {
		if boxes[i].SortOrder != boxes[j].SortOrder {
			return boxes[i].SortOrder < boxes[j].SortOrder
		}
		return boxes[i].Name < boxes[j].Name
	})
	return boxes, nil
}

func folderPath(byID map[string]*Box, box *Box, depth int) string {
	if box.ParentID == "" || depth > 16 {
		return box.Name
	}
	parent, ok := byID[box.ParentID]
	if !ok {
		return box.Name
	}
	return folderPath(byID, parent, depth+1) + "/" + box.Name
}

// includedFolders applies the role exclusions and the user's --exclude list
// (id, role, name, or path; a parent excludes its children) and returns the
// ids that remain, plus the excluded ids.
func includedFolders(boxes []Box, excludes []string) (included map[string]bool, excluded []string) {
	wanted := map[string]bool{}
	for _, e := range excludes {
		if v := strings.ToLower(strings.TrimSpace(e)); v != "" {
			wanted[v] = true
		}
	}
	isExcluded := map[string]bool{}
	for _, b := range boxes {
		if excludedRoles[strings.ToLower(b.Kind)] || wanted[strings.ToLower(b.ID)] || (b.Kind != "" && wanted[strings.ToLower(b.Kind)]) || wanted[strings.ToLower(b.Name)] || wanted[strings.ToLower(b.Path)] {
			isExcluded[b.ID] = true
		}
	}
	// Children of an excluded folder are excluded too.
	byID := map[string]Box{}
	for _, b := range boxes {
		byID[b.ID] = b
	}
	for _, b := range boxes {
		for cur, depth := b, 0; cur.ParentID != "" && depth < 16; depth++ {
			if isExcluded[cur.ParentID] {
				isExcluded[b.ID] = true
				break
			}
			parent, ok := byID[cur.ParentID]
			if !ok {
				break
			}
			cur = parent
		}
	}
	included = map[string]bool{}
	for _, b := range boxes {
		if isExcluded[b.ID] {
			excluded = append(excluded, b.ID)
		} else {
			included[b.ID] = true
		}
	}
	sort.Strings(excluded)
	return included, excluded
}

// FindBox resolves a selector — a role such as "inbox", a mailbox id, or a
// name — among boxes.
func FindBox(boxes []Box, selector string) (*Box, bool) {
	want := strings.TrimSpace(selector)
	if want == "" {
		want = "inbox"
	}
	lower := strings.ToLower(want)
	for i := range boxes {
		if strings.ToLower(boxes[i].Kind) == lower && boxes[i].Kind != "" {
			return &boxes[i], true
		}
	}
	for i := range boxes {
		if boxes[i].ID == want {
			return &boxes[i], true
		}
	}
	for i := range boxes {
		if strings.ToLower(boxes[i].Name) == lower {
			return &boxes[i], true
		}
	}
	return nil, false
}

// BoxPostings reads the newest threads in a box, one posting per thread, with
// the thread's seen state computed over every email of the thread in the box.
// The whole read is one JMAP request chained with result references.
func (c *Client) BoxPostings(ctx context.Context, box *Box, limit int) ([]Posting, error) {
	if limit <= 0 || limit > MaxPostings {
		limit = MaxPostings
	}
	account := jmap.ID(box.AccountID)
	req := &jmap.Request{}
	queryID := req.Invoke(&email.Query{
		Account:         account,
		Filter:          &email.FilterCondition{InMailbox: jmap.ID(box.ID)},
		Sort:            []*email.SortComparator{{Property: "receivedAt", IsAscending: false}},
		Limit:           uint64(limit),
		CollapseThreads: true,
	})
	headID := req.Invoke(&email.Get{
		Account:      account,
		Properties:   postingProperties,
		ReferenceIDs: &jmap.ResultReference{ResultOf: queryID, Name: "Email/query", Path: "/ids"},
	})
	threadID := req.Invoke(&thread.Get{
		Account:      account,
		ReferenceIDs: &jmap.ResultReference{ResultOf: headID, Name: "Email/get", Path: "/list/*/threadId"},
	})
	membersID := req.Invoke(&email.Get{
		Account:      account,
		Properties:   threadMemberProperties,
		ReferenceIDs: &jmap.ResultReference{ResultOf: threadID, Name: "Thread/get", Path: "/list/*/emailIds"},
	})
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}

	queryArgs, err := responseFor(resp, queryID)
	if err != nil {
		return nil, err
	}
	query, _ := queryArgs.(*email.QueryResponse)
	headArgs, err := responseFor(resp, headID)
	if err != nil {
		return nil, err
	}
	heads, _ := headArgs.(*email.GetResponse)
	membersArgs, err := responseFor(resp, membersID)
	if err != nil {
		return nil, err
	}
	members, _ := membersArgs.(*email.GetResponse)
	if query == nil || heads == nil || members == nil {
		return nil, errors.New("unexpected box read response")
	}

	byThread := map[jmap.ID][]*email.Email{}
	for _, e := range members.List {
		byThread[e.ThreadID] = append(byThread[e.ThreadID], e)
	}
	headByID := map[jmap.ID]*email.Email{}
	for _, e := range heads.List {
		headByID[e.ID] = e
	}

	postings := make([]Posting, 0, len(query.IDs))
	for _, id := range query.IDs {
		head, ok := headByID[id]
		if !ok {
			continue
		}
		postings = append(postings, buildPosting(box, head, byThread[head.ThreadID], nil))
	}
	return postings, nil
}

// ViewBox reads one real folder as a BoxView.
func (c *Client) ViewBox(ctx context.Context, box *Box, limit int) (*BoxView, error) {
	postings, err := c.BoxPostings(ctx, box, limit)
	if err != nil {
		return nil, err
	}
	return &BoxView{Box: *box, Folders: []Box{*box}, Postings: postings}, nil
}

// ViewAll reads the virtual all box: the newest Inbox threads plus every
// unseen thread in every other included folder.
func (c *Client) ViewAll(ctx context.Context, account string, boxes []Box, limit int, excludes []string) (*BoxView, error) {
	if limit <= 0 || limit > MaxPostings {
		limit = MaxPostings
	}
	inbox, ok := FindBox(boxes, "inbox")
	if !ok {
		return nil, errors.New("the account has no Inbox")
	}
	included, excluded := includedFolders(boxes, excludes)
	otherThan := make([]jmap.ID, 0, len(excluded)+1)
	for _, id := range excluded {
		otherThan = append(otherThan, jmap.ID(id))
	}
	otherThan = append(otherThan, jmap.ID(inbox.ID))
	acct := jmap.ID(account)

	// Round trip 1: the two queries and their heads.
	req := &jmap.Request{}
	inboxQuery := req.Invoke(&email.Query{
		Account:         acct,
		Filter:          &email.FilterCondition{InMailbox: jmap.ID(inbox.ID)},
		Sort:            []*email.SortComparator{{Property: "receivedAt", IsAscending: false}},
		Limit:           uint64(limit),
		CollapseThreads: true,
	})
	inboxHeads := req.Invoke(&email.Get{
		Account:      acct,
		Properties:   postingProperties,
		ReferenceIDs: &jmap.ResultReference{ResultOf: inboxQuery, Name: "Email/query", Path: "/ids"},
	})
	unseenQuery := req.Invoke(&email.Query{
		Account:         acct,
		Filter:          &email.FilterCondition{NotKeyword: "$seen", InMailboxOtherThan: otherThan},
		Sort:            []*email.SortComparator{{Property: "receivedAt", IsAscending: false}},
		Limit:           uint64(limit),
		CollapseThreads: true,
	})
	unseenHeads := req.Invoke(&email.Get{
		Account:      acct,
		Properties:   postingProperties,
		ReferenceIDs: &jmap.ResultReference{ResultOf: unseenQuery, Name: "Email/query", Path: "/ids"},
	})
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	var heads []*email.Email
	seenThread := map[jmap.ID]bool{}
	for _, call := range []string{inboxHeads, unseenHeads} {
		args, err := responseFor(resp, call)
		if err != nil {
			return nil, err
		}
		got, ok := args.(*email.GetResponse)
		if !ok {
			return nil, errors.New("unexpected Email/get response")
		}
		for _, e := range got.List {
			if seenThread[e.ThreadID] {
				continue
			}
			seenThread[e.ThreadID] = true
			heads = append(heads, e)
		}
	}

	// Round trip 2: thread members for the seen-state.
	byThread := map[jmap.ID][]*email.Email{}
	if len(heads) > 0 {
		threadIDs := make([]jmap.ID, 0, len(heads))
		for _, h := range heads {
			threadIDs = append(threadIDs, h.ThreadID)
		}
		req2 := &jmap.Request{}
		threadCall := req2.Invoke(&thread.Get{Account: acct, IDs: threadIDs})
		membersCall := req2.Invoke(&email.Get{
			Account:      acct,
			Properties:   threadMemberProperties,
			ReferenceIDs: &jmap.ResultReference{ResultOf: threadCall, Name: "Thread/get", Path: "/list/*/emailIds"},
		})
		resp2, err := c.do(ctx, req2)
		if err != nil {
			return nil, err
		}
		args, err := responseFor(resp2, membersCall)
		if err != nil {
			return nil, err
		}
		members, ok := args.(*email.GetResponse)
		if !ok {
			return nil, errors.New("unexpected Email/get response")
		}
		for _, e := range members.List {
			byThread[e.ThreadID] = append(byThread[e.ThreadID], e)
		}
	}

	byID := map[string]Box{}
	for _, b := range boxes {
		byID[b.ID] = b
	}
	postings := make([]Posting, 0, len(heads))
	for _, head := range heads {
		box := folderFor(head, inbox, byID, included)
		postings = append(postings, buildPosting(&box, head, byThread[head.ThreadID], included))
	}
	sort.SliceStable(postings, func(i, j int) bool { return postings[i].ActiveAt > postings[j].ActiveAt })
	if len(postings) > limit {
		postings = postings[:limit]
	}

	view := &BoxView{Box: *inbox, Postings: postings}
	view.ID = "all"
	view.Kind = "all"
	view.Name = "All folders"
	view.UnreadCount = 0
	view.Folders = []Box{*inbox}
	for _, b := range boxes {
		if !included[b.ID] {
			continue
		}
		view.UnreadCount += b.UnreadCount
		if b.ID != inbox.ID && b.UnreadCount > 0 {
			view.Folders = append(view.Folders, b)
		}
	}
	return view, nil
}

// folderFor picks the folder a posting is reported from: the Inbox when the
// head sits there, else its first included folder by sort order.
func folderFor(head *email.Email, inbox *Box, byID map[string]Box, included map[string]bool) Box {
	if head.MailboxIDs[jmap.ID(inbox.ID)] {
		return *inbox
	}
	var candidates []Box
	for id, present := range head.MailboxIDs {
		if !present || !included[string(id)] {
			continue
		}
		if b, ok := byID[string(id)]; ok {
			candidates = append(candidates, b)
		}
	}
	if len(candidates) == 0 {
		return *inbox
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].SortOrder != candidates[j].SortOrder {
			return candidates[i].SortOrder < candidates[j].SortOrder
		}
		return candidates[i].Name < candidates[j].Name
	})
	return candidates[0]
}

// PostingForEmail builds a posting from one email alone, as the watch does.
func PostingForEmail(box *Box, e *email.Email) Posting {
	return buildPosting(box, e, []*email.Email{e}, nil)
}

// buildPosting folds a thread's members into one posting. Members count when
// they sit in the box, or, when scope is given, in any folder scope allows.
func buildPosting(box *Box, head *email.Email, members []*email.Email, scope map[string]bool) Posting {
	inScope := func(m *email.Email) bool {
		if scope != nil {
			for id, present := range m.MailboxIDs {
				if present && scope[string(id)] {
					return true
				}
			}
			return false
		}
		return box == nil || m.MailboxIDs[jmap.ID(box.ID)]
	}
	unseen, visible := 0, 0
	newest := head.ReceivedAt
	for _, m := range members {
		if !inScope(m) {
			continue
		}
		visible++
		if !m.Keywords["$seen"] {
			unseen++
		}
		if m.ReceivedAt != nil && (newest == nil || m.ReceivedAt.After(*newest)) {
			newest = m.ReceivedAt
		}
	}
	if visible == 0 {
		visible = 1
		if !head.Keywords["$seen"] {
			unseen = 1
		}
	}
	creator := creatorOf(head.From)
	boxName := "Inbox"
	accountID := ""
	if box != nil {
		if box.Name != "" {
			boxName = box.Name
		}
		accountID = box.AccountID
	}
	posting := Posting{
		ID:                string(head.ThreadID),
		ThreadID:          string(head.ThreadID),
		EmailID:           string(head.ID),
		AccountID:         "",
		BoxKind:           "inbox",
		BoxName:           boxName,
		Name:              CleanLine(head.Subject),
		Summary:           CleanLine(head.Preview),
		Seen:              unseen == 0,
		UnseenCount:       unseen,
		VisibleEntryCount: visible,
		Flagged:           head.Keywords["$flagged"],
		AppURL:            messageURL(boxName, accountID, string(head.ThreadID), string(head.ID)),
		Creator:           creator,
	}
	if box != nil {
		posting.AccountID = box.AccountID
		posting.BoxID = box.ID
		posting.BoxKind = box.Kind
	}
	if newest != nil {
		posting.ActiveAt = newest.UTC().Format(time.RFC3339)
	}
	if posting.Name == "" {
		posting.Name = "(no subject)"
	}
	return posting
}

func creatorOf(from []*mail.Address) Creator {
	if len(from) == 0 || from[0] == nil {
		return Creator{Name: "Unknown sender", Initials: "?"}
	}
	name := CleanLine(from[0].Name)
	address := CleanLine(from[0].Email)
	if name == "" {
		name = address
	}
	return Creator{Name: name, EmailAddress: address, Initials: initials(name)}
}

func initials(name string) string {
	var out []rune
	for _, word := range strings.Fields(name) {
		for _, r := range word {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, unicode.ToUpper(r))
				break
			}
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

// accountParam is the web app's `u` query value: the JMAP account id without
// its leading "u".
func accountParam(accountID string) string {
	return strings.TrimPrefix(accountID, "u")
}

func boxURL(name, role, accountID string) string {
	segment := name
	if role == "inbox" || segment == "" {
		segment = "Inbox"
	}
	out := WebOrigin + "/mail/" + url.PathEscape(segment)
	if accountID != "" {
		out += "?u=" + url.QueryEscape(accountParam(accountID))
	}
	return out
}

// messageURL is the web app's link to one message in its thread:
// /mail/<Mailbox>/<threadId>.<emailId>?u=<account>. Without an email id the
// thread id alone selects the conversation.
func messageURL(boxName, accountID, threadID, emailID string) string {
	if threadID == "" {
		return boxURL(boxName, "", accountID)
	}
	segment := boxName
	if segment == "" {
		segment = "Inbox"
	}
	target := url.PathEscape(threadID)
	if emailID != "" {
		target += "." + url.PathEscape(emailID)
	}
	out := WebOrigin + "/mail/" + url.PathEscape(segment) + "/" + target
	if accountID != "" {
		out += "?u=" + url.QueryEscape(accountParam(accountID))
	}
	return out
}

// SetSeen adds or removes $seen on the given ids. Each id may be a thread id,
// in which case every email in the thread is patched, or an email id. The
// returned list holds the email ids actually patched.
func (c *Client) SetSeen(ctx context.Context, account string, ids []string, seen bool) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	acct := jmap.ID(account)
	jids := make([]jmap.ID, 0, len(ids))
	for _, id := range ids {
		jids = append(jids, jmap.ID(id))
	}

	lookup := &jmap.Request{}
	threadCall := lookup.Invoke(&thread.Get{Account: acct, IDs: jids})
	resp, err := c.do(ctx, lookup)
	if err != nil {
		return nil, err
	}
	targets := map[jmap.ID]bool{}
	if args, err := responseFor(resp, threadCall); err == nil {
		if got, ok := args.(*thread.GetResponse); ok {
			for _, t := range got.List {
				for _, eid := range t.EmailIDs {
					targets[eid] = true
				}
			}
			for _, missing := range got.NotFound {
				targets[missing] = true
			}
		}
	} else {
		for _, id := range jids {
			targets[id] = true
		}
	}
	if len(targets) == 0 {
		for _, id := range jids {
			targets[id] = true
		}
	}

	update := map[jmap.ID]jmap.Patch{}
	patched := make([]string, 0, len(targets))
	for id := range targets {
		if seen {
			update[id] = jmap.Patch{"keywords/$seen": true}
		} else {
			update[id] = jmap.Patch{"keywords/$seen": nil}
		}
		patched = append(patched, string(id))
	}
	sort.Strings(patched)

	req := &jmap.Request{}
	setCall := req.Invoke(&email.Set{Account: acct, Update: update})
	resp, err = c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	args, err := responseFor(resp, setCall)
	if err != nil {
		return nil, err
	}
	set, ok := args.(*email.SetResponse)
	if !ok {
		return nil, errors.New("unexpected Email/set response")
	}
	if len(set.NotUpdated) > 0 {
		var failed []string
		for id, e := range set.NotUpdated {
			failed = append(failed, fmt.Sprintf("%s (%s)", id, e.Type))
		}
		sort.Strings(failed)
		if len(set.Updated) == 0 {
			return nil, fmt.Errorf("could not update %s", strings.Join(failed, ", "))
		}
	}
	return patched, nil
}

// emailState is Email/get with an explicit empty id list, which returns the
// current Email state and nothing else. go-jmap's Get omits an empty list.
type emailState struct {
	Account jmap.ID   `json:"accountId"`
	IDs     []jmap.ID `json:"ids"`
}

func (m *emailState) Name() string         { return "Email/get" }
func (m *emailState) Requires() []jmap.URI { return []jmap.URI{mail.URI} }

// EmailState returns the account's current Email state string.
func (c *Client) EmailState(ctx context.Context, account string) (string, error) {
	req := &jmap.Request{}
	call := req.Invoke(&emailState{Account: jmap.ID(account), IDs: []jmap.ID{}})
	resp, err := c.do(ctx, req)
	if err != nil {
		return "", err
	}
	args, err := responseFor(resp, call)
	if err != nil {
		return "", err
	}
	got, ok := args.(*email.GetResponse)
	if !ok {
		return "", errors.New("unexpected Email/get response")
	}
	return got.State, nil
}

// ErrCannotCalculateChanges says the server dropped history past the cursor.
var ErrCannotCalculateChanges = errors.New("cannotCalculateChanges")

// EmailChanges lists ids created, updated and destroyed since a state.
func (c *Client) EmailChanges(ctx context.Context, account, since string, max uint64) (*email.ChangesResponse, error) {
	req := &jmap.Request{}
	call := req.Invoke(&email.Changes{Account: jmap.ID(account), SinceState: since, MaxChanges: max})
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	args, err := responseFor(resp, call)
	if err != nil {
		var me *jmap.MethodError
		if errors.As(err, &me) && me.Type == "cannotCalculateChanges" {
			return nil, ErrCannotCalculateChanges
		}
		return nil, err
	}
	got, ok := args.(*email.ChangesResponse)
	if !ok {
		return nil, errors.New("unexpected Email/changes response")
	}
	return got, nil
}

// EmailsByID fetches posting-level properties for ids.
func (c *Client) EmailsByID(ctx context.Context, account string, ids []jmap.ID) ([]*email.Email, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	req := &jmap.Request{}
	call := req.Invoke(&email.Get{Account: jmap.ID(account), IDs: ids, Properties: postingProperties})
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
	return got.List, nil
}
