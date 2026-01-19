package api

import (
	"fmt"
	"net/http"
	netmail "net/mail"
	"sort"
	"strings"

	"fm-cli/internal/model"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"git.sr.ht/~rockorager/go-jmap"
	"git.sr.ht/~rockorager/go-jmap/mail"
	"git.sr.ht/~rockorager/go-jmap/mail/email"
	"git.sr.ht/~rockorager/go-jmap/mail/emailsubmission"
	"git.sr.ht/~rockorager/go-jmap/mail/identity"
	"git.sr.ht/~rockorager/go-jmap/mail/mailbox"
)

type Client struct {
	Client  *jmap.Client
	Session *jmap.Session
}

const FastmailSessionURL = "https://api.fastmail.com/.well-known/jmap"

// NewClient initializes a JMAP client with the given token.
func NewClient(token string) (*Client, error) {
	// Initialize the JMAP client
	c := &jmap.Client{
		SessionEndpoint: FastmailSessionURL,
		HttpClient:      &http.Client{},
	}
	c.WithAccessToken(token)

	// Phase 1: Authentication & Session Discovery
	// We fetch the session object to discover capabilities and URLs.
	if err := c.Authenticate(); err != nil {
		return nil, fmt.Errorf("failed to authenticate: %w", err)
	}

	return &Client{
		Client:  c,
		Session: c.Session,
	}, nil
}

// DebugSession prints session info for debugging
func (c *Client) DebugSession() string {
	if c.Session == nil {
		return "No session"
	}
	var sb strings.Builder
	sb.WriteString("=== JMAP Session Debug ===\n")
	sb.WriteString(fmt.Sprintf("API URL: %s\n", c.Session.APIURL))
	sb.WriteString("\nCapabilities:\n")
	for uri := range c.Session.Capabilities {
		sb.WriteString(fmt.Sprintf("  - %s\n", uri))
	}
	sb.WriteString("\nPrimary Accounts:\n")
	for uri, id := range c.Session.PrimaryAccounts {
		sb.WriteString(fmt.Sprintf("  %s: %s\n", uri, id))
	}
	sb.WriteString("\nAccounts:\n")
	for id, acc := range c.Session.Accounts {
		sb.WriteString(fmt.Sprintf("  Account %s: %s\n", id, acc.Name))
		for uri := range acc.RawCapabilities {
			sb.WriteString(fmt.Sprintf("    - %s\n", uri))
		}
	}
	return sb.String()
}

func (c *Client) getMailAccountID() jmap.ID {
	if c.Session == nil {
		return ""
	}
	return c.Session.PrimaryAccounts[mail.URI]
}

// FetchMailboxes retrieves all mailboxes using standard JMAP calls.
func (c *Client) FetchMailboxes() ([]model.Mailbox, error) {
	var mailboxes []model.Mailbox

	// Create a Request
	req := &jmap.Request{}
	mGet := &mailbox.Get{
		Account: c.getMailAccountID(),
	}
	req.Invoke(mGet)

	// Execute the request
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("JMAP request failed: %w", err)
	}

	// Iterate over the responses
	for _, invocation := range resp.Responses {
		if errArgs, ok := invocation.Args.(*jmap.MethodError); ok {
			return nil, fmt.Errorf("JMAP method error: %s (type: %s)", invocation.Name, errArgs.Type)
		}
		if res, ok := invocation.Args.(*mailbox.GetResponse); ok {
			for _, m := range res.List {
				mailboxes = append(mailboxes, model.Mailbox{
					ID:          string(m.ID),
					Name:        m.Name,
					UnreadCount: int(m.UnreadThreads),
					Role:        string(m.Role),
					ParentID:    string(m.ParentID),
					SortOrder:   int(m.SortOrder),
				})
			}
		}
	}

	// Sort mailboxes by sort order or name for safety
	sort.Slice(mailboxes, func(i, j int) bool {
		return mailboxes[i].SortOrder < mailboxes[j].SortOrder
	})

	return mailboxes, nil
}

