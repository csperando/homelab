package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	dockerSocketPath = "/var/run/docker.sock"
	homelabNetwork   = "homelab-net"
)

// dockerHTTPClient talks to the Docker Engine API over the local Unix
// socket. It only ever issues GET requests (see dockerGet) — read-only
// visibility, never exec/write endpoints — and times out quickly so a slow
// or unresponsive daemon can't block /api/status or dashboard page loads.
var dockerHTTPClient = &http.Client{
	Timeout: 2 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", dockerSocketPath)
		},
	},
}

// dockerGet issues a GET request against the Docker Engine API and decodes
// the JSON response into v.
func dockerGet(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return err
	}
	resp, err := dockerHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker api %s: unexpected status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// dockerContainerPort mirrors the subset of the Engine API's per-container
// port entry we care about.
type dockerContainerPort struct {
	PrivatePort uint16 `json:"PrivatePort"`
	Type        string `json:"Type"`
}

// dockerContainer mirrors the subset of a GET /containers/json list entry
// we read — deliberately excludes Env/Labels, which can carry secrets (e.g.
// POSTGRES_PASSWORD), so they never reach dockerService or any output.
type dockerContainer struct {
	ID     string                `json:"Id"`
	Names  []string              `json:"Names"`
	Image  string                `json:"Image"`
	State  string                `json:"State"`
	Status string                `json:"Status"`
	Ports  []dockerContainerPort `json:"Ports"`
}

// dockerService is the sanitized, dashboard-facing view of one infra
// service container.
type dockerService struct {
	Name   string   `json:"name"`
	Image  string   `json:"image"`
	State  string   `json:"state"`
	Health string   `json:"health,omitempty"`
	Ports  []string `json:"ports,omitempty"`
	Uptime string   `json:"uptime"`
}

// containerStatusHealthRe extracts the health suffix Docker appends to a
// container's human-readable Status string when it has a HEALTHCHECK
// defined, e.g. "Up 5 minutes (healthy)".
var containerStatusHealthRe = regexp.MustCompile(`\((healthy|unhealthy|health: starting)\)`)

// parseContainerStatus splits a container's Status string (e.g. "Up 5
// minutes (healthy)" or "Exited (0) 3 minutes ago") into a health label and
// the remaining human-readable uptime/duration text.
func parseContainerStatus(status string) (health, uptime string) {
	loc := containerStatusHealthRe.FindStringSubmatchIndex(status)
	if loc == nil {
		return "", strings.TrimSpace(status)
	}
	return status[loc[2]:loc[3]], strings.TrimSpace(status[:loc[0]])
}

// formatContainerPorts renders a container's exposed ports as "port/proto"
// strings, deduplicated.
func formatContainerPorts(ports []dockerContainerPort) []string {
	seen := make(map[string]bool)
	var out []string
	for _, p := range ports {
		s := fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// discoverDockerServices lists containers attached to homelab-net,
// excluding the homelab container itself (identified by comparing its ID
// against os.Hostname(), which Docker sets to the container's own short ID
// by default — robust to a future container_name rename, unlike matching
// on name).
func discoverDockerServices(ctx context.Context) ([]dockerService, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}

	filters, err := json.Marshal(map[string][]string{"network": {homelabNetwork}})
	if err != nil {
		return nil, err
	}
	query := url.Values{
		"all":     {"true"},
		"filters": {string(filters)},
	}

	var containers []dockerContainer
	if err := dockerGet(ctx, "/containers/json?"+query.Encode(), &containers); err != nil {
		return nil, err
	}

	var services []dockerService
	for _, c := range containers {
		if strings.HasPrefix(c.ID, hostname) {
			continue
		}
		health, uptime := parseContainerStatus(c.Status)
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		services = append(services, dockerService{
			Name:   name,
			Image:  c.Image,
			State:  c.State,
			Health: health,
			Ports:  formatContainerPorts(c.Ports),
			Uptime: uptime,
		})
	}
	return services, nil
}

// dockerStatus is the dashboard-facing result of a docker discovery
// attempt: either a populated, enabled service list, or a disabled state
// with a human-readable reason (socket not mounted, API error, etc.).
type dockerStatus struct {
	Enabled  bool            `json:"enabled"`
	Services []dockerService `json:"services,omitempty"`
	Reason   string          `json:"reason,omitempty"`
}

// gatherDockerStatus degrades gracefully rather than erroring: if the
// socket isn't mounted (the default — see docker-compose.yml's
// DOCKER_SOCK_PATH opt-in) or the API query fails for any reason, it
// returns a disabled result with an explanatory reason instead of
// propagating an error, consistent with the zero-value-on-error pattern
// used elsewhere in this package (e.g. workspaceDiskUsage, readMemInfo).
// Note that /healthz never calls this — only /api/status and the
// dashboard do — so a slow or unreachable daemon can't affect the cheap
// liveness check Docker's own HEALTHCHECK polls.
func gatherDockerStatus() dockerStatus {
	info, err := os.Stat(dockerSocketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return dockerStatus{Reason: "Disabled"}
	}

	services, err := discoverDockerServices(context.Background())
	if err != nil {
		return dockerStatus{Reason: fmt.Sprintf("docker api query failed: %v", err)}
	}

	return dockerStatus{Enabled: true, Services: services}
}
