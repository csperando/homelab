package main

import (
	"database/sql"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// withAgentRunner substitutes agentRunner with a fake that runs script via
// `sh -c`, so tests exercise the real JSONL-parsing/session-id/final-state
// logic in runAgent without spawning the real (paid) claude CLI.
func withAgentRunner(t *testing.T, script string) {
	t.Helper()
	orig := agentRunner
	t.Cleanup(func() { agentRunner = orig })
	agentRunner = func(workspacePath, prompt string) *exec.Cmd {
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

	registerLiveRun(42, 7, cmd)

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

	registerLiveRun(101, 55, cmd)

	runID, ok := workspaceHasLiveRun(55)
	if !ok || runID != 101 {
		t.Errorf("workspaceHasLiveRun(55) = (%d, %v), want (101, true)", runID, ok)
	}

	if _, ok := workspaceHasLiveRun(999); ok {
		t.Error("workspaceHasLiveRun(other workspace) ok = true, want false")
	}
}

func TestUnregisterLiveRun(t *testing.T) {
	resetLiveRuns(t)

	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting test process: %v", err)
	}
	defer cmd.Wait()

	registerLiveRun(7, 3, cmd)
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
	args := buildAgentArgs("do something")

	mustContainSeq := [][]string{
		{"--restricted"},
		{"--permission-prompts", "none"},
		{"--tools", "Read,Write,Edit,Bash"},
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

func TestBuildAgentArgs_PromptIsLastAfterSeparator(t *testing.T) {
	args := buildAgentArgs("rm -rf / --looks-like-a-flag")

	if len(args) < 2 || args[len(args)-2] != "--" || args[len(args)-1] != "rm -rf / --looks-like-a-flag" {
		t.Errorf("buildAgentArgs() = %v, want the prompt as the last element, preceded by \"--\" so it can never be parsed as a flag", args)
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
		`'{"type":"system","subtype":"init","session_id":"sess-abc"}' `+
		`'{"type":"result","subtype":"success","is_error":false,"session_id":"sess-abc","result":"banana"}'`)

	runAgent(runID, 1, t.TempDir(), "say banana")

	got, err := GetAgentRun(dbConn, runID)
	if err != nil {
		t.Fatalf("GetAgentRun() = %v, want nil", err)
	}
	if got.State != agentRunSucceeded {
		t.Errorf("got.State = %q, want %q (error: %s)", got.State, agentRunSucceeded, got.Error)
	}
	if got.ClaudeSessionID != "sess-abc" {
		t.Errorf("got.ClaudeSessionID = %q, want %q", got.ClaudeSessionID, "sess-abc")
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

	runAgent(runID, 1, t.TempDir(), "do something bad")

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

	runAgent(runID, 1, t.TempDir(), "prompt")

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
	registerLiveRun(1, 1, cmd)

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
	registerLiveRun(2, 1, cmd)

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
	registerLiveRun(3, 1, cmd)
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
		runAgent(runID, 1, t.TempDir(), "long task")
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
