// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build windows

package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/runstore"
	"golang.org/x/sys/windows"
)

const windowsTestStillActive = 259

func configureHelperChild() {}

func TestPrepareStartsWindowsProcessSuspended(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "cmd.exe", "/C", "exit", "0")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	prepare(cmd)
	want := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED)
	if cmd.SysProcAttr.CreationFlags != want {
		t.Fatalf("creation flags = %#x, want %#x", cmd.SysProcAttr.CreationFlags, want)
	}
}

func TestExecuteCancellationKillsWindowsJob(t *testing.T) {
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(runstore.Meta{TaskID: "test:windows-job"})
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cancelAfterFileExists(ctx, cancel, pidFile)
	result, err := Execute(
		ctx,
		helperCommand("tree", pidFile),
		handle,
		Config{Timeout: 2 * time.Second, Grace: 20 * time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != runstore.StatusCancelled {
		t.Fatalf("result = %#v, want cancelled", result)
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
		if !windowsProcessAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process %d survived job termination", pid)
}

func cancelAfterFileExists(ctx context.Context, cancel context.CancelFunc, path string) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			cancel()
			return
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				cancel()
				return
			}
		}
	}
}

func windowsProcessAlive(pid int) bool {
	processID, pidErr := windowsPID(pid)
	if pidErr != nil {
		return false
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		processID,
	)
	if err != nil {
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer func() {
		//nolint:errcheck // The liveness result is independent of handle cleanup.
		_ = windows.CloseHandle(process)
	}()
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process, &exitCode); err != nil {
		return true
	}
	return exitCode == windowsTestStillActive
}
