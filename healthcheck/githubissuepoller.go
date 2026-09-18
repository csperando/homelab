package main

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"time"
)

// githubIssuePollInterval controls how often startGitHubIssuePoller polls —
// a time.Duration package var (not a const) so tests can shrink it below
// what GITHUB_ISSUE_POLL_INTERVAL's whole-seconds env format could express.
var githubIssuePollInterval = parseGitHubIssuePollInterval(envOrDefault("GITHUB_ISSUE_POLL_INTERVAL", "300"))

func parseGitHubIssuePollInterval(s string) time.Duration {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		n = 300
	}
	return time.Duration(n) * time.Second
}

// startGitHubIssuePoller starts this repo's first periodic background loop:
// a time.NewTicker-driven goroutine that calls pollGitHubIssuesOnce on every
// tick. Ticks are consumed one at a time by a single goroutine (never a new
// goroutine per tick), so a slow poll cycle simply delays the next one
// rather than ever running two cycles concurrently.
func startGitHubIssuePoller(db *sql.DB) {
	go func() {
		ticker := time.NewTicker(githubIssuePollInterval)
		defer ticker.Stop()
		for range ticker.C {
			pollGitHubIssuesOnce(db)
		}
	}()
}

// pollGitHubIssuesOnce runs one poll cycle across every opted-in workspace.
// A failure scoped to one workspace (bad remote, GitHub API error, ...) is
// logged and skipped — it never aborts the rest of the cycle.
func pollGitHubIssuesOnce(db *sql.DB) {
	workspaces, err := ListWorkspaces(db)
	if err != nil {
		log.Printf("github issue poller: listing workspaces: %v", err)
		return
	}
	for _, ws := range workspaces {
		if !ws.GitHubIssuePollingEnabled {
			continue
		}
		pollWorkspaceIssuesOnce(db, ws)
	}
}

// pollWorkspaceIssuesOnce starts at most one new agent run for ws, for the
// first open issue that doesn't already have a task. Deliberately stops
// after the first new run rather than looping over every eligible issue:
// startAgentRun registers the run in the live-run store asynchronously,
// inside its own goroutine, only after the claude subprocess's cmd.Start()
// returns — so re-checking workspaceRunInProgress immediately afterward, in
// this same loop iteration, can't be trusted to already see it. Capping
// this cycle to one new run per workspace is what actually prevents two
// `claude` subprocesses starting concurrently against the same working
// directory; any other eligible issues are simply picked up on the next
// poll.
func pollWorkspaceIssuesOnce(db *sql.DB, ws workspace) {
	if workspaceRunInProgress(ws.ID) {
		return
	}

	owner, repo, err := githubRemoteOwnerRepo(ws.Path)
	if err != nil {
		log.Printf("github issue poller: workspace %d (%s): resolving GitHub remote: %v", ws.ID, ws.Name, err)
		return
	}

	issues, err := listGitHubIssues(githubToken, owner, repo)
	if err != nil {
		log.Printf("github issue poller: workspace %d (%s): listing issues for %s/%s: %v", ws.ID, ws.Name, owner, repo, err)
		return
	}

	for _, issue := range issues {
		has, err := githubIssueHasTask(db, ws.ID, issue.Number)
		if err != nil {
			log.Printf("github issue poller: workspace %d (%s): checking existing task for issue #%d: %v", ws.ID, ws.Name, issue.Number, err)
			continue
		}
		if has {
			continue
		}
		startGitHubIssueRun(db, ws, issue)
		return
	}
}

// startGitHubIssueRun creates the same Task -> Agent Run chain
// handleAPIAgentsStart already makes for a manual run, so Approval gating
// applies identically — the real safeguard against issue-text prompt
// injection driving an unattended `git push`/`rm -rf`, not just pipeline
// reuse for its own sake.
func startGitHubIssueRun(db *sql.DB, ws workspace, issue githubIssue) {
	session, err := CreateAgentSession(db, ws.ID)
	if err != nil {
		log.Printf("github issue poller: workspace %d (%s): creating agent session for issue #%d: %v", ws.ID, ws.Name, issue.Number, err)
		return
	}

	prompt := fmt.Sprintf("Investigate and address GitHub issue #%d: %s\n\n%s", issue.Number, issue.Title, issue.Body)
	issueNumber := issue.Number
	tk, err := CreateTask(db, session.ID, prompt, &issueNumber)
	if err != nil {
		log.Printf("github issue poller: workspace %d (%s): creating task for issue #%d: %v", ws.ID, ws.Name, issue.Number, err)
		return
	}

	run, err := CreateAgentRun(db, tk.ID)
	if err != nil {
		log.Printf("github issue poller: workspace %d (%s): creating agent run for issue #%d: %v", ws.ID, ws.Name, issue.Number, err)
		return
	}

	startAgentRun(run.ID, ws.ID, ws.Path, prompt)
	log.Printf("github issue poller: auto-started agent run %d for workspace %d (%s) issue #%d", run.ID, ws.ID, ws.Name, issue.Number)
}
