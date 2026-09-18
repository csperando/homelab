package main

import (
	"database/sql"
	"fmt"
	"time"
)

type approvalState string

const (
	approvalPending  approvalState = "pending"
	approvalApproved approvalState = "approved"
	approvalDenied   approvalState = "denied"
)

// approval is one gated tool-call decision raised by the PreToolUse hook
// (see agentprocess.go) against a specific agent run. Always created in the
// "pending" state and always denied at creation time by the hook itself —
// the run's own subprocess never waits on it; only an operator decision
// (UpdateApprovalState) resolves it.
type approval struct {
	ID         int64         `json:"id"`
	AgentRunID int64         `json:"agent_run_id"`
	ToolName   string        `json:"tool_name"`
	ToolInput  string        `json:"tool_input"`
	Reason     string        `json:"reason"`
	State      approvalState `json:"state"`
	CreatedAt  time.Time     `json:"created_at"`
	DecidedAt  *time.Time    `json:"decided_at,omitempty"`
}

const approvalColumns = `id, agent_run_id, tool_name, tool_input, reason, state, created_at, decided_at`

func scanApproval(s scanner) (approval, error) {
	var a approval
	var decidedAt sql.NullTime

	err := s.Scan(&a.ID, &a.AgentRunID, &a.ToolName, &a.ToolInput, &a.Reason, &a.State, &a.CreatedAt, &decidedAt)
	if err != nil {
		return approval{}, err
	}

	if decidedAt.Valid {
		t := decidedAt.Time
		a.DecidedAt = &t
	}
	return a, nil
}

// CreateApproval records a pending decision for a gated tool call. db may
// be nil (Postgres unreachable) — the hook handler treats that as "can't
// gate, deny anyway", per its own doc comment.
func CreateApproval(db *sql.DB, agentRunID int64, toolName, toolInput, reason string) (approval, error) {
	if db == nil {
		return approval{}, fmt.Errorf("postgres unavailable")
	}
	row := db.QueryRow(`
		INSERT INTO approvals (agent_run_id, tool_name, tool_input, reason, state)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+approvalColumns, agentRunID, toolName, toolInput, reason, approvalPending)
	a, err := scanApproval(row)
	if err != nil {
		return approval{}, fmt.Errorf("creating approval: %w", err)
	}
	return a, nil
}

func GetApproval(db *sql.DB, approvalID int64) (approval, error) {
	if db == nil {
		return approval{}, fmt.Errorf("postgres unavailable")
	}
	row := db.QueryRow(`SELECT `+approvalColumns+` FROM approvals WHERE id = $1`, approvalID)
	a, err := scanApproval(row)
	if err != nil {
		return approval{}, fmt.Errorf("getting approval %d: %w", approvalID, err)
	}
	return a, nil
}

// ListApprovalsForRun returns every approval raised against runID, most
// recent first.
func ListApprovalsForRun(db *sql.DB, runID int64) ([]approval, error) {
	if db == nil {
		return nil, fmt.Errorf("postgres unavailable")
	}
	rows, err := db.Query(`SELECT `+approvalColumns+` FROM approvals WHERE agent_run_id = $1 ORDER BY created_at DESC`, runID)
	if err != nil {
		return nil, fmt.Errorf("listing approvals for run %d: %w", runID, err)
	}
	defer rows.Close()

	var approvals []approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning approval row: %w", err)
		}
		approvals = append(approvals, a)
	}
	return approvals, rows.Err()
}

// UpdateApprovalState transitions an approval to a terminal state
// (approved/denied), stamping decided_at.
func UpdateApprovalState(db *sql.DB, approvalID int64, state approvalState) error {
	if db == nil {
		return fmt.Errorf("postgres unavailable")
	}
	_, err := db.Exec(`
		UPDATE approvals SET state = $1, decided_at = now() WHERE id = $2
	`, state, approvalID)
	if err != nil {
		return fmt.Errorf("updating approval %d state: %w", approvalID, err)
	}
	return nil
}
