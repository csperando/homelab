package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
	}
	for _, c := range cases {
		if got := formatBytes(c.in); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatKB(t *testing.T) {
	if got, want := formatKB(1024), "1.0 MiB"; got != want {
		t.Errorf("formatKB(1024) = %q, want %q", got, want)
	}
	if got, want := formatKB(0), "0 B"; got != want {
		t.Errorf("formatKB(0) = %q, want %q", got, want)
	}
}

func TestToView(t *testing.T) {
	ts := time.Now().UTC()
	s := statusData{
		Status:       "ok",
		Timestamp:    ts,
		ToolVersions: map[string]string{"go": "go1.22"},
		Workspace:    diskUsage{TotalBytes: 2048, UsedBytes: 1024, FreeBytes: 1024},
		Memory:       memInfo{TotalKB: 2048, AvailKB: 1024},
		LoadAvg:      "0.1 0.2 0.3",
		Repos:        []repoStatus{{Path: "foo", Branch: "main"}},
		Docker:       dockerStatus{Reason: "Disabled"},
	}

	v := toView(s)

	if v.Status != "ok" {
		t.Errorf("Status = %q, want ok", v.Status)
	}
	if v.Timestamp != ts.Format(time.RFC3339) {
		t.Errorf("Timestamp = %q, want %q", v.Timestamp, ts.Format(time.RFC3339))
	}
	if v.ToolVersions["go"] != "go1.22" {
		t.Errorf("ToolVersions[go] = %q, want go1.22", v.ToolVersions["go"])
	}
	if v.DiskUsed != "1.0 KiB" || v.DiskTotal != "2.0 KiB" {
		t.Errorf("DiskUsed/DiskTotal = %q/%q, want 1.0 KiB/2.0 KiB", v.DiskUsed, v.DiskTotal)
	}
	if v.MemAvail != "1.0 MiB" || v.MemTotal != "2.0 MiB" {
		t.Errorf("MemAvail/MemTotal = %q/%q, want 1.0 MiB/2.0 MiB", v.MemAvail, v.MemTotal)
	}
	if v.LoadAvg != "0.1 0.2 0.3" {
		t.Errorf("LoadAvg = %q", v.LoadAvg)
	}
	if len(v.Repos) != 1 || v.Repos[0].Path != "foo" {
		t.Errorf("Repos = %+v", v.Repos)
	}
	if v.Docker.Reason != "Disabled" {
		t.Errorf("Docker.Reason = %q", v.Docker.Reason)
	}
}

func TestIsCoveragePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"coverage/index.html", true},
		{"repo/coverage/lcov-report/index.html", true},
		{"repo/src/index.html", false},
		{"", false},
		{"coverage", true},
	}
	for _, c := range cases {
		if got := isCoveragePath(c.path); got != c.want {
			t.Errorf("isCoveragePath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestIsSafeRepoName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"my-repo", true},
		{"my_repo.go", true},
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{"../etc", false},
		{"foo/../bar", false},
		{"/etc/passwd", false},
		{"a\\b", false},
	}
	for _, c := range cases {
		if got := isSafeRepoName(c.name); got != c.want {
			t.Errorf("isSafeRepoName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCoverageOnly(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Inner-Called", "1")
	})
	h := coverageOnly(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/repo/coverage/index.html", nil)
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Inner-Called") != "1" {
		t.Error("expected inner handler to be called for a coverage path")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/repo/src/index.html", nil)
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Inner-Called") == "1" {
		t.Error("expected inner handler NOT to be called for a non-coverage path")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	handleHealthz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	for _, key := range []string{"status", "uptime_seconds", "timestamp"} {
		if _, ok := body[key]; !ok {
			t.Errorf("missing key %q in response body %v", key, body)
		}
	}
}

// withTempStatusFixtures points every DI'd path var at throwaway locations
// and restores the originals on cleanup, so handlers that call gatherStatus
// run deterministically regardless of the host machine.
func withTempStatusFixtures(t *testing.T) {
	t.Helper()

	origWorkspace, origSocket, origMem, origLoad := workspaceDir, dockerSocketPath, procMeminfoPath, procLoadavgPath
	t.Cleanup(func() {
		workspaceDir, dockerSocketPath, procMeminfoPath, procLoadavgPath = origWorkspace, origSocket, origMem, origLoad
	})

	workspaceDir = t.TempDir()
	dockerSocketPath = workspaceDir + "/no-such-socket"
	procMeminfoPath = workspaceDir + "/no-such-meminfo"
	procLoadavgPath = workspaceDir + "/no-such-loadavg"
}

func TestHandleAPIStatus(t *testing.T) {
	withTempStatusFixtures(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	handleAPIStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var s statusData
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if s.Status != "ok" {
		t.Errorf("Status = %q, want ok", s.Status)
	}
	if s.Docker.Enabled {
		t.Error("expected Docker.Enabled = false with no socket present")
	}
}

func TestHandleDashboard(t *testing.T) {
	withTempStatusFixtures(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handleDashboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"<html", "homelab dev container", "uptime"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard body missing %q", want)
		}
	}
}

func TestHandleWorkspaces_NoToken(t *testing.T) {
	withGithubToken(t, "")
	withDB(t, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	handleWorkspaces(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"<html", "workspaces", "GITHUB_TOKEN not set"} {
		if !strings.Contains(body, want) {
			t.Errorf("workspaces body missing %q", want)
		}
	}
}

func TestHandleWorkspaces_WithGithubRepos(t *testing.T) {
	resetCloneJobs(t)
	withDB(t, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"full_name": "octocat/hello-world",
				"name":      "hello-world",
				"private":   false,
				"clone_url": "https://github.com/octocat/hello-world.git",
			},
		})
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)
	withGithubToken(t, "test-token")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	handleWorkspaces(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"octocat/hello-world", "clone"} {
		if !strings.Contains(body, want) {
			t.Errorf("workspaces body missing %q", want)
		}
	}
}

