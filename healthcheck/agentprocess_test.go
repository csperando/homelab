package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// withAgentHookSecret sets agentHookSecret for the duration of a test,
// restoring the original value after — mirrors withGithubToken's pattern.
func withAgentHookSecret(t *testing.T, secret string) {
	t.Helper()
	orig := agentHookSecret
	t.Cleanup(func() { agentHookSecret = orig })
	agentHookSecret = secret
}

// withAgentRunner substitutes agentRunner with a fake that runs script via
// `sh -c`, so tests exercise the real JSONL-parsing/session-id/final-state
// logic in runAgent without spawning the real (paid) claude CLI.
func withAgentRunner(t *testing.T, script string) {
	t.Helper()
	orig := agentRunner
	t.Cleanup(func() { agentRunner = orig })
	agentRunner = func(workspacePath, prompt, sessionID string, resume bool) *exec.Cmd {
		return exec.Command("sh", "-c", script)
	}
}

// seedAgentRun creates a full workspace -> session -> task -> run chain
// (all in the "pending" state) for tests that exercise runAgent/
// finishAgentRun directly, returning the new run's ID.
func seedAgentRun(t *testing.T, db *sql.DB, workspaceName string) int64 {
	t.Helper()
	workspaceID := seedWorkspace(t, db, workspaceName)
	session, err := CreateAgentSession(db, workspaceID)
	if err != nil {
		t.Fatalf("CreateAgentSession: %v", err)
	}
	tk, err := CreateTask(db, session.ID, "test prompt")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	run, err := CreateAgentRun(db, tk.ID)
	if err != nil {
		t.Fatalf("CreateAgentRun: %v", err)
	}
	return run.ID
}

// resetLiveRuns clears the global live-run store between tests, since it's
// package-level shared state (mirrors resetCloneJobs in clone_test.go).
func resetLiveRuns(t *testing.T) {
	t.Helper()
	liveRunsMu.Lock()
	liveRuns = map[int64]*liveRun{}
	liveRunsMu.Unlock()
	t.Cleanup(func() {
		liveRunsMu.Lock()
		liveRuns = map[int64]*liveRun{}
		liveRunsMu.Unlock()
	})
}

func TestLiveRunStore_UnknownRun(t *testing.T) {
	resetLiveRuns(t)

	if _, ok := liveRunTranscript(999); ok {
		t.Error("liveRunTranscript(unknown) ok = true, want false")
	}
	if _, ok := liveRunProcess(999); ok {
		t.Error("liveRunProcess(unknown) ok = true, want false")
	}
	if _, ok := workspaceHasLiveRun(999); ok {
		t.Error("workspaceHasLiveRun(unknown workspace) ok = true, want false")
	}
	// Must not panic even though nothing is registered.
	appendLiveRunLine(999, "ignored")
	unregisterLiveRun(999)
}

func TestLiveRunStore_RegisterAppendTranscript(t *testing.T) {
	resetLiveRuns(t)

	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting test process: %v", err)
	}
	defer cmd.Wait()

	registerLiveRun(42, 7, "sess-42", cmd)

	if _, ok := liveRunTranscript(42); !ok {
		t.Fatal("liveRunTranscript(42) ok = false, want true right after registering")
	}

	appendLiveRunLine(42, `{"type":"system"}`)
	appendLiveRunLine(42, `{"type":"assistant"}`)

	got, ok := liveRunTranscript(42)
	if !ok {
		t.Fatal("liveRunTranscript(42) ok = false, want true")
	}
	want := "{\"type\":\"system\"}\n{\"type\":\"assistant\"}"
	if got != want {
		t.Errorf("liveRunTranscript(42) = %q, want %q", got, want)
	}

	proc, ok := liveRunProcess(42)
	if !ok || proc == nil {
		t.Fatal("liveRunProcess(42) = (nil, false), want the running process")
	}
	if proc.Pid != cmd.Process.Pid {
		t.Errorf("liveRunProcess(42).Pid = %d, want %d", proc.Pid, cmd.Process.Pid)
	}
}

func TestWorkspaceHasLiveRun(t *testing.T) {
	resetLiveRuns(t)

	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting test process: %v", err)
	}
	defer cmd.Wait()

	registerLiveRun(101, 55, "sess-101", cmd)

	runID, ok := workspaceHasLiveRun(55)
	if !ok || runID != 101 {
		t.Errorf("workspaceHasLiveRun(55) = (%d, %v), want (101, true)", runID, ok)
	}

	if _, ok := workspaceHasLiveRun(999); ok {
		t.Error("workspaceHasLiveRun(other workspace) ok = true, want false")
	}
}

