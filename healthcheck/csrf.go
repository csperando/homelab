package main

import (
	"log"
	"net/http"
	"net/url"
	"strings"
)

// withCSRF rejects cross-origin state-changing requests, mirroring Go 1.25's
// http.CrossOriginProtection (unavailable here: the image ships Ubuntu
// 24.04's Go 1.22). Basic Auth alone doesn't stop CSRF — browsers attach
// cached credentials to cross-site form posts — and withAuth fails open
// when unconfigured, so this is applied outside it.
//
// Safe methods always pass. Otherwise Sec-Fetch-Site, when present, decides
// alone (same-origin/none allowed). Browsers omit it for plain-HTTP
// non-localhost origins (e.g. the server's LAN address over the VPN), so the
// fallback compares Origin's host:port to r.Host — which is why nginx must
// forward $http_host, not $host (which strips the port). A request with
// neither header isn't from a browser (curl, scripts) and passes.
// X-Forwarded-Host and similar proxy headers are deliberately never
// consulted: a client can set them freely.
func withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if csrfAllowed(r) {
			next.ServeHTTP(w, r)
			return
		}
		log.Printf("csrf: rejected %s %s (Origin=%q, Sec-Fetch-Site=%q)",
			r.Method, r.URL.Path, r.Header.Get("Origin"), r.Header.Get("Sec-Fetch-Site"))
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
	})
}

func csrfAllowed(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}

	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || r.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
