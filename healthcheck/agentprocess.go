package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// liveRun tracks one still-running `claude` subprocess in memory. Once it
// finishes, its outcome is persisted via UpdateAgentRunState and it's
// removed from this store — Postgres is the source of truth from that
// point on; this store only exists to serve a still-running run's transcript
// and to let the stop action signal its process.
type liveRun struct {
	RunID         int64
	WorkspaceID   int64
	Cmd           *exec.Cmd
	Lines         []string
	StopRequested bool
}

var (
	liveRunsMu sync.Mutex
	liveRuns   = map[int64]*liveRun{}
)

// registerLiveRun adds a newly-started run to the in-memory store.
func registerLiveRun(runID, workspaceID int64, cmd *exec.Cmd) {
	liveRunsMu.Lock()
	defer liveRunsMu.Unlock()
	liveRuns[runID] = &liveRun{RunID: runID, WorkspaceID: workspaceID, Cmd: cmd}
}

// appendLiveRunLine appends one JSONL line to a run's buffered transcript.
// A no-op if the run isn't (or is no longer) tracked.
func appendLiveRunLine(runID int64, line string) {
	liveRunsMu.Lock()
	defer liveRunsMu.Unlock()
	if r, ok := liveRuns[runID]; ok {
		r.Lines = append(r.Lines, line)
	}
}

// liveRunTranscript returns the accumulated transcript for a still-running
// run, and whether it's currently tracked at all.
func liveRunTranscript(runID int64) (string, bool) {
	liveRunsMu.Lock()
	defer liveRunsMu.Unlock()
	r, ok := liveRuns[runID]
	if !ok {
		return "", false
	}
	return strings.Join(r.Lines, "\n"), true
}

// liveRunProcess returns the *os.Process for a still-running run (for the
// stop action to signal) and whether it's currently tracked at all.
func liveRunProcess(runID int64) (*os.Process, bool) {
	liveRunsMu.Lock()
	defer liveRunsMu.Unlock()
	r, ok := liveRuns[runID]
	if !ok || r.Cmd == nil || r.Cmd.Process == nil {
		return nil, false
	}
	return r.Cmd.Process, true
}

// workspaceHasLiveRun reports whether workspaceID already has a run in
// progress, and that run's ID if so — the in-flight check a new Task start
// must pass. A linear scan is fine at this scale (a handful of concurrent
// runs at most), and this is the only place that needs to search by
// workspace rather than by run ID.
func workspaceHasLiveRun(workspaceID int64) (int64, bool) {
	liveRunsMu.Lock()
	defer liveRunsMu.Unlock()
	for _, r := range liveRuns {
		if r.WorkspaceID == workspaceID {
			return r.RunID, true
		}
	}
	return 0, false
}

// stopGracePeriod is how long requestStop waits after SIGTERM before
// escalating to SIGKILL — a var rather than a const so tests can shrink it.
var stopGracePeriod = 5 * time.Second

// requestStop signals runID's process with SIGTERM and marks it
// stop-requested, so runAgent's own completion path reports "stopped"
// instead of "failed" once the signal takes effect. Returns false if the
// run isn't tracked, or the signal couldn't be delivered (e.g. the process
// had already exited on its own) — in the latter case the flag is
// deliberately left unset, so a run that happened to finish naturally right
// as a stop was requested is never mis-reported as stopped.
func requestStop(runID int64) bool {
	liveRunsMu.Lock()
	r, ok := liveRuns[runID]
	liveRunsMu.Unlock()
	if !ok || r.Cmd == nil || r.Cmd.Process == nil {
		return false
	}

	if err := r.Cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return false
	}

	liveRunsMu.Lock()
	r.StopRequested = true
	liveRunsMu.Unlock()
	return true
}

// wasStopRequested reports whether runID was marked stop-requested —
// checked by runAgent right after its process exits, before unregistering.
func wasStopRequested(runID int64) bool {
	liveRunsMu.Lock()
	defer liveRunsMu.Unlock()
	r, ok := liveRuns[runID]
	return ok && r.StopRequested
}

// escalateStopAfterGracePeriod sends SIGKILL if runID is still tracked
// (i.e. it hasn't exited yet) once stopGracePeriod elapses after a SIGTERM.
func escalateStopAfterGracePeriod(runID int64) {
	time.Sleep(stopGracePeriod)
	if proc, ok := liveRunProcess(runID); ok {
		proc.Signal(syscall.SIGKILL)
	}
}

// unregisterLiveRun removes a finished run from the in-memory store.
func unregisterLiveRun(runID int64) {
	liveRunsMu.Lock()
	defer liveRunsMu.Unlock()
	delete(liveRuns, runID)
}