func TestLiveRunBySessionID(t *testing.T) {
	resetLiveRuns(t)

	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting test process: %v", err)
	}
	defer cmd.Wait()

	registerLiveRun(202, 8, "sess-202", cmd)

	runID, ok := liveRunBySessionID("sess-202")
	if !ok || runID != 202 {
		t.Errorf("liveRunBySessionID(sess-202) = (%d, %v), want (202, true)", runID, ok)
	}

	if _, ok := liveRunBySessionID("sess-unknown"); ok {
		t.Error("liveRunBySessionID(unknown session) ok = true, want false")
	}
}

func TestNewUUIDv4(t *testing.T) {
	a := newUUIDv4()
	b := newUUIDv4()

	if len(a) != 36 {
		t.Errorf("newUUIDv4() = %q, want a 36-char string", a)
	}
	if a == b {
		t.Errorf("newUUIDv4() returned the same value twice: %q", a)
	}
	// Version 4, variant 10xx nibble positions per RFC 4122.
	if a[14] != '4' {
		t.Errorf("newUUIDv4() = %q, want version nibble '4' at index 14", a)
	}
	if variant := a[19]; variant != '8' && variant != '9' && variant != 'a' && variant != 'b' {
		t.Errorf("newUUIDv4() = %q, want variant nibble in [89ab] at index 19, got %q", a, string(variant))
	}
}

func TestUnregisterLiveRun(t *testing.T) {
	resetLiveRuns(t)

	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting test process: %v", err)
	}
	defer cmd.Wait()

	registerLiveRun(7, 3, "sess-7", cmd)
	unregisterLiveRun(7)

	if _, ok := liveRunTranscript(7); ok {
		t.Error("liveRunTranscript(7) ok = true after unregister, want false")
	}
	if _, ok := workspaceHasLiveRun(3); ok {
		t.Error("workspaceHasLiveRun(3) ok = true after unregister, want false")
	}
}

// TestBuildAgentArgs_SecurityPosture verifies the unattended-run security
// posture decided in goal.md is actually applied by the constructed argv,
// not merely documented in a comment: --restricted and --permission-prompts
// none (no TTY to answer a prompt) are always present, the tool allowlist
// is exactly what was decided, and --dangerously-skip-permissions never
// appears (Anthropic scopes that flag to network-isolated sandboxes, which
// this container is not).
func TestBuildAgentArgs_SecurityPosture(t *testing.T) {
	args := buildAgentArgs("do something", "sess-1", false)

	mustContainSeq := [][]string{
		{"--restricted"},
		{"--permission-prompts", "none"},
		{"--tools", "Read,Write,Edit,Bash"},
		{"--session-id", "sess-1"},
		{"--verbose"},
	}
	for _, seq := range mustContainSeq {
		if !containsSubsequence(args, seq) {
			t.Errorf("buildAgentArgs() = %v, want it to contain %v", args, seq)
		}
	}

	for _, arg := range args {
		if arg == "--dangerously-skip-permissions" {
			t.Errorf("buildAgentArgs() = %v, must never include --dangerously-skip-permissions", args)
		}
	}
}

// TestBuildAgentArgs_VerboseRequiredForStreamJSON guards against the exact
// bug live-testing caught: claude refuses to start with --print
// --output-format=stream-json unless --verbose is also present ("Error:
// When using --print, --output-format=stream-json requires --verbose").
func TestBuildAgentArgs_VerboseRequiredForStreamJSON(t *testing.T) {
	args := buildAgentArgs("do something", "sess-1", false)

	hasStreamJSON := containsSubsequence(args, []string{"--output-format", "stream-json"})
	hasVerbose := containsSubsequence(args, []string{"--verbose"})
	if hasStreamJSON && !hasVerbose {
		t.Errorf("buildAgentArgs() = %v, uses --output-format=stream-json without --verbose — claude refuses to start", args)
	}
}

func TestBuildAgentArgs_PromptIsLastAfterSeparator(t *testing.T) {
	args := buildAgentArgs("rm -rf / --looks-like-a-flag", "sess-1", false)

	if len(args) < 2 || args[len(args)-2] != "--" || args[len(args)-1] != "rm -rf / --looks-like-a-flag" {
		t.Errorf("buildAgentArgs() = %v, want the prompt as the last element, preceded by \"--\" so it can never be parsed as a flag", args)
	}
}

