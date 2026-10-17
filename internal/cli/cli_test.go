package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseFlagsAnywhere(t *testing.T) {
	a, err := Parse([]string{"--account", "all", "watch", "--events", "added,new", "--box", "inbox", "--box=Archive", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Account != "all" || a.Events != "added,new" || !a.JSON {
		t.Fatalf("unexpected args: %+v", a)
	}
	if len(a.Positional) != 1 || a.Positional[0] != "watch" {
		t.Fatalf("positional: %v", a.Positional)
	}
	if len(a.Boxes) != 2 || a.Boxes[1] != "Archive" {
		t.Fatalf("boxes: %v", a.Boxes)
	}
}

func TestParseRejectsUnknownFlag(t *testing.T) {
	_, err := Parse([]string{"box", "view", "--wat"})
	if err == nil || !strings.Contains(err.Error(), `unknown flag "--wat"`) {
		t.Fatalf("got %v", err)
	}
	_, err = Parse([]string{"box", "--limit", "x"})
	if err == nil {
		t.Fatal("expected a limit error")
	}
}

func TestParseEvents(t *testing.T) {
	events, err := parseEvents("")
	if err != nil || !events[eventAdded] || !events[eventResync] || events[eventNew] {
		t.Fatalf("defaults: %v %v", events, err)
	}
	events, err = parseEvents("new")
	if err != nil || !events[eventNew] || events[eventAdded] {
		t.Fatalf("new only: %v %v", events, err)
	}
	if _, err := parseEvents("added,bogus"); err == nil || !strings.Contains(err.Error(), `unknown event "bogus"`) {
		t.Fatalf("got %v", err)
	}
}

func TestHandled(t *testing.T) {
	for _, argv := range [][]string{{"version"}, {"about"}, {"auth", "status", "--json"}, {"bogus"}, {"--version"}, {"--wat"}} {
		if !Handled(argv) {
			t.Errorf("%v should be handled", argv)
		}
	}
	for _, argv := range [][]string{{}, {"tui"}, {"tui", "--thread", "abc", "--remote"}, {"settings", "offline", "on"}, {"sync"}, {"debug"}} {
		if Handled(argv) {
			t.Errorf("%v should reach the TUI or legacy path", argv)
		}
	}
}

func TestUsageEnvelope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &stderr}
	code := app.Run(context.Background(), []string{"bogus", "--json"})
	if code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout should be empty, got %q", stdout.String())
	}
	var env map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr is not JSON: %q", stderr.String())
	}
	if env["ok"] != false || env["code"] != "usage" || !strings.Contains(env["error"].(string), `unknown command "bogus"`) {
		t.Fatalf("envelope: %v", env)
	}
}

func TestVersionLine(t *testing.T) {
	var stdout bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &bytes.Buffer{}}
	Version = "9.9.9"
	if code := app.Run(context.Background(), []string{"--version"}); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if stdout.String() != "fm-cli version 9.9.9\n" {
		t.Fatalf("got %q", stdout.String())
	}
}

func TestParseBoxFlags(t *testing.T) {
	for _, tc := range []struct {
		argv       []string
		move       bool
		moveTo     string
		positional int
	}{
		{[]string{"box", "delete", "Old", "--move-to", "Archive"}, true, "Archive", 3},
		{[]string{"box", "delete", "Old", "--move-to=Other Services/Gmail", "--json"}, true, "Other Services/Gmail", 3},
		{[]string{"box", "delete", "Old", "--move-to", "--json"}, true, "", 3},
		{[]string{"box", "delete", "Old", "--move-to"}, true, "", 3},
		{[]string{"box", "delete", "Old"}, false, "", 3},
	} {
		a, err := Parse(tc.argv)
		if err != nil {
			t.Fatal(err)
		}
		if a.Move != tc.move || a.MoveTo != tc.moveTo || len(a.Positional) != tc.positional {
			t.Errorf("%v: %+v", tc.argv, a)
		}
	}
	a, err := Parse([]string{"box", "create", "New", "--parent", "Other Services"})
	if err != nil || a.Parent != "Other Services" {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := Parse([]string{"box", "create", "New", "--parent"}); err == nil {
		t.Fatal("--parent needs a value")
	}
}

func TestBoxUsageErrors(t *testing.T) {
	for _, argv := range [][]string{{"box", "create", "--json"}, {"box", "rename", "Old", "--json"}, {"box", "delete", "--json"}} {
		var stdout, stderr bytes.Buffer
		app := &App{Stdout: &stdout, Stderr: &stderr}
		if code := app.Run(context.Background(), argv); code != ExitUsage {
			t.Errorf("%v: exit %d, stderr %s", argv, code, stderr.String())
		}
	}
}

func TestAbout(t *testing.T) {
	var stdout bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &bytes.Buffer{}}
	if code := app.Run(context.Background(), []string{"about", "--json"}); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	var env struct {
		OK   bool              `json:"ok"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil || !env.OK {
		t.Fatalf("not an ok envelope: %q", stdout.String())
	}
	if env.Data["organization"] != "Nine Point Labs" || env.Data["support"] != "help@ninepointlabs.com" || env.Data["privacy"] == "" {
		t.Fatalf("data: %v", env.Data)
	}
}
