package tui

import (
	"fmt"
	"strings"

	"fm-cli/internal/about"
	"fm-cli/internal/images"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// aboutStatusMsg is the result of an About action, shown under the facts.
type aboutStatusMsg string

func (m Model) goAbout() (tea.Model, tea.Cmd) {
	m.state = viewAbout
	m.aboutScroll = 0
	m.aboutShowLicense = false
	m.aboutStatus = ""
	m.clearFilter()
	return m, nil
}

// updateAbout handles the About screen's own keys. ok is false for keys it
// leaves to the global ones (0-5, ctrl+c).
func (m Model) updateAbout(k tea.KeyMsg) (_ tea.Model, _ tea.Cmd, ok bool) {
	page := max(m.bodyViewHeight()-2, 1)
	switch k.String() {
	case "esc", "left", "h":
		m.state = viewMainMenu
	case "up", "k":
		m.aboutScroll--
	case "down", "j":
		m.aboutScroll++
	case "pgup", "ctrl+u":
		m.aboutScroll -= page
	case "pgdown", "ctrl+d", " ":
		m.aboutScroll += page
	case "home", "g":
		m.aboutScroll = 0
	case "end", "G":
		m.aboutScroll = len(m.aboutLines())
	case "l", "enter":
		m.aboutShowLicense = !m.aboutShowLicense
		if m.aboutShowLicense {
			// Bring the license into view.
			m.aboutScroll = len(m.aboutLines(false))
		}
	case "e":
		return m, emailSupportCmd(), true
	case "c":
		return m, copyCmd(about.SupportEmail, "Copied "+about.SupportEmail), true
	case "v":
		return m, copyCmd(about.Details(), "Copied the version details"), true
	default:
		return m, nil, false
	}
	m.aboutScroll = min(max(m.aboutScroll, 0), max(len(m.aboutLines())-m.bodyViewHeight(), 0))
	return m, nil, true
}

func emailSupportCmd() tea.Cmd {
	return func() tea.Msg {
		if err := images.OpenInBrowser(about.MailtoURL()); err != nil {
			return aboutStatusMsg(fmt.Sprintf("Could not open your mail app (%v). Write to %s.", err, about.SupportEmail))
		}
		return aboutStatusMsg("Opening a message to " + about.SupportEmail + " in your mail app…")
	}
}

func copyCmd(text, done string) tea.Cmd {
	return func() tea.Msg {
		if err := clipboard.WriteAll(text); err != nil {
			return aboutStatusMsg("No clipboard available (install wl-clipboard or xclip): " + strings.ReplaceAll(text, "\n", " · "))
		}
		return aboutStatusMsg(done)
	}
}

// aboutLines renders the About screen at the content width. withLicense
// defaults to whether the license is showing.
func (m Model) aboutLines(withLicense ...bool) []string {
	showLicense := m.aboutShowLicense
	if len(withLicense) > 0 {
		showLicense = withLicense[0]
	}
	width := m.contentWidth()
	const labelWidth = 12
	label := contactFieldLabelStyle.Width(labelWidth)
	value := lipgloss.NewStyle().Foreground(fgColor).Width(max(width-labelWidth, 8))
	row := func(name, text string) []string {
		return strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, label.Render(name), value.Render(text)), "\n")
	}

	var lines []string
	lines = append(lines, eventTitleStyle.Render(about.Name)+"  "+helpStyle.Render("Version "+about.Version))
	lines = append(lines, strings.Split(lipgloss.NewStyle().Foreground(fgColor).Width(width).Render(about.Tagline), "\n")...)
	lines = append(lines, "")
	lines = append(lines, row("Made by", about.Organization)...)
	lines = append(lines, row("Created", about.Created)...)
	lines = append(lines, row("License", about.LicenseName+", "+about.Copyright)...)
	lines = append(lines, row("Support", about.SupportEmail)...)
	lines = append(lines, row("Privacy", about.Privacy)...)
	lines = append(lines, row("Built with", about.BuiltWith())...)
	if m.aboutStatus != "" {
		lines = append(lines, "")
		lines = append(lines, strings.Split(warningStyle.Width(width).Render(m.aboutStatus), "\n")...)
	}
	if showLicense {
		lines = append(lines, "", emailDateStyle.Render(strings.Repeat("─", width)), "")
		// One line per paragraph, so it wraps to the terminal's width.
		var paras []string
		for _, p := range strings.Split(about.LicenseText(), "\n\n") {
			if p = strings.Join(strings.Fields(p), " "); p != "" {
				paras = append(paras, p)
			}
		}
		text := lipgloss.NewStyle().Foreground(mutedColor).Width(width).Render(strings.Join(paras, "\n\n"))
		lines = append(lines, strings.Split(text, "\n")...)
	}
	return lines
}

func (m Model) aboutView() string {
	lines := m.aboutLines()
	height := m.bodyViewHeight()
	maxScroll := max(len(lines)-height, 0)
	pos := min(max(m.aboutScroll, 0), maxScroll)
	var s strings.Builder
	for _, line := range lines[pos:min(pos+height, len(lines))] {
		s.WriteString(line + "\n")
	}
	license := "license"
	if m.aboutShowLicense {
		license = "hide license"
	}
	scroll := ""
	if maxScroll > 0 {
		scroll = helpStyle.Render(fmt.Sprintf("%d%%  ", pos*100/maxScroll))
	}
	help := m.renderHelp("esc", "back", "l", license, "e", "email support", "c", "copy address", "v", "copy version", "j/k", "scroll")
	s.WriteString("\n" + lipgloss.NewStyle().MaxWidth(m.contentWidth()).Render(scroll+help))
	return s.String()
}
