package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestAgentRunsRepo_NilDB(t *testing.T) {
	if _, err := CreateAgentSession(nil, 1); err == nil {
		t.Error("CreateAgentSession(nil, ...) = nil error, want an error")
	}
	if _, err := CreateTask(nil, 1, "prompt"); err == nil {
		t.Error("CreateTask(nil, ...) = nil error, want an error")
	}
	if _, err := CreateAgentRun(nil, 1); err == nil {
		t.Error("CreateAgentRun(nil, ...) = nil error, want an error")
	}
	if err := SetAgentRunClaudeSessionID(nil, 1, "sid"); err == nil {
		t.Error("SetAgentRunClaudeSessionID(nil, ...) = nil error, want an error")
	}
	if err := UpdateAgentRunState(nil, 1, agentRunRunning, "", ""); err == nil {
		t.Error("UpdateAgentRunState(nil, ...) = nil error, want an error")
	}
	if _, err := GetAgentRun(nil, 1); err == nil {
		t.Error("GetAgentRun(nil, ...) = nil error, want an error")
	}
	if _, err := ListAgentRuns(nil); err == nil {
		t.Error("ListAgentRuns(nil) = nil error, want an error")
	}
}

// seedWorkspace creates a real workspace row (FK target for agent_sessions)
// and returns its ID, cleaning up everything it creates (in FK-safe order)
// when the test ends.
func seedWorkspace(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	path := filepath.Join("/root/workspace", name)
	if err := UpsertWorkspace(db, name, path, ""); err != nil {
		t.Fatalf("seedWorkspace: UpsertWorkspace: %v", err)
	}
	w, err := findWorkspaceByPath(db, path)
	if err != nil {
		t.Fatalf("seedWorkspace: findWorkspaceByPath: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM agent_runs WHERE task_id IN (SELECT id FROM tasks WHERE agent_session_id IN (SELECT id FROM agent_sessions WHERE workspace_id = $1))`, w.ID)
		db.Exec(`DELETE FROM tasks WHERE agent_session_id IN (SELECT id FROM agent_sessions WHERE workspace_id = $1)`, w.ID)
		db.Exec(`DELETE FROM agent_sessions WHERE workspace_id = $1`, w.ID)
		db.Exec(`DELETE FROM workspaces WHERE id = $1`, w.ID)
	})
	return w.ID
}

func TestAgentRunsRepo_FullLifecycle(t *testing.T) {
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	workspaceID := seedWorkspace(t, db, "agent-repo-lifecycle")

	session, err := CreateAgentSession(db, workspaceID)
	if err != nil {
		t.Fatalf("CreateAgentSession() = %v, want nil", err)
	}
	if session.WorkspaceID != workspaceID {
		t.Errorf("session.WorkspaceID = %d, want %d", session.WorkspaceID, workspaceID)
	}

	tk, err := CreateTask(db, session.ID, "say hello")
	if err != nil {
		t.Fatalf("CreateTask() = %v, want nil", err)
	}
	if tk.Prompt != "say hello" {
		t.Errorf("task.Prompt = %q, want %q", tk.Prompt, "say hello")
	}

	run, err := CreateAgentRun(db, tk.ID)
	if err != nil {
		t.Fatalf("CreateAgentRun() = %v, want nil", err)
	}
	if run.State != agentRunPending {
		t.Errorf("run.State = %q, want %q", run.State, agentRunPending)
	}
	if run.FinishedAt != nil {
		t.Errorf("new run.FinishedAt = %v, want nil", run.FinishedAt)
	}
	if run.ClaudeSessionID != "" {
		t.Errorf("new run.ClaudeSessionID = %q, want empty", run.ClaudeSessionID)
	}

	if err := SetAgentRunClaudeSessionID(db, run.ID, "claude-sid-123"); err != nil {
		t.Fatalf("SetAgentRunClaudeSessionID() = %v, want nil", err)
	}
	if err := UpdateAgentRunState(db, run.ID, agentRunRunning, "", ""); err != nil {
		t.Fatalf("UpdateAgentRunState(running) = %v, want nil", err)
	}

	mid, err := GetAgentRun(db, run.ID)
	if err != nil {
		t.Fatalf("GetAgentRun() (running) = %v, want nil", err)
	}
	if mid.State != agentRunRunning {
		t.Errorf("mid.State = %q, want %q", mid.State, agentRunRunning)
	}
	if mid.ClaudeSessionID != "claude-sid-123" {
		t.Errorf("mid.ClaudeSessionID = %q, want %q", mid.ClaudeSessionID, "claude-sid-123")
	}
	if mid.FinishedAt != nil {
		t.Errorf("mid (running).FinishedAt = %v, want nil", mid.FinishedAt)
	}

	transcript := `{"type":"system"}` + "\n" + `{"type":"result","result":"banana"}`
	if err := UpdateAgentRunState(db, run.ID, agentRunSucceeded, transcript, ""); err != nil {
		t.Fatalf("UpdateAgentRunState(succeeded) = %v, want nil", err)
	}

	final, err := GetAgentRun(db, run.ID)
	if err != nil {
		t.Fatalf("GetAgentRun() (final) = %v, want nil", err)
	}
	if final.State != agentRunSucceeded {
		t.Errorf("final.State = %q, want %q", final.State, agentRunSucceeded)
	}
	if final.Transcript != transcript {
		t.Errorf("final.Transcript = %q, want %q", final.Transcript, transcript)
	}
	if final.FinishedAt == nil {
		t.Error("final (terminal state).FinishedAt = nil, want non-nil")
	}

	all, err := ListAgentRuns(db)
	if err != nil {
		t.Fatalf("ListAgentRuns() = %v, want nil", err)
	}
	found := false
	for _, r := range all {
		if r.ID == run.ID {
			found = true
			if r.State != agentRunSucceeded {
				t.Errorf("ListAgentRuns() entry state = %q, want %q", r.State, agentRunSucceeded)
			}
		}
	}
	if !found {
		t.Errorf("ListAgentRuns() = %+v, want it to contain run %d", all, run.ID)
	}
}

