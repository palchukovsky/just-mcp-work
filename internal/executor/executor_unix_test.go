// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build !windows

package executor

import (
	"context"
	"errors"
	"fmt"
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

func TestProcessGroupGoneAcceptsRecycledGroupNumber(t *testing.T) {
	for _, test := range []struct {
		err  error
		name string
		want bool
	}{
		{name: "no such process", err: syscall.ESRCH, want: true},
		{name: "recycled group number", err: syscall.EPERM, want: true},
		{name: "wrapped recycled group number", err: fmt.Errorf("probe: %w", syscall.EPERM), want: true},
		{name: "invalid argument", err: syscall.EINVAL, want: false},
		{name: "unrelated failure", err: errors.New("failed"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := processGroupGone(test.err); got != test.want {
				t.Fatalf("processGroupGone(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

func executeTree(ctx context.Context, t *testing.T, timeout time.Duration) Result {
	t.Helper()
	root := t.TempDir()
	store, err := runstore.NewForWorktree(root, root)
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
