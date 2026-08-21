// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/agentinit"
	"github.com/palchukovsky/just-mcp-work/internal/runner"
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
	if initErr := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "codex"},
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	); initErr != nil {
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

func TestInitSnippetPinsSelectedLinkedWorktreeWhenCWDIsDifferent(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	callerDir := filepath.Join(base, "caller")
	mainDir := filepath.Join(base, "main")
	worktreeDir := filepath.Join(base, "linked")
	selectedDir := filepath.Join(worktreeDir, "nested")
	entryDir := filepath.Join(mainDir, ".git", "worktrees", "feature")
	for path, contents := range map[string]string{
		filepath.Join(entryDir, "gitdir"):  filepath.Join(worktreeDir, ".git") + "\n",
		filepath.Join(worktreeDir, ".git"): "gitdir: " + entryDir + "\n",
	} {
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o750); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(path, []byte(contents), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	for _, path := range []string{callerDir, selectedDir} {
		if mkdirErr := os.MkdirAll(path, 0o750); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
	}
	t.Chdir(callerDir)

	var output bytes.Buffer
	if initErr := initCommandWithIO(
		[]string{
			"--dir", selectedDir,
			"--agents", "codex",
			"--write-mcp-config=false",
		},
		strings.NewReader(""),
		&output,
		io.Discard,
	); initErr != nil {
		t.Fatal(initErr)
	}
	jsonStart := strings.Index(output.String(), "{")
	if jsonStart < 0 {
		t.Fatalf("init output has no MCP snippet:\n%s", output.String())
	}
	var snippet struct {
		Servers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(output.String()[jsonStart:]), &snippet); err != nil {
		t.Fatalf("decode MCP snippet: %v\n%s", err, output.String())
	}
	args := snippet.Servers["just-mcp-work"].Args
	wantPrefix := []string{"serve", "--root", worktreeDir}
	if len(args) < len(wantPrefix) || !slices.Equal(args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("snippet args = %#v, want prefix %#v", args, wantPrefix)
	}
}

func TestInitQuestionsUseDefaultsAndPersistCanonicalSelections(t *testing.T) {
	dir := t.TempDir()
	var output bytes.Buffer
	err := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "codex"},
		strings.NewReader(""),
		io.Discard,
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantSelections := []string{
		"just=all",
		"cmake=all",
		"docker=all",
		"go=safe",
		"make=all",
	}
	for _, path := range []string{
		filepath.Join(dir, ".mcp.json"),
		filepath.Join(dir, ".codex", "config.toml"),
	} {
		// #nosec G304 -- path is created in this test's temporary directory.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		text := string(data)
		previous := -1
		for _, selection := range wantSelections {
			position := strings.Index(text, `"`+selection+`"`)
			if position <= previous {
				t.Fatalf("%s does not contain canonical selections in order: %s", path, text)
			}
			previous = position
		}
	}
	text := output.String()
	for _, name := range []string{"just", "cmake", "docker", "go", "make"} {
		if !strings.Contains(text, name+" runner") {
			t.Errorf("init output did not ask for %s runner:\n%s", name, text)
		}
	}
	for _, warning := range []string{
		"WARNING: Runner modes reduce exposed commands; they are not a sandbox.",
		"WARNING: Includes every safe-mode risk and also exposes go:any",
	} {
		if !strings.Contains(text, warning) {
			t.Fatalf("Go risk warning %q was not shown before selection:\n%s", warning, text)
		}
	}
}

func TestInitRunnerOverrideSkipsQuestionAndCanDisable(t *testing.T) {
	dir := t.TempDir()
	var output bytes.Buffer
	err := initCommandWithIO(
		[]string{
			"--dir", dir,
			"--agents", "codex",
			"--runner-mode", " go = disabled ",
		},
		strings.NewReader(""),
		io.Discard,
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "go runner") {
		t.Fatalf("overridden Go runner was still questioned:\n%s", output.String())
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "go=disabled") {
		t.Fatalf("disabled Go selection was not persisted:\n%s", data)
	}
}

func TestInitRunnerQuestionRepromptsAndSharesInputWithClaudeConfirmation(t *testing.T) {
	dir := t.TempDir()
	var output bytes.Buffer
	// The first line is invalid for Just, and the fifth line is a valid Go mode
	// typed in the wrong case, which must be rejected literally rather than
	// silently lowercased. The remaining lines answer the repeated Just
	// question, the other runner questions, and the Claude confirmation.
	input := strings.NewReader("safe\nall\nall\nall\nSAFE\nsafe\nall\ny\n")
	err := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "claude"},
		input,
		io.Discard,
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `Unsupported mode "safe"`) {
		t.Fatalf("invalid runner answer was not reported:\n%s", output.String())
	}
	if !strings.Contains(output.String(), `Unsupported mode "SAFE"`) {
		t.Fatalf("uppercase runner answer was not rejected literally:\n%s", output.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); err != nil {
		t.Fatalf("shared buffered input did not reach Claude confirmation: %v", err)
	}
}

