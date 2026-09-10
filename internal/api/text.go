package api

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// CleanText drops control characters from server-supplied text so nothing
// from an email can drive the terminal: C0 controls except newline and tab,
// DEL, the C1 range, and the Unicode line/paragraph separators. Invalid
// UTF-8 is replaced.
func CleanText(s string) string {
	if isCleanText(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == utf8.RuneError:
			b.WriteRune('�')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == ' ' || r == ' ':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CleanLine is CleanText for one-line fields: newlines and tabs become
// spaces and runs of whitespace collapse.
func CleanLine(s string) string {
	cleaned := CleanText(s)
	if !strings.ContainsAny(cleaned, "\n\t") && !strings.Contains(cleaned, "  ") {
		return strings.TrimSpace(cleaned)
	}
	return strings.Join(strings.Fields(cleaned), " ")
}

func isCleanText(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == ' ' || r == ' ' || r == utf8.RuneError {
			return false
		}
	}
	return true
}

// TruncateRunes shortens s to at most n runes, appending an ellipsis when it
// had to cut. It never splits a multi-byte character.
func TruncateRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	runes := []rune(s)
	return string(runes[:n-1]) + "…"
}

// IsSafeLinkURL says whether a URL consists only of RFC 3986 characters, so
// it can be placed inside an OSC 8 hyperlink without terminating or
// extending the escape sequence.
func IsSafeLinkURL(u string) bool {
	if u == "" || len(u) > 2048 {
		return false
	}
	for _, r := range u {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("-._~:/?#[]@!$&'()*+,;=%", r):
		default:
			return false
		}
	}
	return !unicode.IsSpace(rune(u[0]))
}
