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

	if len(names) == 0 {
		t.Fatal("migrationNames() = [], want at least the initial migration")
	}
	if names[0] != "0001_create_workspaces.sql" {
		t.Errorf("migrationNames()[0] = %q, want %q", names[0], "0001_create_workspaces.sql")
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
func TestRunMigrations(t *testing.T) {
	db := testDB(t)

	if _, err := db.Exec(`DROP TABLE IF EXISTS workspaces`); err != nil {
		t.Fatalf("resetting workspaces table: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS schema_migrations`); err != nil {
		t.Fatalf("resetting schema_migrations table: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM workspaces`).Scan(&count); err != nil {
		t.Fatalf("querying workspaces after migration: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("second runMigrations() = %v, want nil (idempotent)", err)
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