// agentToolsAllowlist is the filesystem/git/dev-commands tool set decided
// in goal.md — git and dev commands both run through Bash, since Claude
// Code has no separate git-specific tool. Finer per-command gating (e.g.
// blocking `git push`) is Phase 5's job, not this one.
const agentToolsAllowlist = "Read,Write,Edit,Bash"

// agentMaxBudgetUSD caps a single run's spend (claude's own --max-budget-usd
// flag) — read once at process start like githubToken.
var agentMaxBudgetUSD = envOrDefault("AGENT_MAX_BUDGET_USD", "1.00")

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// buildAgentArgs constructs the claude CLI argv for a headless run,
// separated from the *exec.Cmd construction so the security posture
// (restricted, no interactive prompts, explicit tool allowlist, budget cap)
// is directly unit-testable without spawning a process.
func buildAgentArgs(prompt string) []string {
	return []string{
		"--print",
		"--output-format", "stream-json",
		"--permission-prompts", "none",
		"--restricted",
		"--tools", agentToolsAllowlist,
		"--max-budget-usd", agentMaxBudgetUSD,
		"--",
		prompt,
	}
}

// agentRunner builds the *exec.Cmd for one agent run — a package-level var
// so tests can substitute a cheap fake process instead of the real claude
// CLI, mirroring cloneRunner's indirection pattern in clone.go.
var agentRunner = buildAgentCommand

func buildAgentCommand(workspacePath, prompt string) *exec.Cmd {
	cmd := exec.Command("claude", buildAgentArgs(prompt)...)
	cmd.Dir = workspacePath
	return cmd
}

// claudeJSONLine is the minimal subset of a --output-format stream-json
// line this package reads: every line carries session_id once the run is
// underway, and the final line is type "result", whose is_error field is
// the authoritative success/failure signal.
type claudeJSONLine struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
}

// startAgentRun launches a claude subprocess for run in the background,
// streaming its stdout into the in-memory live-run store and persisting
// the final outcome to Postgres once it completes. Runs in its own
// goroutine, mirroring runClone's goroutine in clone.go.
func startAgentRun(runID, workspaceID int64, workspacePath, prompt string) {
	go runAgent(runID, workspaceID, workspacePath, prompt)
}

func runAgent(runID, workspaceID int64, workspacePath, prompt string) {
	cmd := agentRunner(workspacePath, prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		finishAgentRun(runID, agentRunFailed, "", fmt.Sprintf("creating stdout pipe: %v", err))
		return
	}

	if err := cmd.Start(); err != nil {
		finishAgentRun(runID, agentRunFailed, "", fmt.Sprintf("starting claude: %v", err))
		return
	}

	registerLiveRun(runID, workspaceID, cmd)
	if err := UpdateAgentRunState(db, runID, agentRunRunning, "", ""); err != nil {
		log.Printf("agent run %d: failed to record running state: %v", runID, err)
	}

	sawResult, succeeded, sawSessionID := false, false, false

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		appendLiveRunLine(runID, line)

		var msg claudeJSONLine
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.SessionID != "" && !sawSessionID {
			if err := SetAgentRunClaudeSessionID(db, runID, msg.SessionID); err != nil {
				log.Printf("agent run %d: failed to record claude session id: %v", runID, err)
			}
			sawSessionID = true
		}
		if msg.Type == "result" {
			sawResult = true
			succeeded = !msg.IsError
		}
	}

	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	transcript, _ := liveRunTranscript(runID)
	stopped := wasStopRequested(runID)
	unregisterLiveRun(runID)

	state := agentRunFailed
	errMsg := ""
	switch {
	case stopped:
		state = agentRunStopped
	case waitErr == nil && scanErr == nil && sawResult && succeeded:
		state = agentRunSucceeded
	case waitErr != nil:
		errMsg = waitErr.Error()
	case scanErr != nil:
		errMsg = fmt.Sprintf("reading claude output: %v", scanErr)
	case !sawResult:
		errMsg = "claude exited without a successful result"
	default:
		errMsg = "claude reported a failed result"
	}

	finishAgentRun(runID, state, transcript, errMsg)
}

// finishAgentRun persists a run's final outcome, but only if it isn't
// already terminal — guards against a race with the (not-yet-built) stop
// action, which may finalize the run as "stopped" concurrently with this
// goroutine observing the killed process's exit.
func finishAgentRun(runID int64, state agentRunState, transcript, errMsg string) {
	current, err := GetAgentRun(db, runID)
	if err == nil && isTerminalAgentRunState(current.State) {
		return
	}
	if err := UpdateAgentRunState(db, runID, state, transcript, errMsg); err != nil {
		log.Printf("agent run %d: failed to persist final state: %v", runID, err)
	}
}
