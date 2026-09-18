package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func withDockerLogsFetcher(t *testing.T, fn func(ctx context.Context, id string, tail int) ([]logEntry, error)) {
	t.Helper()
	orig := dockerLogsFetcher
	t.Cleanup(func() { dockerLogsFetcher = orig })
	dockerLogsFetcher = fn
}

// frame builds one multiplexed Docker logs frame for the given stream type
// (1 = stdout, 2 = stderr) and payload.
func frame(streamType byte, payload string) []byte {
	header := make([]byte, 8)
	header[0] = streamType
	binary.BigEndian.PutUint32(header[4:8], uint32(len(payload)))
	return append(header, []byte(payload)...)
}

func TestDemuxDockerLogs_Multiplexed(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(frame(1, "hello stdout\n"))
	buf.Write(frame(2, "line one\nline two\n"))

	entries, err := demuxDockerLogs(&buf, false)
	if err != nil {
		t.Fatalf("demuxDockerLogs() error = %v", err)
	}

	want := []logEntry{
		{Stream: "stdout", Text: "hello stdout"},
		{Stream: "stderr", Text: "line one"},
		{Stream: "stderr", Text: "line two"},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries = %+v, want %+v", entries, want)
	}
}

func TestDemuxDockerLogs_TTY(t *testing.T) {
	buf := bytes.NewBufferString("first line\nsecond line\n")

	entries, err := demuxDockerLogs(buf, true)
	if err != nil {
		t.Fatalf("demuxDockerLogs() error = %v", err)
	}

	want := []logEntry{
		{Stream: "stdout", Text: "first line"},
		{Stream: "stdout", Text: "second line"},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries = %+v, want %+v", entries, want)
	}
}

func TestDemuxDockerLogs_TruncatedFrame(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(frame(1, "complete\n"))
	// A header claiming more payload than actually follows.
	header := make([]byte, 8)
	header[0] = 1
	binary.BigEndian.PutUint32(header[4:8], 100)
	buf.Write(header)
	buf.WriteString("short")

	entries, err := demuxDockerLogs(&buf, false)
	if err == nil {
		t.Fatal("expected an error from a truncated frame, got nil")
	}
	if len(entries) != 1 || entries[0].Text != "complete" {
		t.Errorf("entries before the truncated frame = %+v, want the one complete entry", entries)
	}
}

func TestGatherLogs_Disabled(t *testing.T) {
	withDockerSocketPath(t, filepath.Join(t.TempDir(), "no-such-socket"))

	result := gatherLogs(context.Background(), "", defaultLogTail)
	if result.Enabled {
		t.Error("expected Enabled = false")
	}
	if result.Reason != "Disabled" {
		t.Errorf("Reason = %q, want Disabled", result.Reason)
	}
}

func TestGatherLogs_UnknownContainer(t *testing.T) {
	socketPath := shortTempSocketPath(t)
	containers := []dockerContainer{
		{ID: "selfid1234", Names: []string{"/homelab"}, State: "running", Status: "Up 1 minute"},
	}
	body, _ := json.Marshal(containers)
	startFakeDockerDaemon(t, socketPath, body)
	withDockerSocketPath(t, socketPath)

	origHostname := hostnameFunc
	t.Cleanup(func() { hostnameFunc = origHostname })
	hostnameFunc = func() (string, error) { return "selfid1234", nil }

	result := gatherLogs(context.Background(), "nope", defaultLogTail)
	if !result.Enabled {
		t.Fatalf("expected Enabled = true, got result = %+v", result)
	}
	if result.Reason == "" {
		t.Error("expected a non-empty Reason for an unknown container")
	}
}

