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
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/agentinit"
	"github.com/palchukovsky/just-mcp-work/internal/policy"
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

func TestPrintUsageAndRunHelpReturnSuccess(t *testing.T) {
	const expected = "Usage: just-mcp-work <command> [options]\n" +
		"\nCommands:\n" +
		"  serve    Start the local STDIO MCP server\n" +
		"  init     Add managed task-server instructions for coding agents\n" +
		"  version  Print version and commit\n"

	var output bytes.Buffer
	if err := printUsage(&output); err != nil {
		t.Fatal(err)
	}
	if output.String() != expected {
		t.Fatalf("usage output = %q, want %q", output.String(), expected)
	}

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "no arguments", args: nil},
		{name: "help command", args: []string{"help"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual := captureStdout(t, func() {
				if runErr := run(test.args); runErr != nil {
					t.Fatal(runErr)
				}
			})
			if actual != expected {
				t.Fatalf("usage output = %q, want %q", actual, expected)
			}
		})
	}
}

type erroringWriter struct {
	err error
}

func defaultRunnerInput() *strings.Reader {
	return strings.NewReader(strings.Repeat("\n", 5))
}

func (w erroringWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestPrintUsageWrapsWriteError(t *testing.T) {
	writeErr := errors.New("write failed")
	err := printUsage(erroringWriter{err: writeErr})
	if !errors.Is(err, writeErr) {
		t.Fatalf("printUsage() error = %v, want wrapped %v", err, writeErr)
	}
	if err.Error() != "write usage: write failed" {
		t.Fatalf("printUsage() error = %q", err)
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
		defaultRunnerInput(),
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
		defaultRunnerInput(),
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
	want := []string{"serve", "--root", worktreeDir}
	if !slices.Equal(args, want) {
		t.Fatalf("snippet args = %#v, want %#v", args, want)
	}
}

func TestInitQuestionsUseDefaultsAndPersistCanonicalSelections(t *testing.T) {
	dir := t.TempDir()
	var output bytes.Buffer
	err := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "codex"},
		defaultRunnerInput(),
		io.Discard,
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantSelections := []runner.Selection{
		{Name: "just", Mode: runner.ModeAll},
		{Name: "cmake", Mode: runner.ModeAll},
		{Name: "docker", Mode: runner.ModeAll},
		{Name: "go", Mode: runner.ModeSafe},
		{Name: "make", Mode: runner.ModeAll},
	}
	loaded, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Found || !slices.Equal(loaded.Selections, wantSelections) {
		t.Fatalf("workspace policy = %+v, want selections %#v", loaded, wantSelections)
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
		defaultRunnerInput(),
		io.Discard,
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "go runner") {
		t.Fatalf("overridden Go runner was still questioned:\n%s", output.String())
	}
	loaded, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantGo := runner.Selection{Name: "go", Mode: runner.ModeDisabled}
	if !slices.Contains(loaded.Selections, wantGo) {
		t.Fatalf("disabled Go selection was not persisted: %+v", loaded)
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

func TestInitEOFRejectsUnansweredRunnerQuestion(t *testing.T) {
	dir := t.TempDir()
	var result bytes.Buffer
	var diagnostics bytes.Buffer
	err := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "codex"},
		strings.NewReader(""),
		&result,
		&diagnostics,
	)
	if err == nil || !strings.Contains(err.Error(), `runner "just"`) ||
		!strings.Contains(err.Error(), "--runner-mode") {
		t.Fatalf("init EOF error = %v, want runner name and --runner-mode guidance", err)
	}
	if strings.Contains(result.String(), " runner ") || strings.Contains(result.String(), "Mode [") {
		t.Fatalf("result output contains prompts:\n%s", result.String())
	}
	if result.Len() != 0 {
		t.Fatalf("result output = %q, want empty after unanswered question", result.String())
	}
	if !strings.Contains(diagnostics.String(), "just runner") ||
		!strings.Contains(diagnostics.String(), "Mode [all, default]:") {
		t.Fatalf("diagnostic output does not contain prompts:\n%s", diagnostics.String())
	}
	if _, statErr := os.Stat(policy.Path(dir)); !os.IsNotExist(statErr) {
		t.Fatalf("unanswered question wrote a policy: %v", statErr)
	}
}

func TestInitDryRunWritesOnlyDiffsToResultOutput(t *testing.T) {
	dir := t.TempDir()
	if err := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "codex"},
		defaultRunnerInput(),
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
		defaultRunnerInput(),
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

func TestInitDryRunReportsPolicyWithoutWritingIt(t *testing.T) {
	dir := t.TempDir()
	var result bytes.Buffer
	if err := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "codex", "--dry-run"},
		defaultRunnerInput(),
		&result,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}
	path := policy.Path(dir)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote policy %s: %v", path, err)
	}
	if !strings.Contains(result.String(), "+++ "+path) {
		t.Fatalf("dry-run output does not report policy %s:\n%s", path, result.String())
	}
}

