// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/palchukovsky/just-mcp-work/internal/agentinit"
	"github.com/palchukovsky/just-mcp-work/internal/policy"
	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

func TestInitRunnerModesRoundTripThroughWorkspacePolicy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "go.mod"),
		[]byte("module example.com/runner-round-trip\n\ngo 1.25.0\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := initWithBetaTest(
		false,
		[]string{
			"--dir", root,
			"--instructions-target", "workspace",
			"--agents", "codex",
			"--ai", "codex",
			"--shell-permission", "ask",
			"--runner-mode", "just=all",
			"--runner-mode", "agent=safe",
			"--runner-mode", "cmake=all",
			"--runner-mode", "docker=disabled",
			"--runner-mode", "go=all",
			"--runner-mode", "make=all",
		},
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}

	wantSelections := []runner.Selection{
		{Name: "just", Mode: runner.ModeAll},
		{Name: "agent", Mode: runner.ModeSafe},
		{Name: "cmake", Mode: runner.ModeAll},
		{Name: "docker", Mode: runner.ModeDisabled},
		{Name: "go", Mode: runner.ModeAll},
		{Name: "make", Mode: runner.ModeAll},
	}
	loaded, err := policy.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Found || !slices.Equal(loaded.Selections, wantSelections) {
		t.Fatalf("workspace policy = %+v, want selections %#v", loaded, wantSelections)
	}
	// Only codex is declared, so only the Codex configuration carries --ai.
	configs := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "mcp json",
			args: readJSONManagedServeArgs(
				t,
				filepath.Join(root, ".mcp.json"),
			),
			want: []string{"serve", "--root", root},
		},
		{
			name: "codex toml",
			args: readCodexManagedServeArgs(
				t,
				filepath.Join(root, ".codex", "config.toml"),
			),
			want: []string{"serve", "--root", root, "--ai", "codex"},
		},
	}
	for _, config := range configs {
		t.Run(config.name, func(t *testing.T) {
			if !slices.Equal(config.args, config.want) {
				t.Fatalf("managed serve args = %#v, want %#v", config.args, config.want)
			}
		})
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := runnerRegistry(root, logger)
	if err != nil {
		t.Fatalf("resolve workspace runner policy: %v", err)
	}
	if _, found := registry.Get("docker"); found {
		t.Fatal("persisted disabled Docker runner was constructed")
	}
	goRunner, found := registry.Get("go")
	if !found {
		t.Fatal("persisted all-mode Go runner is absent")
	}
	tasks, err := goRunner.ListTasks(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		gotIDs = append(gotIDs, task.ID)
	}
	wantIDs := []string{
		"go:build",
		"go:test",
		"go:vet",
		"go:mod:download",
		"go:fmt",
		"go:mod:tidy",
		"go:any",
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("persisted all-mode Go tasks = %#v, want %#v", gotIDs, wantIDs)
	}
}

