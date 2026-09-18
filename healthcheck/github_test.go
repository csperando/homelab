package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func withGitHubAPIBase(t *testing.T, base string) {
	t.Helper()
	orig := githubAPIBase
	t.Cleanup(func() { githubAPIBase = orig })
	githubAPIBase = base
}

func withGithubToken(t *testing.T, token string) {
	t.Helper()
	orig := githubToken
	t.Cleanup(func() { githubToken = orig })
	githubToken = token
}

func TestGatherRepoList_MissingToken(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)

	got := gatherRepoList("")

	if got.Enabled {
		t.Fatalf("Enabled = true, want false for missing token")
	}
	if got.Reason == "" {
		t.Error("expected a non-empty Reason for missing token")
	}
	if called {
		t.Error("expected no GitHub API request when token is missing")
	}
}

func TestGatherRepoList_Success(t *testing.T) {
	const token = "test-token-value"
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"full_name":      "octocat/hello-world",
				"name":           "hello-world",
				"description":    "my first repo",
				"private":        false,
				"default_branch": "main",
				"updated_at":     "2024-01-01T00:00:00Z",
				"clone_url":      "https://github.com/octocat/hello-world.git",
			},
			{
				"full_name":      "octocat/secret-project",
				"name":           "secret-project",
				"description":    "",
				"private":        true,
				"default_branch": "main",
				"updated_at":     "2024-02-02T00:00:00Z",
				"clone_url":      "https://github.com/octocat/secret-project.git",
			},
		})
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)

	got := gatherRepoList(token)

	if !got.Enabled {
		t.Fatalf("Enabled = false, want true; Reason = %q", got.Reason)
	}
	if len(got.Repos) != 2 {
		t.Fatalf("len(Repos) = %d, want 2", len(got.Repos))
	}
	if got.Repos[0].FullName != "octocat/hello-world" || got.Repos[0].Name != "hello-world" {
		t.Errorf("Repos[0] = %+v", got.Repos[0])
	}
	if !got.Repos[1].Private {
		t.Errorf("Repos[1].Private = false, want true")
	}
	if gotAuth != "Bearer "+token {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer "+token)
	}
}

func TestGatherRepoList_NonOKStatus(t *testing.T) {
	const token = "bad-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)

	got := gatherRepoList(token)

	if got.Enabled {
		t.Fatal("Enabled = true, want false for a non-200 response")
	}
	if !strings.Contains(got.Reason, "403") {
		t.Errorf("Reason = %q, want it to mention the 403 status", got.Reason)
	}
	if strings.Contains(got.Reason, token) {
		t.Errorf("Reason leaked the token: %q", got.Reason)
	}
}

func TestGatherRepoList_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{not valid json"))
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)

	got := gatherRepoList("some-token")

	if got.Enabled {
		t.Fatal("Enabled = true, want false for a malformed JSON response")
	}
	if got.Reason == "" {
		t.Error("expected a non-empty Reason for malformed JSON")
	}
	if strings.Contains(got.Reason, "some-token") {
		t.Errorf("Reason leaked the token: %q", got.Reason)
	}
}

func TestListGitHubIssues_FiltersPullRequests(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"number": 1, "title": "a real issue", "body": "please fix", "pull_request": nil},
			{"number": 2, "title": "a pull request", "body": "", "pull_request": map[string]any{"url": "https://api.github.com/x"}},
			{"number": 3, "title": "another real issue", "body": "", "pull_request": nil},
		})
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)

	issues, err := listGitHubIssues("test-token", "octocat", "hello-world")
	if err != nil {
		t.Fatalf("listGitHubIssues() = %v, want nil", err)
	}
	if len(issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2 (PR filtered out): %+v", len(issues), issues)
	}
	if issues[0].Number != 1 || issues[1].Number != 3 {
		t.Errorf("issue numbers = [%d %d], want [1 3]", issues[0].Number, issues[1].Number)
	}
	if gotPath != "/repos/octocat/hello-world/issues?state=open" {
		t.Errorf("request path = %q, want %q", gotPath, "/repos/octocat/hello-world/issues?state=open")
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer test-token")
	}
}

func TestListGitHubIssues_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)

	if _, err := listGitHubIssues("token", "octocat", "hello-world"); err == nil {
		t.Error("listGitHubIssues() with a 403 response = nil error, want an error")
	}
}

func TestPostGitHubIssueComment_Success(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)

	err := postGitHubIssueComment("test-token", "octocat", "hello-world", 42, "the run finished")
	if err != nil {
		t.Fatalf("postGitHubIssueComment() = %v, want nil", err)
	}
	if gotPath != "/repos/octocat/hello-world/issues/42/comments" {
		t.Errorf("request path = %q, want %q", gotPath, "/repos/octocat/hello-world/issues/42/comments")
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer test-token")
	}
	if !strings.Contains(gotBody, "the run finished") {
		t.Errorf("request body = %q, want it to contain the comment text", gotBody)
	}
}

func TestPostGitHubIssueComment_NonCreatedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	withGitHubAPIBase(t, srv.URL)

	if err := postGitHubIssueComment("token", "octocat", "hello-world", 42, "body"); err == nil {
		t.Error("postGitHubIssueComment() with a 403 response = nil error, want an error")
	}
}
