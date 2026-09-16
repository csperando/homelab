package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBasicAuth(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Inner-Called", "1")
	})
	h := basicAuth("admin", "secret", inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("correct credentials: status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("X-Inner-Called") != "1" {
		t.Error("correct credentials: expected inner handler to be called")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing header: status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="homelab"` {
		t.Errorf("missing header: WWW-Authenticate = %q, want Basic realm=\"homelab\"", got)
	}
	if rec.Header().Get("X-Inner-Called") == "1" {
		t.Error("missing header: expected inner handler NOT to be called")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("wronguser", "secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong username: status = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "wrongpass")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: status = %d, want 401", rec.Code)
	}
}

func TestWithAuthFailOpen(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Inner-Called", "1")
	})

	for _, tc := range []struct {
		name string
		user string
		pass string
	}{
		{"empty user", "", "secret"},
		{"empty pass", "admin", ""},
		{"both empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := withAuth(tc.user, tc.pass, inner)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (fail-open)", rec.Code)
			}
			if rec.Header().Get("X-Inner-Called") != "1" {
				t.Error("expected inner handler to be called")
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "" {
				t.Errorf("WWW-Authenticate = %q, want empty", got)
			}
		})
	}
}

func TestGithubExposureWarning(t *testing.T) {
	cases := []struct {
		name        string
		adminUser   string
		adminPass   string
		githubToken string
		wantWarning bool
	}{
		{"no token set", "", "", "", false},
		{"token set, auth configured", "admin", "secret", "ghp_x", false},
		{"token set, auth fully disabled", "", "", "ghp_x", true},
		{"token set, only user missing", "", "secret", "ghp_x", true},
		{"token set, only pass missing", "admin", "", "ghp_x", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := githubExposureWarning(c.adminUser, c.adminPass, c.githubToken)
			if (got != "") != c.wantWarning {
				t.Errorf("githubExposureWarning(%q, %q, %q) = %q, wantWarning = %v", c.adminUser, c.adminPass, c.githubToken, got, c.wantWarning)
			}
		})
	}
}

func TestWithAuthEnforced(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Inner-Called", "1")
	})
	h := withAuth("admin", "secret", inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no credentials: status = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("correct credentials: status = %d, want 200", rec.Code)
	}
}
