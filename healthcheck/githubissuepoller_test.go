package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"
)

// waitForIssueRunDone waits for the agent run belonging to the task created
// for workspaceID/issueNumber to reach a terminal state — startGitHubIssueRun
// starts the run asynchronously (see startAgentRun/runAgent), so tests that
// exercise it must synchronize before the test's own db connection tears
// down, or runAgent's completion goroutine races against it (mirrors
// waitForAgentRunDone's role in the approve-flow tests).
func waitForIssueRunDone(t *testing.T, db *sql.DB, workspaceID int64, issueNumber int, timeout time.Duration) agentRun {
	t.Helper()
	var runID int64
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := db.QueryRow(`
			SELECT agent_runs.id FROM agent_runs
			JOIN tasks ON tasks.id = agent_runs.task_id
			JOIN agent_sessions ON agent_sessions.id = tasks.agent_session_id
			WHERE agent_sessions.workspace_id = $1 AND tasks.github_issue_number = $2
		`, workspaceID, issueNumber).Scan(&runID)
		if err == nil {
			return waitForAgentRunDone(t, db, runID, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no agent run found for workspace %d issue #%d within %s", workspaceID, issueNumber, timeout)
	return agentRun{}
}

func TestParseGitHubIssuePollInterval(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"300", 300},
		{"5", 5},
		{"", 300},
		{"not-a-number", 300},
		{"0", 300},
		{"-5", 300},
	}
	for _, c := range cases {
		got := parseGitHubIssuePollInterval(c.in)
		if got.Seconds() != float64(c.want) {
			t.Errorf("parseGitHubIssuePollInterval(%q) = %v, want %ds", c.in, got, c.want)
		}
	}
}

// seedIssuePollingWorkspace creates a real git repo fixture with an origin
// remote pointing at owner/repo on github.com, persists it as a workspace
// with polling enabled, and returns its ID.
func seedIssuePollingWorkspace(t *testing.T, db *sql.DB, name, owner, repo string) workspace {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	setOriginRemote(t, dir, "https://github.com/"+owner+"/"+repo+".git")

	if err := UpsertWorkspace(db, name, dir, ""); err != nil {
		t.Fatalf("UpsertWorkspace() = %v, want nil", err)
	}
	ws, err := GetWorkspaceByPath(db, dir)
	if err != nil {
		t.Fatalf("GetWorkspaceByPath() = %v, want nil", err)
	}
	t.Cleanup(func() { cleanupWorkspaceCascade(db, ws.ID) })
	if err := SetWorkspaceGitHubIssuePolling(db, ws.ID, true); err != nil {
		t.Fatalf("SetWorkspaceGitHubIssuePolling() = %v, want nil", err)
	}
	ws.GitHubIssuePollingEnabled = true
	return ws
}