// FetchEmails retrieves emails for a specific mailbox.
func (c *Client) FetchEmails(mailboxID string, position int) ([]model.Email, error) {
	var emails []model.Email
	const limit = 20

	// 1. Email/query
	// Sequential fallback is cleaner for this stage
	reqQuery := &jmap.Request{}
	q := &email.Query{
		Account: c.getMailAccountID(),
		Filter: &email.FilterCondition{
			InMailbox: jmap.ID(mailboxID),
		},
		Sort: []*email.SortComparator{
			{Property: "receivedAt", IsAscending: false},
		},
		Limit:    limit,
		Position: int64(position),
	}
	reqQuery.Invoke(q)

	resp1, err := c.Client.Do(reqQuery)
	if err != nil {
		return nil, fmt.Errorf("Email/query failed: %w", err)
	}

	var ids []jmap.ID
	for _, inv := range resp1.Responses {
		if res, ok := inv.Args.(*email.QueryResponse); ok {
			ids = res.IDs
		}
	}

	if len(ids) == 0 {
		return []model.Email{}, nil
	}

	// 2. Email/get
	reqGet := &jmap.Request{}
	g := &email.Get{
		Account:    c.getMailAccountID(),
		IDs:        ids,
		Properties: []string{"id", "subject", "from", "to", "cc", "bcc", "replyTo", "preview", "receivedAt", "mailboxIds", "threadId", "keywords"},
	}
	reqGet.Invoke(g)

	resp2, err := c.Client.Do(reqGet)
	if err != nil {
		return nil, fmt.Errorf("Email/get failed: %w", err)
	}

	for _, inv := range resp2.Responses {
		if res, ok := inv.Args.(*email.GetResponse); ok {
			for _, e := range res.List {
				// Convert to model.Email
				sender := formatAddresses(e.From)
				to := formatAddresses(e.To)
				cc := formatAddresses(e.CC)
				bcc := formatAddresses(e.BCC)
				replyTo := formatAddresses(e.ReplyTo)

				isUnread := true
				if _, ok := e.Keywords["$seen"]; ok {
					isUnread = false
				}
				
				isFlagged := false
				if _, ok := e.Keywords["$flagged"]; ok {
					isFlagged = true
				}

				isDraft := false
				if _, ok := e.Keywords["$draft"]; ok {
					isDraft = true
				}

				var boxIDs []string
				for k := range e.MailboxIDs {
					boxIDs = append(boxIDs, string(k))
				}

				dateStr := ""
				if e.ReceivedAt != nil {
					dateStr = e.ReceivedAt.Format("2006-01-02 15:04")
				}

				emails = append(emails, model.Email{
					ID:         string(e.ID),
					Subject:    e.Subject,
					From:       sender,
					To:         to,
					Cc:         cc,
					Bcc:        bcc,
					ReplyTo:    replyTo,
					Preview:    e.Preview,
					Date:       dateStr,
					IsUnread:   isUnread,
					IsFlagged:  isFlagged,
					IsDraft:    isDraft,
					ThreadID:   string(e.ThreadID),
					MailboxIDs: boxIDs,
				})
			}
		}
	}
	return emails, nil
}

// SearchEmails searches for emails across all mailboxes using the given query.
// The query searches across subject, from, to, and body text.
func (c *Client) SearchEmails(query string, limit int) ([]model.Email, error) {
	var emails []model.Email
	if limit <= 0 {
		limit = 50
	}

	// 1. Email/query with text filter (searches all fields)
	reqQuery := &jmap.Request{}
	q := &email.Query{
		Account: c.getMailAccountID(),
		Filter: &email.FilterCondition{
			Text: query,
		},
		Sort: []*email.SortComparator{
			{Property: "receivedAt", IsAscending: false},
		},
		Limit: uint64(limit),
	}
	reqQuery.Invoke(q)

	resp1, err := c.Client.Do(reqQuery)
	if err != nil {
		return nil, fmt.Errorf("Email/query (search) failed: %w", err)
	}

	var ids []jmap.ID
	for _, inv := range resp1.Responses {
		if res, ok := inv.Args.(*email.QueryResponse); ok {
			ids = res.IDs
		}
	}

	if len(ids) == 0 {
		return []model.Email{}, nil
	}

	// 2. Email/get
	reqGet := &jmap.Request{}
	g := &email.Get{
		Account:    c.getMailAccountID(),
		IDs:        ids,
		Properties: []string{"id", "subject", "from", "to", "cc", "bcc", "replyTo", "preview", "receivedAt", "mailboxIds", "threadId", "keywords"},
	}
	reqGet.Invoke(g)

	resp2, err := c.Client.Do(reqGet)
	if err != nil {
		return nil, fmt.Errorf("Email/get (search) failed: %w", err)
	}

	for _, inv := range resp2.Responses {
		if res, ok := inv.Args.(*email.GetResponse); ok {
			for _, e := range res.List {
				sender := formatAddresses(e.From)
				to := formatAddresses(e.To)
				cc := formatAddresses(e.CC)
				bcc := formatAddresses(e.BCC)
				replyTo := formatAddresses(e.ReplyTo)

				isUnread := true
				if _, ok := e.Keywords["$seen"]; ok {
					isUnread = false
				}

				isFlagged := false
				if _, ok := e.Keywords["$flagged"]; ok {
					isFlagged = true
				}

				isDraft := false
				if _, ok := e.Keywords["$draft"]; ok {
					isDraft = true
				}

				var boxIDs []string
				for k := range e.MailboxIDs {
					boxIDs = append(boxIDs, string(k))
				}

				dateStr := ""
				if e.ReceivedAt != nil {
					dateStr = e.ReceivedAt.Format("2006-01-02 15:04")
				}

				emails = append(emails, model.Email{
					ID:         string(e.ID),
					Subject:    e.Subject,
					From:       sender,
					To:         to,
					Cc:         cc,
					Bcc:        bcc,
					ReplyTo:    replyTo,
					Preview:    e.Preview,
					Date:       dateStr,
					IsUnread:   isUnread,
					IsFlagged:  isFlagged,
					IsDraft:    isDraft,
					ThreadID:   string(e.ThreadID),
					MailboxIDs: boxIDs,
				})
			}
		}
	}
	return emails, nil
}

