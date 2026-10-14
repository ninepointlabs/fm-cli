package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fm-cli/internal/api"
	"fm-cli/internal/images"
	"fm-cli/internal/model"
	"fm-cli/internal/storage"

	tea "github.com/charmbracelet/bubbletea"
)

func searchEmailsCmd(client *api.Client, query string) tea.Cmd {
	return func() tea.Msg {
		results, err := client.SearchEmails(query, 50)
		if err != nil {
			return errorMsg(err)
		}
		return searchResultsMsg(results)
	}
}

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
		msg := identitiesLoadedMsg{}
		pref := client.PreferredIdentity(identities)
		for i, id := range identities {
			if id == pref {
				msg.defaultIdx = i
			}
			msg.emails = append(msg.emails, id.Email)
		}
		return msg
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
			go prefetchBodies(client, db, emails)
		}
		return emailsLoadedMsg(emails)
	}
}

// prefetchBodies caches the bodies of emails that are not cached yet, so
// they can be read offline later.
func prefetchBodies(client *api.Client, db *storage.DB, emails []model.Email) {
	for _, email := range emails {
		existing, _ := db.GetEmailBody(email.ID)
		if existing == "" || strings.HasPrefix(existing, "[Full email body not cached") || strings.HasPrefix(existing, "[Email body not available") {
			body, err := client.FetchEmailBody(email.ID)
			if err == nil && body != "" {
				db.SaveEmailBody(email.ID, body)
			}
		}
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
			go prefetchBodies(client, db, emails)
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

// Calendar Commands (using CalDAV)
func fetchCalendarsCmd(davClient *api.DAVClient) tea.Cmd {
	return func() tea.Msg {
		if davClient == nil {
			return errorMsg(fmt.Errorf("calendar needs an app password: run 'fm-cli auth dav'"))
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
			return errorMsg(fmt.Errorf("contacts need an app password: run 'fm-cli auth dav'"))
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
