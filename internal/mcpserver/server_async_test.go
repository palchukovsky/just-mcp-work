// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/runmanager"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

func TestSyncReceiptCarriesStatisticsWithoutRunningFields(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	for range 2 {
		_, receipt, err := server.runShellCommand(
			context.Background(),
			nil,
			runShellCommandInput{Command: shellOutputCommand()},
		)
		if err != nil || !receipt.OK {
			t.Fatalf("run = %#v, %v", receipt, err)
		}
	}
	_, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellOutputCommand()},
	)
	if err != nil || !receipt.OK {
		t.Fatalf("third run = %#v, %v", receipt, err)
	}
	if receipt.runDetails == nil || receipt.Stats == nil || receipt.Stats.Exact == nil {
		t.Fatalf("completed receipt = %#v, want the pre-run statistics", receipt)
	}
	if receipt.Stats.Exact.Runs != 2 {
		t.Fatalf("exact runs = %d, want the two finished runs", receipt.Stats.Exact.Runs)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"completed", "promoted", "\"pid\"", "owned_by_this_server"} {
		if strings.Contains(string(encoded), unwanted) {
			t.Fatalf("completed receipt %s must not contain %q", encoded, unwanted)
		}
	}
}

func TestSyncWaitBounds(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	immediate := int64(0)
	_, started, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellSleepCommand(), MaxWaitMS: &immediate},
	)
	if err != nil || started.Status != runstore.StatusRunning || !started.Promoted {
		t.Fatalf("max_wait_ms=0 = %#v, %v, want an immediate running receipt", started, err)
	}
	stopRunOrFail(t, server, started.RunID)

	untilTimeout := int64(-1)
	_, waited, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellOutputCommand(), MaxWaitMS: &untilTimeout},
	)
	if err != nil || waited.Status != runstore.StatusOK {
		t.Fatalf("max_wait_ms=-1 = %#v, %v, want a completed run", waited, err)
	}

	// The caller's wait budget is independent from the task timeout.
	beyondTimeout := int64(time.Hour / time.Millisecond)
	wait, err := server.syncWaitDuration(&beyondTimeout)
	if err != nil || wait.duration != time.Hour || wait.untilCompletion {
		t.Fatalf("explicit wait = %#v, %v, want one hour", wait, err)
	}
	server.config.Timeout = 0
	server.config.TimeoutUnlimited = true
	server.config.SyncDeadline = 40 * time.Millisecond
	defaultWait, err := server.syncWaitDuration(nil)
	if err != nil || defaultWait.duration != server.config.SyncDeadline || defaultWait.untilCompletion {
		t.Fatalf("default wait = %#v, %v", defaultWait, err)
	}
	_, unlimited, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellBriefSleepCommand(), MaxWaitMS: &untilTimeout},
	)
	if err != nil || unlimited.Status != runstore.StatusOK {
		t.Fatalf("unlimited max_wait_ms=-1 = %#v, %v", unlimited, err)
	}
	invalid := int64(-2)
	if _, err := server.syncWaitDuration(&invalid); err == nil {
		t.Fatal("max_wait_ms=-2 must be rejected")
	}
}

func TestWaitRunDurationAcceptsLegacyTimeoutMS(t *testing.T) {
	legacy := int64(25)
	wait, err := waitRunDuration(waitRunInput{TimeoutMS: &legacy})
	if err != nil || wait != 25*time.Millisecond {
		t.Fatalf("legacy timeout_ms wait = %v, %v", wait, err)
	}
	current := int64(25)
	wait, err = waitRunDuration(waitRunInput{MaxWaitMS: &current, TimeoutMS: &legacy})
	if err != nil || wait != 25*time.Millisecond {
		t.Fatalf("matching wait aliases = %v, %v", wait, err)
	}
	legacy++
	if _, err = waitRunDuration(waitRunInput{MaxWaitMS: &current, TimeoutMS: &legacy}); err == nil {
		t.Fatal("conflicting wait aliases must be rejected")
	}
}