// initWorkspaceBelowScope writes a workspace whose .mcp.json anchors the scope
// one level above the project init is pointed at, and runs init there. That
// layout separates the directory init writes its state to from the --root a
// hand-written serve invocation is given, which no init-generated configuration
// produces on its own.
func initWorkspaceBelowScope(t *testing.T) (scope string, project string) {
	t.Helper()
	scope, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project = filepath.Join(scope, "project")
	if mkdirErr := os.MkdirAll(project, 0o750); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	for path, contents := range map[string]string{
		filepath.Join(scope, ".mcp.json"): "{}\n",
		filepath.Join(project, "go.mod"): "module example.com/root-below-scope\n" +
			"\ngo 1.25.0\n",
	} {
		if writeErr := os.WriteFile(path, []byte(contents), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if initErr := initWithBetaTest(
		false,
		[]string{
			"--dir", project,
			"--instructions-target", "workspace",
			"--agents", "codex",
			"--ai", "codex",
			"--shell-permission", "ask",
			"--runner-mode", "just=all",
			"--runner-mode", "agent=safe",
			"--runner-mode", "cmake=all",
			"--runner-mode", "docker=disabled",
			"--runner-mode", "go=all",
			"--runner-mode", "make=all",
		},
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	); initErr != nil {
		t.Fatal(initErr)
	}
	return scope, project
}

// The two tests below exercise resolveWorkspaceState directly, so they pin the
// resolution rather than serve's use of it. The two that follow drive serve and
// are what would fail if the wiring were reverted.
func TestResolveWorkspaceStateFindsPolicyAndManifestInitWroteAboveRoot(t *testing.T) {
	scope, project := initWorkspaceBelowScope(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stateRoot, surfaces, err := resolveWorkspaceState(project, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	if stateRoot != scope {
		t.Fatalf("state root for --root %q = %q, want init scope %q", project, stateRoot, scope)
	}
	if surfaces.AgentGuidePath == "" {
		t.Fatal("managed manifest at the state root recorded no agent guide")
	}
	loaded, err := policy.Load(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Found {
		t.Fatalf("runner policy %s was not found", policy.Path(stateRoot))
	}
	// Neither file exists below the scope, and the manifest's absence is silently
	// accepted, so reading them at the served root would restore the defect.
	if _, statErr := os.Stat(policy.Path(project)); !os.IsNotExist(statErr) {
		t.Fatalf("stat %s = %v, want no policy below the scope", policy.Path(project), statErr)
	}
	below, err := agentinit.VerifyManagedSurfaces(project)
	if err != nil || below != (agentinit.ManagedSurfaces{}) {
		t.Fatalf("managed surfaces below the scope = %+v, %v, want an empty result", below, err)
	}
}

func TestResolvedStateRootKeepsRunnersEnabledBelowWorkspaceScope(t *testing.T) {
	_, project := initWorkspaceBelowScope(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stateRoot, _, err := resolveWorkspaceState(project, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runnerRegistry(stateRoot, logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Get("go"); !found {
		t.Fatal("Go runner enabled by the workspace policy is absent")
	}
	if _, found := registry.Get("docker"); found {
		t.Fatal("Docker runner disabled by the workspace policy was constructed")
	}
	// The registry must stay a strict read of the one root it is given: an upward
	// search inside it would be an unrequested fallback rather than this fix.
	below, err := runnerRegistry(project, logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := below.Get("go"); found {
		t.Fatal("runnerRegistry searched above the root it was given")
	}
}

func TestServeReadsWorkspaceStateAtTheScopeInitWroteIt(t *testing.T) {
	scope, project := initWorkspaceBelowScope(t)

	// Reaching an unparsable policy at the scope proves serve verified the
	// manifest there too: an unverified manifest below would have been accepted
	// silently and never named.
	if err := os.WriteFile(policy.Path(scope), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A parsable but incomplete policy below the scope. A serve still reading the
	// served root refuses over this one instead of starting and blocking the test.
	if err := os.WriteFile(
		policy.Path(project),
		[]byte(`{"version":1,"runners":[{"name":"go","mode":"all"}]}`+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	err := serve([]string{"--root", project})
	if err == nil ||
		!strings.Contains(err.Error(), "load runner policy") ||
		!strings.Contains(err.Error(), policy.Path(scope)) ||
		strings.Contains(err.Error(), policy.Path(project)) {
		t.Fatalf("serve error = %v, want the runner policy read at scope %s", err, scope)
	}
}

func TestServeRetiredRunnerModeNamesThePolicyAtTheScope(t *testing.T) {
	scope, project := initWorkspaceBelowScope(t)

	err := serve([]string{"--root", project, "--runner-mode", "go=all"})
	if err == nil ||
		!strings.Contains(err.Error(), policy.Path(scope)) ||
		strings.Contains(err.Error(), policy.Path(project)) {
		t.Fatalf("serve error = %v, want the migration message to name %s", err, policy.Path(scope))
	}
}

func readJSONManagedServeArgs(t *testing.T, path string) []string {
	t.Helper()
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCPServers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	return config.MCPServers["just-mcp-work"].Args
}

func readCodexManagedServeArgs(t *testing.T, path string) []string {
	t.Helper()
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCPServers map[string]struct {
			Args []string
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(data), &config); err != nil {
		t.Fatal(err)
	}
	return config.MCPServers["just-mcp-work"].Args
}
