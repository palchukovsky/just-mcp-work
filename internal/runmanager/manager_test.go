// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runmanager

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/executor"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

func TestManagerReleasesFinishedRunsAndReportsCompletion(t *testing.T) {
	store := newStore(t)
	var finished atomic.Int64
	manager := New(func() { finished.Add(1) }, 0)
	run, runID := startHelper(t, store, "exit")
	if err := manager.Start(run); err != nil {
		t.Fatal(err)
	}
	run.Wait(context.Background())
	waitFor(t, func() bool {
		_, live := manager.Get(runID)
		return !live && finished.Load() == 1
	}, "run released and completion reported")
	if err := manager.Start(run); err != nil {
		t.Fatalf("re-registering a terminal run = %v, want nil", err)
	}
	if _, live := manager.Get(runID); live {
		t.Fatal("terminal run must not be tracked")
	}
	if _, retained := manager.Terminal(runID); retained {
		t.Fatal("terminal run with persisted metadata must not be retained")
	}
}

func TestManagerLimitsAndSerializesTerminalRepair(t *testing.T) {
	store := newStore(t)
	manager := New(nil, 2)
	for range 3 {
		run, runID := startHelper(t, store, "exit")
		<-run.Done()
		manager.terminal[runID] = run
	}

	manager.repairMu.Lock()
	repaired, err := manager.RepairTerminals()
	manager.repairMu.Unlock()
	if err != nil || repaired != 0 || len(manager.terminal) != 3 {
		t.Fatalf("concurrent repair = %d, %v; retained = %d", repaired, err, len(manager.terminal))
	}

	repaired, err = manager.RepairTerminals()
	if err != nil || repaired != 2 || len(manager.terminal) != 1 {
		t.Fatalf("bounded repair = %d, %v; retained = %d", repaired, err, len(manager.terminal))
	}
	repaired, err = manager.RepairTerminals()
	if err != nil || repaired != 1 || len(manager.terminal) != 0 {
		t.Fatalf("remaining repair = %d, %v; retained = %d", repaired, err, len(manager.terminal))
	}
}

func TestManagerRejectsExcessAndDuplicateRuns(t *testing.T) {
	store := newStore(t)
	manager := New(nil, 2)
	defer shutdown(t, manager)
	first, firstID := startHelper(t, store, "sleep")
	second, _ := startHelper(t, store, "sleep")
	third, _ := startHelper(t, store, "sleep")
	defer stop(t, third)
	if err := manager.Start(first); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(second); err != nil {
		t.Fatal(err)
	}
	err := manager.Start(third)
	if err == nil || !strings.Contains(err.Error(), "too many concurrent runs: limit is 2") {
		t.Fatalf("third run = %v, want concurrency limit error", err)
	}
	if err := manager.Start(first); err == nil ||
		!strings.Contains(err.Error(), "already managed") {
		t.Fatalf("duplicate run = %v, want already-managed error", err)
	}
	if _, live := manager.Get(firstID); !live {
		t.Fatal("first run must stay tracked")
	}
	if err := manager.Start(nil); err == nil {
		t.Fatal("nil run must be rejected")
	}
}

func TestManagerReservationsApplyConcurrencyBeforeStart(t *testing.T) {
	manager := New(nil, 1)
	if err := manager.Reserve("first"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reserve("second"); err == nil ||
		!strings.Contains(err.Error(), "too many concurrent runs") {
		t.Fatalf("second reservation = %v, want concurrency limit error", err)
	}
	manager.Release("first")
	if err := manager.Reserve("second"); err != nil {
		t.Fatalf("reservation after release = %v", err)
	}
}

func TestManagerShutdownStopsRunsAndFinalizesLedger(t *testing.T) {
	store := newStore(t)
	manager := New(nil, 0)
	run, runID := startHelper(t, store, "sleep")
	if err := manager.Start(run); err != nil {
		t.Fatal(err)
	}
	shutdown(t, manager)
	meta, err := store.Get(runID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != runstore.StatusCancelled || meta.Error != "server shutdown" {
		t.Fatalf("meta after shutdown = %#v, want cancelled by server shutdown", meta)
	}
	if runstore.ProcessMatches(meta.PID, meta.ProcessIdentity) {
		t.Fatalf("process %d survived shutdown", meta.PID)
	}
	if _, live := manager.Get(runID); live {
		t.Fatal("stopped run must not stay tracked")
	}
}

func TestManagerShutdownReturnsWhenContextExpires(t *testing.T) {
	store := newStore(t)
	manager := New(nil, 0)
	run, _ := startHelper(t, store, "sleep")
	if err := manager.Start(run); err != nil {
		t.Fatal(err)
	}
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		manager.Shutdown(expired)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return for an expired context")
	}
	select {
	case <-run.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not stop its live run after returning")
	}
}

func newStore(t *testing.T) *runstore.Store {
	t.Helper()
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func startHelper(t *testing.T, store *runstore.Store, mode string) (*executor.Run, string) {
	t.Helper()
	handle, err := store.Begin(runstore.Meta{TaskID: "test:" + mode})
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G204,G702 -- fixed test binary and helper selector.
	cmd := exec.CommandContext(
		context.Background(),
		os.Args[0],
		"-test.run=TestManagerHelperProcess",
	)
	cmd.Env = append(os.Environ(), "JMW_RUNMANAGER_HELPER="+mode)
	run, err := executor.Start(
		cmd,
		handle,
		executor.Config{Timeout: time.Minute, Grace: 10 * time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	return run, handle.Meta.RunID
}

func stop(t *testing.T, run *executor.Run) {
	t.Helper()
	if err := run.Stop(); err != nil {
		t.Fatalf("stop run: %v", err)
	}
}

func shutdown(t *testing.T, manager *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager.Shutdown(ctx)
}

func waitFor(t *testing.T, condition func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestManagerHelperProcess(_ *testing.T) {
	switch os.Getenv("JMW_RUNMANAGER_HELPER") {
	case "exit":
		return
	case "sleep":
		for {
			time.Sleep(time.Hour)
		}
	}
}
