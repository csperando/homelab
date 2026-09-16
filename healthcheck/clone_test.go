package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// resetCloneJobs clears the global job store between tests, since it's
// package-level shared state.
func resetCloneJobs(t *testing.T) {
	t.Helper()
	cloneJobsMu.Lock()
	cloneJobs = map[string]*cloneJob{}
	cloneJobsMu.Unlock()
	t.Cleanup(func() {
		cloneJobsMu.Lock()
		cloneJobs = map[string]*cloneJob{}
		cloneJobsMu.Unlock()
	})
}

// bareRepoFixture creates a local bare git repo (with one commit) that can
// be cloned over a plain filesystem path, so clone tests need no network.
func bareRepoFixture(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	initGitRepo(t, src)

	bareDir := filepath.Join(t.TempDir(), "repo.git")
	if out, err := exec.Command("git", "clone", "-q", "--bare", src, bareDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	return bareDir
}

func waitForJobDone(t *testing.T, name string, timeout time.Duration) *cloneJob {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cloneJobsMu.Lock()
		job, ok := cloneJobs[name]
		var snapshot cloneJob
		if ok {
			snapshot = *job
		}
		cloneJobsMu.Unlock()
		if ok && (snapshot.State == cloneStateSucceeded || snapshot.State == cloneStateFailed) {
			return &snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("clone job %q did not finish within %s", name, timeout)
	return nil
}

func TestStartClone_Success(t *testing.T) {
	resetCloneJobs(t)
	ws := t.TempDir()
	withWorkspaceDir(t, ws)
	bareRepo := bareRepoFixture(t)

	job, err := startClone("myrepo", bareRepo, "")
	if err != nil {
		t.Fatalf("startClone: %v", err)
	}
	if job.State != cloneStatePending {
		t.Errorf("initial job state = %q, want %q", job.State, cloneStatePending)
	}

	done := waitForJobDone(t, "myrepo", 5*time.Second)
	if done.State != cloneStateSucceeded {
		t.Fatalf("job state = %q, want succeeded (error: %s)", done.State, done.Error)
	}
	if _, err := os.Stat(filepath.Join(ws, "myrepo", "README.md")); err != nil {
		t.Errorf("expected README.md to be cloned: %v", err)
	}
}

func TestStartClone_InvalidName(t *testing.T) {
	resetCloneJobs(t)
	withWorkspaceDir(t, t.TempDir())

	if _, err := startClone("../escape", "https://example.com/repo.git", ""); err == nil {
		t.Fatal("expected an error for an unsafe repo name")
	}
}

func TestStartClone_DuplicateDestination(t *testing.T) {
	resetCloneJobs(t)
	ws := t.TempDir()
	withWorkspaceDir(t, ws)
	if err := os.MkdirAll(filepath.Join(ws, "existing"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := startClone("existing", "https://example.com/repo.git", ""); err == nil {
		t.Fatal("expected an error when the destination already exists")
	}
}

func TestStartClone_FailureCleansUpDestination(t *testing.T) {
	resetCloneJobs(t)
	ws := t.TempDir()
	withWorkspaceDir(t, ws)

	if _, err := startClone("badrepo", filepath.Join(t.TempDir(), "no-such-repo.git"), ""); err != nil {
		t.Fatalf("startClone: %v", err)
	}

	done := waitForJobDone(t, "badrepo", 5*time.Second)
	if done.State != cloneStateFailed {
		t.Fatalf("job state = %q, want failed", done.State)
	}
	if done.Error == "" {
		t.Error("expected a non-empty Error on failure")
	}
	if _, err := os.Stat(filepath.Join(ws, "badrepo")); !os.IsNotExist(err) {
		t.Errorf("expected destination to be cleaned up after a failed clone, stat err = %v", err)
	}
}

func TestListCloneJobs(t *testing.T) {
	resetCloneJobs(t)
	ws := t.TempDir()
	withWorkspaceDir(t, ws)
	bareRepo := bareRepoFixture(t)

	if _, err := startClone("listed-repo", bareRepo, ""); err != nil {
		t.Fatalf("startClone: %v", err)
	}
	waitForJobDone(t, "listed-repo", 5*time.Second)

	jobs := listCloneJobs()
	if len(jobs) != 1 || jobs[0].Repo != "listed-repo" {
		t.Errorf("listCloneJobs() = %+v, want one job for listed-repo", jobs)
	}
}
