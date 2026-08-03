// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

func TestInitRunnerModesRoundTripThroughManagedServeConfigs(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "go.mod"),
		[]byte("module example.com/runner-round-trip\n\ngo 1.25.0\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := initCommandWithIO(
		[]string{
			"--dir", root,
			"--agents", "codex",
			"--runner-mode", "docker=disabled",
			"--runner-mode", "go=all",
		},
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}

	wantSelections := []runner.Selection{
		{Name: "just", Mode: runner.ModeAll},
		{Name: "cmake", Mode: runner.ModeAll},
		{Name: "docker", Mode: runner.ModeDisabled},
		{Name: "go", Mode: runner.ModeAll},
		{Name: "make", Mode: runner.ModeAll},
	}
	modeArgs := make([]string, 0, len(wantSelections)*2)
	for _, selection := range wantSelections {
		modeArgs = append(
			modeArgs,
			"--runner-mode",
			selection.Name+"="+string(selection.Mode),
		)
	}
	configs := []managedServeConfig{
		{
			name: "mcp json",
			root: ".",
			args: readJSONManagedServeArgs(
				t,
				filepath.Join(root, ".mcp.json"),
			),
		},
		{
			name: "codex toml",
			root: root,
			args: readCodexManagedServeArgs(
				t,
				filepath.Join(root, ".codex", "config.toml"),
			),
		},
	}
	catalog, err := runnerCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range configs {
		t.Run(config.name, func(t *testing.T) {
			assertManagedServeConfigRoundTrip(
				t,
				root,
				config,
				modeArgs,
				wantSelections,
				catalog,
			)
		})
	}
}

type managedServeConfig struct {
	name string
	root string
	args []string
}

func assertManagedServeConfigRoundTrip(
	t *testing.T,
	projectRoot string,
	config managedServeConfig,
	modeArgs []string,
	wantSelections []runner.Selection,
	catalog *runner.Catalog,
) {
	t.Helper()
	wantArgs := append([]string{"serve", "--root", config.root}, modeArgs...)
	if !slices.Equal(config.args, wantArgs) {
		t.Fatalf("managed serve args = %#v, want %#v", config.args, wantArgs)
	}
	options, err := parseServeOptions(config.args[1:])
	if err != nil {
		t.Fatalf("parse persisted serve args: %v", err)
	}
	if options.Root != config.root || !slices.Equal(options.RunnerModes, wantSelections) {
		t.Fatalf("parsed persisted serve options = %#v", options)
	}
	registry, err := catalog.Resolve(options.RunnerModes)
	if err != nil {
		t.Fatalf("resolve persisted runner modes: %v", err)
	}
	if _, found := registry.Get("docker"); found {
		t.Fatal("persisted disabled Docker runner was constructed")
	}
	goRunner, found := registry.Get("go")
	if !found {
		t.Fatal("persisted all-mode Go runner is absent")
	}
	tasks, err := goRunner.ListTasks(context.Background(), projectRoot)
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
