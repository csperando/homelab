package main

import "testing"

func TestApprovalsRepo_NilDB(t *testing.T) {
	if _, err := CreateApproval(nil, 1, "Bash", `{"command":"git push"}`, "gated"); err == nil {
		t.Error("CreateApproval(nil, ...) = nil error, want an error")
	}
	if _, err := GetApproval(nil, 1); err == nil {
		t.Error("GetApproval(nil, ...) = nil error, want an error")
	}
	if _, err := ListApprovalsForRun(nil, 1); err == nil {
		t.Error("ListApprovalsForRun(nil, ...) = nil error, want an error")
	}
	if err := UpdateApprovalState(nil, 1, approvalApproved); err == nil {
		t.Error("UpdateApprovalState(nil, ...) = nil error, want an error")
	}
}

func TestApprovalsRepo_FullLifecycle(t *testing.T) {
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	runID := seedAgentRun(t, db, "approvals-repo-lifecycle")

	a, err := CreateApproval(db, runID, "Bash", `{"command":"git push origin main"}`, "gated command: git push")
	if err != nil {
		t.Fatalf("CreateApproval() = %v, want nil", err)
	}
	if a.State != approvalPending {
		t.Errorf("a.State = %q, want %q", a.State, approvalPending)
	}
	if a.DecidedAt != nil {
		t.Errorf("new approval DecidedAt = %v, want nil", a.DecidedAt)
	}
	if a.AgentRunID != runID {
		t.Errorf("a.AgentRunID = %d, want %d", a.AgentRunID, runID)
	}

	got, err := GetApproval(db, a.ID)
	if err != nil {
		t.Fatalf("GetApproval() = %v, want nil", err)
	}
	if got.ToolName != "Bash" || got.Reason != "gated command: git push" {
		t.Errorf("GetApproval() = %+v, want tool_name/reason matching what was created", got)
	}

	all, err := ListApprovalsForRun(db, runID)
	if err != nil {
		t.Fatalf("ListApprovalsForRun() = %v, want nil", err)
	}
	if len(all) != 1 || all[0].ID != a.ID {
		t.Errorf("ListApprovalsForRun() = %+v, want exactly the one approval just created", all)
	}

	if err := UpdateApprovalState(db, a.ID, approvalApproved); err != nil {
		t.Fatalf("UpdateApprovalState(approved) = %v, want nil", err)
	}

	final, err := GetApproval(db, a.ID)
	if err != nil {
		t.Fatalf("GetApproval() (final) = %v, want nil", err)
	}
	if final.State != approvalApproved {
		t.Errorf("final.State = %q, want %q", final.State, approvalApproved)
	}
	if final.DecidedAt == nil {
		t.Error("final.DecidedAt = nil, want non-nil after a decision")
	}
}

func TestListApprovalsForRun_EmptyForUnknownRun(t *testing.T) {
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}

	all, err := ListApprovalsForRun(db, 99999999)
	if err != nil {
		t.Fatalf("ListApprovalsForRun() = %v, want nil", err)
	}
	if len(all) != 0 {
		t.Errorf("ListApprovalsForRun(unknown run) = %+v, want empty", all)
	}
}
