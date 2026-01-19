package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"fm-cli/internal/model"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

// DAVClient holds CalDAV and CardDAV clients
type DAVClient struct {
	CalDAV       *caldav.Client
	CardDAV      *carddav.Client
	httpClient   webdav.HTTPClient
	email        string
}

// NewDAVClient creates CalDAV/CardDAV clients with app password auth
func NewDAVClient(email, appPassword string) (*DAVClient, error) {
	httpClient := webdav.HTTPClientWithBasicAuth(nil, email, appPassword)

	// Fastmail CalDAV/CardDAV endpoints with principal path
	calURL := "https://caldav.fastmail.com/dav/principals/user/" + email + "/"
	cardURL := "https://carddav.fastmail.com/dav/principals/user/" + email + "/"

	calClient, err := caldav.NewClient(httpClient, calURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create CalDAV client: %w", err)
	}

	cardClient, err := carddav.NewClient(httpClient, cardURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create CardDAV client: %w", err)
	}

	return &DAVClient{
		CalDAV:     calClient,
		CardDAV:    cardClient,
		httpClient: httpClient,
		email:      email,
	}, nil
}

type basicAuthTransport struct {
	username string
	password string
	base     http.RoundTripper
}

func (t *basicAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(t.username, t.password)
	return t.base.RoundTrip(req)
}

// FetchCalendars retrieves all calendars via CalDAV
func (d *DAVClient) FetchCalendars(ctx context.Context) ([]model.Calendar, error) {
	// Use principal discovery
	principal, err := d.CalDAV.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find principal: %w", err)
	}

	homeSet, err := d.CalDAV.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("failed to find calendar home set: %w", err)
	}

	cals, err := d.CalDAV.FindCalendars(ctx, homeSet)
	if err != nil {
		return nil, fmt.Errorf("failed to find calendars: %w", err)
	}

	var calendars []model.Calendar
	for i, cal := range cals {
		calendars = append(calendars, model.Calendar{
			ID:             cal.Path,
			Name:           cal.Name,
			Color:          "",
			IsVisible:      true,
			IsDefault:      i == 0,
			MayReadItems:   true,
			MayAddItems:    true,
			MayModifyItems: true,
			MayRemoveItems: true,
		})
	}

	return calendars, nil
}

// FetchEvents retrieves calendar events within a date range via CalDAV
func (d *DAVClient) FetchEvents(ctx context.Context, calendarPaths []string, start, end time.Time) ([]model.CalendarEvent, error) {
	var allEvents []model.CalendarEvent

	for _, calPath := range calendarPaths {
		query := &caldav.CalendarQuery{
			CompRequest: caldav.CalendarCompRequest{
				Name:  "VCALENDAR",
				Props: []string{"VERSION"},
				Comps: []caldav.CalendarCompRequest{{
					Name: "VEVENT",
					Props: []string{
						"SUMMARY", "DTSTART", "DTEND", "DURATION",
						"LOCATION", "DESCRIPTION", "UID", "STATUS",
						"ORGANIZER", "ATTENDEE",
					},
				}},
			},
			CompFilter: caldav.CompFilter{
				Name: "VCALENDAR",
				Comps: []caldav.CompFilter{{
					Name:  "VEVENT",
					Start: start,
					End:   end,
				}},
			},
		}

		objects, err := d.CalDAV.QueryCalendar(ctx, calPath, query)
		if err != nil {
			continue // Skip calendars we can't read
		}

		for _, obj := range objects {
			event := parseCalendarObject(obj, calPath)
			if event != nil {
				allEvents = append(allEvents, *event)
			}
		}
	}

	// Sort by start time
	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].Start.Before(allEvents[j].Start)
	})

	return allEvents, nil
}

