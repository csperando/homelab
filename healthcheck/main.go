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
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*.html
var templatesFS embed.FS

var pageTmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

// defaultLogTail is the number of lines fetched when a request doesn't
// specify one; maxLogTail bounds a client-supplied "tail" query param so it
// can't force an unbounded fetch through the API.
const (
	defaultLogTail = 200
	maxLogTail     = 2000
)

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

// workspacesView is the template data for the "workspaces" tab (formerly
// "repos"): the GitHub repo list (or a disabled/error reason if it couldn't
// be fetched) plus any in-flight or completed clone jobs, keyed by repo
// name for per-row lookup — and, independently, the persisted Workspace
// list (or its own disabled/error reason, since Postgres and GitHub are
// unrelated failure domains).
type workspacesView struct {
	Active            string
	Enabled           bool
	Reason            string
	Repos             []repoListing
	Jobs              map[string]*cloneJob
	WorkspacesEnabled bool
	WorkspacesReason  string
	Workspaces        []workspaceRow
}

// logsView is the template data for the "logs" tab.
type logsView struct {
	Active     string
	Enabled    bool
	Reason     string
	Containers []dockerLogContainer
	Container  string
	Entries    []logEntry
}

func jobsByName(jobs []*cloneJob) map[string]*cloneJob {
	m := make(map[string]*cloneJob, len(jobs))
	for _, j := range jobs {
		m[j.Repo] = j
	}
	return m
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

// handleWorkspaces renders the workspaces tab, merging the persisted
// Workspace list (Postgres) with live branch/dirty state (scanWorkspaceRepos)
// alongside the existing GitHub-repo-listing/clone flow. The two sections
// degrade independently: a missing GITHUB_TOKEN doesn't hide the workspace
// list, and an unreachable Postgres doesn't hide the GitHub repo list.
func handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	status := gatherRepoList(githubToken)
	view := workspacesView{
		Active:  "workspaces",
		Enabled: status.Enabled,
		Reason:  status.Reason,
		Repos:   status.Repos,
		Jobs:    jobsByName(listCloneJobs()),
	}

	workspaces, err := ListWorkspaces(db)
	if err != nil {
		view.WorkspacesReason = err.Error()
	} else {
		view.WorkspacesEnabled = true
		view.Workspaces = mergeWorkspaceRows(workspaces, scanWorkspaceRepos())
	}

	if err := pageTmpl.ExecuteTemplate(w, "workspaces.html", view); err != nil {
		log.Printf("failed to render workspaces page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func handleAgents(w http.ResponseWriter, r *http.Request) {
	if err := pageTmpl.ExecuteTemplate(w, "agents.html", pageView{Active: "agents"}); err != nil {
		log.Printf("failed to render agents page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// handleLogs renders the logs tab, server-rendering the initial tail so the
// page isn't blank before the poll loop's first fetch completes.
func handleLogs(w http.ResponseWriter, r *http.Request) {
	result := gatherLogs(r.Context(), r.URL.Query().Get("container"), defaultLogTail)
	view := logsView{
		Active:     "logs",
		Enabled:    result.Enabled,
		Reason:     result.Reason,
		Containers: result.Containers,
		Container:  result.Container,
		Entries:    result.Entries,
	}
	if err := pageTmpl.ExecuteTemplate(w, "logs.html", view); err != nil {
		log.Printf("failed to render logs page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// handleAPILogs serves the latest log tail as JSON, for the logs page's
// poll loop.
func handleAPILogs(w http.ResponseWriter, r *http.Request) {
	tail := defaultLogTail
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxLogTail {
			tail = n
		}
	}

	result := gatherLogs(r.Context(), r.URL.Query().Get("container"), tail)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Printf("failed to encode logs response: %v", err)
	}
}

// isCoveragePath reports whether a /files/ path (already stripped of that
// prefix) passes through a directory literally named "coverage" — the file
// server only ever exposes coverage reports, not full repo contents.
func isCoveragePath(urlPath string) bool {
	return slices.Contains(strings.Split(path.Clean(urlPath), "/"), "coverage")
}

// handleAPIWorkspacesClone triggers a background clone of the repo named in
// the "name"/"clone_url" form fields, returning the created job as JSON.
// POST-only, since it has a side effect.
func handleAPIWorkspacesClone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	job, err := startClone(r.FormValue("name"), r.FormValue("clone_url"), githubToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(job); err != nil {
		log.Printf("failed to encode clone response: %v", err)
	}
}

// handleAPIWorkspacesStatus reports every known clone job, for the
// workspaces tab to poll while a clone is in progress.
func handleAPIWorkspacesStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(listCloneJobs()); err != nil {
		log.Printf("failed to encode workspaces status response: %v", err)
	}
}

// isSafeRepoName reports whether name is safe to use as a single path
// segment under workspaceDir (e.g. via filepath.Join(workspaceDir, name))
// or in an exec.Command argument — rejecting anything empty, containing a
// path separator, or a "." / ".." traversal segment.
func isSafeRepoName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, "/\\") {
		return false
	}
	return true
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

	if msg := githubExposureWarning(adminUser, adminPass, githubToken); msg != "" {
		log.Println(msg)
	}

	// Postgres is best-effort at startup: an unreachable database disables
	// DB-backed features (degrading like every other optional integration
	// in this package) rather than crashing the whole dashboard. A failed
	// migration is different — it means the code and an actually-reachable
	// database have drifted — and is fatal.
	if conn, err := openDB(); err != nil {
		log.Printf("postgres unavailable, DB-backed features disabled: %v", err)
	} else {
		db = conn
		if err := runMigrations(db); err != nil {
			log.Fatalf("running migrations: %v", err)
		}
		if err := backfillWorkspaces(db); err != nil {
			log.Printf("backfilling workspaces: %v", err)
		}
	}

	http.HandleFunc("/healthz", handleHealthz)
	http.Handle("/api/status", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPIStatus)))
	http.Handle("/", withAuth(adminUser, adminPass, http.HandlerFunc(handleDashboard)))
	http.Handle("/workspaces", withAuth(adminUser, adminPass, http.HandlerFunc(handleWorkspaces)))
	http.Handle("/api/workspaces/clone", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPIWorkspacesClone)))
	http.Handle("/api/workspaces/status", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPIWorkspacesStatus)))
	http.Handle("/agents", withAuth(adminUser, adminPass, http.HandlerFunc(handleAgents)))
	http.Handle("/logs", withAuth(adminUser, adminPass, http.HandlerFunc(handleLogs)))
	http.Handle("/api/logs", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPILogs)))
	http.Handle("/files/", withAuth(adminUser, adminPass, http.StripPrefix("/files/", coverageOnly(http.FileServer(http.Dir(workspaceDir))))))

	addr := ":55123"
	log.Printf("homelab-healthcheck listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("healthcheck server failed: %v", err)
	}
}
