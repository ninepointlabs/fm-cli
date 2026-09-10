package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~rockorager/go-jmap"
)

// PushHandlers receives EventSource lifecycle and state events.
type PushHandlers struct {
	// Open runs once the stream is connected (HTTP 200), before any event.
	Open func()
	// StateChange runs per StateChange event.
	StateChange func(*jmap.StateChange)
}

// ErrPushDisplaced reports that the server closed the stream on purpose,
// which Fastmail does when another connection for the same sign-in opens.
var ErrPushDisplaced = errors.New("the server closed the push connection; another client for this sign-in probably connected")

// Listen holds a JMAP EventSource connection open and dispatches events until
// the connection drops (returned as an error) or ctx ends (returns nil). The
// server pings every pingSeconds; a silence twice that long plus a margin
// counts as a dead connection.
func (c *Client) Listen(ctx context.Context, types []string, pingSeconds int, handlers PushHandlers) error {
	if c.Session == nil || c.Session.EventSourceURL == "" {
		return errors.New("the server offers no push connection")
	}
	if pingSeconds <= 0 {
		pingSeconds = 60
	}
	target, err := eventSourceURL(c.Session.EventSourceURL, types, pingSeconds)
	if err != nil {
		return err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.Client.HttpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("push connection failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("push connection refused (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if handlers.Open != nil {
		handlers.Open()
	}

	// The watchdog closes the stream when the server goes quiet.
	silence := time.Duration(pingSeconds)*2*time.Second + 30*time.Second
	watchdog := time.AfterFunc(silence, cancel)
	defer watchdog.Stop()

	const maxLine = 1 << 20
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), maxLine)
	var event string
	var data strings.Builder
	for {
		if !scanner.Scan() {
			err := scanner.Err()
			if ctx.Err() != nil {
				return nil
			}
			if streamCtx.Err() != nil {
				return errors.New("push connection went silent")
			}
			if err == nil {
				err = io.EOF
			}
			return fmt.Errorf("push connection closed: %w", err)
		}
		watchdog.Reset(silence)
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case line == "":
			switch event {
			case "state":
				if data.Len() > 0 && handlers.StateChange != nil {
					change := &jmap.StateChange{}
					if json.Unmarshal([]byte(data.String()), change) == nil {
						handlers.StateChange(change)
					}
				}
			case "close":
				return ErrPushDisplaced
			}
			event = ""
			data.Reset()
		case strings.HasPrefix(line, ":"):
			// comment / keep-alive
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				event = value
			case "data":
				if data.Len()+len(value) > maxLine {
					// An event this large is not a state change; drop it.
					event = ""
					data.Reset()
					continue
				}
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(value)
			}
		}
	}
}

// eventSourceURL fills the session's URL template (RFC 8620 §7.3) or, for a
// server that hands out a plain URL, sets the parameters as a query.
func eventSourceURL(template string, types []string, ping int) (string, error) {
	typeList := "*"
	if len(types) > 0 {
		typeList = strings.Join(types, ",")
	}
	if strings.Contains(template, "{types}") || strings.Contains(template, "{closeafter}") || strings.Contains(template, "{ping}") {
		out := strings.NewReplacer(
			"{types}", url.QueryEscape(typeList),
			"{closeafter}", "no",
			"{ping}", strconv.Itoa(ping),
		).Replace(template)
		return out, nil
	}
	u, err := url.Parse(template)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("types", typeList)
	q.Set("closeafter", "no")
	q.Set("ping", strconv.Itoa(ping))
	u.RawQuery = q.Encode()
	return u.String(), nil
}
