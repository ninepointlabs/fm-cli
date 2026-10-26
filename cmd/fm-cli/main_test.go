package main

import "testing"

func TestParseFromSetting(t *testing.T) {
	for in, want := range map[string]string{"me@example.com": "me@example.com", " me@example.com ": "me@example.com", "off": "", "": ""} {
		got, err := parseFromSetting(in)
		if err != nil || got != want {
			t.Errorf("parseFromSetting(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"not-an-address", "me@", "Me <me@example.com>", "a@b.com, c@d.com"} {
		if _, err := parseFromSetting(in); err == nil {
			t.Errorf("parseFromSetting(%q) should fail", in)
		}
	}
}
