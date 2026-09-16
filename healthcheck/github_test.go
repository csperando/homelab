package main

import (
	"encoding/json"
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