// TestHandleWorkspaces_PostgresUnreachable confirms the workspaces section
// degrades with a Reason (rather than 500ing the whole page) when db is nil
// — independent of the unrelated GitHub-token failure domain.
func TestHandleWorkspaces_PostgresUnreachable(t *testing.T) {
	withGithubToken(t, "")
	withDB(t, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	handleWorkspaces(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "postgres unavailable") {
		t.Errorf("workspaces body missing the postgres-unavailable reason: %s", rec.Body.String())
	}
}

// TestHandleWorkspaces_ListsPersistedWorkspaces exercises the happy path
// against a real Postgres (skipping if unreachable), confirming a persisted
// workspace's name/branch/dirty are rendered.
func TestHandleWorkspaces_ListsPersistedWorkspaces(t *testing.T) {
	withGithubToken(t, "")
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)

	ws := t.TempDir()
	withWorkspaceDir(t, ws)
	repoDir := filepath.Join(ws, "handler-test-repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, repoDir)
	t.Cleanup(func() { dbConn.Exec(`DELETE FROM workspaces WHERE path = $1`, repoDir) })

	if err := UpsertWorkspace(dbConn, "handler-test-repo", repoDir, "https://example.com/x.git"); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	handleWorkspaces(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"handler-test-repo", "main", `data-workspace-path="` + repoDir + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("workspaces body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `data-workspace-path="`+repoDir+`" checked`) {
		t.Error("workspaces body has the polling checkbox pre-checked, want unchecked (default false)")
	}
}

func TestHandleAPIWorkspacesClone_MethodNotAllowed(t *testing.T) {
	resetCloneJobs(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/clone", nil)
	handleAPIWorkspacesClone(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleAPIWorkspacesClone_InvalidName(t *testing.T) {
	resetCloneJobs(t)
	withWorkspaceDir(t, t.TempDir())

	form := url.Values{"name": {"../escape"}, "clone_url": {"https://example.com/repo.git"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/clone", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIWorkspacesClone(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleAPIWorkspacesClone_SuccessAndStatus(t *testing.T) {
	resetCloneJobs(t)
	ws := t.TempDir()
	withWorkspaceDir(t, ws)
	bareRepo := bareRepoFixture(t)

	form := url.Values{"name": {"cloned-via-api"}, "clone_url": {bareRepo}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/clone", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIWorkspacesClone(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var job cloneJob
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if job.Repo != "cloned-via-api" {
		t.Errorf("job.Repo = %q, want cloned-via-api", job.Repo)
	}

	waitForJobDone(t, "cloned-via-api", 5*time.Second)

	statusRec := httptest.NewRecorder()
	statusReq := httptest.NewRequest(http.MethodGet, "/api/workspaces/status", nil)
	handleAPIWorkspacesStatus(statusRec, statusReq)

	if statusRec.Code != http.StatusOK {
		t.Fatalf("status endpoint status = %d, want 200", statusRec.Code)
	}
	var jobs []cloneJob
	if err := json.Unmarshal(statusRec.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Repo != "cloned-via-api" || jobs[0].State != cloneStateSucceeded {
		t.Errorf("jobs = %+v, want one succeeded job for cloned-via-api", jobs)
	}
}

func TestHandleAPIWorkspacesPolling_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/polling", nil)
	handleAPIWorkspacesPolling(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleAPIWorkspacesPolling_UnknownWorkspace(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)

	form := url.Values{"workspace": {"/no/such/path"}, "enabled": {"true"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/polling", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIWorkspacesPolling(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleAPIWorkspacesPolling_TogglesOnAndOff(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)

	const path = "/root/workspace/handler-polling-toggle"
	t.Cleanup(func() { dbConn.Exec(`DELETE FROM workspaces WHERE path = $1`, path) })
	if err := UpsertWorkspace(dbConn, "handler-polling-toggle", path, ""); err != nil {
		t.Fatalf("UpsertWorkspace() = %v, want nil", err)
	}

	form := url.Values{"workspace": {path}, "enabled": {"true"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/polling", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIWorkspacesPolling(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	got, err := GetWorkspaceByPath(dbConn, path)
	if err != nil {
		t.Fatalf("GetWorkspaceByPath() = %v, want nil", err)
	}
	if !got.GitHubIssuePollingEnabled {
		t.Error("GitHubIssuePollingEnabled = false after enabling via the handler, want true")
	}

	form = url.Values{"workspace": {path}, "enabled": {"false"}}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/workspaces/polling", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIWorkspacesPolling(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	got, err = GetWorkspaceByPath(dbConn, path)
	if err != nil {
		t.Fatalf("GetWorkspaceByPath() = %v, want nil", err)
	}
	if got.GitHubIssuePollingEnabled {
		t.Error("GitHubIssuePollingEnabled = true after disabling via the handler, want false")
	}
}

func TestHandleAgents_PostgresUnreachable(t *testing.T) {
	withDB(t, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	handleAgents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"<html", "agents"} {
		if !strings.Contains(body, want) {
			t.Errorf("agents body missing %q", want)
		}
	}
	if strings.Count(body, "postgres unavailable") < 2 {
		t.Errorf("agents body wants two independent \"postgres unavailable\" reasons (workspaces + runs), got: %s", body)
	}
}

func TestHandleAgents_ListsWorkspacesAndRuns(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)

	ws := t.TempDir()
	if err := UpsertWorkspace(dbConn, "agents-page-workspace", ws, ""); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	t.Cleanup(func() { dbConn.Exec(`DELETE FROM workspaces WHERE path = $1`, ws) })

	runID := seedAgentRun(t, dbConn, "agents-page-run")
	if err := UpdateAgentRunState(dbConn, runID, agentRunSucceeded, `{"type":"result"}`, ""); err != nil {
		t.Fatalf("UpdateAgentRunState: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	handleAgents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"agents-page-workspace", "run " + strconv.FormatInt(runID, 10), "succeeded"} {
		if !strings.Contains(body, want) {
			t.Errorf("agents body missing %q:\n%s", want, body)
		}
	}
}

func TestHandleAgents_ShowsPendingApproval(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)

	runID := seedAgentRun(t, dbConn, "agents-page-pending-approval")
	if err := UpdateAgentRunState(dbConn, runID, agentRunAwaitingApproval, `{"type":"system"}`, ""); err != nil {
		t.Fatalf("UpdateAgentRunState() = %v, want nil", err)
	}
	if _, err := CreateApproval(dbConn, runID, "Bash", `{"command":"git push origin main"}`, "gated command: git push origin main"); err != nil {
		t.Fatalf("CreateApproval() = %v, want nil", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	handleAgents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"gated command: git push origin main", "git push origin main", "approve", "deny", "data-approval-id"} {
		if !strings.Contains(body, want) {
			t.Errorf("agents body missing %q:\n%s", want, body)
		}
	}
}

// waitForAgentRunDone polls GetAgentRun until it reaches a terminal state
// or timeout elapses, for tests that need a background agent run to finish.
func waitForAgentRunDone(t *testing.T, db *sql.DB, runID int64, timeout time.Duration) agentRun {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		run, err := GetAgentRun(db, runID)
		if err == nil && isTerminalAgentRunState(run.State) {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent run %d did not finish within %s", runID, timeout)
	return agentRun{}
}

func TestHandleAPIAgentsStart_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/start", nil)
	handleAPIAgentsStart(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleAPIAgentsStart_MissingPrompt(t *testing.T) {
	form := url.Values{"workspace": {"/root/workspace/whatever"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIAgentsStart(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleAPIAgentsStart_UnknownWorkspace(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)

	form := url.Values{"workspace": {"/no/such/workspace"}, "prompt": {"hi"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIAgentsStart(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleAPIAgentsStart_DirectoryMissing(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)

	// A path that's a valid workspace row but doesn't exist on disk.
	path := filepath.Join(t.TempDir(), "does-not-exist-on-disk")
	if err := UpsertWorkspace(dbConn, "gone", path, ""); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	t.Cleanup(func() { dbConn.Exec(`DELETE FROM workspaces WHERE path = $1`, path) })

	form := url.Values{"workspace": {path}, "prompt": {"hi"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIAgentsStart(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleAPIAgentsStart_AlreadyInProgress(t *testing.T) {
	resetLiveRuns(t)
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)

	ws := t.TempDir()
	if err := UpsertWorkspace(dbConn, "busy", ws, ""); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	t.Cleanup(func() { dbConn.Exec(`DELETE FROM workspaces WHERE path = $1`, ws) })
	seeded, err := GetWorkspaceByPath(dbConn, ws)
	if err != nil {
		t.Fatalf("GetWorkspaceByPath: %v", err)
	}

	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting fake in-progress process: %v", err)
	}
	defer cmd.Wait()
	registerLiveRun(1, seeded.ID, "sess-1", cmd)

	form := url.Values{"workspace": {ws}, "prompt": {"hi"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIAgentsStart(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestHandleAPIAgentsStart_Success(t *testing.T) {
	resetLiveRuns(t)
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	withAgentRunner(t, `printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"sess-xyz"}'`)

	ws := t.TempDir()
	if err := UpsertWorkspace(dbConn, "start-success", ws, ""); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	t.Cleanup(func() { dbConn.Exec(`DELETE FROM workspaces WHERE path = $1`, ws) })

	form := url.Values{"workspace": {ws}, "prompt": {"say hi"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIAgentsStart(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var run agentRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if run.State != agentRunPending {
		t.Errorf("run.State = %q, want %q", run.State, agentRunPending)
	}

	done := waitForAgentRunDone(t, dbConn, run.ID, 5*time.Second)
	if done.State != agentRunSucceeded {
		t.Errorf("done.State = %q, want %q (error: %s)", done.State, agentRunSucceeded, done.Error)
	}
}

func TestHandleAPIAgentsStatus_MissingRunParam(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/status", nil)
	handleAPIAgentsStatus(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleAPIAgentsStatus_UnknownRun(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/status?run=999999999", nil)
	handleAPIAgentsStatus(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleAPIAgentsStatus_PersistedOnly(t *testing.T) {
	resetLiveRuns(t)
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "agent-status-persisted")

	if err := UpdateAgentRunState(dbConn, runID, agentRunSucceeded, `{"type":"result"}`, ""); err != nil {
		t.Fatalf("UpdateAgentRunState: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/status?run="+strconv.FormatInt(runID, 10), nil)
	handleAPIAgentsStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var run agentRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if run.State != agentRunSucceeded {
		t.Errorf("run.State = %q, want %q", run.State, agentRunSucceeded)
	}
	if run.Transcript != `{"type":"result"}` {
		t.Errorf("run.Transcript = %q, want the persisted transcript", run.Transcript)
	}
}

func TestHandleAPIAgentsStatus_LiveOverridesPersisted(t *testing.T) {
	resetLiveRuns(t)
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "agent-status-live")

	// Row still says "pending" in Postgres (the narrow race window
	// documented on handleAPIAgentsStatus), but it's tracked live.
	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting fake process: %v", err)
	}
	defer cmd.Wait()
	registerLiveRun(runID, 1, "sess-live", cmd)
	appendLiveRunLine(runID, `{"type":"system"}`)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/status?run="+strconv.FormatInt(runID, 10), nil)
	handleAPIAgentsStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var run agentRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if run.State != agentRunRunning {
		t.Errorf("run.State = %q, want %q (live-tracked overrides the persisted \"pending\")", run.State, agentRunRunning)
	}
	if run.Transcript != `{"type":"system"}` {
		t.Errorf("run.Transcript = %q, want the live buffered transcript", run.Transcript)
	}
}

func TestHandleAPIAgentsStatus_IncludesPendingApproval(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "agent-status-pending-approval")
	if err := UpdateAgentRunState(dbConn, runID, agentRunAwaitingApproval, "", ""); err != nil {
		t.Fatalf("UpdateAgentRunState() = %v, want nil", err)
	}
	a, err := CreateApproval(dbConn, runID, "Bash", `{"command":"rm -rf /tmp/x"}`, "gated command: rm -rf")
	if err != nil {
		t.Fatalf("CreateApproval() = %v, want nil", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/status?run="+strconv.FormatInt(runID, 10), nil)
	handleAPIAgentsStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		State           agentRunState `json:"state"`
		PendingApproval *approval     `json:"pending_approval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if resp.PendingApproval == nil {
		t.Fatal("pending_approval = nil, want the approval just created")
	}
	if resp.PendingApproval.ID != a.ID {
		t.Errorf("pending_approval.ID = %d, want %d", resp.PendingApproval.ID, a.ID)
	}
}

func TestHandleAPIAgentsStop_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/stop", nil)
	handleAPIAgentsStop(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleAPIAgentsStop_MissingRunField(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/stop", strings.NewReader(url.Values{}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIAgentsStop(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleAPIAgentsStop_NotInProgress(t *testing.T) {
	resetLiveRuns(t)
	form := url.Values{"run": {"123456789"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/stop", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIAgentsStop(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleAPIAgentsStop_Success(t *testing.T) {
	resetLiveRuns(t)
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting test process: %v", err)
	}
	defer cmd.Wait()
	registerLiveRun(42, 1, "sess-42", cmd)

	form := url.Values{"run": {"42"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/stop", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAPIAgentsStop(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if !wasStopRequested(42) {
		t.Error("wasStopRequested(42) = false, want true after a successful stop request")
	}
}

func TestHandleAPIAgentsPreToolUseHook_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/hooks/pre-tool-use", nil)
	handleAPIAgentsPreToolUseHook(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleAPIAgentsPreToolUseHook_SecretUnset(t *testing.T) {
	withAgentHookSecret(t, "")

	body := strings.NewReader(`{"session_id":"sess-x","tool_name":"Bash","tool_input":{"command":"git push"}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/hooks/pre-tool-use", body)
	req.Header.Set("Authorization", "Bearer ")
	handleAPIAgentsPreToolUseHook(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d (an unset secret must never authenticate)", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleAPIAgentsPreToolUseHook_WrongSecret(t *testing.T) {
	withAgentHookSecret(t, "the-real-secret")

	body := strings.NewReader(`{"session_id":"sess-x","tool_name":"Bash","tool_input":{"command":"git push"}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/hooks/pre-tool-use", body)
	req.Header.Set("Authorization", "Bearer wrong-secret")
	handleAPIAgentsPreToolUseHook(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleAPIAgentsPreToolUseHook_NonGatedCommand_Allows(t *testing.T) {
	withAgentHookSecret(t, "the-real-secret")

	body := strings.NewReader(`{"session_id":"sess-x","tool_name":"Bash","tool_input":{"command":"ls -la"}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/hooks/pre-tool-use", body)
	req.Header.Set("Authorization", "Bearer the-real-secret")
	handleAPIAgentsPreToolUseHook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"permissionDecision":"allow"`) {
		t.Errorf("body = %s, want an explicit allow decision", rec.Body.String())
	}
}

func TestHandleAPIAgentsPreToolUseHook_NonBashTool_Allows(t *testing.T) {
	withAgentHookSecret(t, "the-real-secret")

	// Even a command-shaped string is irrelevant for a non-Bash tool.
	body := strings.NewReader(`{"session_id":"sess-x","tool_name":"Read","tool_input":{"command":"git push"}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/hooks/pre-tool-use", body)
	req.Header.Set("Authorization", "Bearer the-real-secret")
	handleAPIAgentsPreToolUseHook(rec, req)

	if !strings.Contains(rec.Body.String(), `"permissionDecision":"allow"`) {
		t.Errorf("body = %s, want an explicit allow decision for a non-Bash tool", rec.Body.String())
	}
}

func TestHandleAPIAgentsPreToolUseHook_GatedCommand_DeniesAndRecordsApproval(t *testing.T) {
	resetLiveRuns(t)
	withAgentHookSecret(t, "the-real-secret")
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "hook-gated-command")

	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting test process: %v", err)
	}
	defer cmd.Wait()
	registerLiveRun(runID, 1, "sess-gated", cmd)

	body := strings.NewReader(`{"session_id":"sess-gated","tool_name":"Bash","tool_input":{"command":"git push origin main"}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/hooks/pre-tool-use", body)
	req.Header.Set("Authorization", "Bearer the-real-secret")
	handleAPIAgentsPreToolUseHook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"permissionDecision":"deny"`) {
		t.Errorf("body = %s, want an explicit deny decision", rec.Body.String())
	}

	approvals, err := ListApprovalsForRun(dbConn, runID)
	if err != nil {
		t.Fatalf("ListApprovalsForRun() = %v, want nil", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("ListApprovalsForRun() = %+v, want exactly one pending approval", approvals)
	}
	if approvals[0].State != approvalPending {
		t.Errorf("approvals[0].State = %q, want %q", approvals[0].State, approvalPending)
	}
	if approvals[0].ToolName != "Bash" || !strings.Contains(approvals[0].ToolInput, "git push origin main") {
		t.Errorf("approvals[0] = %+v, want ToolName=Bash and ToolInput containing the command", approvals[0])
	}
}

func TestHandleAPIAgentsPreToolUseHook_UnknownSession_DeniesWithoutApproval(t *testing.T) {
	resetLiveRuns(t)
	withAgentHookSecret(t, "the-real-secret")

	body := strings.NewReader(`{"session_id":"sess-does-not-exist","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/hooks/pre-tool-use", body)
	req.Header.Set("Authorization", "Bearer the-real-secret")
	handleAPIAgentsPreToolUseHook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"permissionDecision":"deny"`) {
		t.Errorf("body = %s, want an explicit deny decision (fail closed) even with an unresolvable session", rec.Body.String())
	}
}

func decideRequest(approvalID int64, decision string) *http.Request {
	form := url.Values{"approval": {strconv.FormatInt(approvalID, 10)}, "decision": {decision}}
	req := httptest.NewRequest(http.MethodPost, "/api/agents/approvals/decide", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestHandleAPIAgentsApprovalsDecide_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/approvals/decide", nil)
	handleAPIAgentsApprovalsDecide(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleAPIAgentsApprovalsDecide_InvalidDecision(t *testing.T) {
	rec := httptest.NewRecorder()
	req := decideRequest(1, "maybe")
	handleAPIAgentsApprovalsDecide(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleAPIAgentsApprovalsDecide_UnknownApproval(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)

	rec := httptest.NewRecorder()
	req := decideRequest(999999999, "deny")
	handleAPIAgentsApprovalsDecide(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleAPIAgentsApprovalsDecide_AlreadyDecided(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "decide-already-decided")
	a, err := CreateApproval(dbConn, runID, "Bash", `{"command":"git push"}`, "gated command: git push")
	if err != nil {
		t.Fatalf("CreateApproval() = %v, want nil", err)
	}
	if err := UpdateApprovalState(dbConn, a.ID, approvalDenied); err != nil {
		t.Fatalf("UpdateApprovalState() = %v, want nil", err)
	}

	rec := httptest.NewRecorder()
	req := decideRequest(a.ID, "approve")
	handleAPIAgentsApprovalsDecide(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestHandleAPIAgentsApprovalsDecide_Deny(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "decide-deny")

	// Give the run a real pre-existing transcript, to confirm denying
	// doesn't wipe it.
	const existingTranscript = `{"type":"system"}`
	if err := UpdateAgentRunState(dbConn, runID, agentRunAwaitingApproval, existingTranscript, ""); err != nil {
		t.Fatalf("UpdateAgentRunState() = %v, want nil", err)
	}
	a, err := CreateApproval(dbConn, runID, "Bash", `{"command":"git push origin main"}`, "gated command: git push origin main")
	if err != nil {
		t.Fatalf("CreateApproval() = %v, want nil", err)
	}

	rec := httptest.NewRecorder()
	req := decideRequest(a.ID, "deny")
	handleAPIAgentsApprovalsDecide(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	gotApproval, err := GetApproval(dbConn, a.ID)
	if err != nil {
		t.Fatalf("GetApproval() = %v, want nil", err)
	}
	if gotApproval.State != approvalDenied || gotApproval.DecidedAt == nil {
		t.Errorf("gotApproval = %+v, want State=denied and a non-nil DecidedAt", gotApproval)
	}

	gotRun, err := GetAgentRun(dbConn, runID)
	if err != nil {
		t.Fatalf("GetAgentRun() = %v, want nil", err)
	}
	if gotRun.State != agentRunFailed {
		t.Errorf("gotRun.State = %q, want %q", gotRun.State, agentRunFailed)
	}
	if gotRun.Error != "approval denied by operator" {
		t.Errorf("gotRun.Error = %q, want %q", gotRun.Error, "approval denied by operator")
	}
	if gotRun.Transcript != existingTranscript {
		t.Errorf("gotRun.Transcript = %q, want it preserved as %q (not wiped)", gotRun.Transcript, existingTranscript)
	}
}

func TestHandleAPIAgentsApprovalsDecide_Approve(t *testing.T) {
	resetLiveRuns(t)
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	withAgentRunner(t, `printf '%s\n' '{"type":"result","subtype":"success","is_error":false}'`)

	runID := seedAgentRun(t, dbConn, "decide-approve")
	const originalSessionID = "original-claude-session-id"
	if err := SetAgentRunClaudeSessionID(dbConn, runID, originalSessionID); err != nil {
		t.Fatalf("SetAgentRunClaudeSessionID() = %v, want nil", err)
	}
	a, err := CreateApproval(dbConn, runID, "Bash", `{"command":"git push origin main"}`, "gated command: git push origin main")
	if err != nil {
		t.Fatalf("CreateApproval() = %v, want nil", err)
	}

	rec := httptest.NewRecorder()
	req := decideRequest(a.ID, "approve")
	handleAPIAgentsApprovalsDecide(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var newRun agentRun
	if err := json.Unmarshal(rec.Body.Bytes(), &newRun); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if newRun.ID == runID {
		t.Error("approve reused the original run's ID, want a genuinely new Agent Run")
	}

	gotApproval, err := GetApproval(dbConn, a.ID)
	if err != nil {
		t.Fatalf("GetApproval() = %v, want nil", err)
	}
	if gotApproval.State != approvalApproved {
		t.Errorf("gotApproval.State = %q, want %q", gotApproval.State, approvalApproved)
	}

	done := waitForAgentRunDone(t, dbConn, newRun.ID, 5*time.Second)
	if done.State != agentRunSucceeded {
		t.Errorf("done.State = %q, want %q (error: %s)", done.State, agentRunSucceeded, done.Error)
	}
	if done.ClaudeSessionID != originalSessionID {
		t.Errorf("done.ClaudeSessionID = %q, want %q (the resumed run must reuse the original session)", done.ClaudeSessionID, originalSessionID)
	}
}

// TestHandleAPIAgentsApprovalsDecide_Approve_CarriesGitHubIssueNumber
// confirms a resumed run's new task copies the original task's
// github_issue_number forward — without this, a Job-created run that pauses
// for approval would lose track of which issue to comment on once it later
// reaches a terminal state.
func TestHandleAPIAgentsApprovalsDecide_Approve_CarriesGitHubIssueNumber(t *testing.T) {
	resetLiveRuns(t)
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	withAgentRunner(t, `printf '%s\n' '{"type":"result","subtype":"success","is_error":false}'`)

	workspaceID := seedWorkspace(t, dbConn, "decide-approve-issue-number")
	session, err := CreateAgentSession(dbConn, workspaceID)
	if err != nil {
		t.Fatalf("CreateAgentSession() = %v, want nil", err)
	}
	issueNumber := 99
	tk, err := CreateTask(dbConn, session.ID, "investigate issue #99", &issueNumber)
	if err != nil {
		t.Fatalf("CreateTask() = %v, want nil", err)
	}
	run, err := CreateAgentRun(dbConn, tk.ID)
	if err != nil {
		t.Fatalf("CreateAgentRun() = %v, want nil", err)
	}

	a, err := CreateApproval(dbConn, run.ID, "Bash", `{"command":"git push origin main"}`, "gated command: git push origin main")
	if err != nil {
		t.Fatalf("CreateApproval() = %v, want nil", err)
	}

	rec := httptest.NewRecorder()
	req := decideRequest(a.ID, "approve")
	handleAPIAgentsApprovalsDecide(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var newRun agentRun
	if err := json.Unmarshal(rec.Body.Bytes(), &newRun); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}

	waitForAgentRunDone(t, dbConn, newRun.ID, 5*time.Second)

	newTask, err := GetTask(dbConn, newRun.TaskID)
	if err != nil {
		t.Fatalf("GetTask() = %v, want nil", err)
	}
	if newTask.GitHubIssueNumber == nil || *newTask.GitHubIssueNumber != 99 {
		t.Errorf("newTask.GitHubIssueNumber = %v, want pointer to 99 (carried from the original task)", newTask.GitHubIssueNumber)
	}
}
