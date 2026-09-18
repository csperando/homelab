package main

import (
	"database/sql"
	"fmt"
	"time"
)

// agentSession groups the tasks/runs of one continuous unit of work against
// a workspace. Each task defaults to a fresh session for now (see
// CreateAgentSession's caller) — nothing resumes an existing session yet.
type agentSession struct {
	ID          int64     `json:"id"`
	WorkspaceID int64     `json:"workspace_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// task is a single free-text prompt submitted within an agent session.
type task struct {
	ID             int64     `json:"id"`
	AgentSessionID int64     `json:"agent_session_id"`
	Prompt         string    `json:"prompt"`
	CreatedAt      time.Time `json:"created_at"`
}

type agentRunState string

const (
	agentRunPending     agentRunState = "pending"
	agentRunRunning     agentRunState = "running"
	agentRunSucceeded   agentRunState = "succeeded"
	agentRunFailed      agentRunState = "failed"
	agentRunStopped     agentRunState = "stopped"
	agentRunInterrupted agentRunState = "interrupted"
)

// agentRun is one `claude` subprocess invocation for a task. Transcript is
// only populated once the run finishes (see the in-memory live-run store
// for the still-running transcript) — never written to incrementally.
type agentRun struct {
	ID              int64         `json:"id"`
	TaskID          int64         `json:"task_id"`
	ClaudeSessionID string        `json:"claude_session_id,omitempty"`
	State           agentRunState `json:"state"`
	Transcript      string        `json:"transcript,omitempty"`
	Error           string        `json:"error,omitempty"`
	StartedAt       time.Time     `json:"started_at"`
	FinishedAt      *time.Time    `json:"finished_at,omitempty"`
}

func CreateAgentSession(db *sql.DB, workspaceID int64) (agentSession, error) {
	if db == nil {
		return agentSession{}, fmt.Errorf("postgres unavailable")
	}
	var s agentSession
	err := db.QueryRow(`
		INSERT INTO agent_sessions (workspace_id) VALUES ($1)
		RETURNING id, workspace_id, created_at
	`, workspaceID).Scan(&s.ID, &s.WorkspaceID, &s.CreatedAt)
	if err != nil {
		return agentSession{}, fmt.Errorf("creating agent session: %w", err)
	}
	return s, nil
}

func CreateTask(db *sql.DB, agentSessionID int64, prompt string) (task, error) {
	if db == nil {
		return task{}, fmt.Errorf("postgres unavailable")
	}
	var t task
	err := db.QueryRow(`
		INSERT INTO tasks (agent_session_id, prompt) VALUES ($1, $2)
		RETURNING id, agent_session_id, prompt, created_at
	`, agentSessionID, prompt).Scan(&t.ID, &t.AgentSessionID, &t.Prompt, &t.CreatedAt)
	if err != nil {
		return task{}, fmt.Errorf("creating task: %w", err)
	}
	return t, nil
}

const agentRunColumns = `id, task_id, claude_session_id, state, transcript, error, started_at, finished_at`

// scanner is satisfied by both *sql.Row and *sql.Rows, letting
// scanAgentRun back every agent-run query in this file.
type scanner interface {
	Scan(dest ...any) error
}

// scanAgentRun scans a row selected with agentRunColumns, translating the
// nullable TEXT/TIMESTAMPTZ columns into agentRun's plain string/*time.Time
// fields.
func scanAgentRun(s scanner) (agentRun, error) {
	var r agentRun
	var claudeSessionID, transcript, errStr sql.NullString
	var finishedAt sql.NullTime

	err := s.Scan(&r.ID, &r.TaskID, &claudeSessionID, &r.State, &transcript, &errStr, &r.StartedAt, &finishedAt)
	if err != nil {
		return agentRun{}, err
	}

	r.ClaudeSessionID = claudeSessionID.String
	r.Transcript = transcript.String
	r.Error = errStr.String
	if finishedAt.Valid {
		t := finishedAt.Time
		r.FinishedAt = &t
	}
	return r, nil
}

// CreateAgentRun inserts a new run in the "pending" state; the caller
// transitions it via UpdateAgentRunState as the subprocess progresses.
func CreateAgentRun(db *sql.DB, taskID int64) (agentRun, error) {
	if db == nil {
		return agentRun{}, fmt.Errorf("postgres unavailable")
	}
	row := db.QueryRow(`
		INSERT INTO agent_runs (task_id, state) VALUES ($1, $2)
		RETURNING `+agentRunColumns, taskID, agentRunPending)
	r, err := scanAgentRun(row)
	if err != nil {
		return agentRun{}, fmt.Errorf("creating agent run: %w", err)
	}
	return r, nil
}

// SetAgentRunClaudeSessionID records the claude session ID once it's known
// (read from the subprocess's JSONL stream) — a small, targeted update
// separate from UpdateAgentRunState's state-transition responsibility.
func SetAgentRunClaudeSessionID(db *sql.DB, runID int64, claudeSessionID string) error {
	if db == nil {
		return fmt.Errorf("postgres unavailable")
	}
	_, err := db.Exec(`UPDATE agent_runs SET claude_session_id = $1 WHERE id = $2`, claudeSessionID, runID)
	if err != nil {
		return fmt.Errorf("setting claude session id for run %d: %w", runID, err)
	}
	return nil
}

// UpdateAgentRunState transitions a run's state, setting finished_at
// automatically for terminal states (mirrors clone.go's setJobState).
// transcript/errMsg are only meaningful once the run reaches a terminal
// state; pass "" for either while merely transitioning to "running".
func UpdateAgentRunState(db *sql.DB, runID int64, state agentRunState, transcript, errMsg string) error {
	if db == nil {
		return fmt.Errorf("postgres unavailable")
	}

	var finishedAt any
	if isTerminalAgentRunState(state) {
		finishedAt = time.Now().UTC()
	}

	_, err := db.Exec(`
		UPDATE agent_runs SET state = $1, transcript = $2, error = $3, finished_at = $4
		WHERE id = $5
	`, state, nullIfEmpty(transcript), nullIfEmpty(errMsg), finishedAt, runID)
	if err != nil {
		return fmt.Errorf("updating agent run %d state: %w", runID, err)
	}
	return nil
}

func isTerminalAgentRunState(s agentRunState) bool {
	switch s {
	case agentRunSucceeded, agentRunFailed, agentRunStopped, agentRunInterrupted:
		return true
	default:
		return false
	}
}

func GetAgentRun(db *sql.DB, runID int64) (agentRun, error) {
	if db == nil {
		return agentRun{}, fmt.Errorf("postgres unavailable")
	}
	row := db.QueryRow(`SELECT `+agentRunColumns+` FROM agent_runs WHERE id = $1`, runID)
	r, err := scanAgentRun(row)
	if err != nil {
		return agentRun{}, fmt.Errorf("getting agent run %d: %w", runID, err)
	}
	return r, nil
}

// ListAgentRuns returns every persisted run, most recent first.
func ListAgentRuns(db *sql.DB) ([]agentRun, error) {
	if db == nil {
		return nil, fmt.Errorf("postgres unavailable")
	}
	rows, err := db.Query(`SELECT ` + agentRunColumns + ` FROM agent_runs ORDER BY started_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing agent runs: %w", err)
	}
	defer rows.Close()

	var runs []agentRun
	for rows.Next() {
		r, err := scanAgentRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning agent run row: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ReconcileAgentRuns marks any agent_runs row still pending/running at
// process startup as interrupted. The in-memory live-run store is always
// empty on a fresh process start, so nothing could legitimately still be
// tracking such a row — it means a previous healthcheck process was
// killed/restarted mid-run, mirroring the reverted subagent-tracking
// feature's boot-time reset lesson (see agentprocess.go).
func ReconcileAgentRuns(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("postgres unavailable")
	}
	_, err := db.Exec(`
		UPDATE agent_runs
		SET state = $1, error = 'interrupted by a healthcheck restart', finished_at = now()
		WHERE state IN ($2, $3)
	`, agentRunInterrupted, agentRunPending, agentRunRunning)
	if err != nil {
		return fmt.Errorf("reconciling agent runs: %w", err)
	}
	return nil
}