func parseCalendarObject(obj caldav.CalendarObject, calPath string) *model.CalendarEvent {
	if obj.Data == nil {
		return nil
	}

	for _, comp := range obj.Data.Children {
		if comp.Name != ical.CompEvent {
			continue
		}

		event := &model.CalendarEvent{
			ID:         obj.Path,
			CalendarID: calPath,
		}

		// Parse properties
		if prop := comp.Props.Get(ical.PropSummary); prop != nil {
			event.Title = prop.Value
		}
		if prop := comp.Props.Get(ical.PropDescription); prop != nil {
			event.Description = prop.Value
		}
		if prop := comp.Props.Get(ical.PropLocation); prop != nil {
			event.Location = prop.Value
		}
		if prop := comp.Props.Get(ical.PropStatus); prop != nil {
			event.Status = strings.ToLower(prop.Value)
		}

		// Parse start time
		if prop := comp.Props.Get(ical.PropDateTimeStart); prop != nil {
			if t, err := prop.DateTime(time.Local); err == nil {
				event.Start = t
			}
			// Check if all-day event
			if val := prop.Params.Get(ical.ParamValue); val == "DATE" {
				event.IsAllDay = true
			}
		}

		// Parse end time or duration
		if prop := comp.Props.Get(ical.PropDateTimeEnd); prop != nil {
			if t, err := prop.DateTime(time.Local); err == nil {
				event.End = t
			}
		} else if prop := comp.Props.Get(ical.PropDuration); prop != nil {
			event.Duration = prop.Value
			if dur, err := prop.Duration(); err == nil {
				event.End = event.Start.Add(dur)
			}
		}

		// Parse participants
		for _, prop := range comp.Props.Values(ical.PropAttendee) {
			participant := model.EventParticipant{
				Email: strings.TrimPrefix(prop.Value, "mailto:"),
			}
			if name := prop.Params.Get(ical.ParamCommonName); name != "" {
				participant.Name = name
			}
			if status := prop.Params.Get(ical.ParamParticipationStatus); status != "" {
				participant.Status = strings.ToLower(status)
			}
			if role := prop.Params.Get(ical.ParamRole); role != "" {
				participant.Role = strings.ToLower(role)
			}
			event.Participants = append(event.Participants, participant)
		}

		return event
	}

	return nil
}

// CreateEvent creates a new calendar event via CalDAV
func (d *DAVClient) CreateEvent(ctx context.Context, event model.CalendarEvent) (string, error) {
	// Create iCal event
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//FM-CLI//EN")

	vevent := ical.NewComponent(ical.CompEvent)
	uid := fmt.Sprintf("%d@fm-cli", time.Now().UnixNano())
	vevent.Props.SetText(ical.PropUID, uid)
	vevent.Props.SetText(ical.PropSummary, event.Title)

	if event.Description != "" {
		vevent.Props.SetText(ical.PropDescription, event.Description)
	}
	if event.Location != "" {
		vevent.Props.SetText(ical.PropLocation, event.Location)
	}

	// Set start time
	dtstart := ical.NewProp(ical.PropDateTimeStart)
	if event.IsAllDay {
		dtstart.SetDate(event.Start)
	} else {
		dtstart.SetDateTime(event.Start)
	}
	vevent.Props.Set(dtstart)

	// Set end time or duration
	if !event.End.IsZero() {
		dtend := ical.NewProp(ical.PropDateTimeEnd)
		if event.IsAllDay {
			dtend.SetDate(event.End)
		} else {
			dtend.SetDateTime(event.End)
		}
		vevent.Props.Set(dtend)
	} else if event.Duration != "" {
		vevent.Props.SetText(ical.PropDuration, event.Duration)
	} else {
		// Default 1 hour
		dtend := ical.NewProp(ical.PropDateTimeEnd)
		dtend.SetDateTime(event.Start.Add(time.Hour))
		vevent.Props.Set(dtend)
	}

	vevent.Props.SetDateTime(ical.PropDateTimeStamp, time.Now())
	cal.Children = append(cal.Children, vevent)

	// Put to server
	path := event.CalendarID + uid + ".ics"
	_, err := d.CalDAV.PutCalendarObject(ctx, path, cal)
	if err != nil {
		return "", fmt.Errorf("failed to create event: %w", err)
	}

	return path, nil
}

// UpdateEvent updates an existing calendar event via CalDAV
func (d *DAVClient) UpdateEvent(ctx context.Context, event model.CalendarEvent) error {
	// First get the existing event to preserve UID
	objects, err := d.CalDAV.MultiGetCalendar(ctx, event.CalendarID, &caldav.CalendarMultiGet{
		Paths: []string{event.ID},
		CompRequest: caldav.CalendarCompRequest{
			Name: "VCALENDAR",
			Comps: []caldav.CalendarCompRequest{{
				Name: "VEVENT",
			}},
		},
	})
	if err != nil || len(objects) == 0 {
		return fmt.Errorf("failed to get existing event: %w", err)
	}

	// Get existing UID
	existingCal := objects[0].Data
	var uid string
	for _, comp := range existingCal.Children {
		if comp.Name == ical.CompEvent {
			if prop := comp.Props.Get(ical.PropUID); prop != nil {
				uid = prop.Value
			}
		}
	}

	// Create updated iCal event
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//FM-CLI//EN")

	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, uid)
	vevent.Props.SetText(ical.PropSummary, event.Title)

	if event.Description != "" {
		vevent.Props.SetText(ical.PropDescription, event.Description)
	}
	if event.Location != "" {
		vevent.Props.SetText(ical.PropLocation, event.Location)
	}

	dtstart := ical.NewProp(ical.PropDateTimeStart)
	if event.IsAllDay {
		dtstart.SetDate(event.Start)
	} else {
		dtstart.SetDateTime(event.Start)
	}
	vevent.Props.Set(dtstart)

	if !event.End.IsZero() {
		dtend := ical.NewProp(ical.PropDateTimeEnd)
		if event.IsAllDay {
			dtend.SetDate(event.End)
		} else {
			dtend.SetDateTime(event.End)
		}
		vevent.Props.Set(dtend)
	}

	vevent.Props.SetDateTime(ical.PropDateTimeStamp, time.Now())
	cal.Children = append(cal.Children, vevent)

	_, err = d.CalDAV.PutCalendarObject(ctx, event.ID, cal)
	if err != nil {
		return fmt.Errorf("failed to update event: %w", err)
	}

	return nil
}

