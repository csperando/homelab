package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestToolVersion(t *testing.T) {
	if got, want := toolVersion("echo", "hello"), "hello"; got != want {
		t.Errorf("toolVersion(echo) = %q, want %q", got, want)
	}
	if got, want := toolVersion("no-such-binary-xyz"), "unavailable"; got != want {
		t.Errorf("toolVersion(missing) = %q, want %q", got, want)
	}
}

// withWorkspaceDir points workspaceDir at dir and restores the original on
// cleanup, since it's a shared package-level global.
func withWorkspaceDir(t *testing.T, dir string) {
	t.Helper()
	orig := workspaceDir
	t.Cleanup(func() { workspaceDir = orig })
	workspaceDir = dir
}

func TestWorkspaceDiskUsage(t *testing.T) {
	withWorkspaceDir(t, t.TempDir())

	du := workspaceDiskUsage()
	if du.TotalBytes == 0 {
		t.Fatal("expected nonzero TotalBytes for a real directory")
	}
	if du.TotalBytes != du.UsedBytes+du.FreeBytes {
		t.Errorf("Total(%d) != Used(%d) + Free(%d)", du.TotalBytes, du.UsedBytes, du.FreeBytes)
	}

	withWorkspaceDir(t, filepath.Join(t.TempDir(), "does-not-exist"))
	if got := workspaceDiskUsage(); got != (diskUsage{}) {
		t.Errorf("workspaceDiskUsage() for missing dir = %+v, want zero value", got)
	}
}

func withProcMeminfoPath(t *testing.T, path string) {
	t.Helper()
	orig := procMeminfoPath
	t.Cleanup(func() { procMeminfoPath = orig })
	procMeminfoPath = path
}

func TestReadMemInfo(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "meminfo")
	content := "MemTotal:       16384000 kB\nMemFree:         100000 kB\nMemAvailable:   8192000 kB\n"
	if err := os.WriteFile(fixture, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	withProcMeminfoPath(t, fixture)

	mi := readMemInfo()
	if mi.TotalKB != 16384000 || mi.AvailKB != 8192000 {
		t.Errorf("readMemInfo() = %+v, want TotalKB=16384000 AvailKB=8192000", mi)
	}

	withProcMeminfoPath(t, filepath.Join(t.TempDir(), "no-such-file"))
	if got := readMemInfo(); got != (memInfo{}) {
		t.Errorf("readMemInfo() for missing file = %+v, want zero value", got)
	}
}

func withProcLoadavgPath(t *testing.T, path string) {
	t.Helper()
	orig := procLoadavgPath
	t.Cleanup(func() { procLoadavgPath = orig })
	procLoadavgPath = path
}

func TestReadLoadAvg(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "loadavg")
	if err := os.WriteFile(fixture, []byte("0.10 0.20 0.30 1/200 12345\n"), 0644); err != nil {
		t.Fatal(err)
	}
	withProcLoadavgPath(t, fixture)

	if got, want := readLoadAvg(), "0.10 0.20 0.30"; got != want {
		t.Errorf("readLoadAvg() = %q, want %q", got, want)
	}

	withProcLoadavgPath(t, filepath.Join(t.TempDir(), "no-such-file"))
	if got, want := readLoadAvg(), "unavailable"; got != want {
		t.Errorf("readLoadAvg() for missing file = %q, want %q", got, want)
	}
}

// initGitRepo creates a git repo at dir with one commit, so scanWorkspaceRepos
// can report a real branch name and dirty status.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("init", "-q", "-b", "main")
	runGit("add", "README.md")
	runGit("commit", "-q", "-m", "init")
}

func TestScanWorkspaceRepos(t *testing.T) {
	ws := t.TempDir()
	withWorkspaceDir(t, ws)

	repoDir := filepath.Join(ws, "myrepo")
	if err := os.Mkdir(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, repoDir)
	// make it dirty
	if err := os.WriteFile(filepath.Join(repoDir, "untracked.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	notRepoDir := filepath.Join(ws, "notrepo")
	if err := os.Mkdir(notRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	repos := scanWorkspaceRepos()
	if len(repos) != 1 {
		t.Fatalf("scanWorkspaceRepos() returned %d repos, want 1: %+v", len(repos), repos)
	}
	r := repos[0]
	if r.Path != "myrepo" {
		t.Errorf("Path = %q, want myrepo", r.Path)
	}
	if r.Branch != "main" {
		t.Errorf("Branch = %q, want main", r.Branch)
	}
	if !r.Dirty {
		t.Error("expected Dirty = true due to untracked file")
	}
}

func TestGatherStatus(t *testing.T) {
	ws := t.TempDir()
	withWorkspaceDir(t, ws)

	repoDir := filepath.Join(ws, "repo1")
	if err := os.Mkdir(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, repoDir)

	withProcMeminfoPath(t, filepath.Join(ws, "no-such-meminfo"))
	withProcLoadavgPath(t, filepath.Join(ws, "no-such-loadavg"))

	origSocket := dockerSocketPath
	t.Cleanup(func() { dockerSocketPath = origSocket })
	dockerSocketPath = filepath.Join(ws, "no-such-socket")

	s := gatherStatus()

	if s.Status != "ok" {
		t.Errorf("Status = %q, want ok", s.Status)
	}
	for _, tool := range []string{"go", "node", "npm", "pnpm", "git", "claude"} {
		if _, ok := s.ToolVersions[tool]; !ok {
			t.Errorf("ToolVersions missing key %q", tool)
		}
	}
	if len(s.Repos) != 1 || s.Repos[0].Path != "repo1" {
		t.Errorf("Repos = %+v, want one entry for repo1", s.Repos)
	}
	if s.Docker.Enabled {
		t.Error("expected Docker.Enabled = false with no socket present")
	}
	if s.Docker.Reason != "Disabled" {
		t.Errorf("Docker.Reason = %q, want Disabled", s.Docker.Reason)
	}
}
