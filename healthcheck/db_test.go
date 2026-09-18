package main

import (
	"os"
	"strings"
	"testing"
)

func withPostgresEnv(t *testing.T, host, port, user, password, database string) {
	t.Helper()
	origHost, origPort, origUser, origPassword, origDatabase :=
		postgresHost, postgresPort, postgresUser, postgresPassword, postgresDatabase
	t.Cleanup(func() {
		postgresHost, postgresPort, postgresUser, postgresPassword, postgresDatabase =
			origHost, origPort, origUser, origPassword, origDatabase
	})
	postgresHost, postgresPort, postgresUser, postgresPassword, postgresDatabase =
		host, port, user, password, database
}

func TestPostgresDSN(t *testing.T) {
	withPostgresEnv(t, "dbhost", "5432", "dbuser", "s3cr3t", "dbname")

	dsn := postgresDSN()

	for _, want := range []string{"host=dbhost", "port=5432", "user=dbuser", "password=s3cr3t", "dbname=dbname", "sslmode=disable"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("postgresDSN() = %q, want it to contain %q", dsn, want)
		}
	}
}

// TestOpenDB_Reachable exercises openDB against a real Postgres, matching
// what CI's postgres service container and local `docker compose up -d
// postgres` both provide. It skips rather than failing when neither is
// reachable, so `go test ./...` stays runnable on a machine without Docker.
// Credentials come from the real POSTGRES_USER/POSTGRES_PASSWORD/
// POSTGRES_DATABASE process environment (loaded from .env locally, or set on
// CI's service container) — never hardcoded here. Only the host is
// overridden, since the process env's POSTGRES_HOST is the in-container
// service name ("postgres"), not reachable from a host-side `go test` run.
func TestOpenDB_Reachable(t *testing.T) {
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
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("Ping() after successful openDB() = %v, want nil", err)
	}
}

func TestOpenDB_Unreachable(t *testing.T) {
	withPostgresEnv(t, "127.0.0.1", "1", "homelab", "wrong", "homelab")

	origTries, origBackoff := dbConnectTries, dbRetryBackoff
	dbConnectTries, dbRetryBackoff = 1, 0
	defer func() { dbConnectTries, dbRetryBackoff = origTries, origBackoff }()

	if _, err := openDB(); err == nil {
		t.Fatal("openDB() = nil error, want an error for an unreachable host")
	}
}

// testPostgresHost lets a local run point at the docker-compose postgres
// service published on localhost:5432, which is also how CI's postgres
// service container (added in a later plan item) is reachable from the job.
func testPostgresHost() string {
	if h := os.Getenv("POSTGRES_TEST_HOST"); h != "" {
		return h
	}
	return "localhost"
}