// DeleteEvent deletes a calendar event via CalDAV
func (d *DAVClient) DeleteEvent(ctx context.Context, eventPath string) error {
	err := d.CalDAV.RemoveAll(ctx, eventPath)
	if err != nil {
		return fmt.Errorf("failed to delete event: %w", err)
	}
	return nil
}

// FetchAddressBooks retrieves all address books via CardDAV
func (d *DAVClient) FetchAddressBooks(ctx context.Context) ([]model.AddressBook, error) {
	// Use principal discovery
	principal, err := d.CardDAV.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find principal: %w", err)
	}

	homeSet, err := d.CardDAV.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("failed to find address book home set: %w", err)
	}

	abs, err := d.CardDAV.FindAddressBooks(ctx, homeSet)
	if err != nil {
		return nil, fmt.Errorf("failed to find address books: %w", err)
	}

	var addressBooks []model.AddressBook
	for i, ab := range abs {
		addressBooks = append(addressBooks, model.AddressBook{
			ID:             ab.Path,
			Name:           ab.Name,
			IsDefault:      i == 0,
			MayReadItems:   true,
			MayAddItems:    true,
			MayModifyItems: true,
			MayRemoveItems: true,
		})
	}

	return addressBooks, nil
}

// FetchContacts retrieves contacts from an address book via CardDAV
func (d *DAVClient) FetchContacts(ctx context.Context, addressBookPath string, limit int) ([]model.Contact, error) {
	query := &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{
			Props: []string{
				vcard.FieldFormattedName,
				vcard.FieldName,
				vcard.FieldNickname,
				vcard.FieldOrganization,
				vcard.FieldTitle,
				vcard.FieldEmail,
				vcard.FieldTelephone,
				vcard.FieldAddress,
				vcard.FieldNote,
				vcard.FieldBirthday,
				vcard.FieldAnniversary,
				vcard.FieldUID,
			},
		},
	}

	objects, err := d.CardDAV.QueryAddressBook(ctx, addressBookPath, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query contacts: %w", err)
	}

	var contacts []model.Contact
	for _, obj := range objects {
		contact := parseAddressObject(obj, addressBookPath)
		if contact != nil {
			contacts = append(contacts, *contact)
		}
		if limit > 0 && len(contacts) >= limit {
			break
		}
	}

	// Sort by name
	sort.Slice(contacts, func(i, j int) bool {
		return strings.ToLower(contacts[i].FullName) < strings.ToLower(contacts[j].FullName)
	})

	return contacts, nil
}

