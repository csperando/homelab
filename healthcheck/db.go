package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// db is the process-wide Postgres connection pool, set by main() at
// startup. It stays nil when Postgres is unreachable — callers (e.g. the
// eventual workspaces.go repository layer) must handle a nil db the same
// way docker.go/github.go handle a disabled feature: degrade with a Reason,
// never panic.
var db *sql.DB

// Postgres connection settings, read once at process start like githubToken
// in github.go — env_file: already delivers these into the container from
// .env (see .env.sample).
var (
	postgresHost     = os.Getenv("POSTGRES_HOST")
	postgresPort     = os.Getenv("POSTGRES_PORT")
	postgresUser     = os.Getenv("POSTGRES_USER")
	postgresPassword = os.Getenv("POSTGRES_PASSWORD")
	postgresDatabase = os.Getenv("POSTGRES_DATABASE")
)

// dbConnectTries/dbRetryBackoff are vars rather than consts so tests can
// shrink the retry loop instead of waiting out the real backoff.
var (
	dbPingTimeout  = 5 * time.Second
	dbConnectTries = 5
	dbRetryBackoff = 2 * time.Second
)

// postgresDSN builds a libpq-style connection string. It's assembled but
// deliberately never logged in full anywhere in this package — only openDB's
// own error messages are logged, and those never include it — mirroring the
// existing POSTGRES_PASSWORD redaction already applied to docker-inspect
// output in docker.go.
func postgresDSN() string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		postgresHost, postgresPort, postgresUser, postgresPassword, postgresDatabase)
}

// openDB opens a Postgres connection pool, retrying with backoff since the
// postgres container may not yet be accepting connections when healthcheck
// starts (compose's depends_on/service_healthy narrows but doesn't
// eliminate this window). It returns an error rather than exiting so the
// caller decides whether a failure here is fatal.
func openDB() (*sql.DB, error) {
	db, err := sql.Open("pgx", postgresDSN())
	if err != nil {
		return nil, fmt.Errorf("opening postgres connection: %w", err)
	}

	var pingErr error
	for attempt := 1; attempt <= dbConnectTries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), dbPingTimeout)
		pingErr = db.PingContext(ctx)
		cancel()
		if pingErr == nil {
			return db, nil
		}
		log.Printf("postgres not ready (attempt %d/%d): %v", attempt, dbConnectTries, pingErr)
		if attempt < dbConnectTries {
			time.Sleep(dbRetryBackoff)
		}
	}

	db.Close()
	return nil, fmt.Errorf("postgres unreachable after %d attempts: %w", dbConnectTries, pingErr)
}
