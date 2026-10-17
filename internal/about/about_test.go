package about

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestFactsAgreeWithTheLicense(t *testing.T) {
	license, err := os.ReadFile("../../LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	if LicenseText() != string(license) {
		t.Fatal("the embedded license differs from LICENSE")
	}
	if !strings.HasPrefix(LicenseText(), LicenseName+"\n") {
		t.Fatalf("LICENSE does not start with %q", LicenseName)
	}
	if want := strings.Replace(Copyright, "©", "Copyright (c)", 1); !strings.Contains(LicenseText(), want) {
		t.Fatalf("LICENSE does not contain %q", want)
	}
	if SupportEmail != "help@ninepointlabs.com" || Organization != "Nine Point Labs" {
		t.Fatalf("support %q, organization %q", SupportEmail, Organization)
	}
}

func TestMailtoCarriesTheDetails(t *testing.T) {
	Version = "9.9.9"
	link := MailtoURL()
	if strings.Contains(link, "+") {
		t.Fatalf("spaces must be %%20 in a mailto link: %s", link)
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "mailto" || u.Opaque != SupportEmail {
		t.Fatalf("bad link %q (%v)", link, err)
	}
	q := u.Query()
	if q.Get("subject") != "fm-cli 9.9.9" {
		t.Fatalf("subject %q", q.Get("subject"))
	}
	if body := q.Get("body"); !strings.Contains(body, Details()) || !strings.Contains(body, "Go ") {
		t.Fatalf("body %q", body)
	}
}

func TestText(t *testing.T) {
	text := Text()
	for _, want := range []string{Name, Tagline, Organization, Created, SupportEmail, "Privacy: ", "Permission is hereby granted"} {
		if !strings.Contains(text, want) {
			t.Errorf("about text lacks %q", want)
		}
	}
}