func TestStartedRunIgnoresRequestCancellation(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	_, started, err := server.startShellCommand(
		ctx,
		nil,
		startShellCommandInput{Command: shellSleepCommand()},
	)
	if err != nil || started.Status != runstore.StatusRunning {
		t.Fatalf("start = %#v, %v", started, err)
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: started.RunID},
	)
	if err != nil || status.Status != runstore.StatusRunning {
		t.Fatalf("status after request cancellation = %#v, %v, want still running", status, err)
	}
	if status.ProcessAlive == nil || !*status.ProcessAlive {
		t.Fatalf("process must stay alive after request cancellation: %#v", status.runDetails)
	}
	stopRunOrFail(t, server, started.RunID)
}

func TestForeignRunIsObservableWithABoundedWait(t *testing.T) {
	server, runID := newForeignRun(t)
	_, status, err := server.getRunStatus(context.Background(), nil, getRunStatusInput{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if status.OwnedByThis == nil || *status.OwnedByThis {
		t.Fatalf("foreign run must not be reported as owned: %#v", status.runDetails)
	}
	timeout := int64(100)
	begin := time.Now()
	_, waited, err := server.waitRun(
		context.Background(),
		nil,
		waitRunInput{RunID: runID, MaxWaitMS: &timeout},
	)
	if err != nil || waited.Completed == nil || *waited.Completed {
		t.Fatalf("wait on a foreign run = %#v, %v, want an unfinished snapshot", waited, err)
	}
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("wait took %v, want the requested bound", elapsed)
	}
}

func TestForeignRunCannotBeStopped(t *testing.T) {
	server, runID := newForeignRun(t)
	result, stopped, err := server.stopRun(context.Background(), nil, stopRunInput{RunID: runID})
	if err != nil || result == nil || !result.IsError || stopped.Error == nil {
		t.Fatalf("stopping a foreign run = %#v, %#v, %v, want a refusal", result, stopped, err)
	}
	if !strings.Contains(stopped.Error.Message, "cannot be stopped by this server") {
		t.Fatalf("refusal message = %q", stopped.Error.Message)
	}
}

func TestStatusDoesNotTreatLiveForeignOwnerAsThisServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// #nosec G204,G702 -- fixed test binary and helper selector.
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestMCPServerHelperProcess")
	cmd.Env = append(os.Environ(), "JMW_TEST_HELPER_PROCESS=sleep")
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		//nolint:errcheck // Cancellation is the expected helper exit path.
		_ = cmd.Wait()
	})
	identity := runstore.ProcessIdentity(cmd.Process.Pid)
	if identity == "" {
		t.Skip("process identity is unavailable on this platform")
	}
	if !runstore.ProcessMatches(cmd.Process.Pid, identity) {
		t.Fatal("foreign helper must be live for the ownership assertion")
	}
	server := newShellTestServer(t, t.TempDir())
	handle, err := server.store.Begin(runstore.Meta{TaskID: "shell:command"})
	if err != nil {
		t.Fatal(err)
	}
	finishRunningRunAtCleanup(t, handle)
	handle.Meta.OwnerPID = cmd.Process.Pid
	handle.Meta.OwnerIdentity = identity
	if persistErr := handle.PersistRunning(); persistErr != nil {
		t.Fatal(persistErr)
	}
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: handle.Meta.RunID},
	)
	if err != nil || status.OwnedByThis == nil || *status.OwnedByThis {
		t.Fatalf("live foreign owner = %#v, %v", status.runDetails, err)
	}
}

func TestStatusDoesNotTreatReusedOwnerPIDAsThisServer(t *testing.T) {
	identity := runstore.ProcessIdentity(os.Getpid())
	if identity == "" {
		t.Skip("process identity is unavailable on this platform")
	}
	server := newShellTestServer(t, t.TempDir())
	handle, err := server.store.Begin(runstore.Meta{TaskID: "shell:command"})
	if err != nil {
		t.Fatal(err)
	}
	finishRunningRunAtCleanup(t, handle)
	handle.Meta.OwnerPID = os.Getpid()
	handle.Meta.OwnerIdentity = identity + ":reused"
	if persistErr := handle.PersistRunning(); persistErr != nil {
		t.Fatal(persistErr)
	}
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: handle.Meta.RunID},
	)
	if err != nil || status.OwnedByThis == nil || *status.OwnedByThis {
		t.Fatalf("reused owner identity = %#v, %v", status.runDetails, err)
	}
}