// FetchEmailBody fetches the full text body for a specific email ID.
func (c *Client) FetchEmailBody(emailID string) (string, error) {
	req := &jmap.Request{}
	g := &email.Get{
		Account:             c.getMailAccountID(),
		IDs:                 []jmap.ID{jmap.ID(emailID)},
		Properties:          []string{"bodyValues", "textBody", "htmlBody"},
		FetchTextBodyValues: true,
		FetchHTMLBodyValues: true,
	}
	req.Invoke(g)

	resp, err := c.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Email/get failed: %w", err)
	}

	for _, inv := range resp.Responses {
		if res, ok := inv.Args.(*email.GetResponse); ok {
			if len(res.List) > 0 {
				e := res.List[0]

				// 1. Try Plain Text
				for _, part := range e.TextBody {
					if val, ok := e.BodyValues[part.PartID]; ok {
						return val.Value, nil
					}
				}

				// 2. Try HTML
				for _, part := range e.HTMLBody {
					if val, ok := e.BodyValues[part.PartID]; ok {
						converter := md.NewConverter("", true, nil)
						text, err := converter.ConvertString(val.Value)
						if err != nil {
							// Fallback to raw HTML (well, partial)
							return "[HTML Convert Error] " + val.Value, nil
						}
						// Add a header to indicate converted content
						return "[Converted HTML]\n" + text, nil
					}
				}

				return "(No text or html body found)", nil
			}
		}
	}
	return "", fmt.Errorf("email not found")
}

// FetchEmailHTMLBody returns the raw HTML body of an email for image rendering
func (c *Client) FetchEmailHTMLBody(emailID string) (string, error) {
	req := &jmap.Request{}
	g := &email.Get{
		Account:             c.getMailAccountID(),
		IDs:                 []jmap.ID{jmap.ID(emailID)},
		Properties:          []string{"bodyValues", "htmlBody"},
		FetchHTMLBodyValues: true,
	}
	req.Invoke(g)

	resp, err := c.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Email/get failed: %w", err)
	}

	for _, inv := range resp.Responses {
		if res, ok := inv.Args.(*email.GetResponse); ok {
			if len(res.List) > 0 {
				e := res.List[0]
				for _, part := range e.HTMLBody {
					if val, ok := e.BodyValues[part.PartID]; ok {
						return val.Value, nil
					}
				}
			}
		}
	}
	return "", fmt.Errorf("no HTML body found")
}

// GetMailboxIDByRole finds a mailbox ID by its role (e.g., "drafts", "sent").
func (c *Client) GetMailboxIDByRole(role string) (string, error) {
mbs, err := c.FetchMailboxes()
if err != nil {
return "", err
}
for _, mb := range mbs {
if mb.Role == role {
return mb.ID, nil
}
}
return "", fmt.Errorf("mailbox with role %s not found", role)
}

// DeleteEmail moves an email to Trash (or deletes it).
func (c *Client) DeleteEmail(emailID string) error {
	req := &jmap.Request{}
	req.Invoke(&email.Set{
		Account: c.getMailAccountID(),
		Destroy: []jmap.ID{jmap.ID(emailID)},
	})
	_, err := c.Client.Do(req)
	return err
}

