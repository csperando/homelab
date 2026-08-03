package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
	}
	for _, c := range cases {
		if got := formatBytes(c.in); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatKB(t *testing.T) {
	if got, want := formatKB(1024), "1.0 MiB"; got != want {
		t.Errorf("formatKB(1024) = %q, want %q", got, want)
	}
	if got, want := formatKB(0), "0 B"; got != want {
		t.Errorf("formatKB(0) = %q, want %q", got, want)
	}
}

func TestToView(t *testing.T) {
	ts := time.Now().UTC()
	s := statusData{
		Status:       "ok",
		Timestamp:    ts,
		ToolVersions: map[string]string{"go": "go1.22"},
		Workspace:    diskUsage{TotalBytes: 2048, UsedBytes: 1024, FreeBytes: 1024},
		Memory:       memInfo{TotalKB: 2048, AvailKB: 1024},
		LoadAvg:      "0.1 0.2 0.3",
		Repos:        []repoStatus{{Path: "foo", Branch: "main"}},
		Docker:       dockerStatus{Reason: "Disabled"},
	}

	v := toView(s)

	if v.Status != "ok" {
		t.Errorf("Status = %q, want ok", v.Status)
	}
	if v.Timestamp != ts.Format(time.RFC3339) {
		t.Errorf("Timestamp = %q, want %q", v.Timestamp, ts.Format(time.RFC3339))
	}
	if v.ToolVersions["go"] != "go1.22" {
		t.Errorf("ToolVersions[go] = %q, want go1.22", v.ToolVersions["go"])
	}
	if v.DiskUsed != "1.0 KiB" || v.DiskTotal != "2.0 KiB" {
		t.Errorf("DiskUsed/DiskTotal = %q/%q, want 1.0 KiB/2.0 KiB", v.DiskUsed, v.DiskTotal)
	}
	if v.MemAvail != "1.0 MiB" || v.MemTotal != "2.0 MiB" {
		t.Errorf("MemAvail/MemTotal = %q/%q, want 1.0 MiB/2.0 MiB", v.MemAvail, v.MemTotal)
	}
	if v.LoadAvg != "0.1 0.2 0.3" {
		t.Errorf("LoadAvg = %q", v.LoadAvg)
	}
	if len(v.Repos) != 1 || v.Repos[0].Path != "foo" {
		t.Errorf("Repos = %+v", v.Repos)
	}
	if v.Docker.Reason != "Disabled" {
		t.Errorf("Docker.Reason = %q", v.Docker.Reason)
	}
}

func TestIsCoveragePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"coverage/index.html", true},
		{"repo/coverage/lcov-report/index.html", true},
		{"repo/src/index.html", false},
		{"", false},
		{"coverage", true},
	}
	for _, c := range cases {
		if got := isCoveragePath(c.path); got != c.want {
			t.Errorf("isCoveragePath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestCoverageOnly(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Inner-Called", "1")
	})
	h := coverageOnly(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/repo/coverage/index.html", nil)
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Inner-Called") != "1" {
		t.Error("expected inner handler to be called for a coverage path")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/repo/src/index.html", nil)
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Inner-Called") == "1" {
		t.Error("expected inner handler NOT to be called for a non-coverage path")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	handleHealthz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	for _, key := range []string{"status", "uptime_seconds", "timestamp"} {
		if _, ok := body[key]; !ok {
			t.Errorf("missing key %q in response body %v", key, body)
		}
	}
}

// withTempStatusFixtures points every DI'd path var at throwaway locations
// and restores the originals on cleanup, so handlers that call gatherStatus
// run deterministically regardless of the host machine.
func withTempStatusFixtures(t *testing.T) {
	t.Helper()

	origWorkspace, origSocket, origMem, origLoad := workspaceDir, dockerSocketPath, procMeminfoPath, procLoadavgPath
	t.Cleanup(func() {
		workspaceDir, dockerSocketPath, procMeminfoPath, procLoadavgPath = origWorkspace, origSocket, origMem, origLoad
	})

	workspaceDir = t.TempDir()
	dockerSocketPath = workspaceDir + "/no-such-socket"
	procMeminfoPath = workspaceDir + "/no-such-meminfo"
	procLoadavgPath = workspaceDir + "/no-such-loadavg"
}

func TestHandleAPIStatus(t *testing.T) {
	withTempStatusFixtures(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	handleAPIStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var s statusData
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if s.Status != "ok" {
		t.Errorf("Status = %q, want ok", s.Status)
	}
	if s.Docker.Enabled {
		t.Error("expected Docker.Enabled = false with no socket present")
	}
}

func TestHandleDashboard(t *testing.T) {
	withTempStatusFixtures(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handleDashboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"<html", "homelab dev container", "uptime"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard body missing %q", want)
		}
	}
}
