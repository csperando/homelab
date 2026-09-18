package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"
)

// workspace is the persisted, dashboard-facing record of one repo cloned
// under workspaceDir. Branch/dirty state is intentionally absent — it stays
// live-computed via scanWorkspaceRepos (status.go) so it never goes stale.
// EnvConfig is a placeholder for future per-workspace config (raw JSON
// text); nothing writes anything but the "{}" column default yet.
type workspace struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	RepoURL   string    `json:"repo_url,omitempty"`
	EnvConfig string    `json:"env_config"`
	CreatedAt time.Time `json:"created_at"`
}

// UpsertWorkspace records (or updates) the workspace at path, keyed by its
// unique path column. db may be nil (Postgres unreachable) — callers decide
// whether that's fatal; startClone's caller treats it as best-effort.
func UpsertWorkspace(db *sql.DB, name, path, repoURL string) error {
	if db == nil {
		return fmt.Errorf("postgres unavailable")
	}
	_, err := db.Exec(`
		INSERT INTO workspaces (name, path, repo_url)
		VALUES ($1, $2, $3)
		ON CONFLICT (path) DO UPDATE SET name = EXCLUDED.name, repo_url = EXCLUDED.repo_url
	`, name, path, repoURL)
	if err != nil {
		return fmt.Errorf("upserting workspace %s: %w", path, err)
	}
	return nil
}

// ListWorkspaces returns every persisted workspace, ordered by name. db may
// be nil (Postgres unreachable), in which case it returns an error rather
// than panicking — the caller (the eventual Workspace tab handler) degrades
// with a Reason, following the existing Enabled/Reason pattern.
func ListWorkspaces(db *sql.DB) ([]workspace, error) {
	if db == nil {
		return nil, fmt.Errorf("postgres unavailable")
	}
	rows, err := db.Query(`SELECT id, name, path, repo_url, env_config::text, created_at FROM workspaces ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}
	defer rows.Close()

	var workspaces []workspace
	for rows.Next() {
		var w workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.Path, &w.RepoURL, &w.EnvConfig, &w.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning workspace row: %w", err)
		}
		workspaces = append(workspaces, w)
	}
	return workspaces, rows.Err()
}

// workspaceRow is the template-facing merge of a persisted workspace with
// its live git status — branch/dirty are never persisted (see workspace's
// doc comment), so this join happens at request time, by path.
type workspaceRow struct {
	Name      string
	Path      string
	RepoURL   string
	EnvConfig string
	Branch    string
	Dirty     bool
}

// mergeWorkspaceRows joins persisted workspaces with scanWorkspaceRepos'
// live repoStatus list by full path (repoStatus.Path is only the basename,
// see scanWorkspaceRepos). A workspace whose directory is currently missing
// from disk still appears, just with an empty Branch.
func mergeWorkspaceRows(workspaces []workspace, repos []repoStatus) []workspaceRow {
	statusByPath := make(map[string]repoStatus, len(repos))
	for _, r := range repos {
		statusByPath[filepath.Join(workspaceDir, r.Path)] = r
	}

	rows := make([]workspaceRow, 0, len(workspaces))
	for _, w := range workspaces {
		row := workspaceRow{Name: w.Name, Path: w.Path, RepoURL: w.RepoURL, EnvConfig: w.EnvConfig}
		if s, ok := statusByPath[w.Path]; ok {
			row.Branch = s.Branch
			row.Dirty = s.Dirty
		}
		rows = append(rows, row)
	}
	return rows
}

// backfillWorkspaces upserts a row for any repo scanWorkspaceRepos finds
// under workspaceDir that isn't already persisted — so repos that existed
// before this feature (or were cloned by some means other than the repos
// tab's clone flow) aren't invisible in the Workspace tab. It never
// overwrites an existing row (scanWorkspaceRepos has no repo_url to offer
// anyway), only inserts missing ones.
func backfillWorkspaces(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("postgres unavailable")
	}

	existing, err := ListWorkspaces(db)
	if err != nil {
		return fmt.Errorf("listing existing workspaces: %w", err)
	}
	known := make(map[string]bool, len(existing))
	for _, w := range existing {
		known[w.Path] = true
	}

	for _, r := range scanWorkspaceRepos() {
		fullPath := filepath.Join(workspaceDir, r.Path)
		if known[fullPath] {
			continue
		}
		if err := UpsertWorkspace(db, r.Path, fullPath, ""); err != nil {
			return fmt.Errorf("backfilling workspace %s: %w", fullPath, err)
		}
	}
	return nil
}
