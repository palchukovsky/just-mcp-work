// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func nativeTestOptions(t *testing.T, root, agent string) Options {
	t.Helper()
	return Options{
		Dir:                root,
		Agents:             []string{agent},
		InstructionsTarget: InstructionsTargetWorkspace,
		ShellPermission:    ShellPermissionAsk,
		ClaudePermissions:  ClaudePermissionsNo,
		WriteMCPConfig:     true,
		AIFamilies:         testAIFamilies(),
		RunnerModes:        testRunnerModes(t),
	}
}

func TestNativeClientsGenerateUsableConfigsAndVerifyTheirManifest(t *testing.T) {
	for _, config := range nativeMCPConfigs() {
		t.Run(config.agent, func(t *testing.T) {
			root := t.TempDir()
			options := nativeTestOptions(t, root, config.agent)
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			instruction, found := agentTarget(config.agent)
			if !found {
				t.Fatal("native instruction target is missing")
			}
			data, err := os.ReadFile(filepath.Join(root, instruction.path))
			if err != nil || !bytes.Contains(data, []byte(beginMarker)) {
				t.Fatalf("instructions = %q, %v", data, err)
			}
			path := filepath.Join(root, config.path)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = VerifyManagedSurfaces(root); err != nil {
				t.Fatalf("verify new client: %v", err)
			}
			result, err := Apply(options)
			if err != nil || len(result.Paths) != 0 {
				t.Fatalf("repeat init = %q, %v", result.Paths, err)
			}
			// #nosec G703 -- path is a fixed native config inside this test's temporary root.
			if err = os.WriteFile(path, bytes.ReplaceAll(content,
				[]byte("serve"), []byte("tampered")), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err = VerifyManagedSurfaces(root); err == nil {
				t.Fatal("verification accepted a tampered native MCP entry")
			}
		})
	}
}

func TestGrokMCPConfigOmitsCodexApprovalSettings(t *testing.T) {
	config := nativeMCPConfig{agent: "grok"}
	content, err := config.merge(nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Servers map[string]map[string]any `toml:"mcp_servers"`
	}
	if _, err = toml.Decode(string(content), &parsed); err != nil {
		t.Fatal(err)
	}
	entry := parsed.Servers[serverName]
	if entry["command"] == nil || entry["args"] == nil || entry["tools"] != nil {
		t.Fatalf("Grok MCP entry = %#v", entry)
	}
}

func TestNativeClientsPreserveOtherServersAndCleanOnlyTheirEntry(t *testing.T) {
	for _, config := range nativeMCPConfigs() {
		t.Run(config.agent, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, config.path)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			before := []byte("{\"mcpServers\":{\"other\":{\"command\":\"other-tool\"}}}\n")
			if config.agent == "grok" {
				before = []byte("# owner settings\n[mcp_servers.other]\ncommand = \"other-tool\"\n")
			}
			if err := os.WriteFile(path, before, 0o600); err != nil {
				t.Fatal(err)
			}
			options := nativeTestOptions(t, root, config.agent)
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			options.WriteMCPConfig = false
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(after, []byte("other-tool")) ||
				bytes.Contains(after, []byte(serverName)) {
				t.Fatalf("cleanup removed owner data or kept managed entry: %q, %v", after, err)
			}
			if _, err = VerifyManagedSurfaces(root); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeselectedNativeConfigStaysManaged(t *testing.T) {
	root := t.TempDir()
	options := nativeTestOptions(t, root, "grok")
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	options.Agents = []string{"codex"}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".grok/config.toml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- path is the fixed Grok config inside this test's temporary root.
	if err = os.WriteFile(path, bytes.ReplaceAll(content,
		[]byte("serve"), []byte("tampered")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyManagedSurfaces(root); err == nil || !strings.Contains(err.Error(), ".grok") {
		t.Fatalf("deselected native config escaped verification: %v", err)
	}
}

func TestNativeClientCleanupPreservesConfigFileSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	for _, config := range nativeMCPConfigs() {
		t.Run(config.agent, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, config.path)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "shared-config")
			content, err := config.merge(nil, root)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(target, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(filepath.Join("..", filepath.Base(target)), path); err != nil {
				t.Fatal(err)
			}
			options := nativeTestOptions(t, root, config.agent)
			if _, err = Apply(options); err != nil {
				t.Fatal(err)
			}
			options.WriteMCPConfig = false
			if _, err = Apply(options); err != nil {
				t.Fatal(err)
			}
			assertFileSymlink(t, path)
			assertNativeConfigCleaned(t, config.agent, target)
			if _, err = VerifyManagedSurfaces(root); err != nil {
				t.Fatal(err)
			}
			options.WriteMCPConfig = true
			if _, err = Apply(options); err != nil {
				t.Fatalf("reinitialize cleaned config: %v", err)
			}
			assertFileSymlink(t, path)
			if _, err = VerifyManagedSurfaces(root); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertNativeConfigCleaned(t *testing.T, agent, path string) {
	t.Helper()
	// #nosec G304 -- path is the config target created in this test's temporary root.
	after, err := os.ReadFile(path)
	if err != nil || bytes.Contains(after, []byte(serverName)) {
		t.Fatalf("cleanup lost symlink target or kept JMW entry: %q, %v", after, err)
	}
	if agent == "antigravity" && !json.Valid(after) {
		t.Fatalf("cleanup left invalid Antigravity JSON: %q", after)
	}
}

func TestNativeClientRejectsMalformedConfigBeforeWritingInstructions(t *testing.T) {
	for _, config := range nativeMCPConfigs() {
		t.Run(config.agent, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, config.path)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("not a config"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(nativeTestOptions(t, root, config.agent)); err == nil {
				t.Fatal("init accepted a malformed native configuration")
			}
			target, _ := agentTarget(config.agent)
			if _, err := os.Stat(filepath.Join(root, target.path)); !os.IsNotExist(err) {
				t.Fatalf("init wrote instructions before refusing the config: %v", err)
			}
		})
	}
}
