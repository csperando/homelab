package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// logEntry is one line of container log output. Stream is "stdout" or
// "stderr" — except for a container started with a TTY (the homelab
// container itself, for interactive `make shell` use), where Docker merges
// both streams and doesn't preserve the distinction, so Stream is always
// "stdout" there. Text keeps Docker's inserted RFC3339Nano timestamp
// (requested via timestamps=true) as its leading token.
type logEntry struct {
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// logsResult is the logs tab's degrade-gracefully result, mirroring
// dockerStatus/repoListStatus: either a populated, enabled result, or a
// disabled/empty state with a human-readable reason instead of a
// propagated error — a handler can always render something.
type logsResult struct {
	Enabled    bool                 `json:"enabled"`
	Containers []dockerLogContainer `json:"containers,omitempty"`
	Container  string               `json:"container,omitempty"`
	Entries    []logEntry           `json:"entries,omitempty"`
	Reason     string               `json:"reason,omitempty"`
}

// isKnownContainer validates a client-supplied container name against the
// actual, currently discovered set — never used directly to build a Docker
// socket path without this check.
func isKnownContainer(name string, containers []dockerLogContainer) (dockerLogContainer, bool) {
	for _, c := range containers {
		if c.Name == name {
			return c, true
		}
	}
	return dockerLogContainer{}, false
}

// dockerInspect mirrors the subset of GET /containers/{id}/json we read.
type dockerInspect struct {
	Config struct {
		Tty bool `json:"Tty"`
	} `json:"Config"`
}

// containerIsTTY reports whether a container was started with a TTY, which
// determines whether its log stream is raw or multiplexed (see
// demuxDockerLogs).
func containerIsTTY(ctx context.Context, id string) (bool, error) {
	var insp dockerInspect
	if err := dockerGet(ctx, "/containers/"+url.PathEscape(id)+"/json", &insp); err != nil {
		return false, err
	}
	return insp.Config.Tty, nil
}

// dockerGetRaw issues a GET against the Docker Engine API and returns the
// raw response body, for endpoints (like container logs) that aren't JSON.
func dockerGetRaw(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := dockerHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("docker api %s: unexpected status %d", path, resp.StatusCode)
	}
	return resp.Body, nil
}

// splitLogLines splits a chunk of raw log text into non-empty lines, all
// tagged with the given stream.
func splitLogLines(text, stream string) []logEntry {
	var entries []logEntry
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if line != "" {
			entries = append(entries, logEntry{Stream: stream, Text: line})
		}
	}
	return entries
}

// demuxDockerLogs parses a GET /containers/{id}/logs response body. A
// container started with a TTY returns a raw stream with stdout/stderr
// already merged (Docker doesn't preserve the distinction in that mode —
// not something this function can recover, not a bug to "fix" here). A
// non-TTY container returns a stream multiplexed into frames of
// [stream-type(1) 0 0 0 size(4 big-endian)] + payload, stream-type 1 =
// stdout, 2 = stderr.
func demuxDockerLogs(r io.Reader, tty bool) ([]logEntry, error) {
	if tty {
		body, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		return splitLogLines(string(body), "stdout"), nil
	}

	var entries []logEntry
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if err == io.EOF {
				break
			}
			return entries, err
		}
		stream := "stdout"
		if header[0] == 2 {
			stream = "stderr"
		}
		size := binary.BigEndian.Uint32(header[4:8])
		payload := make([]byte, size)
		if _, err := io.ReadFull(r, payload); err != nil {
			return entries, err
		}
		entries = append(entries, splitLogLines(string(payload), stream)...)
	}
	return entries, nil
}

// fetchContainerLogs is the default dockerLogsFetcher implementation: one
// inspect call to determine framing, then the bounded (non-following)
// GET .../logs call itself.
func fetchContainerLogs(ctx context.Context, id string, tail int) ([]logEntry, error) {
	tty, err := containerIsTTY(ctx, id)
	if err != nil {
		return nil, err
	}

	query := url.Values{
		"stdout":     {"true"},
		"stderr":     {"true"},
		"timestamps": {"true"},
		"tail":       {strconv.Itoa(tail)},
	}
	body, err := dockerGetRaw(ctx, "/containers/"+url.PathEscape(id)+"/logs?"+query.Encode())
	if err != nil {
		return nil, err
	}
	defer body.Close()

	return demuxDockerLogs(body, tty)
}

// dockerLogsFetcher is a package-level var so tests can substitute a fake
// without a real Docker socket, mirroring cloneRunner in clone.go.
var dockerLogsFetcher = fetchContainerLogs

// gatherLogs degrades gracefully rather than erroring, consistent with
// gatherDockerStatus/gatherRepoList: any precondition failure or API error
// yields a Reason instead of propagating an error, so a handler can always
// render something.
func gatherLogs(ctx context.Context, requested string, tail int) logsResult {
	info, err := os.Stat(dockerSocketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return logsResult{Reason: "Disabled"}
	}

	containers, err := listLogContainers(ctx)
	if err != nil {
		return logsResult{Reason: fmt.Sprintf("docker api query failed: %v", err)}
	}
	if len(containers) == 0 {
		return logsResult{Reason: "no containers found on homelab-net"}
	}

	target := containers[0] // self, first per listLogContainers' self-first ordering
	if requested != "" {
		c, ok := isKnownContainer(requested, containers)
		if !ok {
			return logsResult{Enabled: true, Containers: containers, Reason: fmt.Sprintf("unknown container %q", requested)}
		}
		target = c
	}

	entries, err := dockerLogsFetcher(ctx, target.ID, tail)
	if err != nil {
		return logsResult{Enabled: true, Containers: containers, Container: target.Name, Reason: fmt.Sprintf("fetching logs: %v", err)}
	}
	if len(entries) == 0 {
		return logsResult{Enabled: true, Containers: containers, Container: target.Name, Reason: "no logs yet"}
	}
	return logsResult{Enabled: true, Containers: containers, Container: target.Name, Entries: entries}
}