func TestInitEOFUsesDefaultsAndSeparatesPromptsFromResults(t *testing.T) {
	dir := t.TempDir()
	var result bytes.Buffer
	var diagnostics bytes.Buffer
	err := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "codex"},
		strings.NewReader(""),
		&result,
		&diagnostics,
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.String(), " runner ") || strings.Contains(result.String(), "Mode [") {
		t.Fatalf("result output contains prompts:\n%s", result.String())
	}
	if !strings.Contains(result.String(), "Updated ") {
		t.Fatalf("result output does not report changes:\n%s", result.String())
	}
	if !strings.Contains(diagnostics.String(), "go runner") ||
		!strings.Contains(diagnostics.String(), "Mode [safe]:") {
		t.Fatalf("diagnostic output does not contain prompts:\n%s", diagnostics.String())
	}
}

func TestInitDryRunWritesOnlyDiffsToResultOutput(t *testing.T) {
	dir := t.TempDir()
	if err := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "codex"},
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(dir, "AGENTS.md")
	before, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	var result bytes.Buffer
	var diagnostics bytes.Buffer
	err = initCommandWithIO(
		[]string{
			"--dir", dir,
			"--agents", "windsurf",
			"--dry-run",
			"--write-mcp-config=false",
		},
		strings.NewReader(""),
		&result,
		&diagnostics,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.String(), "--- ") ||
		strings.Contains(result.String(), "go runner") || strings.Contains(result.String(), "Mode [") {
		t.Fatalf("dry-run result is not diff-only:\n%s", result.String())
	}
	if !strings.Contains(diagnostics.String(), "go runner") {
		t.Fatalf("dry-run prompts are not on diagnostics:\n%s", diagnostics.String())
	}
	after, err := os.ReadFile(agentPath)
	if err != nil || !slices.Equal(after, before) {
		t.Fatalf("dry run changed agent instructions: %q, %v", after, err)
	}
}

func TestInitHelpAndFlagErrorsUseDiagnosticOutput(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "help", args: []string{"--help"}},
		{name: "error", args: []string{"--unknown"}, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var result bytes.Buffer
			var diagnostics bytes.Buffer
			err := initCommandWithIO(
				testCase.args,
				strings.NewReader(""),
				&result,
				&diagnostics,
			)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("init error = %v, wantErr %v", err, testCase.wantErr)
			}
			if result.Len() != 0 {
				t.Fatalf("result output = %q, want empty", result.String())
			}
			if diagnostics.Len() == 0 {
				t.Fatal("diagnostic output is empty")
			}
		})
	}
}

