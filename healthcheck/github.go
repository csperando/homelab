package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

var githubAPIBase = "https://api.github.com"

// githubToken is read once at process start, like adminUser/adminPass in
// main(), but kept as a package var (rather than passed through main())
// since both handleRepos and the clone handlers need it independently.
var githubToken = os.Getenv("GITHUB_TOKEN")

// githubHTTPClient talks to the GitHub REST API. It times out quickly so a
// slow or unreachable GitHub can't hang the repos page load, mirroring
// dockerHTTPClient in docker.go.
var githubHTTPClient = &http.Client{
	Timeout: 5 * time.Second,
}

// repoListing is the sanitized, dashboard-facing view of one GitHub repo.
// Name is the bare repo name (used as the local clone destination); FullName
// is owner/repo (used to identify the repo when requesting a clone).
type repoListing struct {
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	UpdatedAt     string `json:"updated_at"`
	CloneURL      string `json:"clone_url"`
}

// repoListStatus is the result of a repo-listing attempt: either a
// populated, enabled repo list, or a disabled state with a human-readable
// reason (token missing, API error, etc.) — mirrors dockerStatus.
type repoListStatus struct {
	Enabled bool          `json:"enabled"`
	Repos   []repoListing `json:"repos,omitempty"`
	Reason  string        `json:"reason,omitempty"`
}

// gatherRepoList degrades gracefully rather than erroring: if the token is
// unset or the GitHub API call fails for any reason, it returns a disabled
// result with an explanatory reason instead of propagating an error,
// consistent with gatherDockerStatus in docker.go. The reason never
// includes the token itself.
func gatherRepoList(token string) repoListStatus {
	if token == "" {
		return repoListStatus{Reason: "GITHUB_TOKEN not set"}
	}

	repos, err := listGitHubRepos(token)
	if err != nil {
		return repoListStatus{Reason: fmt.Sprintf("github api query failed: %v", err)}
	}
	return repoListStatus{Enabled: true, Repos: repos}
}

func listGitHubRepos(token string) ([]repoListing, error) {
	req, err := http.NewRequest(http.MethodGet, githubAPIBase+"/user/repos", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var repos []repoListing
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return repos, nil
}

// githubIssue is the subset of GitHub's issue fields the poller needs.
// PullRequest is non-nil when this "issue" is actually a pull request —
// GitHub's issues-list API returns both, and listGitHubIssues filters using
// it so PRs never spawn an agent run meant for issues.
type githubIssue struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	PullRequest any    `json:"pull_request"`
}

// listGitHubIssues lists open issues for owner/repo, filtering out entries
// that are actually pull requests.
func listGitHubIssues(token, owner, repo string) ([]githubIssue, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/issues?state=open", githubAPIBase, owner, repo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var all []githubIssue
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	issues := make([]githubIssue, 0, len(all))
	for _, issue := range all {
		if issue.PullRequest != nil {
			continue
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// postGitHubIssueComment posts body as a new comment on issue number in
// owner/repo.
func postGitHubIssueComment(token, owner, repo string, number int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", githubAPIBase, owner, repo, number)
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return fmt.Errorf("encoding comment body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
