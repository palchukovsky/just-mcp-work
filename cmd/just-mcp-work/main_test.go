// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPrintsVersionWithFlagAlias(t *testing.T) {
	output := captureStdout(t, func() {
		if err := run([]string{"--version"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.HasPrefix(output, "just-mcp-work ") {
		t.Fatalf("version output = %q", output)
	}
}

func TestInitWritesMCPConfigByDefault(t *testing.T) {
	dir := t.TempDir()
	if err := initCommand([]string{"--dir", dir, "--agents", "codex"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".mcp.json")
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
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
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(data)
}