// TestBuildAgentArgs_ResumeUsesResumeFlag confirms the approve action's
// continuation path uses --resume <existing session> rather than
// --session-id <new session> — resume=true must genuinely continue the
// same claude conversation, not start a fresh one.
func TestBuildAgentArgs_ResumeUsesResumeFlag(t *testing.T) {
	args := buildAgentArgs("proceed", "existing-sess", true)

	if !containsSubsequence(args, []string{"--resume", "existing-sess"}) {
		t.Errorf("buildAgentArgs(resume=true) = %v, want --resume existing-sess", args)
	}
	if containsSubsequence(args, []string{"--session-id"}) {
		t.Errorf("buildAgentArgs(resume=true) = %v, must not also pass --session-id", args)
	}
}

func TestAgentHookSecretWarning(t *testing.T) {
	withAgentHookSecret(t, "")
	if got := agentHookSecretWarning(); got == "" {
		t.Error("agentHookSecretWarning() with unset secret = \"\", want a warning message")
	}

	withAgentHookSecret(t, "a-real-secret")
	if got := agentHookSecretWarning(); got != "" {
		t.Errorf("agentHookSecretWarning() with secret set = %q, want \"\"", got)
	}
}

func TestEnsureAgentHookSettingsFile(t *testing.T) {
	path, err := ensureAgentHookSettingsFile()
	if err != nil {
		t.Fatalf("ensureAgentHookSettingsFile() = %v, want nil", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var settings struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type           string            `json:"type"`
					URL            string            `json:"url"`
					Headers        map[string]string `json:"headers"`
					AllowedEnvVars []string          `json:"allowedEnvVars"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings file isn't valid JSON: %v\n%s", err, data)
	}

	if len(settings.Hooks.PreToolUse) != 1 || settings.Hooks.PreToolUse[0].Matcher != "Bash" {
		t.Fatalf("settings = %+v, want exactly one PreToolUse hook matching Bash", settings)
	}
	hook := settings.Hooks.PreToolUse[0].Hooks
	if len(hook) != 1 {
		t.Fatalf("hooks = %+v, want exactly one hook config", hook)
	}
	if hook[0].Type != "http" || hook[0].URL != agentHookURL {
		t.Errorf("hook = %+v, want type=http url=%s", hook[0], agentHookURL)
	}
	if hook[0].Headers["Authorization"] != "Bearer ${AGENT_HOOK_SECRET}" {
		t.Errorf("Authorization header = %q, want the ${AGENT_HOOK_SECRET} interpolation reference, not a literal secret", hook[0].Headers["Authorization"])
	}
	found := false
	for _, v := range hook[0].AllowedEnvVars {
		if v == "AGENT_HOOK_SECRET" {
			found = true
		}
	}
	if !found {
		t.Errorf("allowedEnvVars = %v, want it to include AGENT_HOOK_SECRET", hook[0].AllowedEnvVars)
	}
}

func TestBuildAgentArgs_AttachesSettingsOnlyWhenSecretSet(t *testing.T) {
	withAgentHookSecret(t, "")
	args := buildAgentArgs("prompt", "sess-1", false)
	if containsSubsequence(args, []string{"--settings"}) {
		t.Errorf("buildAgentArgs() with no secret = %v, want no --settings flag", args)
	}

	withAgentHookSecret(t, "a-real-secret")
	args = buildAgentArgs("prompt", "sess-1", false)
	found := false
	for i, a := range args {
		if a == "--settings" && i+1 < len(args) {
			found = true
			if _, err := os.Stat(args[i+1]); err != nil {
				t.Errorf("--settings path %q doesn't exist: %v", args[i+1], err)
			}
		}
	}
	if !found {
		t.Errorf("buildAgentArgs() with secret set = %v, want a --settings flag", args)
	}
}

// containsSubsequence reports whether want appears as a contiguous
// subsequence anywhere in args.
func containsSubsequence(args, want []string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		match := true
		for j, w := range want {
			if args[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestEnvOrDefault(t *testing.T) {
	t.Setenv("AGENTPROCESS_TEST_VAR", "")
	if got := envOrDefault("AGENTPROCESS_TEST_VAR", "fallback"); got != "fallback" {
		t.Errorf("envOrDefault(unset) = %q, want %q", got, "fallback")
	}
	t.Setenv("AGENTPROCESS_TEST_VAR", "explicit")
	if got := envOrDefault("AGENTPROCESS_TEST_VAR", "fallback"); got != "explicit" {
		t.Errorf("envOrDefault(set) = %q, want %q", got, "explicit")
	}
}

func TestRunAgent_Success(t *testing.T) {
	resetLiveRuns(t)
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "agent-run-success")

	withAgentRunner(t, `printf '%s\n' `+
		`'{"type":"system","subtype":"init"}' `+
		`'{"type":"result","subtype":"success","is_error":false,"result":"banana"}'`)

	runAgent(runID, 1, t.TempDir(), "say banana", "")

	got, err := GetAgentRun(dbConn, runID)
	if err != nil {
		t.Fatalf("GetAgentRun() = %v, want nil", err)
	}
	if got.State != agentRunSucceeded {
		t.Errorf("got.State = %q, want %q (error: %s)", got.State, agentRunSucceeded, got.Error)
	}
	// runAgent generates its own session ID (--session-id) rather than
	// reading one back from the stream, so we can't predict the exact
	// value — just confirm one was recorded, in the expected UUID shape.
	if len(got.ClaudeSessionID) != 36 {
		t.Errorf("got.ClaudeSessionID = %q, want a 36-char UUID", got.ClaudeSessionID)
	}
	if !strings.Contains(got.Transcript, "banana") {
		t.Errorf("got.Transcript = %q, want it to contain the captured output", got.Transcript)
	}
	if _, ok := liveRunTranscript(runID); ok {
		t.Error("run still tracked in the live-run store after finishing, want it removed")
	}
}

func TestRunAgent_FailedResult(t *testing.T) {
	resetLiveRuns(t)
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "agent-run-failed-result")

	withAgentRunner(t, `printf '%s\n' '{"type":"result","subtype":"error","is_error":true}'`)

	runAgent(runID, 1, t.TempDir(), "do something bad", "")

	got, err := GetAgentRun(dbConn, runID)
	if err != nil {
		t.Fatalf("GetAgentRun() = %v, want nil", err)
	}
	if got.State != agentRunFailed {
		t.Errorf("got.State = %q, want %q", got.State, agentRunFailed)
	}
	if got.Error == "" {
		t.Error("got.Error = \"\", want a non-empty failure reason")
	}
}

func TestRunAgent_ProcessExitError(t *testing.T) {
	resetLiveRuns(t)
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "agent-run-exit-error")

	withAgentRunner(t, `exit 1`)

	runAgent(runID, 1, t.TempDir(), "prompt", "")

	got, err := GetAgentRun(dbConn, runID)
	if err != nil {
		t.Fatalf("GetAgentRun() = %v, want nil", err)
	}
	if got.State != agentRunFailed {
		t.Errorf("got.State = %q, want %q", got.State, agentRunFailed)
	}
	if got.Error == "" {
		t.Error("got.Error = \"\", want the process exit error")
	}
}

// TestFinishAgentRun_DoesNotClobberTerminalState guards against the race
// with the (not-yet-built) stop action: if something already finalized the
// run (e.g. marked it "stopped"), runAgent's own completion path must not
// overwrite that outcome.
func TestFinishAgentRun_DoesNotClobberTerminalState(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "agent-run-finish-race")

	if err := UpdateAgentRunState(dbConn, runID, agentRunStopped, "", ""); err != nil {
		t.Fatalf("UpdateAgentRunState(stopped) = %v, want nil", err)
	}

	finishAgentRun(runID, agentRunFailed, "should not be written", "should not be written")

	got, err := GetAgentRun(dbConn, runID)
	if err != nil {
		t.Fatalf("GetAgentRun() = %v, want nil", err)
	}
	if got.State != agentRunStopped {
		t.Errorf("got.State = %q, want %q (finishAgentRun must not clobber a terminal state)", got.State, agentRunStopped)
	}
	if got.Error != "" {
		t.Errorf("got.Error = %q, want empty (must not have been overwritten)", got.Error)
	}
}

func TestRequestStop_UnknownRun(t *testing.T) {
	resetLiveRuns(t)
	if requestStop(999) {
		t.Error("requestStop(unknown) = true, want false")
	}
}

func TestRequestStop_SignalsAndMarksFlag(t *testing.T) {
	resetLiveRuns(t)
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting test process: %v", err)
	}
	registerLiveRun(1, 1, "sess-1", cmd)

	if !requestStop(1) {
		t.Fatal("requestStop() = false, want true")
	}
	if !wasStopRequested(1) {
		t.Error("wasStopRequested(1) = false, want true after requestStop")
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cmd.Process.Kill()
		t.Error("process did not exit after SIGTERM within 2s")
	}
}

// TestRequestStop_AlreadyExited confirms the StopRequested flag is only set
// once the signal is actually delivered — a process that already exited on
// its own must not be mis-reported as later having been stopped.
func TestRequestStop_AlreadyExited(t *testing.T) {
	resetLiveRuns(t)
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting test process: %v", err)
	}
	cmd.Wait()
	registerLiveRun(2, 1, "sess-2", cmd)

	if requestStop(2) {
		t.Error("requestStop() on an already-exited process = true, want false")
	}
	if wasStopRequested(2) {
		t.Error("wasStopRequested(2) = true, want false (signal failed, flag must not be set)")
	}
}

func TestEscalateStopAfterGracePeriod_SendsSIGKILL(t *testing.T) {
	resetLiveRuns(t)
	origGrace := stopGracePeriod
	stopGracePeriod = 50 * time.Millisecond
	t.Cleanup(func() { stopGracePeriod = origGrace })

	// Ignores SIGTERM, so only SIGKILL (unblockable) can end it.
	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting test process: %v", err)
	}
	registerLiveRun(3, 1, "sess-3", cmd)
	cmd.Process.Signal(syscall.SIGTERM)

	go escalateStopAfterGracePeriod(3)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cmd.Process.Kill()
		t.Fatal("process did not exit after SIGKILL escalation within 2s")
	}
}

// TestRunAgent_RespectsStop confirms a run stopped mid-flight is recorded
// as "stopped", not "failed", even though the killed process makes
// cmd.Wait() return a non-nil error.
func TestRunAgent_RespectsStop(t *testing.T) {
	resetLiveRuns(t)
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "agent-run-respects-stop")

	withAgentRunner(t, "sleep 5")

	done := make(chan struct{})
	go func() {
		runAgent(runID, 1, t.TempDir(), "long task", "")
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := liveRunProcess(runID); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !requestStop(runID) {
		t.Fatal("requestStop() = false, want true")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runAgent did not finish within 5s after stop")
	}

	got, err := GetAgentRun(dbConn, runID)
	if err != nil {
		t.Fatalf("GetAgentRun() = %v, want nil", err)
	}
	if got.State != agentRunStopped {
		t.Errorf("got.State = %q, want %q (error: %s)", got.State, agentRunStopped, got.Error)
	}
}

// TestRunAgent_OverridesToAwaitingApproval simulates what a real run looks
// like after the PreToolUse hook has denied a gated command: the fake
// subprocess still reports a normal successful result (matching real
// observed behavior — the model gracefully explains it couldn't proceed and
// ends the turn normally), but a pending Approval already exists for this
// run. runAgent's completion logic must report awaiting_approval, not
// succeeded.
func TestRunAgent_OverridesToAwaitingApproval(t *testing.T) {
	resetLiveRuns(t)
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "agent-run-awaiting-approval")

	if _, err := CreateApproval(dbConn, runID, "Bash", `{"command":"git push origin main"}`, "gated command: git push origin main"); err != nil {
		t.Fatalf("CreateApproval() = %v, want nil", err)
	}

	withAgentRunner(t, `printf '%s\n' '{"type":"result","subtype":"success","is_error":false}'`)
	runAgent(runID, 1, t.TempDir(), "push my changes", "")

	got, err := GetAgentRun(dbConn, runID)
	if err != nil {
		t.Fatalf("GetAgentRun() = %v, want nil", err)
	}
	if got.State != agentRunAwaitingApproval {
		t.Errorf("got.State = %q, want %q (error: %s)", got.State, agentRunAwaitingApproval, got.Error)
	}
}

func TestHasPendingApproval(t *testing.T) {
	dbConn := testDB(t)
	if err := runMigrations(dbConn); err != nil {
		t.Fatalf("runMigrations() = %v, want nil", err)
	}
	withDB(t, dbConn)
	runID := seedAgentRun(t, dbConn, "agent-has-pending-approval")

	if hasPendingApproval(runID) {
		t.Error("hasPendingApproval() = true before any approval exists, want false")
	}

	a, err := CreateApproval(dbConn, runID, "Bash", `{"command":"rm -rf /"}`, "gated command: rm -rf")
	if err != nil {
		t.Fatalf("CreateApproval() = %v, want nil", err)
	}
	if !hasPendingApproval(runID) {
		t.Error("hasPendingApproval() = false with a pending approval, want true")
	}

	if err := UpdateApprovalState(dbConn, a.ID, approvalDenied); err != nil {
		t.Fatalf("UpdateApprovalState() = %v, want nil", err)
	}
	if hasPendingApproval(runID) {
		t.Error("hasPendingApproval() = true after the approval was decided, want false")
	}
}