// MoveEmail moves an email from one mailbox to another.
func (c *Client) MoveEmail(emailID, fromMailboxID, toMailboxID string) error {
	req := &jmap.Request{}
	
	patch := map[string]interface{}{
		"mailboxIds/" + toMailboxID: true,
	}
	if fromMailboxID != "" && fromMailboxID != toMailboxID {
		patch["mailboxIds/" + fromMailboxID] = nil
	}

	req.Invoke(&email.Set{
		Account: c.getMailAccountID(),
		Update: map[jmap.ID]jmap.Patch{
			jmap.ID(emailID): patch,
		},
	})
	_, err := c.Client.Do(req)
	return err
}

// SetUnread toggles the $seen keyword.
func (c *Client) SetUnread(emailID string, isUnread bool) error {
	req := &jmap.Request{}
	
	patch := map[string]interface{}{}
	if isUnread {
		patch["keywords/$seen"] = nil // Remove $seen to mark unread
	} else {
		patch["keywords/$seen"] = true // Add $seen to mark read
	}

	req.Invoke(&email.Set{
		Account: c.getMailAccountID(),
		Update: map[jmap.ID]jmap.Patch{
			jmap.ID(emailID): patch,
		},
	})
	_, err := c.Client.Do(req)
	return err
}

// SetFlagged toggles the $flagged keyword.
func (c *Client) SetFlagged(emailID string, isFlagged bool) error {
	req := &jmap.Request{}
	
	patch := map[string]interface{}{}
	if isFlagged {
		patch["keywords/$flagged"] = true
	} else {
		patch["keywords/$flagged"] = nil
	}

	req.Invoke(&email.Set{
		Account: c.getMailAccountID(),
		Update: map[jmap.ID]jmap.Patch{
			jmap.ID(emailID): patch,
		},
	})
	_, err := c.Client.Do(req)
	return err
}

// GetDefaultIdentity retrieves the first available identity.
func (c *Client) GetDefaultIdentity() (*identity.Identity, error) {
	identities, err := c.GetIdentities()
	if err != nil {
		return nil, err
	}
	if len(identities) == 0 {
		return nil, fmt.Errorf("no identities found")
	}
	return identities[0], nil
}

// GetIdentities retrieves all available sending identities.
func (c *Client) GetIdentities() ([]*identity.Identity, error) {
	req := &jmap.Request{}
	req.Invoke(&identity.Get{
		Account: c.getMailAccountID(),
	})

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Identity/get failed: %w", err)
	}

	var identities []*identity.Identity
	for _, inv := range resp.Responses {
		if res, ok := inv.Args.(*identity.GetResponse); ok {
			for _, id := range res.List {
				if id.Email != "" {
					identities = append(identities, id)
				}
			}
		}
	}
	return identities, nil
}

