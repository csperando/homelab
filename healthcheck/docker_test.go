package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParseContainerStatus(t *testing.T) {
	cases := []struct {
		in         string
		wantHealth string
		wantUptime string
	}{
		{"Up 5 minutes (healthy)", "healthy", "Up 5 minutes"},
		{"Up 2 minutes (health: starting)", "health: starting", "Up 2 minutes"},
		{"Exited (0) 3 minutes ago", "", "Exited (0) 3 minutes ago"},
		{"Up 10 seconds", "", "Up 10 seconds"},
	}
	for _, c := range cases {
		health, uptime := parseContainerStatus(c.in)
		if health != c.wantHealth || uptime != c.wantUptime {
			t.Errorf("parseContainerStatus(%q) = (%q, %q), want (%q, %q)",
				c.in, health, uptime, c.wantHealth, c.wantUptime)
		}
	}
}

func TestFormatContainerPorts(t *testing.T) {
	ports := []dockerContainerPort{
		{PrivatePort: 80, Type: "tcp"},
		{PrivatePort: 80, Type: "tcp"},
		{PrivatePort: 443, Type: "tcp"},
	}
	got := formatContainerPorts(ports)
	want := []string{"80/tcp", "443/tcp"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("formatContainerPorts() = %v, want %v", got, want)
	}
}

func withDockerSocketPath(t *testing.T, path string) {
	t.Helper()
	orig := dockerSocketPath
	t.Cleanup(func() { dockerSocketPath = orig })
	dockerSocketPath = path
}

func TestGatherDockerStatusDisabled(t *testing.T) {
	withDockerSocketPath(t, filepath.Join(t.TempDir(), "no-such-socket"))

	status := gatherDockerStatus()
	if status.Enabled {
		t.Error("expected Enabled = false")
	}
	if status.Reason != "Disabled" {
		t.Errorf("Reason = %q, want Disabled", status.Reason)
	}
}

// shortTempSocketPath returns a short, flat path for a Unix socket listener.
// t.TempDir() embeds the test name into the path, which can exceed the
// ~104-byte sun_path limit on macOS (Linux allows more headroom), so this
// uses os.TempDir() directly with a short random suffix instead.
func shortTempSocketPath(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "hc-docker-*.sock")
	if err != nil {
		t.Fatalf("failed to allocate temp socket path: %v", err)
	}
	path := f.Name()
	f.Close()
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })
	return path
}

// startFakeDockerDaemon serves containersJSON for GET /containers/json on a
// Unix socket at socketPath, mimicking the Docker Engine API surface that
// dockerGet talks to.
func startFakeDockerDaemon(t *testing.T, socketPath string, containersJSON []byte) {
	t.Helper()

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to listen on unix socket: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(containersJSON)
	})
	srv := &http.Server{Handler: mux}

	go srv.Serve(ln)
	t.Cleanup(func() {
		srv.Close()
	})
}

func TestGatherDockerStatusEnabled(t *testing.T) {
	socketPath := shortTempSocketPath(t)

	containers := []dockerContainer{
		{
			ID:     "selfcontainerid1234",
			Names:  []string{"/healthcheck"},
			Image:  "homelab/healthcheck",
			State:  "running",
			Status: "Up 1 minute (healthy)",
		},
		{
			ID:     "othercontainerid5678",
			Names:  []string{"/postgres"},
			Image:  "postgres:16",
			State:  "running",
			Status: "Up 10 minutes (healthy)",
			Ports:  []dockerContainerPort{{PrivatePort: 5432, Type: "tcp"}},
		},
	}
	body, err := json.Marshal(containers)
	if err != nil {
		t.Fatal(err)
	}
	startFakeDockerDaemon(t, socketPath, body)

	withDockerSocketPath(t, socketPath)

	origHostname := hostnameFunc
	t.Cleanup(func() { hostnameFunc = origHostname })
	hostnameFunc = func() (string, error) { return "selfcontainerid1234", nil }

	status := gatherDockerStatus()
	if !status.Enabled {
		t.Fatalf("expected Enabled = true, got status = %+v", status)
	}
	if len(status.Services) != 1 {
		t.Fatalf("expected 1 service (self excluded), got %d: %+v", len(status.Services), status.Services)
	}

	svc := status.Services[0]
	if svc.Name != "postgres" {
		t.Errorf("Name = %q, want postgres", svc.Name)
	}
	if svc.Image != "postgres:16" {
		t.Errorf("Image = %q, want postgres:16", svc.Image)
	}
	if svc.Health != "healthy" {
		t.Errorf("Health = %q, want healthy", svc.Health)
	}
	if svc.Uptime != "Up 10 minutes" {
		t.Errorf("Uptime = %q, want %q", svc.Uptime, "Up 10 minutes")
	}
	if !reflect.DeepEqual(svc.Ports, []string{"5432/tcp"}) {
		t.Errorf("Ports = %v, want [5432/tcp]", svc.Ports)
	}
}

func TestDiscoverDockerServicesExcludesSelf(t *testing.T) {
	socketPath := shortTempSocketPath(t)
	containers := []dockerContainer{
		{ID: "selfid", Names: []string{"/self"}, State: "running", Status: "Up 1 second"},
	}
	body, _ := json.Marshal(containers)
	startFakeDockerDaemon(t, socketPath, body)

	withDockerSocketPath(t, socketPath)
	origHostname := hostnameFunc
	t.Cleanup(func() { hostnameFunc = origHostname })
	hostnameFunc = func() (string, error) { return "selfid", nil }

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	services, err := discoverDockerServices(ctx)
	if err != nil {
		t.Fatalf("discoverDockerServices() error = %v", err)
	}
	if len(services) != 0 {
		t.Errorf("expected self container to be excluded, got %+v", services)
	}
}
