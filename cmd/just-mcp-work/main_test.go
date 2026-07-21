// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/agentinit"
)

func TestRunPrintsVersionWithFlagAlias(t *testing.T) {
	output := captureStdout(t, func() {
		if runErr := run([]string{"--version"}); runErr != nil {
			t.Fatal(runErr)
		}
	})
	if !strings.HasPrefix(output, "just-mcp-work ") {
		t.Fatalf("version output = %q", output)
	}
}

func TestHelpFlagsReturnSuccess(t *testing.T) {
	for _, command := range []string{"init", "serve"} {
		if runErr := run([]string{command, "--help"}); runErr != nil {
			t.Errorf("%s --help: %v", command, runErr)
		}
	}
}

func TestServerRunErrorAcceptsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serverRunError(ctx, fmt.Errorf("transport: %w", context.Canceled)); err != nil {
		t.Fatalf("cancelled server = %v", err)
	}
	failure := errors.New("transport failed")
	err := serverRunError(context.Background(), failure)
	if !errors.Is(err, failure) {
		t.Fatalf("server failure = %v", err)
	}
}

func TestInitWritesMCPConfigByDefault(t *testing.T) {
	dir := t.TempDir()
	if initErr := initCommand([]string{"--dir", dir, "--agents", "codex"}); initErr != nil {
		t.Fatal(initErr)
	}
	path := filepath.Join(dir, ".mcp.json")
	// #nosec G304 -- path is created in this test's temporary directory.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), `"just-mcp-work"`) {
		t.Fatalf("MCP config does not contain the server entry:\n%s", data)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = original
	}()
	fn()
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	data, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return string(data)
}

func TestParseServeOptionsResolvesDeadlinePrecedence(t *testing.T) {
	t.Setenv("JMW_SYNC_DEADLINE", "90s")
	options, err := parseServeOptions(nil)
	if err != nil || options.SyncDeadline != 90*time.Second {
		t.Fatalf("environment deadline = %v, %v, want 90s", options.SyncDeadline, err)
	}
	options, err = parseServeOptions([]string{"--sync-deadline", "5s"})
	if err != nil || options.SyncDeadline != 5*time.Second {
		t.Fatalf("flag deadline = %v, %v, want 5s", options.SyncDeadline, err)
	}
	t.Setenv("JMW_SYNC_DEADLINE", "not-a-duration")
	options, err = parseServeOptions(nil)
	if err != nil || options.SyncDeadline != time.Minute {
		t.Fatalf("fallback deadline = %v, %v, want 1m", options.SyncDeadline, err)
	}
	if _, err := parseServeOptions([]string{"unexpected"}); err == nil {
		t.Fatal("positional arguments must be rejected")
	}
}

func TestParseServeOptionsAllowsUnlimitedTimeout(t *testing.T) {
	options, err := parseServeOptions([]string{"--timeout", "0"})
	if err != nil || !options.TimeoutUnlimited || options.Timeout != 0 {
		t.Fatalf("zero timeout options = %#v, %v", options, err)
	}
	if _, err := parseServeOptions([]string{"--timeout", "-1s"}); err == nil {
		t.Fatal("negative timeout must be rejected")
	}
	if _, err := parseServeOptions([]string{"--timeout", "500us"}); err == nil {
		t.Fatal("sub-millisecond timeout must be rejected")
	}
}

func TestInitWritesClaudePermissionsWithFlag(t *testing.T) {
	dir := t.TempDir()
	initErr := initCommand(
		[]string{"--dir", dir, "--agents", "claude", "--claude-permissions", "yes"},
	)
	if initErr != nil {
		t.Fatal(initErr)
	}
	path := filepath.Join(dir, ".claude", "settings.json")
	// #nosec G304 -- path is created in this test's temporary directory.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, rule := range []string{
		agentinit.ClaudeToolPrefix + "run_task",
		agentinit.ClaudeToolPrefix + "run_shell_command",
	} {
		if !strings.Contains(string(data), rule) {
			t.Fatalf("Claude settings do not contain %q:\n%s", rule, data)
		}
	}
}

func TestInitKeepsClaudePermissionsWhenDeclinedByFlag(t *testing.T) {
	dir := t.TempDir()
	initErr := initCommand(
		[]string{"--dir", dir, "--agents", "claude", "--claude-permissions", "no"},
	)
	if initErr != nil {
		t.Fatal(initErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".claude", "settings.json")); !os.IsNotExist(statErr) {
		t.Fatalf("Claude settings were written: %v", statErr)
	}
}

func TestInitRejectsUnsupportedClaudePermissionsMode(t *testing.T) {
	err := initCommand([]string{"--dir", t.TempDir(), "--claude-permissions", "maybe"})
	if err == nil || !strings.Contains(err.Error(), "unsupported Claude permission mode") {
		t.Fatalf("initCommand error = %v", err)
	}
}