// SaveDraft creates or updates a draft without submitting it.
func (c *Client) SaveDraft(existingDraftID, from, to, subject, body string) error {
	// identityID unused for pure draft save unless we want to attach it to the Email object?
	// The Email object structure doesn't seem to hold identityID directly, mostly used for Submission.
	// So we can ignore it here.

	if from == "" {
		ident, err := c.GetDefaultIdentity()
		if err == nil {
			from = ident.Email
		} else if c.Session != nil && c.Session.Username != "" {
			from = c.Session.Username
		}
	}

	draftsID, err := c.GetMailboxIDByRole("drafts")
	if err != nil {
		return fmt.Errorf("could not find Drafts folder: %w", err)
	}

	// 1. Prepare Email Object
	// Parse the "to" address - might be in "Name <email>" format
	to = strings.TrimSpace(to)
	toEmail := to
	toName := ""
	if parsedTo, err := netmail.ParseAddress(to); err == nil {
		toEmail = parsedTo.Address
		toName = parsedTo.Name
	}
	
	// Always use a new creation ID
	creationID := jmap.ID("draft-0")
	
	emailObj := &email.Email{
		From:    []*mail.Address{{Email: from}},
		To:      []*mail.Address{{Name: toName, Email: toEmail}},
		Subject: subject,
		TextBody: []*email.BodyPart{
			{
				PartID: "text",
				Type:   "text/plain",
			},
		},
		BodyValues: map[string]*email.BodyValue{
			"text": {Value: body},
		},
		MailboxIDs: map[jmap.ID]bool{jmap.ID(draftsID): true},
		Keywords:   map[string]bool{"$draft": true},
	}

	req := &jmap.Request{}

	emailSet := &email.Set{
		Account: c.getMailAccountID(),
		Create: map[jmap.ID]*email.Email{
			creationID: emailObj,
		},
	}

	if existingDraftID != "" {
		// Instead of updating, we destroy the old draft and create a new one.
		emailSet.Destroy = []jmap.ID{jmap.ID(existingDraftID)}
	}

	req.Invoke(emailSet)

	resp, err := c.Client.Do(req)
	if err != nil {
		return fmt.Errorf("JMAP request failed: %w", err)
	}

	// Check for errors
	for _, inv := range resp.Responses {
		if methodErr, ok := inv.Args.(*jmap.MethodError); ok {
			// Try to provide more context if properties are available
			// methodErr might have Properties field? go-jmap definitions not fully visible but we can format the struct
			return fmt.Errorf("method error in %s: %s (%+v)", inv.Name, methodErr.Type, methodErr)
		}
		if setResp, ok := inv.Args.(*email.SetResponse); ok {
			if len(setResp.NotCreated) > 0 {
				var errs []string
				for id, errObj := range setResp.NotCreated {
					desc := ""
					if errObj.Description != nil {
						desc = *errObj.Description
					}
					errs = append(errs, fmt.Sprintf("ID %s: %s (%s)", id, errObj.Type, desc))
				}
				return fmt.Errorf("failed to save draft: %s", strings.Join(errs, "; "))
			}
			// Update failure?
			if len(setResp.NotUpdated) > 0 {
				return fmt.Errorf("failed to update draft %s", existingDraftID)
			}
		}
	}
	
	return nil
}