func TestInitRejectsRunnerOverridesBeforeWritingFiles(t *testing.T) {
	tests := [][]string{
		{"--runner-mode", "unknown=all"},
		{"--runner-mode", "go=invalid"},
		{"--runner-mode", "go=all", "--runner-mode", "go=safe"},
		{"--runner-mode", "just=safe"},
		{"--runner-mode", "GO=safe"},
		{"--runner-mode", "go=SAFE"},
	}
	for _, extra := range tests {
		t.Run(strings.Join(extra, "_"), func(t *testing.T) {
			dir := t.TempDir()
			args := append([]string{"--dir", dir, "--agents", "codex"}, extra...)
			err := initCommandWithIO(args, strings.NewReader(""), io.Discard, io.Discard)
			if err == nil {
				t.Fatalf("init accepted invalid runner override %v", extra)
			}
			for _, path := range []string{"AGENTS.md", ".mcp.json", ".codex"} {
				if _, statErr := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(statErr) {
					t.Fatalf("invalid override wrote %s: %v", path, statErr)
				}
			}
		})
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

func TestParseServeOptionsTracksExplicitRoot(t *testing.T) {
	t.Setenv("JMW_ROOT", "")
	options, err := parseServeOptions(nil)
	if err != nil || options.RootExplicit {
		t.Fatalf("default root options = %#v, %v", options, err)
	}
	options, err = parseServeOptions([]string{"--root", "."})
	if err != nil || !options.RootExplicit {
		t.Fatalf("flag root options = %#v, %v", options, err)
	}
	t.Setenv("JMW_ROOT", t.TempDir())
	options, err = parseServeOptions(nil)
	if err != nil || !options.RootExplicit {
		t.Fatalf("environment root options = %#v, %v", options, err)
	}
}

func TestResolveServeRootAnchorsOnlyImplicitLinkedWorktree(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mainDir := filepath.Join(base, "main")
	worktreeDir := filepath.Join(base, "linked")
	nested := filepath.Join(worktreeDir, "nested")
	entryDir := filepath.Join(mainDir, ".git", "worktrees", "feature")
	for path, contents := range map[string]string{
		filepath.Join(entryDir, "gitdir"):  filepath.Join(worktreeDir, ".git") + "\n",
		filepath.Join(worktreeDir, ".git"): "gitdir: " + entryDir + "\n",
	} {
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o750); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(path, []byte(contents), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if mkdirErr := os.MkdirAll(nested, 0o750); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	root, err := resolveServeRoot(serveOptions{Root: nested})
	if err != nil || root != worktreeDir {
		t.Fatalf("implicit root = %q, %v, want %q", root, err, worktreeDir)
	}
	root, err = resolveServeRoot(serveOptions{Root: nested, RootExplicit: true})
	if err != nil || root != nested {
		t.Fatalf("explicit root = %q, %v, want %q", root, err, nested)
	}
}

func TestResolveServeRootReportsMalformedActiveMarker(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(
		filepath.Join(root, ".git"),
		[]byte("gitdir: first\nsecond\n"),
		0o600,
	); writeErr != nil {
		t.Fatal(writeErr)
	}

	if _, resolveErr := resolveServeRoot(serveOptions{Root: root}); resolveErr == nil ||
		!strings.Contains(resolveErr.Error(), "must contain one line") {
		t.Fatalf("resolveServeRoot error = %v, want malformed root marker error", resolveErr)
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

func TestParseServeOptionsCollectsRunnerModes(t *testing.T) {
	options, err := parseServeOptions(
		[]string{
			"--runner-mode", " go = all ",
			"--runner-mode", "docker=disabled",
			// A mixed-case value is not a parse error: the parser carries it
			// through verbatim, and rejecting a case mismatch is the catalog's
			// job at Resolve, not the flag's job at Set.
			"--runner-mode", "Go=ALL",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []runner.Selection{
		{Name: "go", Mode: runner.ModeAll},
		{Name: "docker", Mode: runner.ModeDisabled},
		{Name: "Go", Mode: runner.Mode("ALL")},
	}
	if !slices.Equal(options.RunnerModes, want) {
		t.Fatalf("runner modes = %#v, want %#v", options.RunnerModes, want)
	}
	for _, value := range []string{"go", "=all", "go="} {
		if _, err := parseServeOptions([]string{"--runner-mode", value}); err == nil {
			t.Fatalf("runner mode %q was accepted", value)
		}
	}
}

func TestProductionRunnerCatalogIncludesEveryRunnerAndUsesDeclaredDefaults(t *testing.T) {
	catalog, err := runnerCatalog()
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"just", "cmake", "docker", "go", "make"}
	if names := catalog.Names(); !slices.Equal(names, wantNames) {
		t.Fatalf("catalog names = %#v, want %#v", names, wantNames)
	}
	registry, err := catalog.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range wantNames {
		if _, found := registry.Get(name); !found {
			t.Errorf("default registry does not contain %q", name)
		}
	}
	goRunner, found := registry.Get("go")
	if !found {
		t.Fatal("default registry does not contain Go")
	}
	projectDir := t.TempDir()
	goMod := []byte("module example.com/default-safe\n\ngo 1.25.0\n")
	if writeErr := os.WriteFile(filepath.Join(projectDir, "go.mod"), goMod, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	tasks, err := goRunner.ListTasks(context.Background(), projectDir)
	if err != nil {
		t.Fatal(err)
	}
	taskIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.ID)
	}
	wantDefaultGoTasks := []string{"go:build", "go:test", "go:vet", "go:mod:download"}
	if !slices.Equal(taskIDs, wantDefaultGoTasks) {
		t.Fatalf("default Go task IDs = %#v, want %#v", taskIDs, wantDefaultGoTasks)
	}
	registry, err = catalog.Resolve(
		[]runner.Selection{
			{Name: "go", Mode: runner.ModeDisabled},
			{Name: "docker", Mode: runner.ModeDisabled},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go", "docker"} {
		if _, found := registry.Get(name); found {
			t.Errorf("disabled registry contains %q", name)
		}
	}
}

func TestProductionCatalogDeclaresEveryInitQuestion(t *testing.T) {
	catalog, err := runnerCatalog()
	if err != nil {
		t.Fatal(err)
	}
	requests := catalog.PermissionRequests()
	if len(requests) != 5 {
		t.Fatalf("permission requests = %#v", requests)
	}
	for _, index := range []int{0, 1, 2, 4} {
		request := requests[index]
		if request.Reviewed || request.Default != runner.ModeAll || len(request.Choices) != 2 ||
			request.Choices[0].Mode != runner.ModeAll ||
			request.Choices[1].Mode != runner.ModeDisabled {
			t.Errorf("unreviewed permission request = %#v", request)
		}
	}
	if request := requests[3]; request.Name != "go" || !request.Reviewed ||
		request.Default != runner.ModeSafe {
		t.Fatalf("Go permission request = %#v", request)
	}
}

func TestProductionRunnerCatalogRejectsInvalidServeSelections(t *testing.T) {
	catalog, err := runnerCatalog()
	if err != nil {
		t.Fatal(err)
	}
	tests := [][]string{
		{"--runner-mode", "unknown=all"},
		{"--runner-mode", "go=invalid"},
		{"--runner-mode", "just=safe"},
		{"--runner-mode", "go=all", "--runner-mode", "go=safe"},
		{"--runner-mode", "GO=safe"},
		{"--runner-mode", "go=SAFE"},
	}
	for _, args := range tests {
		options, parseErr := parseServeOptions(args)
		if parseErr != nil {
			t.Fatalf("parseServeOptions(%v): %v", args, parseErr)
		}
		if _, resolveErr := catalog.Resolve(options.RunnerModes); resolveErr == nil {
			t.Fatalf("catalog accepted selections from %v", args)
		}
	}
}

func TestInitWritesClaudePermissionsWithFlag(t *testing.T) {
	dir := t.TempDir()
	initErr := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "claude", "--claude-permissions", "yes"},
		strings.NewReader(""),
		io.Discard,
		io.Discard,
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
	initErr := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "claude", "--claude-permissions", "no"},
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	)
	if initErr != nil {
		t.Fatal(initErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".claude", "settings.json")); !os.IsNotExist(statErr) {
		t.Fatalf("Claude settings were written: %v", statErr)
	}
}

func TestInitClaudeConfirmationReportsAccurateOutcomeOnFreshWorkspace(t *testing.T) {
	dir := t.TempDir()
	var output bytes.Buffer
	// Closed stdin gives an empty answer at the Claude confirmation prompt on a
	// workspace that never had a settings file, so nothing is actually removed.
	initErr := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "claude"},
		strings.NewReader(""),
		io.Discard,
		&output,
	)
	if initErr != nil {
		t.Fatal(initErr)
	}
	if !strings.Contains(output.String(), "not applied") ||
		!strings.Contains(output.String(), "--claude-permissions=no") {
		t.Fatalf("no-answer message does not report the real outcome:\n%s", output.String())
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".claude", "settings.json")); !os.IsNotExist(statErr) {
		t.Fatalf("Claude settings were created on a fresh workspace: %v", statErr)
	}
}

