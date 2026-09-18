package main

import (
	"database/sql"
	"testing"
)

// testDB returns a real, reachable Postgres connection for tests that need
// one, skipping (not failing) when credentials or the server itself aren't
// available — same convention as TestOpenDB_Reachable in db_test.go.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	origHost := postgresHost
	t.Cleanup(func() { postgresHost = origHost })
	postgresHost = testPostgresHost()

	if postgresUser == "" || postgresPassword == "" || postgresDatabase == "" {
		t.Skip("POSTGRES_USER/POSTGRES_PASSWORD/POSTGRES_DATABASE not set, skipping")
	}

	db, err := openDB()
	if err != nil {
		t.Skipf("postgres not reachable, skipping: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrationNames(t *testing.T) {
	names, err := migrationNames()
	if err != nil {
		t.Fatalf("migrationNames() = %v, want nil error", err)
	}

	if len(names) < 4 {
		t.Fatalf("migrationNames() = %v, want at least 4 migrations", names)
	}
	if names[0] != "0001_create_workspaces.sql" {
		t.Errorf("migrationNames()[0] = %q, want %q", names[0], "0001_create_workspaces.sql")
	}
	if names[1] != "0002_create_agent_tables.sql" {
		t.Errorf("migrationNames()[1] = %q, want %q", names[1], "0002_create_agent_tables.sql")
	}
	if names[2] != "0003_create_approvals.sql" {
		t.Errorf("migrationNames()[2] = %q, want %q", names[2], "0003_create_approvals.sql")
	}
	if names[3] != "0004_github_issue_polling.sql" {
		t.Errorf("migrationNames()[3] = %q, want %q", names[3], "0004_github_issue_polling.sql")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("migrationNames() not sorted: %q >= %q", names[i-1], names[i])
		}
	}
}

// TestRunMigrations exercises the full apply path against a real database,
// starting from a clean slate so the test is meaningful regardless of
// whether migrations were already applied by a previous run, then confirms
// a second run is a no-op rather than an error (e.g. "relation already
// exists").
// resetSchema drops every table this package's migrations create, in
// FK-safe order (dependents before what they reference), so tests can start
// from a clean slate regardless of what a previous run left behind.
func resetSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"approvals", "agent_runs", "tasks", "agent_sessions", "workspaces", "schema_migrations"} {
		if _, err := db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
			t.Fatalf("resetting %s table: %v", table, err)
		}
	}
}

func TestRunMigrations(t *testing.T) {
	db := testDB(t)
	resetSchema(t, db)

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}

	for _, table := range []string{"workspaces", "agent_sessions", "tasks", "agent_runs", "approvals"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Errorf("querying %s after migration: %v", table, err)
		}
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("second runMigrations() = %v, want nil (idempotent)", err)
	}
}

// TestAgentRunsStateCheck_AllowsAwaitingApproval confirms migration 0003
// actually extended the CHECK constraint (drop+recreate, not just adding a
// new value on paper) — a raw SQL update here rather than going through
// UpdateAgentRunState, since this test's job is to verify the schema
// itself, independent of the Go repository layer built on top of it.
func TestAgentRunsStateCheck_AllowsAwaitingApproval(t *testing.T) {
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	workspaceID := seedWorkspace(t, db, "migration-approval-state-check")
	session, err := CreateAgentSession(db, workspaceID)
	if err != nil {
		t.Fatalf("CreateAgentSession() = %v, want nil", err)
	}
	tk, err := CreateTask(db, session.ID, "prompt", nil)
	if err != nil {
		t.Fatalf("CreateTask() = %v, want nil", err)
	}
	run, err := CreateAgentRun(db, tk.ID)
	if err != nil {
		t.Fatalf("CreateAgentRun() = %v, want nil", err)
	}

	if _, err := db.Exec(`UPDATE agent_runs SET state = 'awaiting_approval' WHERE id = $1`, run.ID); err != nil {
		t.Errorf("UPDATE agent_runs SET state = 'awaiting_approval' = %v, want nil (CHECK constraint should allow it)", err)
	}

	if _, err := db.Exec(`UPDATE agent_runs SET state = 'not_a_real_state' WHERE id = $1`, run.ID); err == nil {
		t.Error("UPDATE agent_runs SET state = 'not_a_real_state' = nil error, want the CHECK constraint to still reject unknown values")
	}
}

// TestGitHubIssuePollingColumns confirms migration 0004 actually added both
// new columns with the right default/nullability — a raw SQL check here,
// independent of the Go repository layer built on top of it (which doesn't
// exist yet as of this migration; that's a later plan item).
func TestGitHubIssuePollingColumns(t *testing.T) {
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}

	var pollingDefault bool
	if err := db.QueryRow(`
		INSERT INTO workspaces (name, path) VALUES ('migration-poll-column-check', '/tmp/migration-poll-column-check')
		RETURNING github_issue_polling_enabled
	`).Scan(&pollingDefault); err != nil {
		t.Fatalf("inserting workspace without specifying github_issue_polling_enabled: %v", err)
	}
	defer db.Exec(`DELETE FROM workspaces WHERE path = $1`, "/tmp/migration-poll-column-check")
	if pollingDefault != false {
		t.Errorf("github_issue_polling_enabled default = %v, want false", pollingDefault)
	}

	var issueNumber sql.NullInt64
	workspaceID := seedWorkspace(t, db, "migration-issue-number-column-check")
	session, err := CreateAgentSession(db, workspaceID)
	if err != nil {
		t.Fatalf("CreateAgentSession() = %v, want nil", err)
	}
	if err := db.QueryRow(`
		INSERT INTO tasks (agent_session_id, prompt) VALUES ($1, 'prompt')
		RETURNING github_issue_number
	`, session.ID).Scan(&issueNumber); err != nil {
		t.Fatalf("inserting task without specifying github_issue_number: %v", err)
	}
	if issueNumber.Valid {
		t.Errorf("github_issue_number default = %v, want NULL", issueNumber)
	}
}

// TestApplyMigration_UnknownName exercises the error path a genuinely
// broken/missing migration would take — runMigrations treats this as fatal
// (see its doc comment), so the error must actually propagate.
func TestApplyMigration_UnknownName(t *testing.T) {
	db := testDB(t)

	if err := applyMigration(db, "9999_does_not_exist.sql"); err == nil {
		t.Fatal("applyMigration() with an unknown name = nil error, want an error")
	}
}
