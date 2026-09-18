package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// githubOwnerRepoPattern matches the owner/repo portion of both remote URL
// forms GitHub issues: "https://github.com/owner/repo(.git)" and
// "git@github.com:owner/repo(.git)".
var githubOwnerRepoPattern = regexp.MustCompile(`github\.com[:/]([^/]+)/([^/]+?)(?:\.git)?/?$`)

// githubRemoteOwnerRepo resolves the GitHub owner/repo a workspace directory
// points at by reading its "origin" remote directly from disk, rather than
// relying on the workspaces.repo_url column — every real workspace as of
// Phase 6 has an empty repo_url (see roadmap.md), so the poller must derive
// this live instead. Returns a plain error (never panics) if the path
// doesn't exist, isn't a git repo, has no "origin" remote, or that remote
// isn't a GitHub URL.
func githubRemoteOwnerRepo(path string) (owner, repo string, err error) {
	out, err := exec.Command("git", "-C", path, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", "", fmt.Errorf("getting origin remote for %s: %w", path, err)
	}

	remoteURL := strings.TrimSpace(string(out))
	m := githubOwnerRepoPattern.FindStringSubmatch(remoteURL)
	if m == nil {
		return "", "", fmt.Errorf("origin remote %q for %s is not a recognizable GitHub URL", remoteURL, path)
	}
	return m[1], m[2], nil
}