func TestUpdateAgentRunState_FailedSetsError(t *testing.T) {
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	workspaceID := seedWorkspace(t, db, "agent-repo-failed")

	session, err := CreateAgentSession(db, workspaceID)
	if err != nil {
		t.Fatalf("CreateAgentSession() = %v, want nil", err)
	}
	tk, err := CreateTask(db, session.ID, "do something")
	if err != nil {
		t.Fatalf("CreateTask() = %v, want nil", err)
	}
	run, err := CreateAgentRun(db, tk.ID)
	if err != nil {
		t.Fatalf("CreateAgentRun() = %v, want nil", err)
	}

	if err := UpdateAgentRunState(db, run.ID, agentRunFailed, "", "exec: \"claude\": executable file not found in $PATH"); err != nil {
		t.Fatalf("UpdateAgentRunState(failed) = %v, want nil", err)
	}

	got, err := GetAgentRun(db, run.ID)
	if err != nil {
		t.Fatalf("GetAgentRun() = %v, want nil", err)
	}
	if got.State != agentRunFailed {
		t.Errorf("got.State = %q, want %q", got.State, agentRunFailed)
	}
	if got.Error == "" {
		t.Error("got.Error = \"\", want the failure message")
	}
	if got.FinishedAt == nil {
		t.Error("got.FinishedAt = nil, want non-nil for a terminal state")
	}
}

func TestReconcileAgentRuns_NilDB(t *testing.T) {
	if err := ReconcileAgentRuns(nil); err == nil {
		t.Fatal("ReconcileAgentRuns(nil) = nil error, want an error")
	}
}

// TestReconcileAgentRuns_MarksPendingAndRunningInterrupted seeds one run in
// each state and confirms only pending/running get reset to interrupted —
// a terminal state (succeeded) must be left untouched.
func TestReconcileAgentRuns_MarksPendingAndRunningInterrupted(t *testing.T) {
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	workspaceID := seedWorkspace(t, db, "agent-reconcile")
	session, err := CreateAgentSession(db, workspaceID)
	if err != nil {
		t.Fatalf("CreateAgentSession() = %v, want nil", err)
	}

	newRun := func(state agentRunState) int64 {
		tk, err := CreateTask(db, session.ID, "prompt")
		if err != nil {
			t.Fatalf("CreateTask() = %v, want nil", err)
		}
		run, err := CreateAgentRun(db, tk.ID)
		if err != nil {
			t.Fatalf("CreateAgentRun() = %v, want nil", err)
		}
		if state != agentRunPending {
			if err := UpdateAgentRunState(db, run.ID, state, "", ""); err != nil {
				t.Fatalf("UpdateAgentRunState(%s) = %v, want nil", state, err)
			}
		}
		return run.ID
	}

	pendingID := newRun(agentRunPending)
	runningID := newRun(agentRunRunning)
	succeededID := newRun(agentRunSucceeded)

	if err := ReconcileAgentRuns(db); err != nil {
		t.Fatalf("ReconcileAgentRuns() = %v, want nil", err)
	}

	for _, id := range []int64{pendingID, runningID} {
		got, err := GetAgentRun(db, id)
		if err != nil {
			t.Fatalf("GetAgentRun(%d) = %v, want nil", id, err)
		}
		if got.State != agentRunInterrupted {
			t.Errorf("run %d state = %q, want %q", id, got.State, agentRunInterrupted)
		}
		if got.FinishedAt == nil {
			t.Errorf("run %d FinishedAt = nil, want non-nil after reconciliation", id)
		}
	}

	succeeded, err := GetAgentRun(db, succeededID)
	if err != nil {
		t.Fatalf("GetAgentRun(succeeded) = %v, want nil", err)
	}
	if succeeded.State != agentRunSucceeded {
		t.Errorf("succeeded run state = %q, want unchanged %q", succeeded.State, agentRunSucceeded)
	}
}
