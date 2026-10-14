package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const principalResponse = `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/dav/</D:href>
    <D:propstat>
      <D:prop><D:current-user-principal><D:href>%s</D:href></D:current-user-principal></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

func TestDiscoverCurrentUserPrincipal(t *testing.T) {
	for _, tc := range []struct {
		name, href, want string
	}{
		{"path href", "/dav/principals/user/noah_hopping@example.com/", "/dav/principals/user/noah_hopping@example.com/"},
		{"absolute href", "https://caldav.example.com/dav/principals/user/me@example.com/", "/dav/principals/user/me@example.com/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Cyrus answers 405 on the bare /dav path, so the trailing slash must survive.
				if r.Method != "PROPFIND" || r.URL.Path != "/dav/" || r.Header.Get("Depth") != "0" {
					t.Errorf("got %s %s Depth=%q", r.Method, r.URL.Path, r.Header.Get("Depth"))
				}
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), "current-user-principal") {
					t.Errorf("request body %q", body)
				}
				w.WriteHeader(http.StatusMultiStatus)
				io.WriteString(w, strings.Replace(principalResponse, "%s", tc.href, 1))
			}))
			defer srv.Close()

			got, err := discoverCurrentUserPrincipal(context.Background(), srv.Client(), srv.URL+"/dav/")
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}

	t.Run("non-multistatus", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))
		defer srv.Close()
		if _, err := discoverCurrentUserPrincipal(context.Background(), srv.Client(), srv.URL+"/dav/"); err == nil {
			t.Fatal("expected an error for 405")
		}
	})

	t.Run("no principal", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, strings.Replace(principalResponse, "%s", "", 1))
		}))
		defer srv.Close()
		if _, err := discoverCurrentUserPrincipal(context.Background(), srv.Client(), srv.URL+"/dav/"); err == nil {
			t.Fatal("expected an error when the response has no principal href")
		}
	})
}