// SendEmail creates or updates a draft and submits it.
func (c *Client) SendEmail(existingDraftID, from, to, subject, body string) error {
	var identityID jmap.ID

	// Parse the "to" address(es) - might be in "Name <email>" format or comma-separated
	to = strings.TrimSpace(to)
	var toAddresses []*netmail.Address
	var rcptTo []*emailsubmission.Address
	
	// Try parsing as address list first
	if parsed, err := netmail.ParseAddressList(to); err == nil {
		toAddresses = parsed
	} else if parsed, err := netmail.ParseAddress(to); err == nil {
		// Single address
		toAddresses = []*netmail.Address{parsed}
	} else {
		// Fallback: treat as plain email
		toAddresses = []*netmail.Address{{Address: to}}
	}
	
	// Build recipient list for submission envelope
	for _, addr := range toAddresses {
		rcptTo = append(rcptTo, &emailsubmission.Address{Email: addr.Address})
	}
	
	// Convert to mail.Address for Email object
	var mailToAddrs []*mail.Address
	for _, addr := range toAddresses {
		mailToAddrs = append(mailToAddrs, &mail.Address{Name: addr.Name, Email: addr.Address})
	}

	// Always fetch identities to get the correct identityID
	identities, err := c.GetIdentities()
	if err != nil {
		return fmt.Errorf("failed to fetch identities: %w", err)
	}
	if len(identities) == 0 {
		return fmt.Errorf("no sending identities configured")
	}

	// Find matching identity for the from address, or use first one
	if from == "" {
		from = identities[0].Email
		identityID = identities[0].ID
	} else {
		// Find identity matching the from address
		for _, ident := range identities {
			if ident.Email == from {
				identityID = ident.ID
				break
			}
		}
		if identityID == "" {
			// No matching identity found, use first one but keep the from address
			identityID = identities[0].ID
		}
	}
	
	draftsID, err := c.GetMailboxIDByRole("drafts")
	if err != nil {
		return fmt.Errorf("could not find Drafts folder: %w", err)
	}
	
	sentID, err := c.GetMailboxIDByRole("sent")
	if err != nil {
		return fmt.Errorf("could not find Sent folder: %w", err)
	}

	// 1. Prepare Email Object
	// Create in Drafts first - only move to Sent on successful submission
	creationID := jmap.ID("draft-0")
	
	// Create in Drafts first - only move to Sent on successful submission
	emailObj := &email.Email{
		From:    []*mail.Address{{Email: from}},
		To:      mailToAddrs,
		Subject: subject,
		TextBody: []*email.BodyPart{
			{
				PartID: "text",
				Type:   "text/plain",
			},
		},
		BodyValues: map[string]*email.BodyValue{
			"text": {Value: body},
		},
		MailboxIDs: map[jmap.ID]bool{jmap.ID(draftsID): true},
		Keywords:   map[string]bool{"$draft": true},
	}

	// 2. Prepare Submission Object
	submitID := jmap.ID("submit-0")
	
	submissionObj := &emailsubmission.EmailSubmission{
		EmailID:    jmap.ID("#" + string(creationID)),
		IdentityID: identityID,
		Envelope: &emailsubmission.Envelope{
			MailFrom: &emailsubmission.Address{Email: from},
			RcptTo:   rcptTo,
		},
	}

	// 3. Chain Requests
	req := &jmap.Request{}

	emailSet := &email.Set{
		Account: c.getMailAccountID(),
		Create: map[jmap.ID]*email.Email{
			creationID: emailObj, // Use the new object
		},
	}

	if existingDraftID != "" {
		// Instead of updating, we destroy the old draft and create a new one.
		// This avoids issues with patching complex properties like bodyStructure/bodyValues.
		emailSet.Destroy = []jmap.ID{jmap.ID(existingDraftID)}
	}

	req.Invoke(emailSet)

	// EmailSubmission/set - OnSuccessUpdateEmail moves to Sent only if submission succeeds
	// The key must use "#" prefix to reference the submission being created
	req.Invoke(&emailsubmission.Set{
		Account: c.getMailAccountID(),
		Create: map[jmap.ID]*emailsubmission.EmailSubmission{
			submitID: submissionObj,
		},
		OnSuccessUpdateEmail: map[jmap.ID]jmap.Patch{
			jmap.ID("#" + string(submitID)): {
				"mailboxIds/" + draftsID: nil,  // Remove from Drafts
				"mailboxIds/" + sentID:   true, // Add to Sent
				"keywords/$draft":        nil,  // Remove draft keyword
				"keywords/$seen":         true, // Mark as read
			},
		},
	})

resp, err := c.Client.Do(req)
if err != nil {
return fmt.Errorf("JMAP request failed: %w", err)
}

// Check response for errors
for _, inv := range resp.Responses {
if methodErr, ok := inv.Args.(*jmap.MethodError); ok {
// Log full error object for debugging
desc := ""
if methodErr.Description != nil {
desc = *methodErr.Description
}
return fmt.Errorf("method error in %s: %s (desc: %s)", inv.Name, methodErr.Type, desc)
}
// Also check SetResponse for NotCreated and NotDestroyed
if setResp, ok := inv.Args.(*email.SetResponse); ok {
if len(setResp.NotDestroyed) > 0 {
var errs []string
for id, errObj := range setResp.NotDestroyed {
desc := ""
if errObj.Description != nil {
desc = *errObj.Description
}
errs = append(errs, fmt.Sprintf("ID %s: %s (%s)", id, errObj.Type, desc))
}
return fmt.Errorf("failed to destroy email: %s", strings.Join(errs, "; "))
}
if len(setResp.NotCreated) > 0 {
var errs []string
for id, errObj := range setResp.NotCreated {
desc := ""
if errObj.Description != nil {
desc = *errObj.Description
}
props := ""
if errObj.Properties != nil {
props = fmt.Sprintf(" [props: %v]", *errObj.Properties)
}
errs = append(errs, fmt.Sprintf("ID %s: %s (%s)%s", id, errObj.Type, desc, props))
}
return fmt.Errorf("failed to create email (from: %s): %s", from, strings.Join(errs, "; "))
}
}
if subResp, ok := inv.Args.(*emailsubmission.SetResponse); ok {
if len(subResp.NotCreated) > 0 {
var errs []string
for id, errObj := range subResp.NotCreated {
desc := ""
if errObj.Description != nil {
desc = *errObj.Description
}
errs = append(errs, fmt.Sprintf("ID %s: %s (%s)", id, errObj.Type, desc))
}
// Build recipient list for error message
var toList []string
for _, addr := range toAddresses {
toList = append(toList, addr.Address)
}
return fmt.Errorf("failed to submit email (from: %s, to: %v): %s", from, toList, strings.Join(errs, "; "))
}
}
}

return nil
}

func formatAddresses(addrs []*mail.Address) string {
var parts []string
for _, a := range addrs {
if a.Name != "" {
parts = append(parts, fmt.Sprintf("%s <%s>", a.Name, a.Email))
} else {
parts = append(parts, a.Email)
}
}
return strings.Join(parts, ", ")
}

