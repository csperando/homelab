package main

import (
	"crypto/subtle"
	"log"
	"net/http"
)

// basicAuth enforces HTTP Basic Auth against the given credentials,
// comparing in constant time to avoid leaking timing information.
func basicAuth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqUser, reqPass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(reqUser), []byte(user)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(reqPass), []byte(pass)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="homelab"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// githubExposureWarning returns a warning message when a GitHub token is
// configured but auth is (fully or partially) disabled, since the repos
// tab's clone functionality — and the token itself — would then be
// reachable by anyone who can reach the dashboard. Returns "" when there's
// nothing to warn about.
func githubExposureWarning(adminUser, adminPass, githubToken string) string {
	if githubToken == "" || (adminUser != "" && adminPass != "") {
		return ""
	}
	return "WARNING: GITHUB_TOKEN is set but ADMIN_USER/ADMIN_PASSWORD are not fully configured — repo cloning (and the configured token) is exposed unauthenticated"
}

// withAuth wraps next with basicAuth, unless user or pass is empty — in
// which case it fails open and serves next unauthenticated, logging a
// warning. This lets an unconfigured environment keep serving the
// dashboard rather than locking it out entirely.
func withAuth(user, pass string, next http.Handler) http.Handler {
	if user == "" || pass == "" {
		log.Println("ADMIN_USER/ADMIN_PASSWORD not set — dashboard auth disabled")
		return next
	}
	return basicAuth(user, pass, next)
}
