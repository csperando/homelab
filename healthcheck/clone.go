package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type cloneState string

const (
	cloneStatePending   cloneState = "pending"
	cloneStateRunning   cloneState = "running"
	cloneStateSucceeded cloneState = "succeeded"
	cloneStateFailed    cloneState = "failed"
)

// cloneJob tracks one background `git clone` invocation triggered from the
// repos tab. ID/Repo are both the destination directory name (one clone per
// name at a time).
type cloneJob struct {
	ID         string     `json:"id"`
	Repo       string     `json:"repo"`
	State      cloneState `json:"state"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at,omitempty"`
}

var (
	cloneJobsMu sync.Mutex
	cloneJobs   = map[string]*cloneJob{}
)

// cloneRunner performs the actual clone; a package-level var so tests can
// substitute it if needed. Defaults to runGitClone.
var cloneRunner = runGitClone

// listCloneJobs returns a snapshot of all known clone jobs, for the
// /api/repos/status endpoint.
func listCloneJobs() []*cloneJob {
	cloneJobsMu.Lock()
	defer cloneJobsMu.Unlock()

	jobs := make([]*cloneJob, 0, len(cloneJobs))
	for _, j := range cloneJobs {
		snapshot := *j
		jobs = append(jobs, &snapshot)
	}
	return jobs
}

// startClone validates name, rejects the request if a clone of that name is
// already running or its destination already exists under workspaceDir,
// then registers a pending job and runs the clone in the background,
// returning the job immediately (the job's State reflects progress).
func startClone(name, cloneURL, token string) (*cloneJob, error) {
	if !isSafeRepoName(name) {
		return nil, fmt.Errorf("invalid repo name %q", name)
	}

	dest := filepath.Join(workspaceDir, name)

	cloneJobsMu.Lock()
	if existing, ok := cloneJobs[name]; ok && (existing.State == cloneStatePending || existing.State == cloneStateRunning) {
		cloneJobsMu.Unlock()
		return nil, fmt.Errorf("clone of %s is already in progress", name)
	}
	if _, err := os.Stat(dest); err == nil {
		cloneJobsMu.Unlock()
		return nil, fmt.Errorf("%s already exists in the workspace", name)
	} else if !os.IsNotExist(err) {
		cloneJobsMu.Unlock()
		return nil, fmt.Errorf("checking destination: %w", err)
	}

	job := &cloneJob{
		ID:        name,
		Repo:      name,
		State:     cloneStatePending,
		StartedAt: time.Now().UTC(),
	}
	cloneJobs[name] = job
	cloneJobsMu.Unlock()

	go runClone(job, dest, cloneURL, token)

	return job, nil
}

func setJobState(job *cloneJob, state cloneState, errMsg string) {
	cloneJobsMu.Lock()
	defer cloneJobsMu.Unlock()
	job.State = state
	job.Error = errMsg
	if state == cloneStateSucceeded || state == cloneStateFailed {
		job.FinishedAt = time.Now().UTC()
	}
}

func runClone(job *cloneJob, dest, cloneURL, token string) {
	setJobState(job, cloneStateRunning, "")
	log.Printf("clone: starting %s -> %s", job.Repo, dest)

	if err := cloneRunner(cloneURL, dest, token); err != nil {
		os.RemoveAll(dest)
		setJobState(job, cloneStateFailed, err.Error())
		log.Printf("clone: %s failed: %v", job.Repo, err)
		return
	}

	setJobState(job, cloneStateSucceeded, "")
	log.Printf("clone: %s succeeded", job.Repo)

	// Persisting the workspace record is best-effort: a clone that
	// otherwise succeeded must not be reported as failed just because
	// Postgres is unreachable (db may be nil) or the write itself errors.
	if err := UpsertWorkspace(db, job.Repo, dest, cloneURL); err != nil {
		log.Printf("clone: %s succeeded but failed to persist workspace record: %v", job.Repo, err)
	}
}

// gitAskpassScript is a static credential helper: it never contains the
// token itself, only echoing whatever GIT_ASKPASS_TOKEN is set to in its
// environment, so the token only ever travels as a process env var, never
// written to disk or visible in a process's argv.
const gitAskpassScript = "#!/bin/sh\necho \"$GIT_ASKPASS_TOKEN\"\n"

func ensureAskpassScript() (string, error) {
	path := filepath.Join(os.TempDir(), "homelab-git-askpass.sh")
	if err := os.WriteFile(path, []byte(gitAskpassScript), 0o700); err != nil {
		return "", fmt.Errorf("writing askpass script: %w", err)
	}
	return path, nil
}

// runGitClone runs `git clone` via exec.Command with an explicit argument
// list (never shell interpolation). When token is non-empty, it's supplied
// to git through a GIT_ASKPASS helper rather than embedded in cloneURL or
// passed as a command-line argument.
func runGitClone(cloneURL, dest, token string) error {
	cmd := exec.Command("git", "clone", "--", cloneURL, dest)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	if token != "" {
		askpass, err := ensureAskpassScript()
		if err != nil {
			return err
		}
		cmd.Env = append(cmd.Env,
			"GIT_ASKPASS="+askpass,
			"GIT_ASKPASS_TOKEN="+token,
		)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
