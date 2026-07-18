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
