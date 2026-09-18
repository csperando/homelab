package main

import (
	"os/exec"
	"testing"
)

// setOriginRemote points dir's "origin" remote at url, adding it if it
// doesn't already exist — dir must already be a git repo (see initGitRepo).
func setOriginRemote(t *testing.T, dir, url string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", url).CombinedOutput(); err != nil {
		t.Fatalf("git remote add origin: %v\n%s", err, out)
	}
}

func TestGitHubRemoteOwnerRepo_HTTPSForm(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	setOriginRemote(t, dir, "https://github.com/csperando/homelab.git")

	owner, repo, err := githubRemoteOwnerRepo(dir)
	if err != nil {
		t.Fatalf("githubRemoteOwnerRepo() = %v, want nil", err)
	}
	if owner != "csperando" || repo != "homelab" {
		t.Errorf("githubRemoteOwnerRepo() = (%q, %q), want (\"csperando\", \"homelab\")", owner, repo)
	}
}

func TestGitHubRemoteOwnerRepo_HTTPSForm_NoDotGit(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	setOriginRemote(t, dir, "https://github.com/csperando/homelab")

	owner, repo, err := githubRemoteOwnerRepo(dir)
	if err != nil {
		t.Fatalf("githubRemoteOwnerRepo() = %v, want nil", err)
	}
	if owner != "csperando" || repo != "homelab" {
		t.Errorf("githubRemoteOwnerRepo() = (%q, %q), want (\"csperando\", \"homelab\")", owner, repo)
	}
}

func TestGitHubRemoteOwnerRepo_SSHForm(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	setOriginRemote(t, dir, "git@github.com:csperando/homelab.git")

	owner, repo, err := githubRemoteOwnerRepo(dir)
	if err != nil {
		t.Fatalf("githubRemoteOwnerRepo() = %v, want nil", err)
	}
	if owner != "csperando" || repo != "homelab" {
		t.Errorf("githubRemoteOwnerRepo() = (%q, %q), want (\"csperando\", \"homelab\")", owner, repo)
	}
}

func TestGitHubRemoteOwnerRepo_NoOriginRemote(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	if _, _, err := githubRemoteOwnerRepo(dir); err == nil {
		t.Error("githubRemoteOwnerRepo() with no origin remote = nil error, want an error")
	}
}

func TestGitHubRemoteOwnerRepo_NonGitHubRemote(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	setOriginRemote(t, dir, "https://gitlab.com/csperando/homelab.git")

	if _, _, err := githubRemoteOwnerRepo(dir); err == nil {
		t.Error("githubRemoteOwnerRepo() with a non-GitHub remote = nil error, want an error")
	}
}

func TestGitHubRemoteOwnerRepo_PathDoesNotExist(t *testing.T) {
	if _, _, err := githubRemoteOwnerRepo("/no/such/path"); err == nil {
		t.Error("githubRemoteOwnerRepo() for a nonexistent path = nil error, want an error")
	}
}
