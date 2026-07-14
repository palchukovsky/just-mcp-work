// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

func TestExecuteCapturesSuccessAndNonzero(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
		ok   bool
		code int
	}{
		{name: "success", mode: "success", ok: true, code: 0},
		{name: "nonzero", mode: "nonzero", ok: false, code: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := runstore.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			handle, err := store.Begin(runstore.Meta{TaskID: "test:helper"})
			if err != nil {
				t.Fatal(err)
			}
			cmd := helperCommand(test.mode, "")
			result, err := Execute(
				context.Background(),
				cmd,
				handle,
				Config{Timeout: 5 * time.Second, Grace: 10 * time.Millisecond},
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.OK != test.ok || result.ExitCode != test.code {
				t.Fatalf("result = %#v", result)
			}
			stdout, err := store.ReadLog(result.RunID, "stdout", 0, 1024)
			if err != nil || !strings.Contains(string(stdout), "stdout\n") {
				t.Fatalf("stdout = %q, %v", stdout, err)
			}
			stderr, err := store.ReadLog(result.RunID, "stderr", 0, 1024)
			if err != nil || string(stderr) != "stderr\n" {
				t.Fatalf("stderr = %q, %v", stderr, err)
			}
		})
	}
}

func TestExecuteCancellation(t *testing.T) {
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(runstore.Meta{TaskID: "test:cancel"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	result, err := Execute(
		ctx,
		helperCommand("tree", ""),
		handle,
		Config{Timeout: time.Second, Grace: 20 * time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != runstore.StatusCancelled {
		t.Fatalf("result = %#v, want cancelled", result)
	}
}

func TestExecutorHelperProcess(_ *testing.T) {
	mode := os.Getenv("JMW_EXECUTOR_HELPER")
	if mode == "" {
		return
	}
	//nolint:errcheck // The helper exits immediately when test output is unavailable.
	_, _ = os.Stdout.WriteString("stdout\n")
	//nolint:errcheck // The helper exits immediately when test output is unavailable.
	_, _ = os.Stderr.WriteString("stderr\n")
	switch mode {
	case "success":
		return
	case "nonzero":
		os.Exit(7)
	case "child":
		configureHelperChild()
		for {
			time.Sleep(time.Hour)
		}
	case "tree":
		child := helperCommand("child", "")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		// #nosec G304,G703 -- the test controls its temporary PID file path.
		//nolint:errcheck // The helper process exits with failure only through its parent test.
		_ = os.WriteFile(os.Getenv("JMW_EXECUTOR_CHILD_PID"), []byte(stringPID(child.Process.Pid)), 0o600)
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(3)
}

func helperCommand(mode, pidPath string) *exec.Cmd {
	// #nosec G204,G702 -- fixed test binary and helper selector.
	cmd := exec.CommandContext(
		context.Background(),
		os.Args[0],
		"-test.run=TestExecutorHelperProcess",
	)
	cmd.Env = append(os.Environ(), "JMW_EXECUTOR_HELPER="+mode)
	if pidPath != "" {
		cmd.Env = append(cmd.Env, "JMW_EXECUTOR_CHILD_PID="+pidPath)
	}
	return cmd
}

func stringPID(pid int) string {
	return fmt.Sprint(pid)
}
