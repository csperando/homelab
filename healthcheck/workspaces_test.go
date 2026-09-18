package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestUpsertWorkspace_NilDB(t *testing.T) {
	if err := UpsertWorkspace(nil, "name", "/path", "url"); err == nil {
		t.Fatal("UpsertWorkspace(nil, ...) = nil error, want an error")
	}
}

func TestListWorkspaces_NilDB(t *testing.T) {
	if _, err := ListWorkspaces(nil); err == nil {
		t.Fatal("ListWorkspaces(nil) = nil error, want an error")
	}
}

func TestUpsertWorkspace_InsertThenUpdate(t *testing.T) {
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}

	const path = "/root/workspace/test-upsert-workspace"
	t.Cleanup(func() { db.Exec(`DELETE FROM workspaces WHERE path = $1`, path) })

	if err := UpsertWorkspace(db, "test-upsert-workspace", path, "https://example.com/a.git"); err != nil {
		t.Fatalf("UpsertWorkspace() insert = %v, want nil", err)
	}

	got, err := findWorkspaceByPath(db, path)
	if err != nil {
		t.Fatalf("findWorkspaceByPath() after insert = %v, want nil", err)
	}
	if got.Name != "test-upsert-workspace" || got.RepoURL != "https://example.com/a.git" {
		t.Fatalf("after insert: got %+v, want name=test-upsert-workspace repo_url=https://example.com/a.git", got)
	}
	firstID := got.ID

	if err := UpsertWorkspace(db, "renamed", path, "https://example.com/b.git"); err != nil {
		t.Fatalf("UpsertWorkspace() update = %v, want nil", err)
	}

	got, err = findWorkspaceByPath(db, path)
	if err != nil {
		t.Fatalf("findWorkspaceByPath() after update = %v, want nil", err)
	}
	if got.Name != "renamed" || got.RepoURL != "https://example.com/b.git" {
		t.Fatalf("after update: got %+v, want name=renamed repo_url=https://example.com/b.git", got)
	}
	if got.ID != firstID {
		t.Fatalf("ID changed across upsert: got %d, want %d (same row, not a new one)", got.ID, firstID)
	}
}

func TestListWorkspaces_OrderedByName(t *testing.T) {
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}

	paths := []string{
		"/root/workspace/test-list-zeta",
		"/root/workspace/test-list-alpha",
	}
	t.Cleanup(func() {
		for _, p := range paths {
			db.Exec(`DELETE FROM workspaces WHERE path = $1`, p)
		}
	})

	if err := UpsertWorkspace(db, "zeta", paths[0], ""); err != nil {
		t.Fatalf("UpsertWorkspace(zeta) = %v, want nil", err)
	}
	if err := UpsertWorkspace(db, "alpha", paths[1], ""); err != nil {
		t.Fatalf("UpsertWorkspace(alpha) = %v, want nil", err)
	}

	all, err := ListWorkspaces(db)
	if err != nil {
		t.Fatalf("ListWorkspaces() = %v, want nil", err)
	}

	var names []string
	for _, w := range all {
		if w.Path == paths[0] || w.Path == paths[1] {
			names = append(names, w.Name)
		}
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "zeta" {
		t.Fatalf("names among test rows = %v, want [alpha zeta] in that order", names)
	}
}

func TestBackfillWorkspaces_NilDB(t *testing.T) {
	if err := backfillWorkspaces(nil); err == nil {
		t.Fatal("backfillWorkspaces(nil) = nil error, want an error")
	}
}

// TestBackfillWorkspaces_InsertsMissingOnly puts two real git repos on disk
// under a fresh workspaceDir, pre-persists one of them (with a real
// repo_url), then confirms backfill adds only the missing one and leaves
// the existing row's repo_url untouched (never clobbered with "").
func TestBackfillWorkspaces_InsertsMissingOnly(t *testing.T) {
	db := testDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}

	ws := t.TempDir()
	withWorkspaceDir(t, ws)

	knownDir := filepath.Join(ws, "known-repo")
	if err := os.MkdirAll(knownDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, knownDir)
	missingDir := filepath.Join(ws, "missing-repo")
	if err := os.MkdirAll(missingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, missingDir)

	t.Cleanup(func() {
		db.Exec(`DELETE FROM workspaces WHERE path = $1 OR path = $2`, knownDir, missingDir)
	})

	if err := UpsertWorkspace(db, "known-repo", knownDir, "https://example.com/known.git"); err != nil {
		t.Fatalf("seeding known workspace: %v", err)
	}

	if err := backfillWorkspaces(db); err != nil {
		t.Fatalf("backfillWorkspaces() = %v, want nil", err)
	}

	known, err := findWorkspaceByPath(db, knownDir)
	if err != nil {
		t.Fatalf("findWorkspaceByPath(known) = %v, want nil", err)
	}
	if known.RepoURL != "https://example.com/known.git" {
		t.Errorf("backfill clobbered existing repo_url: got %q, want unchanged", known.RepoURL)
	}

	missing, err := findWorkspaceByPath(db, missingDir)
	if err != nil {
		t.Fatalf("findWorkspaceByPath(missing) after backfill = %v, want nil (should have been inserted)", err)
	}
	if missing.Name != "missing-repo" {
		t.Errorf("backfilled workspace name = %q, want %q", missing.Name, "missing-repo")
	}
}

// findWorkspaceByPath is a test-only helper; production code only ever
// needs ListWorkspaces.
func findWorkspaceByPath(db *sql.DB, path string) (workspace, error) {
	all, err := ListWorkspaces(db)
	if err != nil {
		return workspace{}, err
	}
	for _, w := range all {
		if w.Path == path {
			return w, nil
		}
	}
	return workspace{}, fmt.Errorf("workspace %s not found", path)
}
