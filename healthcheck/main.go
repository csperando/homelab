package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
	"time"
)

//go:embed templates/*.html
var templatesFS embed.FS

var pageTmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

type dashboardView struct {
	Active       string
	Status       string
	Uptime       string
	Timestamp    string
	ToolVersions map[string]string
	DiskUsed     string
	DiskTotal    string
	MemAvail     string
	MemTotal     string
	LoadAvg      string
	Repos        []repoStatus
	Docker       dockerStatus
}

// pageView is the minimal template data for placeholder pages that don't yet
// have any content of their own beyond the shared head/nav.
type pageView struct {
	Active string
}

// shortID truncates an id for compact table display, e.g. a UUID
func shortID(id string) string {
	const n = 8
	if len(id) <= n {
		return id
	}
	return id[:n]
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatKB(kb uint64) string {
	return formatBytes(kb * 1024)
}

func toView(s statusData) dashboardView {
	return dashboardView{
		Active:       "dashboard",
		Status:       s.Status,
		Uptime:       time.Since(startTime).Round(time.Second).String(),
		Timestamp:    s.Timestamp.Format(time.RFC3339),
		ToolVersions: s.ToolVersions,
		DiskUsed:     formatBytes(s.Workspace.UsedBytes),
		DiskTotal:    formatBytes(s.Workspace.TotalBytes),
		MemAvail:     formatKB(s.Memory.AvailKB),
		MemTotal:     formatKB(s.Memory.TotalKB),
		LoadAvg:      s.LoadAvg,
		Repos:        s.Repos,
		Docker:       s.Docker,
	}
}

// handleHealthz is the fast liveness check Docker's HEALTHCHECK polls —
// deliberately cheap, no shelling out or repo scanning.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":         "ok",
		"uptime_seconds": time.Since(startTime).Seconds(),
		"timestamp":      time.Now().UTC(),
	})
}

func handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(gatherStatus()); err != nil {
		log.Printf("failed to encode status response: %v", err)
	}
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	view := toView(gatherStatus())
	if err := pageTmpl.ExecuteTemplate(w, "dashboard.html", view); err != nil {
		log.Printf("failed to render dashboard: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func handleRepos(w http.ResponseWriter, r *http.Request) {
	if err := pageTmpl.ExecuteTemplate(w, "repos.html", pageView{Active: "repos"}); err != nil {
		log.Printf("failed to render repos page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func handleAgents(w http.ResponseWriter, r *http.Request) {
	if err := pageTmpl.ExecuteTemplate(w, "agents.html", pageView{Active: "agents"}); err != nil {
		log.Printf("failed to render agents page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// isCoveragePath reports whether a /files/ path (already stripped of that
// prefix) passes through a directory literally named "coverage" — the file
// server only ever exposes coverage reports, not full repo contents.
func isCoveragePath(urlPath string) bool {
	return slices.Contains(strings.Split(path.Clean(urlPath), "/"), "coverage")
}

func coverageOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isCoveragePath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func main() {
	adminUser := os.Getenv("ADMIN_USER")
	adminPass := os.Getenv("ADMIN_PASSWORD")

	http.HandleFunc("/healthz", handleHealthz)
	http.Handle("/api/status", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPIStatus)))
	http.Handle("/", withAuth(adminUser, adminPass, http.HandlerFunc(handleDashboard)))
	http.Handle("/repos", withAuth(adminUser, adminPass, http.HandlerFunc(handleRepos)))
	http.Handle("/agents", withAuth(adminUser, adminPass, http.HandlerFunc(handleAgents)))
	http.Handle("/files/", withAuth(adminUser, adminPass, http.StripPrefix("/files/", coverageOnly(http.FileServer(http.Dir(workspaceDir))))))

	addr := ":55123"
	log.Printf("homelab-healthcheck listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("healthcheck server failed: %v", err)
	}
}