func TestRunStatusUsesPersistedTaskTimeout(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	timeout := int64((10 * time.Second) / time.Millisecond)
	handle, err := server.store.Begin(
		runstore.Meta{TaskID: "shell:command", TaskTimeoutMS: &timeout},
	)
	if err != nil {
		t.Fatal(err)
	}
	finishRunningRunAtCleanup(t, handle)
	server.config.Timeout = 0
	server.config.TimeoutUnlimited = true
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: handle.Meta.RunID},
	)
	if err != nil ||
		status.TaskTimeoutMS == nil ||
		*status.TaskTimeoutMS != timeout ||
		status.TimeToTaskTimeoutMS == nil ||
		status.TimeoutMS != timeout ||
		status.TimeToTimeoutMS == nil {
		t.Fatalf("persisted task timeout = %#v, %v", status.runDetails, err)
	}
}

func TestShutdownBudgetCoversTerminationAndWaitDelay(t *testing.T) {
	const grace = 3 * time.Second
	if got, want := shutdownBudget(grace), 2*grace+time.Second; got != want {
		t.Fatalf("shutdown budget = %v, want %v", got, want)
	}
}

func TestStatusZeroTailSuppressesLiveOutput(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	_, started, err := server.startShellCommand(
		context.Background(),
		nil,
		startShellCommandInput{Command: shellLongOutputCommand()},
	)
	if err != nil || started.Status != runstore.StatusRunning {
		t.Fatalf("start shell command = %#v, %v", started, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, stateErr := server.store.LogState(started.RunID)
		if stateErr == nil && state.StdoutBytes > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	zero := int64(0)
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: started.RunID, TailBytes: &zero},
	)
	if err != nil || status.StdoutTail != "" || status.StderrTail != "" {
		t.Fatalf("zero-tail status = %#v, %v", status, err)
	}
	stopRunOrFail(t, server, started.RunID)
}

func TestAdmissionRejectsBeforeRejectedShellCommandStarts(t *testing.T) {
	root := t.TempDir()
	server := newShellTestServer(t, root)
	server.manager = runmanager.New(server.stats.Invalidate, 1)
	_, first, err := server.startShellCommand(
		context.Background(),
		nil,
		startShellCommandInput{Command: shellSleepCommand()},
	)
	if err != nil || first.Status != runstore.StatusRunning {
		t.Fatalf("first shell command = %#v, %v", first, err)
	}
	marker := filepath.Join(root, "rejected-marker")
	_, rejected, err := server.startShellCommand(
		context.Background(),
		nil,
		startShellCommandInput{Command: shellMarkerCommand(marker)},
	)
	if err != nil || rejected.Status != runstore.StatusSpawnError {
		t.Fatalf("rejected shell command = %#v, %v", rejected, err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("rejected command created marker: %v", statErr)
	}
	stopRunOrFail(t, server, first.RunID)
}

func TestStatusRepairsFailedFinalMetadata(t *testing.T) {
	server, runID, _ := failedTerminalRun(t)
	_, status, err := server.getRunStatus(
		context.Background(),
		nil,
		getRunStatusInput{RunID: runID},
	)
	if err != nil || status.Status != runstore.StatusCancelled ||
		status.Completed == nil || !*status.Completed {
		t.Fatalf("repaired terminal status = %#v, %v", status, err)
	}
	_, stored, err := server.getRun(context.Background(), nil, getRunInput{RunID: runID})
	if err != nil || stored.Run.Status != runstore.StatusCancelled {
		t.Fatalf("repaired terminal metadata = %#v, %v", stored, err)
	}
}

func TestListRunsRepairsFailedFinalMetadata(t *testing.T) {
	server, runID, _ := failedTerminalRun(t)
	_, listed, err := server.listRuns(
		context.Background(),
		nil,
		listRunsInput{Status: []string{string(runstore.StatusCancelled)}},
	)
	if err != nil || len(listed.Runs) != 1 ||
		listed.Runs[0].RunID != runID || listed.Runs[0].Status != runstore.StatusCancelled {
		t.Fatalf("listed repaired terminal run = %#v, %v", listed, err)
	}
}

func TestStatisticsRepairFailedFinalMetadata(t *testing.T) {
	server, runID, command := failedTerminalRun(t)
	immediate := int64(0)
	_, started, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: command, MaxWaitMS: &immediate},
	)
	if err != nil || started.Status != runstore.StatusRunning ||
		started.Stats == nil || started.Stats.Exact == nil ||
		started.Stats.Exact.Runs != 1 || started.Stats.Exact.AbortedRuns != 1 {
		t.Fatalf("statistics after terminal repair = %#v, %v", started, err)
	}
	stopRunOrFail(t, server, started.RunID)
	meta, err := server.store.Get(runID)
	if err != nil || meta.Status != runstore.StatusCancelled {
		t.Fatalf("stored repaired terminal run = %#v, %v", meta, err)
	}
}

