package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithCSRF(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		host        string
		origin      string // "" = header absent
		fetchSite   string // "" = header absent
		contentType string
		basicAuth   bool
		wantAllowed bool
	}{
		// Safe methods are never blocked, whatever the headers say.
		{name: "GET cross-site", method: http.MethodGet, host: "localhost:55123", origin: "http://evil.example", fetchSite: "cross-site", wantAllowed: true},
		{name: "HEAD cross-site", method: http.MethodHead, host: "localhost:55123", origin: "http://evil.example", fetchSite: "cross-site", wantAllowed: true},
		{name: "OPTIONS cross-site", method: http.MethodOptions, host: "localhost:55123", origin: "http://evil.example", fetchSite: "cross-site", wantAllowed: true},

		// Sec-Fetch-Site present: it alone decides.
		{name: "Sec-Fetch-Site same-origin", method: http.MethodPost, host: "localhost:55123", origin: "http://localhost:55123", fetchSite: "same-origin", basicAuth: true, wantAllowed: true},
		{name: "Sec-Fetch-Site none", method: http.MethodPost, host: "localhost:55123", fetchSite: "none", wantAllowed: true},
		{name: "Sec-Fetch-Site cross-site form", method: http.MethodPost, host: "localhost:55123", origin: "http://evil.example", fetchSite: "cross-site", contentType: "application/x-www-form-urlencoded", basicAuth: true, wantAllowed: false},
		{name: "Sec-Fetch-Site cross-site multipart", method: http.MethodPost, host: "localhost:55123", origin: "http://evil.example", fetchSite: "cross-site", contentType: "multipart/form-data; boundary=x", basicAuth: true, wantAllowed: false},
		{name: "Sec-Fetch-Site cross-site text/plain", method: http.MethodPost, host: "localhost:55123", origin: "http://evil.example", fetchSite: "cross-site", contentType: "text/plain", basicAuth: true, wantAllowed: false},
		{name: "Sec-Fetch-Site same-site (other port, e.g. dev server)", method: http.MethodPost, host: "localhost:55123", origin: "http://localhost:5173", fetchSite: "same-site", wantAllowed: false},
		{name: "Sec-Fetch-Site unknown value", method: http.MethodPost, host: "localhost:55123", fetchSite: "bogus", wantAllowed: false},
		{name: "Sec-Fetch-Site cross-site overrides matching Origin", method: http.MethodPost, host: "localhost:55123", origin: "http://localhost:55123", fetchSite: "cross-site", wantAllowed: false},

		// Sec-Fetch-Site absent (plain-HTTP non-localhost origins, older browsers, non-browser clients).
		{name: "no headers (curl/scripts)", method: http.MethodPost, host: "localhost:55123", basicAuth: true, wantAllowed: true},
		{name: "Origin matches Host", method: http.MethodPost, host: "192.0.2.10:8080", origin: "http://192.0.2.10:8080", wantAllowed: true},
		{name: "Origin matches Host, default port", method: http.MethodPost, host: "192.0.2.10", origin: "http://192.0.2.10", wantAllowed: true},
		{name: "Origin matches Host case-insensitively", method: http.MethodPost, host: "LocalHost:55123", origin: "http://localhost:55123", wantAllowed: true},
		{name: "Origin port stripped from Host (nginx $host)", method: http.MethodPost, host: "localhost", origin: "http://localhost:8080", wantAllowed: false},
		{name: "Origin different host form", method: http.MethodPost, host: "localhost:55123", origin: "http://evil.example", contentType: "application/x-www-form-urlencoded", basicAuth: true, wantAllowed: false},
		{name: "Origin different host text/plain", method: http.MethodPost, host: "localhost:55123", origin: "http://evil.example", contentType: "text/plain", wantAllowed: false},
		{name: "Origin null", method: http.MethodPost, host: "localhost:55123", origin: "null", wantAllowed: false},
		{name: "Origin unparseable", method: http.MethodPost, host: "localhost:55123", origin: "http://[::1", wantAllowed: false},
		{name: "Origin empty host", method: http.MethodPost, host: "", origin: "http://localhost:55123", wantAllowed: false},
		{name: "other unsafe methods covered", method: http.MethodDelete, host: "localhost:55123", origin: "http://evil.example", wantAllowed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
			})
			h := withCSRF(inner)

			req := httptest.NewRequest(tt.method, "/api/workspaces/polling", strings.NewReader("workspace=x"))
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			if tt.basicAuth {
				req.SetBasicAuth("admin", "secret")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if called != tt.wantAllowed {
				t.Errorf("inner handler called = %v, want %v", called, tt.wantAllowed)
			}
			if !tt.wantAllowed {
				if rec.Code != http.StatusForbidden {
					t.Errorf("status = %d, want 403", rec.Code)
				}
				if !strings.Contains(rec.Body.String(), "cross-origin request rejected") {
					t.Errorf("body = %q, want it to mention cross-origin request rejected", rec.Body.String())
				}
			}
		})
	}
}

// X-Forwarded-Host is client-controllable and must never make a mismatched
// Origin look same-origin.
func TestWithCSRFIgnoresForwardedHost(t *testing.T) {
	called := false
	h := withCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	req := httptest.NewRequest(http.MethodPost, "/api/agents/start", nil)
	req.Host = "localhost:55123"
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("X-Forwarded-Host", "evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called || rec.Code != http.StatusForbidden {
		t.Errorf("called = %v, status = %d; want rejected with 403", called, rec.Code)
	}
}