func parseAddressObject(obj carddav.AddressObject, abPath string) *model.Contact {
	if obj.Card == nil {
		return nil
	}

	contact := &model.Contact{
		ID:            obj.Path,
		AddressBookID: abPath,
	}

	// Full name
	if fn := obj.Card.Get(vcard.FieldFormattedName); fn != nil {
		contact.FullName = fn.Value
	}

	// Name components
	if name := obj.Card.Name(); name != nil {
		contact.LastName = name.FamilyName
		contact.FirstName = name.GivenName
		contact.Prefix = name.HonorificPrefix
		contact.Suffix = name.HonorificSuffix
	}

	// Nickname
	if nick := obj.Card.Get(vcard.FieldNickname); nick != nil {
		contact.Nickname = nick.Value
	}

	// Organization
	if org := obj.Card.Get(vcard.FieldOrganization); org != nil {
		contact.Company = org.Value
	}

	// Title
	if title := obj.Card.Get(vcard.FieldTitle); title != nil {
		contact.JobTitle = title.Value
	}

	// Emails
	for _, email := range obj.Card[vcard.FieldEmail] {
		emailType := "other"
		if types := email.Params.Types(); len(types) > 0 {
			switch strings.ToLower(types[0]) {
			case "work":
				emailType = "work"
			case "home":
				emailType = "home"
			}
		}
		contact.Emails = append(contact.Emails, model.ContactEmail{
			Type:      emailType,
			Email:     email.Value,
			IsDefault: email.Params.Get("PREF") != "",
		})
	}

	// Phones
	for _, phone := range obj.Card[vcard.FieldTelephone] {
		phoneType := "other"
		if types := phone.Params.Types(); len(types) > 0 {
			t := strings.ToLower(types[0])
			switch {
			case strings.Contains(t, "cell") || strings.Contains(t, "mobile"):
				phoneType = "mobile"
			case strings.Contains(t, "work"):
				phoneType = "work"
			case strings.Contains(t, "home"):
				phoneType = "home"
			case strings.Contains(t, "fax"):
				phoneType = "fax"
			}
		}
		contact.Phones = append(contact.Phones, model.ContactPhone{
			Type:      phoneType,
			Number:    phone.Value,
			IsDefault: phone.Params.Get("PREF") != "",
		})
	}

	// Addresses
	for _, addr := range obj.Card[vcard.FieldAddress] {
		addrType := "other"
		if types := addr.Params.Types(); len(types) > 0 {
			switch strings.ToLower(types[0]) {
			case "work":
				addrType = "work"
			case "home":
				addrType = "home"
			}
		}
		// Parse ADR field - semicolon-separated components
		// Format: PO Box;Extended Address;Street;City;Region;Postal Code;Country
		parts := strings.Split(addr.Value, ";")
		var street, city, state, postalCode, country string
		if len(parts) > 2 {
			street = parts[2]
		}
		if len(parts) > 3 {
			city = parts[3]
		}
		if len(parts) > 4 {
			state = parts[4]
		}
		if len(parts) > 5 {
			postalCode = parts[5]
		}
		if len(parts) > 6 {
			country = parts[6]
		}
		contact.Addresses = append(contact.Addresses, model.ContactAddress{
			Type:       addrType,
			Street:     street,
			City:       city,
			State:      state,
			PostalCode: postalCode,
			Country:    country,
		})
	}

	// Notes
	if note := obj.Card.Get(vcard.FieldNote); note != nil {
		contact.Notes = note.Value
	}

	// Birthday
	if bday := obj.Card.Get(vcard.FieldBirthday); bday != nil {
		contact.Birthday = bday.Value
	}

	// Anniversary
	if anniv := obj.Card.Get(vcard.FieldAnniversary); anniv != nil {
		contact.Anniversary = anniv.Value
	}

	return contact
}

// CreateContact creates a new contact via CardDAV
func (d *DAVClient) CreateContact(ctx context.Context, contact model.Contact) (string, error) {
	card := make(vcard.Card)

	// VERSION must be set first for vCard 3.0
	card.SetValue(vcard.FieldVersion, "3.0")

	uid := fmt.Sprintf("%d@fm-cli", time.Now().UnixNano())
	card.SetValue(vcard.FieldUID, uid)
	
	// FN (Formatted Name) is required
	fn := contact.FullName
	if fn == "" {
		fn = strings.TrimSpace(contact.FirstName + " " + contact.LastName)
	}
	if fn == "" {
		fn = "New Contact"
	}
	card.SetValue(vcard.FieldFormattedName, fn)

	// N (Name) is required in vCard 3.0 - must have all components even if empty
	name := &vcard.Name{
		FamilyName:      contact.LastName,
		GivenName:       contact.FirstName,
		HonorificPrefix: contact.Prefix,
		HonorificSuffix: contact.Suffix,
	}
	card.AddName(name)

	if contact.Nickname != "" {
		card.SetValue(vcard.FieldNickname, contact.Nickname)
	}
	if contact.Company != "" {
		card.SetValue(vcard.FieldOrganization, contact.Company)
	}
	if contact.JobTitle != "" {
		card.SetValue(vcard.FieldTitle, contact.JobTitle)
	}

	// Emails
	for _, email := range contact.Emails {
		if email.Email == "" {
			continue
		}
		field := &vcard.Field{
			Value:  email.Email,
			Params: make(vcard.Params),
		}
		if email.Type != "" && email.Type != "other" {
			field.Params.Add(vcard.ParamType, strings.ToUpper(email.Type))
		}
		card.Add(vcard.FieldEmail, field)
	}

	// Phones
	for _, phone := range contact.Phones {
		if phone.Number == "" {
			continue
		}
		field := &vcard.Field{
			Value:  phone.Number,
			Params: make(vcard.Params),
		}
		switch phone.Type {
		case "mobile":
			field.Params.Add(vcard.ParamType, "CELL")
		case "work", "home", "fax":
			field.Params.Add(vcard.ParamType, strings.ToUpper(phone.Type))
		}
		card.Add(vcard.FieldTelephone, field)
	}

	// Notes
	if contact.Notes != "" {
		card.SetValue(vcard.FieldNote, contact.Notes)
	}

	// Birthday
	if contact.Birthday != "" {
		card.SetValue(vcard.FieldBirthday, contact.Birthday)
	}

	path := contact.AddressBookID + uid + ".vcf"
	_, err := d.CardDAV.PutAddressObject(ctx, path, card)
	if err != nil {
		return "", fmt.Errorf("failed to create contact: %w", err)
	}

	return path, nil
}