func TestInitOffersExistingRunnerModesAsCurrent(t *testing.T) {
	dir := t.TempDir()
	catalog, err := runnerCatalog()
	if err != nil {
		t.Fatal(err)
	}
	current, err := catalog.CanonicalSelections(
		[]runner.Selection{
			{Name: "just", Mode: runner.ModeDisabled},
			{Name: "go", Mode: runner.ModeAll},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if saveErr := policy.Save(dir, current); saveErr != nil {
		t.Fatal(saveErr)
	}
	want, err := current.Selections()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	if initErr := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "codex"},
		defaultRunnerInput(),
		io.Discard,
		&diagnostics,
	); initErr != nil {
		t.Fatal(initErr)
	}
	loaded, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(loaded.Selections, want) {
		t.Fatalf("accepted current selections = %#v, want %#v", loaded.Selections, want)
	}
	if !strings.Contains(diagnostics.String(), "Mode [disabled, current]:") ||
		!strings.Contains(diagnostics.String(), "all (default)") {
		t.Fatalf("current offer was not distinguished from the default:\n%s", diagnostics.String())
	}
}

func TestInitReplacesMalformedPolicyAndAnnouncesDefaultFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(policy.Path(dir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	if initErr := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "codex"},
		defaultRunnerInput(),
		io.Discard,
		&diagnostics,
	); initErr != nil {
		t.Fatal(initErr)
	}
	if strings.Count(diagnostics.String(), "could not be read; using declared defaults") != 1 {
		t.Fatalf("malformed-policy fallback was not announced once:\n%s", diagnostics.String())
	}
	if !strings.Contains(diagnostics.String(), policy.Path(dir)) {
		t.Fatalf("malformed-policy announcement does not name %s", policy.Path(dir))
	}
	if _, err := policy.Load(dir); err != nil {
		t.Fatalf("replacement policy is malformed: %v", err)
	}
}

func TestInitReconcilesChangedRunnerSet(t *testing.T) {
	dir := t.TempDir()
	document := `{"version":1,"runners":[` +
		`{"name":"just","mode":"disabled"},` +
		`{"name":"retired","mode":"all"}]}`
	if err := os.WriteFile(policy.Path(dir), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	if initErr := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "codex"},
		defaultRunnerInput(),
		io.Discard,
		&diagnostics,
	); initErr != nil {
		t.Fatal(initErr)
	}
	want := []runner.Selection{
		{Name: "just", Mode: runner.ModeDisabled},
		{Name: "cmake", Mode: runner.ModeAll},
		{Name: "docker", Mode: runner.ModeAll},
		{Name: "go", Mode: runner.ModeSafe},
		{Name: "make", Mode: runner.ModeAll},
	}
	loaded, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(loaded.Selections, want) {
		t.Fatalf("reconciled selections = %#v, want %#v", loaded.Selections, want)
	}
	if strings.Count(diagnostics.String(), "Registered runner set changed") != 1 ||
		!strings.Contains(diagnostics.String(), "Mode [disabled, current]:") ||
		!strings.Contains(diagnostics.String(), "Mode [all, default]:") {
		t.Fatalf("changed runner set was not explained in prompts:\n%s", diagnostics.String())
	}
}

