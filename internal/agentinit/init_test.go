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

func TestPromptDescribesRunRecovery(t *testing.T) {
	for _, expected := range []string{
		"run_shell_command",
		"working_directory",
		"run_task",
		"start_task",
		"wait_run",
		"max_wait_ms: 0",
		"get_run_status",
		"tail_bytes: 0",
		"last_output_age_ms",
		"max_depth",
		"include_hidden",
		"runner_mismatch",
		"cannot be widened",
	} {
		if !strings.Contains(Prompt, expected) {
			t.Errorf("Prompt does not mention %q", expected)
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
