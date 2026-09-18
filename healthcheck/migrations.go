package main

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// runMigrations applies any embedded migrations/*.sql files not yet
// recorded in the schema_migrations table, in filename order (hence the
// numeric prefix convention, e.g. 0001_create_workspaces.sql), each inside
// its own transaction. A returned error means the code and the database
// schema have drifted — the caller (main) treats it as fatal, distinct from
// openDB's transient Postgres-unreachable case.
func runMigrations(db *sql.DB) error {
	if err := ensureMigrationsTable(db); err != nil {
		return fmt.Errorf("ensuring schema_migrations table: %w", err)
	}

	applied, err := appliedMigrations(db)
	if err != nil {
		return fmt.Errorf("reading applied migrations: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return fmt.Errorf("reading embedded migrations: %w", err)
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		if err := applyMigration(db, name); err != nil {
			return fmt.Errorf("applying migration %s: %w", name, err)
		}
		log.Printf("migrations: applied %s", name)
	}
	return nil
}

func ensureMigrationsTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	return err
}

func appliedMigrations(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		applied[name] = true
	}
	return applied, rows.Err()
}

// migrationNames lists the embedded migration filenames in apply order.
// Sorting lexicographically works because filenames use a zero-padded
// numeric prefix (0001_, 0002_, ...).
func migrationNames() ([]string, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func applyMigration(db *sql.DB, name string) error {
	sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(string(sqlBytes)); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
		return err
	}
	return tx.Commit()
}