type erroringReader struct {
	err error
}

func (r erroringReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestInitClaudeConfirmationAbortsOnNonEOFReadFailure(t *testing.T) {
	dir := t.TempDir()
	readErr := errors.New("console broken")
	// Every runner is answered by flag so the only console read left is the
	// Claude confirmation, isolating the failure to that read.
	initErr := initCommandWithIO(
		[]string{
			"--dir", dir,
			"--agents", "claude",
			"--runner-mode", "just=all",
			"--runner-mode", "cmake=all",
			"--runner-mode", "docker=all",
			"--runner-mode", "go=safe",
			"--runner-mode", "make=all",
		},
		erroringReader{err: readErr},
		io.Discard,
		io.Discard,
	)
	if initErr == nil || !errors.Is(initErr, readErr) {
		t.Fatalf("init error = %v, want an error wrapping %v", initErr, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".claude", "settings.json")); !os.IsNotExist(statErr) {
		t.Fatalf("Claude settings were written despite the read failure: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(statErr) {
		t.Fatalf("agent instructions were written despite the read failure: %v", statErr)
	}
}

func TestInitRejectsUnsupportedClaudePermissionsMode(t *testing.T) {
	err := initCommand([]string{"--dir", t.TempDir(), "--claude-permissions", "maybe"})
	if err == nil || !strings.Contains(err.Error(), "unsupported Claude permission mode") {
		t.Fatalf("initCommand error = %v", err)
	}
}
