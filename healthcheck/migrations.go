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

// migrationsTable tracks which embedded migrations have been applied. It is
// deliberately homelab-prefixed: workspace projects share this Postgres
// database, and the generic schema_migrations name collides with their own
// migration tools (golang-migrate, Rails, dbmate, ...).
const migrationsTable = "homelab_schema_migrations"

// legacyMigrationsTable is the name migrationsTable had before the rename;
// see adoptLegacyMigrations.
const legacyMigrationsTable = "schema_migrations"

// runMigrations applies any embedded migrations/*.sql files not yet
// recorded in migrationsTable, in filename order (hence the
// numeric prefix convention, e.g. 0001_create_workspaces.sql), each inside
// its own transaction. A returned error means the code and the database
// schema have drifted — the caller (main) treats it as fatal, distinct from
// openDB's transient Postgres-unreachable case.
func runMigrations(db *sql.DB) error {
	if err := ensureMigrationsTable(db); err != nil {
		return fmt.Errorf("ensuring %s table: %w", migrationsTable, err)
	}

	names, err := migrationNames()
	if err != nil {
		return fmt.Errorf("reading embedded migrations: %w", err)
	}

	if err := adoptLegacyMigrations(db, names); err != nil {
		return fmt.Errorf("adopting %s rows: %w", legacyMigrationsTable, err)
	}

	applied, err := appliedMigrations(db)
	if err != nil {
		return fmt.Errorf("reading applied migrations: %w", err)
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
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS ` + migrationsTable + ` (
		name TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	return err
}

// adoptLegacyMigrations moves this package's own rows out of a pre-rename
// schema_migrations table into migrationsTable, so a database migrated
// before the rename isn't re-migrated from 0001 ("relation already
// exists"). Only rows whose name matches an embedded migration are moved,
// and only from a table with a name column — a workspace project's own
// schema_migrations (any other shape, or other names) is left untouched.
// The legacy table is dropped only once nothing else is left in it.
func adoptLegacyMigrations(db *sql.DB, names []string) error {
	var hasNameColumn bool
	if err := db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND column_name = 'name'
	)`, legacyMigrationsTable).Scan(&hasNameColumn); err != nil {
		return err
	}
	if !hasNameColumn {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO `+migrationsTable+` (name)
		SELECT name FROM `+legacyMigrationsTable+` WHERE name = ANY($1)
		ON CONFLICT (name) DO NOTHING`, names)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM `+legacyMigrationsTable+` WHERE name = ANY($1)`, names); err != nil {
		return err
	}
	var remaining int
	if err := tx.QueryRow(`SELECT count(*) FROM ` + legacyMigrationsTable).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		if _, err := tx.Exec(`DROP TABLE ` + legacyMigrationsTable); err != nil {
			return err
		}
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("migrations: moved %d applied migration(s) from %s to %s", n, legacyMigrationsTable, migrationsTable)
	}
	return tx.Commit()
}

func appliedMigrations(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM ` + migrationsTable)
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
	if _, err := tx.Exec(`INSERT INTO `+migrationsTable+` (name) VALUES ($1)`, name); err != nil {
		return err
	}
	return tx.Commit()
}
