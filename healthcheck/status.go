package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const workspaceDir = "/root/workspace"

var startTime = time.Now()

type diskUsage struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

type memInfo struct {
	TotalKB uint64 `json:"total_kb"`
	AvailKB uint64 `json:"avail_kb"`
}

type repoStatus struct {
	Path     string           `json:"path"`
	Branch   string           `json:"branch"`
	Dirty    bool             `json:"dirty"`
	Coverage []coverageReport `json:"coverage,omitempty"`
}

type statusData struct {
	Status       string            `json:"status"`
	UptimeSecs   float64           `json:"uptime_seconds"`
	Timestamp    time.Time         `json:"timestamp"`
	ToolVersions map[string]string `json:"tool_versions"`
	Workspace    diskUsage         `json:"workspace_disk"`
	Memory       memInfo           `json:"memory"`
	LoadAvg      string            `json:"load_avg"`
	Repos        []repoStatus      `json:"repos"`
	Docker       dockerStatus      `json:"docker"`
	Agents       []runningAgent    `json:"agents"`
}

func toolVersion(name string, args ...string) string {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

func workspaceDiskUsage() diskUsage {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(workspaceDir, &stat); err != nil {
		return diskUsage{}
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	return diskUsage{
		TotalBytes: total,
		FreeBytes:  free,
		UsedBytes:  total - free,
	}
}

func readMemInfo() memInfo {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return memInfo{}
	}
	defer f.Close()

	var mi memInfo
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			mi.TotalKB = val
		case "MemAvailable:":
			mi.AvailKB = val
		}
	}
	return mi
}

func readLoadAvg() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "unavailable"
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return "unavailable"
	}
	return strings.Join(fields[:3], " ")
}

// scanWorkspaceRepos looks one level deep under workspaceDir for git repos
// (i.e. each top-level cloned project), reporting branch and dirty status.
func scanWorkspaceRepos() []repoStatus {
	entries, err := os.ReadDir(workspaceDir)
	if err != nil {
		return nil
	}

	var repos []repoStatus
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		repoPath := filepath.Join(workspaceDir, entry.Name())
		if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
			continue
		}

		branch := toolVersion("git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD")
		dirtyOut, err := exec.Command("git", "-C", repoPath, "status", "--porcelain").Output()
		dirty := err == nil && len(strings.TrimSpace(string(dirtyOut))) > 0

		repos = append(repos, repoStatus{
			Path:     entry.Name(),
			Branch:   branch,
			Dirty:    dirty,
			Coverage: findCoverageReports(repoPath),
		})
	}
	return repos
}

func gatherStatus() statusData {
	return statusData{
		Status:     "ok",
		UptimeSecs: time.Since(startTime).Seconds(),
		Timestamp:  time.Now().UTC(),
		ToolVersions: map[string]string{
			"go":   toolVersion("go", "version"),
			"node": toolVersion("node", "--version"),
			"npm":  toolVersion("npm", "--version"),
			"pnpm":   toolVersion("pnpm", "--version"),
			"git":    toolVersion("git", "--version"),
			"claude": toolVersion("claude", "--version"),
		},
		Workspace: workspaceDiskUsage(),
		Memory:    readMemInfo(),
		LoadAvg:   readLoadAvg(),
		Repos:     scanWorkspaceRepos(),
		Docker:    gatherDockerStatus(),
		Agents:    gatherAgentStatus(),
	}
}