func TestInitRejectsPolicySymlinkWithoutExposingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry-run=%v", dryRun), func(t *testing.T) {
			dir := t.TempDir()
			const targetContents = "policy symlink target must stay private"
			target := filepath.Join(dir, "target.txt")
			if err := os.WriteFile(target, []byte(targetContents), 0o600); err != nil {
				t.Fatal(err)
			}
			path := policy.Path(dir)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			args := []string{"--dir", dir, "--agents", "codex"}
			if dryRun {
				args = append(args, "--dry-run")
			}
			var result bytes.Buffer
			var diagnostics bytes.Buffer
			err := initCommandWithIO(args, defaultRunnerInput(), &result, &diagnostics)
			if err == nil || !strings.Contains(err.Error(), path) ||
				!strings.Contains(err.Error(), "symbolic link") {
				t.Fatalf("init error = %v, want policy path and symbolic-link type", err)
			}
			output := result.String() + diagnostics.String()
			if strings.Contains(output, targetContents) {
				t.Fatalf("init exposed symlink target contents: %q", output)
			}
		})
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
				defaultRunnerInput(),
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
			err := initCommandWithIO(args, defaultRunnerInput(), io.Discard, io.Discard)
			if err == nil {
				t.Fatalf("init accepted invalid runner override %v", extra)
			}
			paths := []string{
				filepath.Join(dir, "AGENTS.md"),
				filepath.Join(dir, ".mcp.json"),
				filepath.Join(dir, ".codex"),
				policy.Path(dir),
			}
			for _, path := range paths {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
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

func TestServeRejectsRetiredRunnerModeWithMigrationMessage(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, ".just-mcp-work", "managed.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := serve([]string{"--root", root, "--runner-mode", "go=safe"})
	if err == nil || !strings.Contains(err.Error(), "no longer accepted by serve") ||
		!strings.Contains(err.Error(), policy.Path(root)) ||
		!strings.Contains(err.Error(), "run just-mcp-work init") {
		t.Fatalf("serve retired runner-mode error = %v, want policy migration guidance", err)
	}
}

func TestServeVerifiesManagedSurfacesBeforeRunnerRegistry(t *testing.T) {
	root := t.TempDir()
	if err := initCommandWithIO(
		[]string{"--dir", root, "--agents", "codex"},
		defaultRunnerInput(),
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}
	managedPath := filepath.Join(root, "AGENTS.md")
	editedBlock := "<!-- BEGIN just-mcp-work (managed) -->\n" +
		"edited\n" +
		"<!-- END just-mcp-work (managed) -->\n"
	if err := os.WriteFile(managedPath, []byte(editedBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy.Path(root), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := serve([]string{"--root", root})
	if err == nil ||
		!strings.Contains(err.Error(), "verify managed surfaces") ||
		!strings.Contains(err.Error(), managedPath) ||
		!strings.Contains(err.Error(), "was edited") ||
		strings.Contains(err.Error(), "load runner policy") {
		t.Fatalf(
			"serve error = %v, want managed-surface refusal before runner policy",
			err,
		)
	}
}

func TestServeWithoutManifestDoesNotSearchForWorkspaceState(t *testing.T) {
	parent := t.TempDir()
	parentManifest := filepath.Join(parent, ".just-mcp-work", "managed.json")
	if err := os.MkdirAll(filepath.Dir(parentManifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parentManifest, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(parent, "nested")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	managedPath := filepath.Join(root, "AGENTS.md")
	managedBlock := "<!-- BEGIN just-mcp-work (managed) -->\n" +
		"locally changed without a manifest\n" +
		"<!-- END just-mcp-work (managed) -->\n"
	if err := os.WriteFile(managedPath, []byte(managedBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy.Path(root), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := serve([]string{"--root", root})
	if err == nil ||
		!strings.Contains(err.Error(), "load runner policy") ||
		strings.Contains(err.Error(), "verify managed surfaces") {
		t.Fatalf(
			"serve error = %v, want unchanged no-manifest policy validation",
			err,
		)
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

func TestRunnerRegistryRequiresCompleteKnownPolicyAndFailsClosedWhenAbsent(t *testing.T) {
	tests := []struct {
		name      string
		document  string
		wantError string
		wantNames []string
	}{
		{
			name: "missing runner",
			document: `{"version":1,"runners":[` +
				`{"name":"just","mode":"all"},` +
				`{"name":"cmake","mode":"all"},` +
				`{"name":"go","mode":"safe"},` +
				`{"name":"make","mode":"all"}]}`,
			wantError: "docker",
		},
		{
			name: "unknown runner",
			document: `{"version":1,"runners":[` +
				`{"name":"just","mode":"all"},` +
				`{"name":"cmake","mode":"all"},` +
				`{"name":"docker","mode":"all"},` +
				`{"name":"go","mode":"safe"},` +
				`{"name":"make","mode":"all"},` +
				`{"name":"future","mode":"all"}]}`,
			wantError: `unknown runner selection "future"`,
		},
		{
			name: "complete policy",
			document: `{"version":1,"runners":[` +
				`{"name":"just","mode":"disabled"},` +
				`{"name":"cmake","mode":"disabled"},` +
				`{"name":"docker","mode":"disabled"},` +
				`{"name":"go","mode":"safe"},` +
				`{"name":"make","mode":"disabled"}]}`,
			wantNames: []string{"go"},
		},
		{name: "absent policy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.document != "" {
				if err := os.WriteFile(
					policy.Path(root),
					[]byte(test.document),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			registry, err := runnerRegistry(root, logger)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) ||
					!strings.Contains(err.Error(), policy.Path(root)) {
					t.Fatalf(
						"runnerRegistry error = %v, want %q and policy path",
						err,
						test.wantError,
					)
				}
				if test.name == "missing runner" &&
					!strings.Contains(err.Error(), "run just-mcp-work init") {
					t.Fatalf("missing runner error has no init guidance: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			catalog, catalogErr := runnerCatalog()
			if catalogErr != nil {
				t.Fatal(catalogErr)
			}
			for _, name := range catalog.Names() {
				_, found := registry.Get(name)
				if found != slices.Contains(test.wantNames, name) {
					t.Errorf(
						"registry runner %q found = %v, want %v",
						name,
						found,
						slices.Contains(test.wantNames, name),
					)
				}
			}
			if len(registry.All()) != len(test.wantNames) {
				t.Fatalf("registry runners = %#v, want %q", registry.All(), test.wantNames)
			}
		})
	}
}

func TestRunnerRegistryWarnsOnceWhenPolicyIsAbsent(t *testing.T) {
	root := t.TempDir()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	registry, err := runnerRegistry(root, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.All()) != 0 {
		t.Fatalf("absent-policy registry contains runners: %#v", registry.All())
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("absent-policy log lines = %d, want 1: %q", len(lines), output.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode absent-policy warning: %v", err)
	}
	const message = "runner policy is absent; no runner is enabled; " +
		"run just-mcp-work init to write it"
	if record["level"] != "WARN" || record["msg"] != message ||
		record["policy_path"] != policy.Path(root) {
		t.Fatalf("absent-policy warning = %#v", record)
	}
}

func TestInitWritesClaudePermissionsWithFlag(t *testing.T) {
	dir := t.TempDir()
	initErr := initCommandWithIO(
		[]string{"--dir", dir, "--agents", "claude", "--claude-permissions", "yes"},
		defaultRunnerInput(),
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
		defaultRunnerInput(),
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
		defaultRunnerInput(),
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
