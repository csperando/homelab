package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
	RunID           int64
	WorkspaceID     int64
	ClaudeSessionID string
	Cmd             *exec.Cmd
	Lines           []string
	StopRequested   bool
}

var (
	liveRunsMu sync.Mutex
	liveRuns   = map[int64]*liveRun{}
)

// registerLiveRun adds a newly-started run to the in-memory store. sessionID
// is the UUID runAgent generated and passed to claude via --session-id
// (known upfront, not read back from the JSONL stream) — see
// liveRunBySessionID, which the approval hook handler uses to map a hook
// callback's session_id back to a run.
func registerLiveRun(runID, workspaceID int64, sessionID string, cmd *exec.Cmd) {
	liveRunsMu.Lock()
	defer liveRunsMu.Unlock()
	liveRuns[runID] = &liveRun{RunID: runID, WorkspaceID: workspaceID, ClaudeSessionID: sessionID, Cmd: cmd}
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

// liveRunBySessionID maps a claude session_id (as reported in a PreToolUse
// hook callback's payload) back to our internal run ID — a linear scan,
// same scale justification as workspaceHasLiveRun. Returns false if the
// session isn't tracked (e.g. an unrecognized/stale session_id).
func liveRunBySessionID(sessionID string) (int64, bool) {
	liveRunsMu.Lock()
	defer liveRunsMu.Unlock()
	for _, r := range liveRuns {
		if r.ClaudeSessionID == sessionID {
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

// newUUIDv4 generates an RFC 4122 version 4 UUID from crypto/rand — hand-
// rolled rather than pulling in a dependency for this alone.
func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("reading random bytes for a UUID: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// agentHookSecret authenticates PreToolUse hook callbacks from a claude
// subprocess back to this dashboard's own HTTP API (see
// handleAPIAgentsPreToolUseHook in main.go). Read once like githubToken —
// never generated or set by this program; provisioning it is a manual step
// (see .env.sample). Left unset, Approval gating is simply never attached
// to agent runs (see buildAgentArgs) rather than attaching a hook nothing
// could ever authenticate against.
var agentHookSecret = os.Getenv("AGENT_HOOK_SECRET")

// agentHookSecretWarning returns a warning message when AGENT_HOOK_SECRET
// isn't set, mirroring githubExposureWarning's pattern (a startup-time,
// once-only check — not logged per run). Returns "" when there's nothing to
// warn about.
func agentHookSecretWarning() string {
	if agentHookSecret != "" {
		return ""
	}
	return "AGENT_HOOK_SECRET not set — approval gating disabled, gated commands (git commit/push, rm -rf, ...) execute unattended like every other allowed tool call"
}

const agentHookURL = "http://localhost:55123/api/agents/hooks/pre-tool-use"

// ensureAgentHookSettingsFile writes the static settings.json an agent run
// needs to attach the PreToolUse approval hook, mirroring clone.go's
// ensureAskpassScript (write-each-time, content is static so this is cheap
// and idempotent). HTTP hooks can only be configured via a real settings
// file, not an inline --settings JSON string (confirmed against Anthropic's
// docs) — nothing per-run needs to go in it, since the hook handler
// correlates a callback to a run via session_id, not anything in this file.
// The secret itself is never written here — only a reference to its env var
// name, resolved by claude itself via allowedEnvVars interpolation.
func ensureAgentHookSettingsFile() (string, error) {
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "Bash",
					"hooks": []map[string]any{
						{
							"type": "http",
							"url":  agentHookURL,
							"headers": map[string]string{
								"Authorization": "Bearer ${AGENT_HOOK_SECRET}",
							},
							"allowedEnvVars": []string{"AGENT_HOOK_SECRET"},
						},
					},
				},
			},
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling agent hook settings: %w", err)
	}

	path := filepath.Join(os.TempDir(), "homelab-agent-hook-settings.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("writing agent hook settings file: %w", err)
	}
	return path, nil
}

// buildAgentArgs constructs the claude CLI argv for a headless run,
// separated from the *exec.Cmd construction so the security posture
// (restricted, no interactive prompts, explicit tool allowlist, budget cap)
// is directly unit-testable without spawning a process. --verbose is
// required alongside --print --output-format=stream-json — confirmed live
// (claude refuses to start otherwise: "Error: When using --print,
// --output-format=stream-json requires --verbose"). sessionID is generated
// by the caller (runAgent) rather than left for Claude Code to assign, so
// the approval hook can map a callback straight back to a run — see
// liveRunBySessionID. When resume is true, sessionID is an *existing*
// claude session to continue (--resume) rather than a fresh one to start
// (--session-id) — used by the approve action to genuinely continue the
// same conversation after a gated command was denied, not start over.
// Attaches the approval hook's --settings file only when agentHookSecret is
// configured (see ensureAgentHookSettingsFile); this is the one place in
// this function with a filesystem side effect, and a failure here degrades
// to "no hook attached" rather than failing the run.
func buildAgentArgs(prompt, sessionID string, resume bool) []string {
	args := []string{
		"--print",
		"--verbose",
		"--output-format", "stream-json",
		"--permission-prompts", "none",
		"--restricted",
		"--tools", agentToolsAllowlist,
		"--max-budget-usd", agentMaxBudgetUSD,
	}
	if resume {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}

	if agentHookSecret != "" {
		if path, err := ensureAgentHookSettingsFile(); err != nil {
			log.Printf("agent hook settings file: %v — approval gating disabled for this run", err)
		} else {
			args = append(args, "--settings", path)
		}
	}

	return append(args, "--", prompt)
}

// agentRunner builds the *exec.Cmd for one agent run — a package-level var
// so tests can substitute a cheap fake process instead of the real claude
// CLI, mirroring cloneRunner's indirection pattern in clone.go.
var agentRunner = buildAgentCommand

func buildAgentCommand(workspacePath, prompt, sessionID string, resume bool) *exec.Cmd {
	cmd := exec.Command("claude", buildAgentArgs(prompt, sessionID, resume)...)
	cmd.Dir = workspacePath
	return cmd
}

// claudeJSONLine is the minimal subset of a --output-format stream-json
// line this package reads: the final line is type "result", whose is_error
// field is the authoritative success/failure signal. session_id isn't read
// here — runAgent generates and passes it upfront (see buildAgentArgs), so
// there's no need to parse it back out of the stream.
type claudeJSONLine struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
}

// startAgentRun launches a claude subprocess for run in the background,
// streaming its stdout into the in-memory live-run store and persisting
// the final outcome to Postgres once it completes. Runs in its own
// goroutine, mirroring runClone's goroutine in clone.go. Starts a fresh
// claude session — see startResumedAgentRun for continuing an existing one.
func startAgentRun(runID, workspaceID int64, workspacePath, prompt string) {
	go runAgent(runID, workspaceID, workspacePath, prompt, "")
}

// startResumedAgentRun is startAgentRun's counterpart for the approve
// action: continues resumeSessionID (an existing claude session, from the
// run whose gated command was denied) rather than starting a new one.
func startResumedAgentRun(runID, workspaceID int64, workspacePath, prompt, resumeSessionID string) {
	go runAgent(runID, workspaceID, workspacePath, prompt, resumeSessionID)
}

// runAgent spawns and drives one claude subprocess. resumeSessionID, when
// non-empty, resumes that existing session (--resume) instead of starting a
// fresh one (--session-id, a newly generated UUID).
func runAgent(runID, workspaceID int64, workspacePath, prompt, resumeSessionID string) {
	resume := resumeSessionID != ""
	sessionID := resumeSessionID
	if !resume {
		sessionID = newUUIDv4()
	}
	cmd := agentRunner(workspacePath, prompt, sessionID, resume)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		finishAgentRun(runID, agentRunFailed, "", fmt.Sprintf("creating stdout pipe: %v", err))
		return
	}

	if err := cmd.Start(); err != nil {
		finishAgentRun(runID, agentRunFailed, "", fmt.Sprintf("starting claude: %v", err))
		return
	}

	// Registered before the process can possibly reach a tool call, so the
	// approval hook (which fires synchronously mid-run) can always resolve
	// this session_id back to a run via liveRunBySessionID.
	registerLiveRun(runID, workspaceID, sessionID, cmd)
	if err := SetAgentRunClaudeSessionID(db, runID, sessionID); err != nil {
		log.Printf("agent run %d: failed to record claude session id: %v", runID, err)
	}
	if err := UpdateAgentRunState(db, runID, agentRunRunning, "", ""); err != nil {
		log.Printf("agent run %d: failed to record running state: %v", runID, err)
	}

	sawResult, succeeded := false, false

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		appendLiveRunLine(runID, line)

		var msg claudeJSONLine
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
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
	case hasPendingApproval(runID):
		state = agentRunAwaitingApproval
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
// already terminal — defense in depth alongside runAgent's own stopped/
// hasPendingApproval overrides, in case anything else ever finalizes a run
// concurrently.
func finishAgentRun(runID int64, state agentRunState, transcript, errMsg string) {
	current, err := GetAgentRun(db, runID)
	if err == nil && isTerminalAgentRunState(current.State) {
		return
	}
	if err := UpdateAgentRunState(db, runID, state, transcript, errMsg); err != nil {
		log.Printf("agent run %d: failed to persist final state: %v", runID, err)
	}
}

// findPendingApproval returns runID's unresolved Approval, if any — nil
// otherwise (including on a query error, which callers treat the same as
// "none": the security-critical decision, denying the tool call, already
// happened in the hook handler regardless of this lookup; it only affects
// how the run's status/UI is presented afterward).
func findPendingApproval(runID int64) *approval {
	approvals, err := ListApprovalsForRun(db, runID)
	if err != nil {
		return nil
	}
	for _, a := range approvals {
		if a.State == approvalPending {
			return &a
		}
	}
	return nil
}

// hasPendingApproval reports whether runID has an unresolved Approval —
// checked once per run completion (the hook that creates one runs
// synchronously, inline with the subprocess's own tool call, so by the
// time cmd.Wait() returns it's already been created if it exists at all).
func hasPendingApproval(runID int64) bool {
	return findPendingApproval(runID) != nil
}