func TestPollGitHubIssuesOnce_SkipsDisabledWorkspaces(t *testing.T) {
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)
	withGithubToken(t, "test-token")

	dir := t.TempDir()
	initGitRepo(t, dir)
	setOriginRemote(t, dir, "https://github.com/testowner/testrepo.git")
	if err := UpsertWorkspace(db, "poll-disabled", dir, ""); err != nil {
		t.Fatalf("UpsertWorkspace() = %v, want nil", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM workspaces WHERE path = $1`, dir) })

	pollGitHubIssuesOnce(db)

	if called {
		t.Error("pollGitHubIssuesOnce() hit the GitHub API for a workspace with polling disabled")
	}
}

func TestPollWorkspaceIssuesOnce_SkipsWhenRunInProgress(t *testing.T) {
	resetLiveRuns(t)
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)
	withGithubToken(t, "test-token")

	ws := seedIssuePollingWorkspace(t, db, "poll-run-in-progress", "testowner", "testrepo")

	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting test process: %v", err)
	}
	defer cmd.Wait()
	registerLiveRun(999, ws.ID, "sess-999", cmd)

	pollWorkspaceIssuesOnce(db, ws)

	if called {
		t.Error("pollWorkspaceIssuesOnce() hit the GitHub API while a run was already in progress")
	}
}

func TestPollWorkspaceIssuesOnce_CreatesRunForNewIssue(t *testing.T) {
	resetLiveRuns(t)
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, db)
	withAgentRunner(t, `printf '%s\n' '{"type":"result","subtype":"success","is_error":false}'`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"number": 5, "title": "fix the thing", "body": "it's broken", "pull_request": nil},
		})
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)
	withGithubToken(t, "test-token")

	ws := seedIssuePollingWorkspace(t, db, "poll-new-issue", "testowner", "testrepo")

	pollWorkspaceIssuesOnce(db, ws)

	has, err := githubIssueHasTask(db, ws.ID, 5)
	if err != nil {
		t.Fatalf("githubIssueHasTask() = %v, want nil", err)
	}
	if !has {
		t.Error("pollWorkspaceIssuesOnce() did not create a task for the new issue")
	}
	waitForIssueRunDone(t, db, ws.ID, 5, 5*time.Second)
}

func TestPollWorkspaceIssuesOnce_SkipsIssueWithExistingTask(t *testing.T) {
	resetLiveRuns(t)
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, db)
	withAgentRunner(t, `printf '%s\n' '{"type":"result","subtype":"success","is_error":false}'`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"number": 1, "title": "already handled", "body": "", "pull_request": nil},
			{"number": 2, "title": "brand new", "body": "", "pull_request": nil},
		})
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)
	withGithubToken(t, "test-token")

	ws := seedIssuePollingWorkspace(t, db, "poll-existing-task", "testowner", "testrepo")

	session, err := CreateAgentSession(db, ws.ID)
	if err != nil {
		t.Fatalf("CreateAgentSession() = %v, want nil", err)
	}
	existingIssue := 1
	if _, err := CreateTask(db, session.ID, "already handled", &existingIssue); err != nil {
		t.Fatalf("CreateTask() = %v, want nil", err)
	}

	pollWorkspaceIssuesOnce(db, ws)

	has, err := githubIssueHasTask(db, ws.ID, 2)
	if err != nil {
		t.Fatalf("githubIssueHasTask() = %v, want nil", err)
	}
	if !has {
		t.Error("pollWorkspaceIssuesOnce() did not create a task for the new issue #2")
	}
	waitForIssueRunDone(t, db, ws.ID, 2, 5*time.Second)
}

func TestPollWorkspaceIssuesOnce_StopsAfterFirstNewIssue(t *testing.T) {
	resetLiveRuns(t)
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, db)
	withAgentRunner(t, `printf '%s\n' '{"type":"result","subtype":"success","is_error":false}'`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"number": 10, "title": "first", "body": "", "pull_request": nil},
			{"number": 11, "title": "second", "body": "", "pull_request": nil},
		})
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)
	withGithubToken(t, "test-token")

	ws := seedIssuePollingWorkspace(t, db, "poll-stops-after-first", "testowner", "testrepo")

	pollWorkspaceIssuesOnce(db, ws)

	has10, err := githubIssueHasTask(db, ws.ID, 10)
	if err != nil {
		t.Fatalf("githubIssueHasTask(10) = %v, want nil", err)
	}
	has11, err := githubIssueHasTask(db, ws.ID, 11)
	if err != nil {
		t.Fatalf("githubIssueHasTask(11) = %v, want nil", err)
	}
	if !has10 {
		t.Error("expected a task for issue #10 (the first eligible issue)")
	}
	if has11 {
		t.Error("expected no task for issue #11 — only one new run should start per poll cycle")
	}
	waitForIssueRunDone(t, db, ws.ID, 10, 5*time.Second)
}

func TestPollWorkspaceIssuesOnce_BadRemoteSkipsWithoutCrashing(t *testing.T) {
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)
	withGithubToken(t, "test-token")

	dir := t.TempDir()
	initGitRepo(t, dir)
	// No origin remote set at all.
	if err := UpsertWorkspace(db, "poll-bad-remote", dir, ""); err != nil {
		t.Fatalf("UpsertWorkspace() = %v, want nil", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM workspaces WHERE path = $1`, dir) })
	ws, err := GetWorkspaceByPath(db, dir)
	if err != nil {
		t.Fatalf("GetWorkspaceByPath() = %v, want nil", err)
	}
	if err := SetWorkspaceGitHubIssuePolling(db, ws.ID, true); err != nil {
		t.Fatalf("SetWorkspaceGitHubIssuePolling() = %v, want nil", err)
	}
	ws.GitHubIssuePollingEnabled = true

	pollWorkspaceIssuesOnce(db, ws)

	if called {
		t.Error("pollWorkspaceIssuesOnce() hit the GitHub API despite having no resolvable remote")
	}
}
