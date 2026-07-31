package main

import (
	"bufio"
	"os"
	"path/filepath"
	"time"
)

const agentsRunningDir = "/root/.claude/agents/running"

// runningAgent is a currently-active Claude Code subagent, tracked by the
// SubagentStart/SubagentStop hooks (.claude/hooks/) writing/removing a file
// per agent under agentsRunningDir.
type runningAgent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	StartedAt time.Time `json:"started_at"`
}

// parseAgentFile reads a running-agent state file: line 1 is the agent
// type, line 2 is an RFC3339 start timestamp.
func parseAgentFile(path string) (agentType string, startedAt time.Time, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return "", time.Time{}, false
	}
	agentType = scanner.Text()
	if !scanner.Scan() {
		return "", time.Time{}, false
	}
	startedAt, err = time.Parse(time.RFC3339, scanner.Text())
	if err != nil {
		return "", time.Time{}, false
	}
	return agentType, startedAt, true
}

// gatherAgentStatus lists currently-running Claude Code subagents by
// reading agentsRunningDir. Degrades to nil if the directory doesn't exist
// yet (no subagent has ever run this boot) or an entry fails to parse,
// consistent with the degrade-gracefully pattern used elsewhere in this
// package (e.g. workspaceDiskUsage, gatherDockerStatus).
func gatherAgentStatus() []runningAgent {
	entries, err := os.ReadDir(agentsRunningDir)
	if err != nil {
		return nil
	}

	var agents []runningAgent
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		agentType, startedAt, ok := parseAgentFile(filepath.Join(agentsRunningDir, entry.Name()))
		if !ok {
			continue
		}
		agents = append(agents, runningAgent{
			ID:        entry.Name(),
			Type:      agentType,
			StartedAt: startedAt,
		})
	}
	return agents
}