func failedTerminalRun(t *testing.T) (*Server, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not block metadata writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	server := newShellTestServer(t, t.TempDir())
	command := shellSleepCommand()
	_, started, err := server.startShellCommand(
		context.Background(),
		nil,
		startShellCommandInput{Command: command},
	)
	if err != nil || started.Status != runstore.StatusRunning {
		t.Fatalf("start shell command = %#v, %v", started, err)
	}
	runDir := filepath.Join(server.store.LogRoot(), started.RunID)
	if chmodErr := os.Chmod(runDir, 0o500); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	run, live := server.manager.Get(started.RunID)
	if !live {
		t.Fatal("started run is not locally managed")
	}
	if stopErr := run.Stop(); stopErr == nil {
		t.Fatal("terminal metadata write must fail while the run directory is read-only")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, live = server.manager.Get(started.RunID); !live {
			if _, fallback := server.manager.Terminal(started.RunID); fallback {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if _, fallback := server.manager.Terminal(started.RunID); !fallback {
		t.Fatal("failed terminal run was not retained for metadata repair")
	}
	if chmodErr := os.Chmod(runDir, 0o750); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	t.Cleanup(func() {
		//nolint:errcheck // Restoring test-directory permissions is best effort.
		_ = os.Chmod(runDir, 0o750)
	})
	return server, started.RunID, command
}

func TestTimeoutStatusUsesPersistedDiagnostic(t *testing.T) {
	meta := runstore.Meta{
		RunID:  "019f7ac4-0000-7000-8000-000000000000",
		Status: runstore.StatusTimeout,
		Error:  "Task exceeded configured timeout of 15m0s; adjust --timeout or JMW_TIMEOUT.",
	}
	if result := resultForMeta(meta); result.Message != meta.Error {
		t.Fatalf("timeout status result = %#v", result)
	}
}

func shellBriefSleepCommand() string {
	if runtime.GOOS == "windows" {
		return "ping -n 2 127.0.0.1 >NUL"
	}
	return "sleep 0.05"
}

func shellMarkerCommand(marker string) string {
	if runtime.GOOS == "windows" {
		return "echo started > \"" + marker + "\" & " + shellSleepCommand()
	}
	return "printf started > " + strconv.Quote(marker) + "; " + shellSleepCommand()
}

// newForeignRun publishes a running ledger entry owned by another server process.
func newForeignRun(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	server := newShellTestServer(t, root)
	store, err := runstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(runstore.Meta{ProjectPath: ".", TaskID: "shell:command"})
	if err != nil {
		t.Fatal(err)
	}
	finishRunningRunAtCleanup(t, handle)
	handle.Meta.OwnerPID = os.Getpid() + 1
	handle.Meta.OwnerIdentity = "other-server"
	if persistErr := handle.PersistRunning(); persistErr != nil {
		t.Fatal(persistErr)
	}
	return server, handle.Meta.RunID
}

func finishRunningRunAtCleanup(t *testing.T, handle *runstore.Handle) {
	t.Helper()
	t.Cleanup(func() {
		if err := handle.Finish(runstore.StatusCancelled, -1, "test cleanup", false, false); err != nil {
			t.Errorf("finish running test run cleanup: %v", err)
		}
	})
}

func stopRunOrFail(t *testing.T, server *Server, runID string) {
	t.Helper()
	_, stopped, err := server.stopRun(context.Background(), nil, stopRunInput{RunID: runID})
	if err != nil || stopped.Status != runstore.StatusCancelled {
		t.Fatalf("stopRun = %#v, %v", stopped, err)
	}
}
