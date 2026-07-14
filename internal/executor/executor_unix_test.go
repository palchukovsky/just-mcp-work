// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build !windows

package executor

import (
	"context"
	"errors"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

func TestExecuteTimeoutKillsProcessGroup(t *testing.T) {
	result := executeTree(context.Background(), t, 80*time.Millisecond)
	if result.Status != runstore.StatusTimeout {
		t.Fatalf("result = %#v, want timeout", result)
	}
}

func TestExecuteCancellationKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	result := executeTree(ctx, t, time.Second)
	if result.Status != runstore.StatusCancelled {
		t.Fatalf("result = %#v, want cancelled", result)
	}
}

func executeTree(ctx context.Context, t *testing.T, timeout time.Duration) Result {
	t.Helper()
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(runstore.Meta{TaskID: "test:tree"})
	if err != nil {
		t.Fatal(err)
	}
	pidFile := t.TempDir() + "/child.pid"
	result, err := Execute(
		ctx,
		helperCommand("tree", pidFile),
		handle,
		Config{Timeout: timeout, Grace: 20 * time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("helper did not write child PID: %v", err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process %d survived termination", pid)
	return result
}
