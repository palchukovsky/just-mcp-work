// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build windows

package agentinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestApplyUpdatesJunctionedCodexConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	targetDirectory := filepath.Join(dir, "shared", "codex")
	if err := os.MkdirAll(targetDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDirectory, "config.toml")
	if err := os.WriteFile(
		target,
		[]byte("[mcp_servers.other]\ncommand = \"other\"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(dir, filepath.Dir(codexConfig))
	if output, err := exec.Command(
		"cmd.exe",
		"/d",
		"/c",
		"mklink",
		"/J",
		link,
		targetDirectory,
	).CombinedOutput(); err != nil {
		t.Fatalf("create directory junction: %v\n%s", err, output)
	}

	result, err := Apply(Options{Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, resolvedTarget) {
		t.Fatalf("updated paths = %#v, want resolved target %s", result.Paths, resolvedTarget)
	}
	assertCodexMCPConfig(t, target, dir)
}
