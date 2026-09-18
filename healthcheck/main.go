package main

import (
	"crypto/subtle"
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

// agentsView is the template data for the "agents" tab: the workspace list
// (for the picker) and the persisted run history (most recent first,
// including any still in-flight — so a page reload doesn't lose track of a
// running task), each degrading independently with its own Reason since
// they're both just different views of the same Postgres availability.
type agentsView struct {
	Active            string
	WorkspacesEnabled bool
	WorkspacesReason  string
	Workspaces        []workspace
	RunsEnabled       bool
	RunsReason        string
	Runs              []agentRun
	// PendingApprovals is keyed by AgentRunID, populated only for runs
	// currently awaiting_approval — a pointer so the template's {{with}}
	// correctly treats a missing entry as absent (a zero-value struct
	// would otherwise be treated as present).
	PendingApprovals map[int64]*approval
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

// handleAgents renders the agents tab: a workspace picker to start a new
// run, plus the run history (including anything still in-flight) so the
// page reflects reality on load rather than starting blank.
func handleAgents(w http.ResponseWriter, r *http.Request) {
	view := agentsView{Active: "agents"}

	if workspaces, err := ListWorkspaces(db); err != nil {
		view.WorkspacesReason = err.Error()
	} else {
		view.WorkspacesEnabled = true
		view.Workspaces = workspaces
	}

	if runs, err := ListAgentRuns(db); err != nil {
		view.RunsReason = err.Error()
	} else {
		view.RunsEnabled = true
		view.Runs = runs
		view.PendingApprovals = make(map[int64]*approval)
		for _, run := range runs {
			if run.State == agentRunAwaitingApproval {
				view.PendingApprovals[run.ID] = findPendingApproval(run.ID)
			}
		}
	}

	if err := pageTmpl.ExecuteTemplate(w, "agents.html", view); err != nil {
		log.Printf("failed to render agents page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// handleAPIAgentsStart triggers a background agent run against the given
// workspace's directory, returning the created run as JSON. POST-only,
// since it has a side effect.
func handleAPIAgentsStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	workspacePath := r.FormValue("workspace")
	prompt := r.FormValue("prompt")
	if prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}

	ws, err := GetWorkspaceByPath(db, workspacePath)
	if err != nil {
		http.Error(w, "unknown workspace", http.StatusBadRequest)
		return
	}
	// A workspace row can outlive its directory — nothing currently deletes
	// a row when the clone is removed by hand — so never spawn against a
	// path that no longer exists.
	if _, err := os.Stat(ws.Path); err != nil {
		http.Error(w, "workspace directory no longer exists", http.StatusBadRequest)
		return
	}
	if _, inProgress := workspaceHasLiveRun(ws.ID); inProgress {
		http.Error(w, "a run is already in progress for this workspace", http.StatusConflict)
		return
	}

	session, err := CreateAgentSession(db, ws.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tk, err := CreateTask(db, session.ID, prompt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	run, err := CreateAgentRun(db, tk.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	startAgentRun(run.ID, ws.ID, ws.Path, prompt)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(run); err != nil {
		log.Printf("failed to encode agent run response: %v", err)
	}
}

// handleAPIAgentsStatus reports one agent run's current state/transcript,
// merging the in-memory live transcript (if the run is still being
// tracked) over the persisted Postgres row otherwise — for the Agents tab
// to poll while a run is in progress.
func handleAPIAgentsStatus(w http.ResponseWriter, r *http.Request) {
	runID, err := strconv.ParseInt(r.URL.Query().Get("run"), 10, 64)
	if err != nil {
		http.Error(w, "invalid or missing run query param", http.StatusBadRequest)
		return
	}

	run, err := GetAgentRun(db, runID)
	if err != nil {
		http.Error(w, "unknown run", http.StatusNotFound)
		return
	}

	// A run still tracked in-memory is, by definition, running — this also
	// covers the brief window between registerLiveRun and the DB write that
	// follows it in runAgent, where the persisted row might still say
	// "pending".
	if transcript, ok := liveRunTranscript(runID); ok {
		run.State = agentRunRunning
		run.Transcript = transcript
	}

	resp := struct {
		agentRun
		PendingApproval *approval `json:"pending_approval,omitempty"`
	}{agentRun: run}
	if run.State == agentRunAwaitingApproval {
		resp.PendingApproval = findPendingApproval(runID)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("failed to encode agent status response: %v", err)
	}
}

// handleAPIAgentsStop signals a running agent run's process to stop
// (SIGTERM, escalating to SIGKILL after a grace period if it doesn't exit
// on its own) and marks it stop-requested so its own completion path
// records "stopped" rather than "failed". POST-only, since it has a side
// effect. Responds immediately rather than waiting out the grace period.
func handleAPIAgentsStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	runID, err := strconv.ParseInt(r.FormValue("run"), 10, 64)
	if err != nil {
		http.Error(w, "invalid or missing run field", http.StatusBadRequest)
		return
	}

	if !requestStop(runID) {
		http.Error(w, "run is not currently in progress", http.StatusNotFound)
		return
	}
	go escalateStopAfterGracePeriod(runID)

	w.WriteHeader(http.StatusAccepted)
}

// preToolUseHookPayload is the subset of a PreToolUse HTTP hook's JSON
// input this handler reads (see ensureAgentHookSettingsFile in
// agentprocess.go for the hook config that posts here).
type preToolUseHookPayload struct {
	SessionID string `json:"session_id"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

type hookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

func writeHookDecision(w http.ResponseWriter, decision, reason string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
	}{
		HookSpecificOutput: hookSpecificOutput{
			HookEventName:            "PreToolUse",
			PermissionDecision:       decision,
			PermissionDecisionReason: reason,
		},
	})
}

// handleAPIAgentsPreToolUseHook receives every Bash call a headless agent
// run attempts (the PreToolUse HTTP hook attached via
// ensureAgentHookSettingsFile) and denies gated ones immediately,
// recording a pending Approval — it never waits and never live-approves
// anything. (Researched in goal.md: a PreToolUse hook fails OPEN on
// timeout, so waiting for a decision here would be a silent bypass, not a
// safety net.) Deliberately not behind withAuth — the subprocess has no
// dashboard credentials — authenticated via a shared secret header instead.
func handleAPIAgentsPreToolUseHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// An unset configured secret must never be treated as a valid
	// credential just because it happens to match an equally-empty header
	// — reject unconditionally rather than falling into the compare below.
	if agentHookSecret == "" {
		http.Error(w, "approval hook not configured", http.StatusUnauthorized)
		return
	}

	const bearerPrefix = "Bearer "
	presented := strings.TrimPrefix(r.Header.Get("Authorization"), bearerPrefix)
	if subtle.ConstantTimeCompare([]byte(presented), []byte(agentHookSecret)) != 1 {
		log.Printf("agent hook: rejected request with an invalid or missing shared secret")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var payload preToolUseHookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	if payload.ToolName != "Bash" || !isGatedCommand(payload.ToolInput.Command) {
		writeHookDecision(w, "allow", "")
		return
	}

	runID, ok := liveRunBySessionID(payload.SessionID)
	if !ok {
		// Fail closed even though we can't attach an Approval to any run:
		// an unresolvable session_id for a gated command is unexpected
		// (every run registers its session_id before it can reach a tool
		// call) and worth a log line, but must never be treated as
		// permission to proceed.
		log.Printf("agent hook: gated command denied for unresolved session_id %q (no run found, no approval recorded)", payload.SessionID)
		writeHookDecision(w, "deny", "no active run found for this session")
		return
	}

	toolInputJSON, err := json.Marshal(payload.ToolInput)
	if err != nil {
		toolInputJSON = []byte(payload.ToolInput.Command)
	}
	reason := fmt.Sprintf("gated command: %s", payload.ToolInput.Command)
	if _, err := CreateApproval(db, runID, payload.ToolName, string(toolInputJSON), reason); err != nil {
		log.Printf("agent run %d: failed to record approval: %v", runID, err)
	}

	writeHookDecision(w, "deny", "awaiting operator approval — resume this session once approved")
}

// handleAPIAgentsApprovalsDecide records an operator's decision on a
// pending Approval. Denying just closes out the original run; approving
// starts a *new* Agent Run for the same Task's session, resuming the
// original claude session (--resume) rather than starting fresh — see
// startResumedAgentRun. Behind withAuth, unlike the hook endpoint that
// creates Approvals — this route is operator-facing.
func handleAPIAgentsApprovalsDecide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	approvalID, err := strconv.ParseInt(r.FormValue("approval"), 10, 64)
	if err != nil {
		http.Error(w, "invalid or missing approval field", http.StatusBadRequest)
		return
	}
	decision := r.FormValue("decision")
	if decision != "approve" && decision != "deny" {
		http.Error(w, `decision must be "approve" or "deny"`, http.StatusBadRequest)
		return
	}

	appr, err := GetApproval(db, approvalID)
	if err != nil {
		http.Error(w, "unknown approval", http.StatusNotFound)
		return
	}
	if appr.State != approvalPending {
		http.Error(w, "approval already decided", http.StatusConflict)
		return
	}

	if decision == "deny" {
		if err := UpdateApprovalState(db, appr.ID, approvalDenied); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		run, err := GetAgentRun(db, appr.AgentRunID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Preserve the run's existing transcript — UpdateAgentRunState
		// overwrites the column, so passing "" here would wipe it.
		if err := UpdateAgentRunState(db, run.ID, agentRunFailed, run.Transcript, "approval denied by operator"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := UpdateApprovalState(db, appr.ID, approvalApproved); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	originalRun, err := GetAgentRun(db, appr.AgentRunID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	originalTask, err := GetTask(db, originalRun.TaskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	session, err := GetAgentSession(db, originalTask.AgentSessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ws, err := GetWorkspaceByID(db, session.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	prompt := fmt.Sprintf("The following action has been approved by the operator: %s %s. Proceed with it now.", appr.ToolName, appr.ToolInput)

	newTask, err := CreateTask(db, session.ID, prompt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	newRun, err := CreateAgentRun(db, newTask.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	startResumedAgentRun(newRun.ID, ws.ID, ws.Path, prompt, originalRun.ClaudeSessionID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(newRun); err != nil {
		log.Printf("failed to encode approval decision response: %v", err)
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
	if msg := agentHookSecretWarning(); msg != "" {
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
		if err := ReconcileAgentRuns(db); err != nil {
			log.Printf("reconciling agent runs: %v", err)
		}
	}

	http.HandleFunc("/healthz", handleHealthz)
	http.Handle("/api/status", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPIStatus)))
	http.Handle("/", withAuth(adminUser, adminPass, http.HandlerFunc(handleDashboard)))
	http.Handle("/workspaces", withAuth(adminUser, adminPass, http.HandlerFunc(handleWorkspaces)))
	http.Handle("/api/workspaces/clone", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPIWorkspacesClone)))
	http.Handle("/api/workspaces/status", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPIWorkspacesStatus)))
	http.Handle("/agents", withAuth(adminUser, adminPass, http.HandlerFunc(handleAgents)))
	http.Handle("/api/agents/start", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPIAgentsStart)))
	http.Handle("/api/agents/status", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPIAgentsStatus)))
	http.Handle("/api/agents/stop", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPIAgentsStop)))
	// Deliberately not wrapped in withAuth: the claude subprocess has no
	// dashboard credentials and authenticates via agentHookSecret instead
	// (see handleAPIAgentsPreToolUseHook).
	http.HandleFunc("/api/agents/hooks/pre-tool-use", handleAPIAgentsPreToolUseHook)
	http.Handle("/api/agents/approvals/decide", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPIAgentsApprovalsDecide)))
	http.Handle("/logs", withAuth(adminUser, adminPass, http.HandlerFunc(handleLogs)))
	http.Handle("/api/logs", withAuth(adminUser, adminPass, http.HandlerFunc(handleAPILogs)))
	http.Handle("/files/", withAuth(adminUser, adminPass, http.StripPrefix("/files/", coverageOnly(http.FileServer(http.Dir(workspaceDir))))))

	addr := ":55123"
	log.Printf("homelab-healthcheck listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("healthcheck server failed: %v", err)
	}
}
