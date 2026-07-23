// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestApplyIsIdempotentAndPreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# Existing\n\nKeep this.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{Dir: dir, Agents: []string{"codex"}}
	first, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Paths) != 1 || first.Paths[0] != path {
		t.Fatalf("first result paths = %#v", first.Paths)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	afterFirst, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(afterFirst), "# Existing\n\nKeep this.") ||
		strings.Count(string(afterFirst), beginMarker) != 1 {
		t.Fatalf("unexpected managed file:\n%s", afterFirst)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("idempotent apply changed paths: %#v", second.Paths)
	}
}

// TestPromptDescribesTheTokenSavingContract keeps the two halves of the contract
// in the served instructions: when a compact receipt replaces the output, and
// when the output itself is the reason not to use this server at all.
func TestPromptDescribesTheTokenSavingContract(t *testing.T) {
	for _, expected := range []string{
		"just-mcp-work (JMW)",
		"save tokens",
		"USE JMW WHEN",
		"RUN IT DIRECTLY WHEN",
		"output itself is the answer",
		"delegate",
		"run_shell_command",
		"working_directory",
		"run_task",
		"status: running",
		"start_task",
		"wait_run",
		"get_run_status",
		"ok: true",
		"exit code 0",
		"stdout_tail",
		"stderr_tail",
		"get_run_logs",
		"tail_bytes: 0",
	} {
		if !strings.Contains(Prompt(), expected) {
			t.Errorf("Prompt does not mention %q", expected)
		}
	}
}

// TestManagedBlockCarriesTheSameContract keeps the block written into AGENTS.md
// and CLAUDE.md on the same rule as the served prompt, without repeating it in
// full: the block is read by agents that may not have the server attached yet.
func TestManagedBlockCarriesTheSameContract(t *testing.T) {
	flat := strings.Join(strings.Fields(managedBlockText), " ")
	for _, expected := range []string{
		"list_tasks -> run_task/start_task",
		"do not need the full output",
		"directly when its full output",
		"sub-agents",
	} {
		if !strings.Contains(flat, expected) {
			t.Errorf("managed block does not mention %q: %s", expected, flat)
		}
	}
}

// TestPromptAndManagedBlockShareTheContract holds the served instructions and
// the written block to one list of terms. The two texts are worded for
// different readers, so nothing but a shared check keeps them from drifting
// into two different rules.
func TestPromptAndManagedBlockShareTheContract(t *testing.T) {
	shared := []string{
		serverName + " (JMW)",
		"save tokens",
		"full output",
		"list_tasks",
		"run_task",
		"start_task",
	}
	for name, text := range map[string]string{
		"Prompt":        strings.Join(strings.Fields(Prompt()), " "),
		"managed block": strings.Join(strings.Fields(managedBlockText), " "),
	} {
		for _, expected := range shared {
			if !strings.Contains(text, expected) {
				t.Errorf("%s does not carry the shared term %q", name, expected)
			}
		}
	}
}

// TestManagedMarkersCarryTheServerName pins the generated markers and the
// permission prefix to serverName, so a rename cannot reach only some of them
// and orphan the blocks a previous init wrote.
func TestManagedMarkersCarryTheServerName(t *testing.T) {
	for name, text := range map[string]string{
		"beginMarker":      beginMarker,
		"endMarker":        endMarker,
		"codexBegin":       codexBegin,
		"codexEnd":         codexEnd,
		"codexTable":       codexTable,
		"claudeServerRule": claudeServerRule,
	} {
		if !strings.Contains(text, serverName) {
			t.Errorf("%s = %q does not carry the server name %q", name, text, serverName)
		}
	}
}

func TestApplyReplacesModifiedManagedBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	modified := "# Existing\n\n" + beginMarker + "\nuser edit\n" + endMarker + "\n"
	if err := os.WriteFile(path, []byte(modified), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{Dir: dir, Agents: []string{"claude"}}
	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "user edit") ||
		strings.Count(string(data), canonicalBlock()) != 1 {
		t.Fatalf("managed block was not replaced:\n%s", data)
	}
}

func TestApplyUpdatesEarlierManagedPrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	previous := beginMarker + "\nold generated wording\n" + endMarker + "\n"
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Apply(Options{Dir: dir, Agents: []string{"claude"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Paths) != 1 {
		t.Fatalf("updated paths = %#v", result.Paths)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != canonicalBlock() {
		t.Fatalf("managed prompt was not upgraded:\n%s", data)
	}
}

func TestApplyMergesMCPConfigWithoutClobberingOtherServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	before := `{"project":"value","mcpServers":{"other":{"command":"other","args":["serve"]}}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if decodeErr := json.Unmarshal(data, &config); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if config["project"] != "value" {
		t.Fatalf("top-level config was clobbered: %#v", config)
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok || servers["other"] == nil || servers["just-mcp-work"] == nil {
		t.Fatalf("merged servers = %#v", config["mcpServers"])
	}
	assertServerCommand(t, servers)
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("idempotent MCP merge changed paths: %#v", second.Paths)
	}
}

// TestApplyKeepsForeignMCPConfigFormatting pins the promise that init only
// rewrites its own entry: key order, indentation width, and the exact text of
// every other value stay as the operator wrote them.
func TestApplyKeepsForeignMCPConfigFormatting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, mcpConfig)
	before := strings.Join(
		[]string{
			"{",
			`    "zeta": 1,`,
			`    "mcpServers": {`,
			`        "other": { "command": "other", "args": ["serve"] }`,
			"    },",
			`    "alpha": [1, 2]`,
			"}",
			"",
		},
		"\n",
	)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"{\n    \"zeta\": 1,\n",
		`        "other": { "command": "other", "args": ["serve"] },`,
		"\n    \"alpha\": [1, 2]\n}\n",
		"\n        \"just-mcp-work\": {\n            \"command\": ",
	} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("merged config lost %q:\n%s", expected, data)
		}
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if containsPath(second.Paths, path) {
		t.Fatalf("second apply rewrote the config:\n%s", data)
	}
}

// TestApplyKeepsForeignClaudeSettingsFormatting checks the same promise for the
// Claude settings, where init also has to delete its retired entries from lists
// that belong to somebody else.
func TestApplyKeepsForeignClaudeSettingsFormatting(t *testing.T) {
	dir := t.TempDir()
	path := claudeSettingsPath(t, dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	before := strings.Join(
		[]string{
			"{",
			`    "model": "opus",`,
			`    "permissions": {`,
			`        "allow": [`,
			`            "mcp__just-mcp-work__retired_tool",`,
			`            "Bash(git status:*)"`,
			"        ],",
			`        "deny": ["Bash(rm:*)"]`,
			"    },",
			`    "env": { "JMW": "1" }`,
			"}",
			"",
		},
		"\n",
	)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{Dir: dir, Agents: []string{"claude"}, ClaudePermissions: ClaudePermissionsYes}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"{\n    \"model\": \"opus\",\n",
		"\n            \"Bash(git status:*)\",\n            \"mcp__just-mcp-work__run_task\",",
		`        "deny": ["Bash(rm:*)"],`,
		"\n    \"env\": { \"JMW\": \"1\" }\n}\n",
	} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("merged settings lost %q:\n%s", expected, data)
		}
	}
	if strings.Contains(string(data), "retired_tool") {
		t.Fatalf("retired entry survived:\n%s", data)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if containsPath(second.Paths, path) {
		t.Fatalf("second apply rewrote the settings:\n%s", data)
	}
}

// TestApplyKeepsCRLFLineEndings checks that every file init edits keeps the
// line ending it is written with, so a workspace checked out with CRLF does not
// end up with two endings mixed in one file.
func TestApplyKeepsCRLFLineEndings(t *testing.T) {
	dir := t.TempDir()
	settings := claudeSettingsPath(t, dir)
	files := map[string]string{
		filepath.Join(dir, "CLAUDE.md"): "# Notes\r\n",
		filepath.Join(dir, "AGENTS.md"): "# Notes\r\n",
		filepath.Join(dir, codexConfig): "# notes\r\n",
		filepath.Join(dir, mcpConfig):   " \r\n",
		settings:                        "{\r\n  \"permissions\": {\r\n    \"allow\": [\r\n    ]\r\n  }\r\n}\r\n",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	options := Options{
		Dir:               dir,
		Agents:            []string{"claude", "codex"},
		WriteMCPConfig:    true,
		ClaudePermissions: ClaudePermissionsYes,
	}
	first, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Paths) != len(files) {
		t.Fatalf("apply changed %v, want all %d files", first.Paths, len(files))
	}
	for path := range files {
		// #nosec G304 -- path is created in this test's temporary directory.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		text := string(data)
		if strings.Count(text, "\n") != strings.Count(text, "\r\n") {
			t.Fatalf("%s mixes line endings:\n%q", path, text)
		}
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("second apply rewrote %v", second.Paths)
	}
}

// TestApplyRepairsLegacyLFBlocksInCRLFDocuments checks the upgrade path from
// init versions that always wrote their managed blocks with LF. The JSON
// config merged in the same run keeps the ending of its existing content.
func TestApplyRepairsLegacyLFBlocksInCRLFDocuments(t *testing.T) {
	dir := t.TempDir()
	agentsPath := filepath.Join(dir, "AGENTS.md")
	codexPath := filepath.Join(dir, codexConfig)
	mcpPath := filepath.Join(dir, mcpConfig)
	legacyCodexBlock := strings.Join(
		[]string{
			codexBegin,
			codexTable,
			`command = "stale"`,
			codexEnd,
		},
		"\n",
	)
	files := map[string]string{
		agentsPath: "# Notes\r\n\n" + canonicalBlock(),
		codexPath:  "# notes\r\n\n" + legacyCodexBlock + "\n",
		mcpPath:    "{\r\n  \"mcpServers\": {}\r\n}\r\n",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	options := Options{Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	wantPrefixes := map[string]string{
		agentsPath: "# Notes\r\n\r\n" + beginMarker + "\r\n",
		codexPath:  "# notes\r\n\r\n" + codexBegin + "\r\n",
	}
	for path := range files {
		// #nosec G304 -- path is created in this test's temporary directory.
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Count(text, "\n") != strings.Count(text, "\r\n") {
			t.Fatalf("%s still mixes line endings:\n%q", path, text)
		}
		wantPrefix, checked := wantPrefixes[path]
		if checked && !strings.HasPrefix(text, wantPrefix) {
			t.Fatalf("%s kept the legacy block boundary:\n%q", path, text)
		}
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("second apply rewrote %v", second.Paths)
	}
}

// TestApplyKeepsCodexBlockInPlace checks that a managed block an operator moved
// is refreshed where it stands instead of being appended again at the end.
func TestApplyKeepsCodexBlockInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	before := strings.Join(
		[]string{
			codexBegin,
			codexTable,
			`command = "stale"`,
			codexEnd,
			"",
			"[mcp_servers.other]",
			`command = "other"`,
			"",
		},
		"\n",
	)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	assertCodexMCPConfig(t, path, dir)
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), codexBegin) {
		t.Fatalf("managed block moved away from the top:\n%s", data)
	}
	if !strings.HasSuffix(string(data), "[mcp_servers.other]\ncommand = \"other\"\n") {
		t.Fatalf("unmanaged tail was rewritten:\n%s", data)
	}
	if strings.Contains(string(data), "stale") {
		t.Fatalf("stale managed entry survived:\n%s", data)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if containsPath(second.Paths, path) {
		t.Fatalf("second apply rewrote the Codex config:\n%s", data)
	}
}

func TestApplyMergesNearestMCPConfig(t *testing.T) {
	workspace := t.TempDir()
	project := filepath.Join(workspace, "projects", "service")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(workspace, mcpConfig)
	before := `{"mcpServers":{"other":{"command":"other"}}}`
	if err := os.WriteFile(configPath, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(
		Options{
			Dir:            project,
			Agents:         []string{"codex"},
			WriteMCPConfig: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, configPath) {
		t.Fatalf("updated paths = %#v, want %s", result.Paths, configPath)
	}
	if _, statErr := os.Stat(filepath.Join(project, mcpConfig)); !os.IsNotExist(statErr) {
		t.Fatalf("child MCP config unexpectedly exists: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(project, "AGENTS.md")); !os.IsNotExist(statErr) {
		t.Fatalf("child agent instructions unexpectedly exist: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, "AGENTS.md")); statErr != nil {
		t.Fatalf("workspace agent instructions were not created: %v", statErr)
	}
	assertCodexMCPConfig(t, filepath.Join(workspace, codexConfig), workspace)
	if _, statErr := os.Stat(filepath.Join(project, codexConfig)); !os.IsNotExist(statErr) {
		t.Fatalf("child Codex config unexpectedly exists: %v", statErr)
	}

	// #nosec G304 -- configPath is created in this test's temporary directory.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok || servers["other"] == nil || servers["just-mcp-work"] == nil {
		t.Fatalf("merged servers = %#v", servers)
	}
	assertServerCommand(t, servers)
}

func TestApplyCreatesMCPConfigInWorkspaceWhenNoneExists(t *testing.T) {
	dir := t.TempDir()
	result, err := Apply(
		Options{
			Dir:            dir,
			Agents:         []string{"codex"},
			WriteMCPConfig: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, mcpConfig)
	if !containsPath(result.Paths, path) {
		t.Fatalf("updated paths = %#v, want %s", result.Paths, path)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("MCP servers = %#v", config["mcpServers"])
	}
	assertServerCommand(t, servers)
	codexPath := filepath.Join(dir, codexConfig)
	assertCodexMCPConfig(t, codexPath, dir)
	assertFileMode(t, codexPath, 0o600)
}

func TestApplySupportsMissingStandaloneWorkspace(t *testing.T) {
	tests := []struct {
		name   string
		dryRun bool
	}{
		{name: "write"},
		{name: "dry run", dryRun: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			resolvedBase, err := filepath.EvalSymlinks(base)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(base, "missing", "workspace")
			result, err := Apply(
				Options{
					Dir:            dir,
					Agents:         []string{"codex"},
					DryRun:         test.dryRun,
					WriteMCPConfig: true,
				},
			)
			if err != nil {
				t.Fatal(err)
			}

			expectedPaths := []string{
				filepath.Join(dir, "AGENTS.md"),
				filepath.Join(dir, mcpConfig),
				filepath.Join(resolvedBase, "missing", "workspace", codexConfig),
			}
			for _, path := range expectedPaths {
				if !containsPath(result.Paths, path) {
					t.Fatalf("updated paths = %#v, want %s", result.Paths, path)
				}
			}
			if test.dryRun {
				if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
					t.Fatalf("dry-run workspace stat error = %v, want not exist", statErr)
				}
				return
			}
			assertCodexMCPConfig(t, filepath.Join(dir, codexConfig), dir)
		})
	}
}

func TestApplyMergesWorkspaceCodexMCPConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	before := "[mcp_servers.other]\ncommand = \"other\"\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Options{Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true}); err != nil {
		t.Fatal(err)
	}
	assertCodexMCPConfig(t, path, dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[mcp_servers.other]") {
		t.Fatalf("unmanaged Codex server was removed:\n%s", data)
	}
}

func TestApplyRejectsUnmanagedCodexServerWithoutChangingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	before := strings.Join(
		[]string{
			"[mcp_servers.other]",
			"command = \"other\"",
			"",
			"[mcp_servers . \"just-mcp-work\"] # configured manually",
			"command = \"just-mcp-work\"",
			"",
		},
		"\n",
	)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Apply(Options{Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true})
	if err == nil || !strings.Contains(err.Error(), "unmanaged "+codexTable) {
		t.Fatalf("Apply error = %v, want an unmanaged Codex server error", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != before {
		t.Fatalf("Codex config changed after rejected merge:\n%s", data)
	}
	for _, unexpected := range []string{"AGENTS.md", mcpConfig} {
		if _, statErr := os.Stat(filepath.Join(dir, unexpected)); !os.IsNotExist(statErr) {
			t.Fatalf("%s was changed before the Codex config rejection: %v", unexpected, statErr)
		}
	}
}

func TestApplyRejectsInlineCodexServerWithoutChangingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	before := strings.Join(
		[]string{
			"[mcp_servers]",
			"just-mcp-work = { command = \"just-mcp-work\", args = [] }",
			"",
		},
		"\n",
	)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Apply(Options{Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true})
	if err == nil || !strings.Contains(err.Error(), "unmanaged "+codexTable) {
		t.Fatalf("Apply error = %v, want an unmanaged Codex server error", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != before {
		t.Fatalf("Codex config changed after rejected merge:\n%s", data)
	}
	for _, unexpected := range []string{"AGENTS.md", mcpConfig} {
		if _, statErr := os.Stat(filepath.Join(dir, unexpected)); !os.IsNotExist(statErr) {
			t.Fatalf("%s was changed before the Codex config rejection: %v", unexpected, statErr)
		}
	}
}

func TestApplyUpdatesSafeSymlinkedCodexConfigDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir := t.TempDir()
	targetDirectory := filepath.Join(dir, "shared", "codex")
	if err := os.MkdirAll(targetDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDirectory, "config.toml")
	before := "[mcp_servers.other]\ncommand = \"other\"\n"
	if err := os.WriteFile(target, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("shared", "codex"),
		filepath.Join(dir, filepath.Dir(codexConfig)),
	); err != nil {
		t.Fatal(err)
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
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[mcp_servers.other]") {
		t.Fatalf("unmanaged Codex server was removed:\n%s", data)
	}
}

func TestApplyUpdatesSafeSymlinkedCodexConfigFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "shared-config.toml")
	before := []byte("[mcp_servers.other]\ncommand = \"other\"\n")
	if err := os.WriteFile(target, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", filepath.Base(target)), path); err != nil {
		t.Fatal(err)
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
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Codex config link was replaced: mode = %s", info.Mode())
	}
}

func TestApplyRejectsEscapingCodexConfigSymlinkWithoutChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, filepath.Dir(codexConfig))); err != nil {
		t.Fatal(err)
	}

	_, err := Apply(Options{Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true})
	if err == nil || !strings.Contains(err.Error(), "resolves outside workspace scope") {
		t.Fatalf("Apply error = %v, want an escaping Codex directory error", err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "config.toml")); !os.IsNotExist(statErr) {
		t.Fatalf("escaping symlink target was unexpectedly changed: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(statErr) {
		t.Fatalf("agent instructions changed before the Codex path rejection: %v", statErr)
	}
}

func TestApplyRejectsInvalidCodexConfigSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	tests := []struct {
		name      string
		link      string
		prepare   func(t *testing.T, dir string)
		wantError string
	}{
		{
			name:      "broken",
			link:      "missing-config.toml",
			wantError: "resolve Codex config",
		},
		{
			name:      "loop",
			link:      "config.toml",
			wantError: "resolve Codex config",
		},
		{
			name: "non-regular target",
			link: "config-directory",
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(dir, "config-directory"), 0o750); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "not a regular file",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			directory := filepath.Join(dir, filepath.Dir(codexConfig))
			if err := os.Mkdir(directory, 0o750); err != nil {
				t.Fatal(err)
			}
			if test.prepare != nil {
				test.prepare(t, directory)
			}
			if err := os.Symlink(test.link, filepath.Join(dir, codexConfig)); err != nil {
				t.Fatal(err)
			}

			_, err := Apply(
				Options{Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true},
			)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Apply error = %v, want error containing %q", err, test.wantError)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(statErr) {
				t.Fatalf("agent instructions changed before the Codex path rejection: %v", statErr)
			}
		})
	}
}

func TestApplyRejectsNonRegularNearestMCPConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, mcpConfig)
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(
		Options{
			Dir:            dir,
			Agents:         []string{"codex"},
			WriteMCPConfig: true,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Apply error = %v, want a non-regular file error", err)
	}
}

func TestApplyUpdatesNearestAgentInstruction(t *testing.T) {
	workspace := t.TempDir()
	project := filepath.Join(workspace, "projects", "service")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# Existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(Options{Dir: project, Agents: []string{"codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, path) {
		t.Fatalf("updated paths = %#v, want %s", result.Paths, path)
	}
	if _, statErr := os.Stat(filepath.Join(project, "AGENTS.md")); !os.IsNotExist(statErr) {
		t.Fatalf("child agent instructions unexpectedly exist: %v", statErr)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), canonicalBlock()) {
		t.Fatalf("agent instructions do not contain the managed block:\n%s", data)
	}
}

func TestApplyUpdatesSafeSymlinkedAgentInstruction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	workspace := t.TempDir()
	instructionsDir := filepath.Join(workspace, "instructions")
	if err := os.Mkdir(instructionsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(instructionsDir, "AGENTS.md")
	if err := os.WriteFile(target, []byte("# Existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "AGENTS.md")
	if err := os.Symlink(filepath.Join("instructions", "AGENTS.md"), path); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(workspace, "projects", "service")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(Options{Dir: project, Agents: []string{"codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, path) {
		t.Fatalf("updated paths = %#v, want %s", result.Paths, path)
	}
	// #nosec G304 -- target is created in this test's temporary directory.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), canonicalBlock()) {
		t.Fatalf("symlink target does not contain the managed block:\n%s", data)
	}
}

func TestMCPConfigSnippetUsesAbsoluteExecutablePath(t *testing.T) {
	snippet, err := MCPConfigSnippet()
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(snippet), &config); err != nil {
		t.Fatal(err)
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("MCP servers = %#v", config["mcpServers"])
	}
	assertServerCommand(t, servers)
}

func assertServerCommand(t *testing.T, servers map[string]any) {
	t.Helper()
	server, ok := servers["just-mcp-work"].(map[string]any)
	if !ok {
		t.Fatalf("just-mcp-work server = %#v", servers["just-mcp-work"])
	}
	command, ok := server["command"].(string)
	if !ok || !filepath.IsAbs(command) {
		t.Fatalf("server command = %#v, want an absolute path", server["command"])
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	if command != executable {
		t.Fatalf("server command = %q, want %q", command, executable)
	}
}

func TestTOMLStringEscapesWindowsPath(t *testing.T) {
	value := `C:\Users\runneradmin\just-mcp-work.exe`
	encoded, err := tomlString(value)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Command string
	}
	if _, err := toml.Decode("command = "+encoded, &config); err != nil {
		t.Fatalf("decode encoded Windows path: %v", err)
	}
	if config.Command != value {
		t.Fatalf("command = %q, want %q", config.Command, value)
	}
}

func assertCodexMCPConfig(t *testing.T, path, root string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCPServers map[string]struct {
			Command           string
			Args              []string
			StartupTimeoutSec int `toml:"startup_timeout_sec"`
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(data), &config); err != nil {
		t.Fatalf("decode workspace Codex config: %v", err)
	}
	server, found := config.MCPServers["just-mcp-work"]
	if !found || server.Command == "" ||
		!slices.Equal(server.Args, []string{"serve", "--root", root}) ||
		server.StartupTimeoutSec != 120 {
		t.Fatalf("invalid workspace Codex config:\n%s", data)
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func containsPath(paths []string, want string) bool {
	return slices.Contains(paths, want)
}

func TestApplyWritesClaudePermissionsWhenAccepted(t *testing.T) {
	dir := t.TempDir()
	path := claudeSettingsPath(t, dir)
	options := Options{
		Dir:               dir,
		Agents:            []string{"claude"},
		ClaudePermissions: ClaudePermissionsYes,
	}
	result, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, path) {
		t.Fatalf("result paths = %#v", result.Paths)
	}
	assertFileMode(t, path, 0o600)
	allow, ask := readClaudePermissions(t, path)
	managed := ClaudeManagedTools()
	if !slices.Equal(allow, managed.Allow) || !slices.Equal(ask, managed.Ask) {
		t.Fatalf("permissions allow = %#v, ask = %#v", allow, ask)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if containsPath(second.Paths, path) {
		t.Fatalf("idempotent apply changed the Claude settings: %#v", second.Paths)
	}
}

func TestApplyReplacesEveryManagedClaudeEntry(t *testing.T) {
	dir := t.TempDir()
	settings := claudeSettingsPath(t, dir)
	if err := os.MkdirAll(filepath.Dir(settings), 0o750); err != nil {
		t.Fatal(err)
	}
	before := `{
	  "model": "opus",
	  "permissions": {
	    "allow": ["Bash(git status:*)", "mcp__just-mcp-work__retired_tool"],
	    "ask": ["mcp__just-mcp-work", "Bash(git add:*)"],
	    "deny": ["mcp__just-mcp-work__run_shell_command", "mcp__just-mcp-work-other__keep"]
	  }
	}`
	if err := os.WriteFile(settings, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Apply(
		Options{
			Dir:               dir,
			Agents:            []string{"claude"},
			ClaudePermissions: ClaudePermissionsYes,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, settings) {
		t.Fatalf("result paths = %#v", result.Paths)
	}
	// #nosec G304 -- settings is created in this test's temporary directory.
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if decodeErr := json.Unmarshal(data, &config); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if config["model"] != "opus" {
		t.Fatalf("unrelated settings were clobbered: %#v", config)
	}
	permissions, ok := config["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions = %#v", config["permissions"])
	}
	allow := stringList(t, permissions["allow"])
	ask := stringList(t, permissions["ask"])
	deny := stringList(t, permissions["deny"])
	managed := ClaudeManagedTools()
	if !slices.Equal(allow, append([]string{"Bash(git status:*)"}, managed.Allow...)) {
		t.Fatalf("allow = %#v", allow)
	}
	if !slices.Equal(ask, append([]string{"Bash(git add:*)"}, managed.Ask...)) {
		t.Fatalf("ask = %#v", ask)
	}
	if !slices.Equal(deny, []string{"mcp__just-mcp-work-other__keep"}) {
		t.Fatalf("deny = %#v", deny)
	}
	if strings.Contains(string(data), "retired_tool") {
		t.Fatalf("unknown managed entry survived:\n%s", data)
	}
}

func TestApplySkipsClaudePermissionsWithoutApproval(t *testing.T) {
	for _, testCase := range []struct {
		options Options
		name    string
	}{
		{name: "declined", options: Options{
			ClaudePermissions: ClaudePermissionsAsk,
			Confirm:           func(string, string) (bool, error) { return false, nil },
		}},
		{name: "no confirmation available", options: Options{
			ClaudePermissions: ClaudePermissionsAsk,
		}},
		{name: "opted out", options: Options{ClaudePermissions: ClaudePermissionsNo}},
		{name: "claude not selected", options: Options{
			Agents:            []string{"codex"},
			ClaudePermissions: ClaudePermissionsYes,
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			options := testCase.options
			options.Dir = dir
			if options.Agents == nil {
				options.Agents = []string{"claude"}
			}
			result, err := Apply(options)
			if err != nil {
				t.Fatal(err)
			}
			path := claudeSettingsPath(t, dir)
			if containsPath(result.Paths, path) {
				t.Fatalf("result reports an unwritten path: %#v", result.Paths)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("Claude settings were written: %v", statErr)
			}
		})
	}
}

func TestApplyReportsClaudePermissionsDiffWithoutAskingOnDryRun(t *testing.T) {
	dir := t.TempDir()
	confirmed := false
	result, err := Apply(
		Options{
			Dir:     dir,
			Agents:  []string{"claude"},
			DryRun:  true,
			Confirm: func(string, string) (bool, error) { confirmed = true; return true, nil },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed {
		t.Fatal("dry run asked for a confirmation")
	}
	path := claudeSettingsPath(t, dir)
	if !containsPath(result.Paths, path) {
		t.Fatalf("result paths = %#v", result.Paths)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("dry run wrote the Claude settings: %v", statErr)
	}
	if !strings.Contains(strings.Join(result.Diffs, "\n"), ClaudeToolPrefix+"run_task") {
		t.Fatalf("diffs do not describe the managed tools: %#v", result.Diffs)
	}
}

func TestApplyRejectsInvalidClaudePermissionLists(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		settings  string
		wantError string
	}{
		{
			name:      "permissions is not an object",
			settings:  `{"permissions": []}`,
			wantError: "is not an object",
		},
		{
			name:      "allow is not a list",
			settings:  `{"permissions": {"allow": "all"}}`,
			wantError: "permissions.allow",
		},
		{
			name:      "invalid JSON",
			settings:  "{",
			wantError: "decode existing",
		},
		{
			name:      "duplicate key",
			settings:  `{"permissions": {"allow": []}, "permissions": {"ask": []}}`,
			wantError: "occurs more than once",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := claudeSettingsPath(t, dir)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(testCase.settings), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Apply(
				Options{
					Dir:               dir,
					Agents:            []string{"claude"},
					ClaudePermissions: ClaudePermissionsYes,
				},
			)
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("Apply error = %v, want %q", err, testCase.wantError)
			}
			// #nosec G304 -- path is created in this test's temporary directory.
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(data) != testCase.settings {
				t.Fatalf("rejected settings were changed:\n%s", data)
			}
		})
	}
}

// TestApplyWritesClaudePermissionsOverNullLists covers the settings file that
// spells an unset permission list as null. validateClaudePermissions accepts
// that spelling, so the edit has to write the list rather than reject it.
func TestApplyWritesClaudePermissionsOverNullLists(t *testing.T) {
	dir := t.TempDir()
	path := claudeSettingsPath(t, dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	settings := "{\n  \"permissions\": {\n    \"allow\": null,\n    \"ask\": null\n  }\n}\n"
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{Dir: dir, Agents: []string{"claude"}, ClaudePermissions: ClaudePermissionsYes}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	allow, ask := readClaudePermissions(t, path)
	managed := ClaudeManagedTools()
	if !slices.Equal(allow, managed.Allow) || !slices.Equal(ask, managed.Ask) {
		t.Fatalf("allow = %#v, ask = %#v", allow, ask)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if containsPath(second.Paths, path) {
		t.Fatal("second apply rewrote the settings")
	}
}

// TestApplyRejectsUnusableMCPConfig keeps init from editing a configuration it
// cannot merge into, and from touching the file it refused.
func TestApplyRejectsUnusableMCPConfig(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		config    string
		wantError string
	}{
		{
			name:      "mcpServers is not an object",
			config:    `{"mcpServers": "everything"}`,
			wantError: "mcpServers in .mcp.json is not an object",
		},
		{
			name:      "duplicate key",
			config:    `{"mcpServers": {}, "mcpServers": {}}`,
			wantError: `object key "mcpServers" occurs more than once`,
		},
		{
			name:      "duplicate server key",
			config:    `{"mcpServers": {"other": {}, "other": {}}}`,
			wantError: `object key "other" occurs more than once`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, mcpConfig)
			if err := os.WriteFile(path, []byte(testCase.config), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Apply(Options{Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true})
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("Apply error = %v, want %q", err, testCase.wantError)
			}
			// #nosec G304 -- path is created in this test's temporary directory.
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(data) != testCase.config {
				t.Fatalf("rejected config was changed:\n%s", data)
			}
		})
	}
}

func TestApplyReportsClaudePermissionConfirmationFailure(t *testing.T) {
	dir := t.TempDir()
	_, err := Apply(
		Options{
			Dir:     dir,
			Agents:  []string{"claude"},
			Confirm: func(string, string) (bool, error) { return false, os.ErrClosed },
		},
	)
	if err == nil || !strings.Contains(err.Error(), "confirm") {
		t.Fatalf("Apply error = %v", err)
	}
}

func TestParseClaudePermissions(t *testing.T) {
	for _, testCase := range []struct {
		value string
		want  ClaudePermissions
	}{
		{value: "", want: ClaudePermissionsAsk},
		{value: "ask", want: ClaudePermissionsAsk},
		{value: " YES ", want: ClaudePermissionsYes},
		{value: "No", want: ClaudePermissionsNo},
	} {
		got, err := ParseClaudePermissions(testCase.value)
		if err != nil || got != testCase.want {
			t.Fatalf("ParseClaudePermissions(%q) = %q, %v", testCase.value, got, err)
		}
	}
	if _, err := ParseClaudePermissions("maybe"); err == nil {
		t.Fatal("ParseClaudePermissions accepted an unsupported mode")
	}
}

func TestClaudeManagedToolsUseTheServerPrefix(t *testing.T) {
	managed := ClaudeManagedTools()
	seen := map[string]struct{}{}
	for _, rule := range slices.Concat(managed.Allow, managed.Ask) {
		if !strings.HasPrefix(rule, ClaudeToolPrefix) || !isManagedClaudeTool(rule) {
			t.Fatalf("managed rule %q is not addressed to this server", rule)
		}
		if _, exists := seen[rule]; exists {
			t.Fatalf("managed rule %q is listed twice", rule)
		}
		seen[rule] = struct{}{}
	}
	if isManagedClaudeTool("mcp__just-mcp-work-other__run_task") {
		t.Fatal("another server's entry is treated as managed")
	}
}

// claudeSettingsPath resolves the settings path the way Apply does, so a
// temporary directory behind a symlink, such as /var on macOS, still matches.
func claudeSettingsPath(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(resolved, ".claude", "settings.json")
}

func readClaudePermissions(t *testing.T, path string) ([]string, []string) {
	t.Helper()
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Ask   []string `json:"ask"`
		} `json:"permissions"`
	}
	if decodeErr := json.Unmarshal(data, &config); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	return config.Permissions.Allow, config.Permissions.Ask
}

func stringList(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value %#v is not a list", value)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, isText := item.(string)
		if !isText {
			t.Fatalf("list item %#v is not a string", item)
		}
		result = append(result, text)
	}
	return result
}
