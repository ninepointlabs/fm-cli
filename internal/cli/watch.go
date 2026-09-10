package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"fm-cli/internal/api"

	"git.sr.ht/~rockorager/go-jmap"
)

// Watch events.
const (
	eventAdded        = "added"
	eventUpdated      = "updated"
	eventDeleted      = "deleted"
	eventNew          = "new"
	eventResync       = "resync"
	eventReady        = "ready"
	eventDisconnected = "disconnected"
)

var defaultEvents = []string{eventAdded, eventUpdated, eventDeleted, eventResync}

const (
	pushPingSeconds   = 60
	maxChangesPerReq  = 256
	reconnectMin      = time.Second
	reconnectMax      = time.Minute
	healthyConnection = 30 * time.Second
)

type watchLine struct {
	Change  string       `json:"change"`
	New     *bool        `json:"new,omitempty"`
	Box     *boxRef      `json:"box,omitempty"`
	Posting *api.Posting `json:"posting,omitempty"`
}

type boxRef struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type accountWatch struct {
	info    api.AccountInfo
	state   string
	boxes   map[string]api.Box
	watched map[string]bool // mailbox ids selected by --box; empty means all
}

type watcher struct {
	app      *App
	client   *api.Client
	events   map[string]bool
	selector []string
	accounts map[string]*accountWatch
	out      *bufio.Writer
	mu       sync.Mutex
}

