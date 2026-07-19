// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

//nolint:gocyclo // One asynchronous lifecycle is clearer asserted as a single sequence.
func TestStartObservesAndStopsWithoutARequestContext(t *testing.T) {
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(runstore.Meta{TaskID: "test:async"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := Start(
		helperCommand("child", ""),
		handle,
		Config{Timeout: time.Minute, Grace: 10 * time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := run.Snapshot()
	if snapshot.RunID == "" || snapshot.Status != runstore.StatusRunning {
		t.Fatalf("running snapshot = %#v", snapshot)
	}

	// An expired wait must neither finish the run nor stop the process.
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if waited := run.Wait(expired); waited.Status != runstore.StatusRunning {
		t.Fatalf("wait after cancellation = %#v, want still running", waited)
	}
	select {
	case <-run.Done():
		t.Fatal("run finished while only the wait context was cancelled")
	default:
	}

	if stopErr := run.StopWithReason("stopped by test"); stopErr != nil {
		t.Fatal(stopErr)
	}
	final := run.Snapshot()
	if final.Status != runstore.StatusCancelled || final.RunID != snapshot.RunID {
		t.Fatalf("final snapshot = %#v, want cancelled", final)
	}
	if stopErr := run.StopWithReason("second stop"); stopErr != nil {
		t.Fatalf("repeated stop = %v, want nil", stopErr)
	}
	meta, err := store.Get(final.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != runstore.StatusCancelled || meta.Error != "stopped by test" {
		t.Fatalf("ledger entry = %#v, want the first stop reason", meta)
	}
	if runstore.ProcessMatches(meta.PID, meta.ProcessIdentity) {
		t.Fatalf("process %d survived the stop", meta.PID)
	}
}

func TestNeedsMetadataRepairIgnoresOperationalErrors(t *testing.T) {
	operational := &Run{handle: &runstore.Handle{}, done: make(chan struct{})}
	operational.complete(Result{}, errors.New("attach process"))
	if operational.NeedsMetadataRepair() {
		t.Fatal("an operational setup error must not require metadata repair")
	}
	failedWrite := &Run{handle: &runstore.Handle{}, done: make(chan struct{})}
	failedWrite.complete(Result{}, runstore.ErrFinalMetadataPersistence)
	if !failedWrite.NeedsMetadataRepair() {
		t.Fatal("a final metadata persistence error must require repair")
	}
}

func TestStartKeepsProcessAfterMetadataPublishFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not block metadata writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	root := t.TempDir()
	store, err := runstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(runstore.Meta{TaskID: "test:persist"})
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(store.LogRoot(), handle.Meta.RunID)
	if chmodErr := os.Chmod(runDir, 0o500); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	defer func() {
		//nolint:errcheck // Restoring permissions is best-effort test cleanup.
		_ = os.Chmod(runDir, 0o750)
	}()

	run, err := Start(
		helperCommand("success", ""),
		handle,
		Config{Timeout: 30 * time.Second, Grace: 10 * time.Millisecond},
	)
	if err != nil {
		t.Fatalf("Start = %v, want a started run despite the metadata failure", err)
	}
	result := run.Wait(context.Background())
	if result.Status != runstore.StatusOK || !result.OK {
		t.Fatalf("result = %#v, want a completed run", result)
	}
	if !run.NeedsMetadataRepair() {
		t.Fatal("failed terminal metadata write was not marked for repair")
	}
	if chmodErr := os.Chmod(runDir, 0o750); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if repairErr := run.RepairFinalMetadata(); repairErr != nil {
		t.Fatal(repairErr)
	}
	if run.NeedsMetadataRepair() {
		t.Fatal("successful metadata repair must clear the repair marker")
	}
	stderr, err := store.ReadLog(result.RunID, "stderr", 0, 4096)
	if err != nil || !strings.Contains(string(stderr), "publish running metadata") {
		t.Fatalf("stderr = %q, %v, want the publish warning", stderr, err)
	}
}

func TestStartUnlimitedTimeoutDoesNotCreateDeadline(t *testing.T) {
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(runstore.Meta{TaskID: "test:unlimited"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := Start(
		helperCommand("child", ""),
		handle,
		Config{
			Timeout:          time.Nanosecond,
			TimeoutUnlimited: true,
			Grace:            10 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if result := run.Wait(ctx); result.Status != runstore.StatusRunning {
		t.Fatalf("unlimited run = %#v, want running", result)
	}
	if stopErr := run.Stop(); stopErr != nil {
		t.Fatal(stopErr)
	}
}

func TestTimeoutDiagnosticPreservesAppliedLimit(t *testing.T) {
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(runstore.Meta{TaskID: "test:timeout-diagnostic"})
	if err != nil {
		t.Fatal(err)
	}
	const timeout = 10 * time.Millisecond
	run, err := Start(
		helperCommand("child", ""),
		handle,
		Config{Timeout: timeout, Grace: 10 * time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := run.Wait(context.Background())
	if result.Status != runstore.StatusTimeout ||
		!strings.Contains(result.Message, timeout.String()) ||
		!strings.Contains(result.Message, "JMW_TIMEOUT") {
		t.Fatalf("timeout result = %#v", result)
	}
	meta, err := store.Get(result.RunID)
	if err != nil || meta.Error != result.Message {
		t.Fatalf("timeout metadata = %#v, %v", meta, err)
	}
}