func TestGatherLogs_Success(t *testing.T) {
	socketPath := shortTempSocketPath(t)
	containers := []dockerContainer{
		{ID: "selfid1234", Names: []string{"/homelab"}, State: "running", Status: "Up 1 minute"},
		{ID: "otherid5678", Names: []string{"/postgres"}, State: "running", Status: "Up 10 minutes"},
	}
	body, _ := json.Marshal(containers)
	startFakeDockerDaemon(t, socketPath, body)
	withDockerSocketPath(t, socketPath)

	origHostname := hostnameFunc
	t.Cleanup(func() { hostnameFunc = origHostname })
	hostnameFunc = func() (string, error) { return "selfid1234", nil }

	withDockerLogsFetcher(t, func(ctx context.Context, id string, tail int) ([]logEntry, error) {
		if id != "otherid5678" {
			t.Errorf("fetcher called with id = %q, want otherid5678", id)
		}
		return []logEntry{{Stream: "stdout", Text: "hello"}}, nil
	})

	result := gatherLogs(context.Background(), "postgres", defaultLogTail)
	if !result.Enabled {
		t.Fatalf("expected Enabled = true, got result = %+v", result)
	}
	if result.Container != "postgres" {
		t.Errorf("Container = %q, want postgres", result.Container)
	}
	if len(result.Entries) != 1 || result.Entries[0].Text != "hello" {
		t.Errorf("Entries = %+v", result.Entries)
	}
}

func TestGatherLogs_NoLogsYet(t *testing.T) {
	socketPath := shortTempSocketPath(t)
	containers := []dockerContainer{
		{ID: "selfid1234", Names: []string{"/homelab"}, State: "running", Status: "Up 1 minute"},
	}
	body, _ := json.Marshal(containers)
	startFakeDockerDaemon(t, socketPath, body)
	withDockerSocketPath(t, socketPath)

	origHostname := hostnameFunc
	t.Cleanup(func() { hostnameFunc = origHostname })
	hostnameFunc = func() (string, error) { return "selfid1234", nil }

	withDockerLogsFetcher(t, func(ctx context.Context, id string, tail int) ([]logEntry, error) {
		return nil, nil
	})

	result := gatherLogs(context.Background(), "", defaultLogTail)
	if !result.Enabled {
		t.Fatalf("expected Enabled = true, got result = %+v", result)
	}
	if result.Reason != "no logs yet" {
		t.Errorf("Reason = %q, want %q", result.Reason, "no logs yet")
	}
}

func TestListLogContainers_SelfFirst(t *testing.T) {
	socketPath := shortTempSocketPath(t)
	containers := []dockerContainer{
		{ID: "otherid5678", Names: []string{"/postgres"}, State: "running", Status: "Up 10 minutes"},
		{ID: "selfid1234", Names: []string{"/homelab"}, State: "running", Status: "Up 1 minute"},
	}
	body, _ := json.Marshal(containers)
	startFakeDockerDaemon(t, socketPath, body)
	withDockerSocketPath(t, socketPath)

	origHostname := hostnameFunc
	t.Cleanup(func() { hostnameFunc = origHostname })
	hostnameFunc = func() (string, error) { return "selfid1234", nil }

	got, err := listLogContainers(context.Background())
	if err != nil {
		t.Fatalf("listLogContainers() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 containers (self included), got %d: %+v", len(got), got)
	}
	if !got[0].Self || got[0].Name != "homelab" {
		t.Errorf("expected self (homelab) first, got %+v", got[0])
	}
	if got[1].Self {
		t.Errorf("expected postgres not marked self, got %+v", got[1])
	}
}

func TestHandleLogs_Disabled(t *testing.T) {
	withDockerSocketPath(t, filepath.Join(t.TempDir(), "no-such-socket"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	handleLogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Disabled") {
		t.Errorf("logs body missing %q, got: %s", "Disabled", body)
	}
}

func TestHandleAPILogs_TailBounds(t *testing.T) {
	withDockerSocketPath(t, filepath.Join(t.TempDir(), "no-such-socket"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/logs?tail=999999", nil)
	handleAPILogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got logsResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Enabled {
		t.Error("expected Enabled = false when docker socket is disabled")
	}
}
