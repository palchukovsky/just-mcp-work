// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
