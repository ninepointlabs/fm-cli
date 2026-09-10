package api

import "testing"

func TestCleanLineDropsControls(t *testing.T) {
	in := "Hi\x1b]52;c;evil\x07 there\n\tnow"
	if got := CleanLine(in); got != "Hi]52;c;evil there now" {
		t.Errorf("CleanLine: %q", got)
	}
}

func TestCleanTextKeepsNewlines(t *testing.T) {
	// The escape byte goes; the bytes that followed it are plain text now.
	// Line and paragraph separators go too; newline and tab stay.
	in := "a\x1b[31mb\nc d\te"
	if got := CleanText(in); got != "a[31mb\ncd\te" {
		t.Errorf("CleanText: %q", got)
	}
	if got := CleanText("plain\ttext\n"); got != "plain\ttext\n" {
		t.Errorf("CleanText passthrough: %q", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := TruncateRunes("héllo wörld", 6); got != "héllo…" {
		t.Errorf("TruncateRunes: %q", got)
	}
	if got := TruncateRunes("short", 10); got != "short" {
		t.Errorf("TruncateRunes passthrough: %q", got)
	}
}

func TestIsSafeLinkURL(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/a?b=1&c=%20":  true,
		"https://example.com/\x1b]8;;evil": false,
		"https://example.com/a b":          false,
		"https://example.com/é":       false,
		"https://example.com/x\x07":        false,
		"":                                 false,
	}
	for u, want := range cases {
		if IsSafeLinkURL(u) != want {
			t.Errorf("IsSafeLinkURL(%q) != %v", u, want)
		}
	}
}