func (app *App) watch(ctx context.Context, a *Args) (*Result, error) {
	events, err := parseEvents(a.Events)
	if err != nil {
		return nil, err
	}
	client, _, err := app.connect(ctx)
	if err != nil {
		return nil, err
	}
	selector := a.Account
	if selector == "" {
		selector = "all"
	}
	accounts, err := selectAccounts(client, selector)
	if err != nil {
		return nil, err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	// Fastmail keeps one push connection per sign-in: a second EventSource
	// closes the first, and two watches then take turns killing each other.
	// One watch per machine, then; the plugin's is normally the one.
	release, err := acquireWatchLock()
	if err != nil {
		return nil, err
	}
	defer release()

	w := &watcher{
		app:      app,
		client:   client,
		events:   events,
		selector: a.Boxes,
		accounts: map[string]*accountWatch{},
		out:      bufio.NewWriter(app.Stdout),
	}
	for _, account := range accounts {
		aw := &accountWatch{info: account}
		if err := w.loadBoxes(ctx, aw); err != nil {
			return nil, err
		}
		state, err := client.EmailState(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		aw.state = state
		w.accounts[account.ID] = aw
	}
	if len(a.Boxes) > 0 {
		matched := false
		for _, aw := range w.accounts {
			if len(aw.watched) > 0 {
				matched = true
			}
		}
		if !matched {
			return nil, &Error{Code: "usage", Message: fmt.Sprintf("no mailbox matches %s", strings.Join(a.Boxes, ", "))}
		}
	}

	return nil, w.run(ctx)
}

func parseEvents(spec string) (map[string]bool, error) {
	events := map[string]bool{}
	list := defaultEvents
	if strings.TrimSpace(spec) != "" {
		list = strings.Split(spec, ",")
	}
	for _, item := range list {
		name := strings.TrimSpace(item)
		switch name {
		case eventAdded, eventUpdated, eventDeleted, eventNew, eventResync:
			events[name] = true
		case "":
		default:
			return nil, usageError("unknown event %q", name)
		}
	}
	return events, nil
}

func (w *watcher) loadBoxes(ctx context.Context, aw *accountWatch) error {
	boxes, err := w.client.Mailboxes(ctx, aw.info.ID)
	if err != nil {
		return err
	}
	aw.boxes = map[string]api.Box{}
	for _, box := range boxes {
		aw.boxes[box.ID] = box
	}
	aw.watched = map[string]bool{}
	for _, sel := range w.selector {
		if box, ok := api.FindBox(boxes, sel); ok {
			aw.watched[box.ID] = true
		}
	}
	return nil
}

func (w *watcher) run(ctx context.Context) error {
	backoff := reconnectMin
	for {
		var openOnce sync.Once
		var openedAt time.Time
		err := w.client.Listen(ctx, []string{"Email", "Mailbox"}, pushPingSeconds, api.PushHandlers{
			Open: func() {
				openOnce.Do(func() {
					openedAt = time.Now()
					w.catchUpAll(ctx)
					w.emit(watchLine{Change: eventReady})
				})
			},
			StateChange: func(change *jmap.StateChange) {
				for id, types := range change.Changed {
					aw, ok := w.accounts[string(id)]
					if !ok {
						continue
					}
					if _, ok := types["Mailbox"]; ok {
						if err := w.loadBoxes(ctx, aw); err != nil {
							w.diag("mailbox reload failed: %v", err)
						}
					}
					if state, ok := types["Email"]; ok && state != aw.state {
						w.catchUp(ctx, aw)
					}
				}
			},
		})
		if ctx.Err() != nil {
			w.out.Flush()
			return nil
		}
		if errors.Is(err, api.ErrUnauthorized) {
			w.out.Flush()
			return err
		}
		w.emit(watchLine{Change: eventDisconnected})
		// A connection that lived a while earns a fresh backoff; one that died
		// at once (another watch on the same sign-in, say) keeps growing it.
		if !openedAt.IsZero() && time.Since(openedAt) > healthyConnection {
			backoff = reconnectMin
		}
		if err != nil {
			w.diag("%v; reconnecting in %s", err, backoff)
		}
		select {
		case <-ctx.Done():
			w.out.Flush()
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > reconnectMax {
			backoff = reconnectMax
		}
	}
}

func (w *watcher) catchUpAll(ctx context.Context) {
	ids := make([]string, 0, len(w.accounts))
	for id := range w.accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		w.catchUp(ctx, w.accounts[id])
	}
}

// catchUp walks Email/changes from the account's cursor to the present and
// emits a line per email, then advances the cursor.
func (w *watcher) catchUp(ctx context.Context, aw *accountWatch) {
	for {
		changes, err := w.client.EmailChanges(ctx, aw.info.ID, aw.state, maxChangesPerReq)
		if err != nil {
			if errors.Is(err, api.ErrCannotCalculateChanges) {
				if w.events[eventResync] {
					w.emit(watchLine{Change: eventResync})
				}
				if state, err := w.client.EmailState(ctx, aw.info.ID); err == nil {
					aw.state = state
				}
				return
			}
			if ctx.Err() == nil {
				w.diag("Email/changes failed: %v", err)
			}
			return
		}

		created := map[jmap.ID]bool{}
		for _, id := range changes.Created {
			created[id] = true
		}
		var fetch []jmap.ID
		fetch = append(fetch, changes.Created...)
		fetch = append(fetch, changes.Updated...)
		emails, err := w.client.EmailsByID(ctx, aw.info.ID, fetch)
		if err != nil {
			if ctx.Err() == nil {
				w.diag("Email/get failed: %v", err)
			}
			return
		}
		for _, e := range emails {
			w.emitEmail(aw, e, created[e.ID])
		}
		for _, id := range changes.Destroyed {
			if w.events[eventDeleted] {
				w.emit(watchLine{
					Change:  eventDeleted,
					New:     boolPtr(false),
					Box:     &boxRef{},
					Posting: &api.Posting{ID: "", EmailID: string(id), AccountID: aw.info.ID},
				})
			}
		}

		aw.state = changes.NewState
		if !changes.HasMoreChanges {
			return
		}
	}
}

func (w *watcher) emitEmail(aw *accountWatch, e *jmapEmail, isCreated bool) {
	box, inWatched := w.boxFor(aw, e.MailboxIDs)
	change := eventUpdated
	if isCreated {
		change = eventAdded
	}
	isNew := isCreated && inWatched && !e.Keywords["$seen"]
	if len(aw.watched) > 0 && !inWatched && isCreated {
		// New mail outside the watched boxes is not this watch's business.
		return
	}
	if !w.events[change] && !(isNew && w.events[eventNew]) {
		return
	}
	var ref *boxRef
	var posting api.Posting
	if box != nil {
		ref = &boxRef{ID: box.ID, Kind: box.Kind, Name: box.Name}
		posting = api.PostingForEmail(box, e)
	} else {
		ref = &boxRef{}
		posting = api.PostingForEmail(nil, e)
		posting.AccountID = aw.info.ID
	}
	w.emit(watchLine{Change: change, New: boolPtr(isNew), Box: ref, Posting: &posting})
}

type jmapEmail = emailAlias

// boxFor picks the box to report an email under: a watched box it is in when
// there is one, else its first box by sort order. inWatched says whether the
// email sits in a watched box (every box counts when none was selected).
func (w *watcher) boxFor(aw *accountWatch, mailboxIDs map[jmap.ID]bool) (*api.Box, bool) {
	var candidates []api.Box
	for id, present := range mailboxIDs {
		if !present {
			continue
		}
		if box, ok := aw.boxes[string(id)]; ok {
			candidates = append(candidates, box)
		}
	}
	if len(candidates) == 0 {
		return nil, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].SortOrder != candidates[j].SortOrder {
			return candidates[i].SortOrder < candidates[j].SortOrder
		}
		return candidates[i].Name < candidates[j].Name
	})
	if len(aw.watched) == 0 {
		return &candidates[0], true
	}
	for i := range candidates {
		if aw.watched[candidates[i].ID] {
			return &candidates[i], true
		}
	}
	return &candidates[0], false
}

func (w *watcher) emit(line watchLine) {
	w.mu.Lock()
	defer w.mu.Unlock()
	enc := json.NewEncoder(w.out)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(line)
	w.out.Flush()
}

func (w *watcher) diag(format string, args ...any) {
	fmt.Fprintf(w.app.Stderr, "fm-cli watch: "+format+"\n", args...)
}

func boolPtr(v bool) *bool { return &v }
