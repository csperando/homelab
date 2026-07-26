package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

const dockerSocketPath = "/var/run/docker.sock"

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
