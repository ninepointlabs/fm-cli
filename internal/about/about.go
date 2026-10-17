// Package about holds the facts the About screen and `fm-cli about` show.
package about

import (
	"fmt"
	"net/url"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	fmcli "fm-cli"
)

const (
	Name         = "fm-cli"
	Tagline      = "Fastmail from the terminal"
	Organization = "Nine Point Labs"
	SupportEmail = "help@ninepointlabs.com"
	Created      = "January 2026" // first commit: 2026-01-16
	LicenseName  = "MIT License"
	Copyright    = "© 2026 Nine Point Labs" // as written in LICENSE

	Privacy = "No telemetry, no analytics and no Nine Point Labs servers. fm-cli talks only to Fastmail: " +
		"api.fastmail.com to sign in (OAuth discovery, client registration, tokens, revocation) and for " +
		"the JMAP session, API and push connection it lists, and caldav.fastmail.com and " +
		"carddav.fastmail.com for calendar and contacts once you store an app password. Tokens and the " +
		"app password stay in your system keyring (or the FM_API_TOKEN / FM_APP_PASSWORD environment), " +
		"never in a file. The terminal app caches the mail it lists, headers and bodies, with settings " +
		"and queued offline changes in ~/.config/fm-cli/emails.db, readable only by you. An email's " +
		"remote images load only when you press i or open it in your browser with b."
)

// Version is set by main from the build's ldflags.
var Version = "dev"

// LicenseText is the full license, as shipped in LICENSE.
func LicenseText() string { return fmcli.License }

// BuiltWith names the Go version and the main libraries the binary was
// built with.
func BuiltWith() string {
	parts := []string{"Go " + strings.TrimPrefix(runtime.Version(), "go")}
	versions := map[string]string{}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			versions[dep.Path] = dep.Version
		}
	}
	for _, lib := range [][2]string{
		{"Bubble Tea", "github.com/charmbracelet/bubbletea"},
		{"Lip Gloss", "github.com/charmbracelet/lipgloss"},
		{"go-jmap", "git.sr.ht/~rockorager/go-jmap"},
	} {
		if v := versions[lib[1]]; v != "" {
			parts = append(parts, lib[0]+" "+v)
		}
	}
	return strings.Join(parts, ", ")
}

// Details are the version and system details to paste into a support email.
func Details() string {
	desktop := os.Getenv("XDG_CURRENT_DESKTOP")
	if desktop == "" {
		desktop = "unknown desktop"
	}
	term := os.Getenv("TERM")
	if term == "" {
		term = "unknown terminal"
	}
	return fmt.Sprintf("%s %s\n%s\n%s/%s, %s, %s", Name, Version, BuiltWith(), runtime.GOOS, runtime.GOARCH, desktop, term)
}

// MailtoURL is a mailto: link to support with the version in the subject and
// the details in the body.
func MailtoURL() string {
	escape := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	return "mailto:" + SupportEmail + "?subject=" + escape(Name+" "+Version) + "&body=" + escape("\n\n--\n"+Details())
}

// Text is the About screen as plain text, for `fm-cli about`.
func Text() string {
	var s strings.Builder
	fmt.Fprintf(&s, "%s %s\n%s\n\n", Name, Version, Tagline)
	for _, row := range [][2]string{
		{"Made by", Organization},
		{"Created", Created},
		{"License", LicenseName + ", " + Copyright},
		{"Support", SupportEmail},
		{"Built with", BuiltWith()},
	} {
		fmt.Fprintf(&s, "%-11s %s\n", row[0], row[1])
	}
	fmt.Fprintf(&s, "\nPrivacy: %s\n\n%s", Privacy, LicenseText())
	return s.String()
}