// UpdateContact updates an existing contact via CardDAV
func (d *DAVClient) UpdateContact(ctx context.Context, contact model.Contact) error {
	// Get existing card to preserve all fields
	objects, err := d.CardDAV.MultiGetAddressBook(ctx, contact.AddressBookID, &carddav.AddressBookMultiGet{
		Paths: []string{contact.ID},
		DataRequest: carddav.AddressDataRequest{
			AllProp: true, // Get all properties
		},
	})
	if err != nil {
		return fmt.Errorf("failed to get existing contact: %w", err)
	}
	if len(objects) == 0 {
		return fmt.Errorf("contact not found")
	}

	// Start with the existing card
	card := objects[0].Card
	
	// Update the fields that can be edited
	// FN (Formatted Name) is required
	fn := contact.FullName
	if fn == "" {
		fn = strings.TrimSpace(contact.FirstName + " " + contact.LastName)
	}
	if fn == "" {
		fn = "New Contact"
	}
	card.SetValue(vcard.FieldFormattedName, fn)

	// Update N (Name) field - remove old one first
	delete(card, vcard.FieldName)
	name := &vcard.Name{
		FamilyName:      contact.LastName,
		GivenName:       contact.FirstName,
		HonorificPrefix: contact.Prefix,
		HonorificSuffix: contact.Suffix,
	}
	card.AddName(name)

	// Update optional fields
	if contact.Nickname != "" {
		card.SetValue(vcard.FieldNickname, contact.Nickname)
	} else {
		delete(card, vcard.FieldNickname)
	}
	
	if contact.Company != "" {
		card.SetValue(vcard.FieldOrganization, contact.Company)
	} else {
		delete(card, vcard.FieldOrganization)
	}
	
	if contact.JobTitle != "" {
		card.SetValue(vcard.FieldTitle, contact.JobTitle)
	} else {
		delete(card, vcard.FieldTitle)
	}

	// Update emails - remove old ones first
	delete(card, vcard.FieldEmail)
	for _, email := range contact.Emails {
		if email.Email == "" {
			continue
		}
		field := &vcard.Field{
			Value:  email.Email,
			Params: make(vcard.Params),
		}
		if email.Type != "" && email.Type != "other" {
			field.Params.Add(vcard.ParamType, strings.ToUpper(email.Type))
		}
		card.Add(vcard.FieldEmail, field)
	}

	// Update phones - remove old ones first
	delete(card, vcard.FieldTelephone)
	for _, phone := range contact.Phones {
		if phone.Number == "" {
			continue
		}
		field := &vcard.Field{
			Value:  phone.Number,
			Params: make(vcard.Params),
		}
		switch phone.Type {
		case "mobile":
			field.Params.Add(vcard.ParamType, "CELL")
		case "work", "home", "fax":
			field.Params.Add(vcard.ParamType, strings.ToUpper(phone.Type))
		}
		card.Add(vcard.FieldTelephone, field)
	}

	// Update notes
	if contact.Notes != "" {
		card.SetValue(vcard.FieldNote, contact.Notes)
	} else {
		delete(card, vcard.FieldNote)
	}

	// Update birthday
	if contact.Birthday != "" {
		card.SetValue(vcard.FieldBirthday, contact.Birthday)
	} else {
		delete(card, vcard.FieldBirthday)
	}

	_, err = d.CardDAV.PutAddressObject(ctx, contact.ID, card)
	if err != nil {
		return fmt.Errorf("failed to update contact: %w", err)
	}

	return nil
}

// DeleteContact deletes a contact via CardDAV
func (d *DAVClient) DeleteContact(ctx context.Context, contactPath string) error {
	err := d.CardDAV.RemoveAll(ctx, contactPath)
	if err != nil {
		return fmt.Errorf("failed to delete contact: %w", err)
	}
	return nil
}
