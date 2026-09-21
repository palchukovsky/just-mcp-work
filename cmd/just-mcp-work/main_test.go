// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/palchukovsky/just-mcp-work/internal/agentinit"
	"github.com/palchukovsky/just-mcp-work/internal/policy"
	"github.com/palchukovsky/just-mcp-work/internal/runner"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
	"github.com/palchukovsky/just-mcp-work/internal/version"
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
	for _, command := range []string{"init", "init-beta-test", "serve"} {
		if runErr := run([]string{command, "--help"}); runErr != nil {
			t.Errorf("%s --help: %v", command, runErr)
		}
	}
}

func TestPrintUsageAndRunHelpReturnSuccess(t *testing.T) {
	const expected = "Usage: just-mcp-work <command> [options]\n" +
		"\nCommands:\n" +
		"  serve           Start the local STDIO MCP server\n" +
		"  init            Add managed task-server instructions for coding agents\n" +
		"  init-beta-test  Add managed instructions with JMW beta feedback guidance\n" +
		"  version         Print version and commit\n"

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
	return strings.NewReader(strings.Repeat("\n", 9))
}

func initArgsWithRunnerModes(dir string) []string {
	return append(initArgsWithRunnerModesWithoutAI(dir), "--ai", "codex")
}

func initArgsWithRunnerModesWithoutAI(dir string) []string {
	return []string{
		"--dir", dir,
		"--instructions-target", "workspace",
		"--agents", "codex",
		"--write-mcp-config=false",
		"--runner-mode", "just=all",
		"--runner-mode", "agent=safe",
		"--runner-mode", "cmake=all",
		"--runner-mode", "docker=all",
		"--runner-mode", "go=safe",
		"--runner-mode", "make=all",
	}
}

func initArgsWithoutQuestions(dir string) []string {
	return initArgsWithRunnerModes(dir)
}

func initArgsWithClaudeShellQuestion(dir string) []string {
	return append(
		initArgsWithRunnerModes(dir),
		"--agents", "claude",
		"--claude-permissions", "yes",
	)
}

func initializeWorkspaceMode(t *testing.T, dir string, betaTest bool) {
	t.Helper()
	if err := initCommandWithIO(
		betaTest,
		initArgsWithoutQuestions(dir),
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceBetaTest(t *testing.T, dir string, want bool) {
	t.Helper()
	if _, err := os.ReadFile(filepath.Join(dir, ".just-mcp-work", "managed.json")); err != nil {
		t.Fatal(err)
	}
	managedSurfaces, err := agentinit.VerifyManagedSurfaces(dir)
	if err != nil {
		t.Fatal(err)
	}
	if managedSurfaces.BetaTest != want {
		t.Fatalf("workspace beta_test = %t, want %t", managedSurfaces.BetaTest, want)
	}
}

func assertWorkspaceInstructionsPointer(t *testing.T, dir string, want bool) {
	t.Helper()
	got, known, err := agentinit.ReadRecordedInstructionsPointer(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !known || got != want {
		t.Fatalf("workspace instructions_pointer = (%t, %t), want (%t, true)", got, known, want)
	}
	if _, err := agentinit.VerifyManagedSurfaces(dir); err != nil {
		t.Fatal(err)
	}
}

func editManagedInstructions(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "AGENTS.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const endMarker = "<!-- END just-mcp-work (managed) -->"
	edited := strings.Replace(string(data), endMarker, "hand edit\n"+endMarker, 1)
	if edited == string(data) {
		t.Fatal("managed end marker not found")
	}
	//nolint:gosec // The test path is a fixed filename below t.TempDir().
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
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

// assertServeProfileDiagnostics checks what serve reports about the AI profile:
// the declared one in full, or a single line saying none was declared, with no
// placeholder family in its log or its instructions.
func assertServeProfileDiagnostics(t *testing.T, ai, stderr, instructions string) {
	t.Helper()
	want := []string{"msg=\"AI profile not declared\""}
	if ai != "" {
		want = []string{
			"msg=\"AI profile selected\"",
			"ai_family=" + ai,
			"profile_id=jmw/" + ai,
			"profile_version=1",
			"transport=mcp-stdio",
		}
	}
	for _, line := range want {
		if !strings.Contains(stderr, line) {
			t.Fatalf("serve diagnostics do not contain %q: %s", line, stderr)
		}
	}
	if ai == "" && (strings.Contains(stderr, "unknown") ||
		strings.Contains(instructions, "AI PROFILE")) {
		t.Fatalf("serve without --ai presented a profile:\n%s\n%s", stderr, instructions)
	}
}

func TestServeUsesVerifiedAgentGuideForInstructions(t *testing.T) {
	for _, test := range []struct {
		name       string
		ai         string
		agentGuide bool
	}{
		{name: "recorded guide", agentGuide: true},
		{name: "pre-guide manifest"},
		{name: "codex profile", ai: "codex"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			initializeWorkspaceMode(t, root, false)
			if !test.agentGuide {
				removeAgentGuideSurface(t, root)
			}
			writeStaleStartupFailure(t, root)
			instructions, stderr := runServeMCP(t, root, test.ai)
			resolvedRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			guidePath := filepath.Join(resolvedRoot, ".just-mcp-work", "guide.txt")
			gotGuide := strings.Contains(instructions, guidePath)
			if gotGuide != test.agentGuide {
				t.Fatalf("served guide pointer = %t, want %t: %s", gotGuide, test.agentGuide, instructions)
			}
			startupFailurePath := filepath.Join(
				root,
				".just-mcp-work",
				"log",
				"startup-error.json",
			)
			if _, err := os.Stat(startupFailurePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("successful serve left startup failure: %v", err)
			}
			assertServeProfileDiagnostics(t, test.ai, stderr, instructions)
		})
	}
}

func runServeMCP(t *testing.T, root, ai string) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	//nolint:gosec // The test intentionally reexecutes the current test binary.
	command := exec.CommandContext(
		ctx,
		os.Args[0],
		"-test.run=^TestServeMCPHelperProcess$",
	)
	command.Env = append(
		os.Environ(),
		"JMW_TEST_HELPER_PROCESS=serve-mcp",
		"JMW_TEST_SERVE_ROOT="+root,
		"JMW_TEST_SERVE_AI="+ai,
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}

	client := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "v1"},
		nil,
	)
	session, err := client.Connect(
		ctx,
		&mcp.IOTransport{Reader: stdout, Writer: stdin},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	instructions := session.InitializeResult().Instructions
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("serve helper failed: %v: %s", err, stderr.String())
	}
	return instructions, stderr.String()
}

func TestServeContinuesWhenStartupFailureRemovalFails(t *testing.T) {
	root := t.TempDir()
	initializeWorkspaceMode(t, root, false)
	startupFailurePath := filepath.Join(
		root,
		".just-mcp-work",
		"log",
		"startup-error.json",
	)
	if err := os.MkdirAll(filepath.Dir(startupFailurePath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(startupFailurePath, 0o750); err != nil {
		t.Fatal(err)
	}
	keepPath := filepath.Join(startupFailurePath, "keep")
	if err := os.WriteFile(keepPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr := runServeMCP(t, root, "")
	if !strings.Contains(
		stderr,
		"level=WARN msg=\"could not remove stale startup failure\"",
	) {
		t.Fatalf("startup failure removal warning missing from stderr: %s", stderr)
	}
	if _, err := os.Stat(keepPath); err != nil {
		t.Fatalf("failed removal changed startup failure directory: %v", err)
	}
}

func TestServeMCPHelperProcess(t *testing.T) {
	mode := os.Getenv("JMW_TEST_HELPER_PROCESS")
	if mode == "" {
		return
	}
	root := os.Getenv("JMW_TEST_SERVE_ROOT")
	switch mode {
	case "serve-mcp":
		args := []string{"--root", root}
		if ai := os.Getenv("JMW_TEST_SERVE_AI"); ai != "" {
			args = append(args, "--ai", ai)
		}
		if err := serve(args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case "main-retired-runner-mode":
		os.Args = []string{
			os.Args[0],
			"serve",
			"--root",
			root,
			"--runner-mode",
			"just=all",
		}
		main()
		t.Fatal("main returned after a startup refusal")
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func TestServeMainRejectsRetiredRunnerModeWithExitOne(t *testing.T) {
	root := t.TempDir()
	t.Setenv("JMW_TIMEOUT", "")
	t.Setenv("JMW_SYNC_DEADLINE", "")
	t.Setenv("JMW_RETENTION", "")
	t.Setenv("JMW_TEST_HELPER_PROCESS", "main-retired-runner-mode")
	t.Setenv("JMW_TEST_SERVE_ROOT", root)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	//nolint:gosec // The test intentionally reexecutes the current test binary.
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServeMCPHelperProcess$")
	command.Env = os.Environ()
	var stderr strings.Builder
	command.Stderr = &stderr
	err := command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("serve process error = %v, want *exec.ExitError", err)
	}
	if got := exitErr.ExitCode(); got != 1 {
		t.Fatalf("serve process exit code = %d, want 1", got)
	}
	want := fmt.Sprintf(
		"--runner-mode is no longer accepted by serve; the runner policy now lives in %s; "+
			"run just-mcp-work init to write it",
		policy.Path(root),
	)
	if !strings.Contains(stderr.String(), "just-mcp-work: "+want) {
		t.Fatalf("serve process stderr = %q, want diagnostic %q", stderr.String(), want)
	}
	failure := readStartupFailureRecord(t, root)
	if failure.Error != want {
		t.Fatalf("recorded startup error = %q, want %q", failure.Error, want)
	}
}

func removeAgentGuideSurface(t *testing.T, root string) {
	t.Helper()
	manifestPath := filepath.Join(root, ".just-mcp-work", "managed.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	surfaces, ok := manifest["surfaces"].([]any)
	if !ok {
		t.Fatal("managed manifest surfaces are not a list")
	}
	filtered := make([]any, 0, len(surfaces)-1)
	for _, value := range surfaces {
		surface, ok := value.(map[string]any)
		if !ok {
			t.Fatal("managed manifest surface is not an object")
		}
		if surface["kind"] != "agent-guide" {
			filtered = append(filtered, surface)
		}
	}
	if len(filtered) != len(surfaces)-1 {
		t.Fatalf("removed %d guide surfaces, want one", len(surfaces)-len(filtered))
	}
	manifest["surfaces"] = filtered
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".just-mcp-work", "guide.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestInitWritesMCPConfigByDefault(t *testing.T) {
	dir := t.TempDir()
	if initErr := initCommandWithIO(
		false,
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

func TestInitOffersBothAIFamiliesByDefault(t *testing.T) {
	dir := t.TempDir()
	var result bytes.Buffer
	var diagnostics bytes.Buffer
	if initErr := initCommandWithIO(
		false,
		initArgsWithRunnerModesWithoutAI(dir),
		strings.NewReader("\n"),
		&result,
		&diagnostics,
	); initErr != nil {
		t.Fatal(initErr)
	}
	for _, want := range []string{
		"Which AI families should the managed just-mcp-work server declare?",
		"  1) codex (default) - declare it in .codex/config.toml",
		"  2) claude (default) - declare it in .mcp.json",
		"AI families [codex,claude, default]:",
	} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Fatalf("AI family prompt lacks %q:\n%s", want, diagnostics.String())
		}
	}
	if strings.Contains(diagnostics.String(), "unknown") {
		t.Fatalf("AI family prompt still offers a placeholder family:\n%s", diagnostics.String())
	}
	if families := initRecordedAIFamilies(t, dir); !slices.Equal(
		families,
		[]string{"codex", "claude"},
	) {
		t.Fatalf("default answer recorded AI families = %#v, want codex and claude", families)
	}
	if args := initSnippetArgs(t, result.String()); !slices.Equal(
		args,
		[]string{"serve", "--root", dir, "--ai", "claude"},
	) {
		t.Fatalf("default snippet args = %#v", args)
	}
}

// TestInitExplicitAIFamiliesWriteManagedArguments covers the flag answering for
// several families at once: each generated configuration declares the family
// whose client reads it, and the manifest records both.
func TestInitExplicitAIFamiliesWriteManagedArguments(t *testing.T) {
	dir := t.TempDir()
	args := append(
		initArgsWithRunnerModesWithoutAI(dir),
		"--write-mcp-config=true",
		"--shell-permission", "ask",
		"--ai", "codex,claude",
	)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		args,
		strings.NewReader(""),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostics.String(), "Which AI families") {
		t.Fatalf("explicit AI families were still questioned:\n%s", diagnostics.String())
	}
	wantMCPArgs := []string{"serve", "--root", dir, "--ai", "claude"}
	if got := initMCPConfigArgs(t, dir); !slices.Equal(got, wantMCPArgs) {
		t.Fatalf("managed MCP args = %#v, want %#v", got, wantMCPArgs)
	}
	codexConfig, err := os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(codexConfig), `"--ai", "codex"`) {
		t.Fatalf("Codex configuration does not declare codex:\n%s", codexConfig)
	}
	if families := initRecordedAIFamilies(t, dir); !slices.Equal(
		families,
		[]string{"codex", "claude"},
	) {
		t.Fatalf("managed manifest AI families = %#v, want codex and claude", families)
	}
}

// TestInitAcceptsAIFamilyNumbersOnTheConsole picks a family by the number
// printed beside it instead of typing its name, and leaves the configuration of
// the client that was not picked without a profile.
func TestInitAcceptsAIFamilyNumbersOnTheConsole(t *testing.T) {
	dir := t.TempDir()
	args := append(
		initArgsWithRunnerModesWithoutAI(dir),
		"--write-mcp-config=true",
		"--shell-permission", "ask",
	)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		args,
		strings.NewReader("2\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	if families := initRecordedAIFamilies(t, dir); !slices.Equal(families, []string{"claude"}) {
		t.Fatalf("numbered answer recorded AI families = %#v, want claude", families)
	}
	wantMCPArgs := []string{"serve", "--root", dir, "--ai", "claude"}
	if got := initMCPConfigArgs(t, dir); !slices.Equal(got, wantMCPArgs) {
		t.Fatalf("managed MCP args = %#v, want %#v", got, wantMCPArgs)
	}
	codexConfig, err := os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(codexConfig), `"--ai"`) {
		t.Fatalf("Codex configuration declares a family that was not picked:\n%s", codexConfig)
	}
}

// TestInitRepeatsAIFamilyQuestionUntilTheAnswerIsUsable keeps the answers that
// cannot be honoured out of the manifest: a number nobody offered, and a family
// named twice.
func TestInitRepeatsAIFamilyQuestionUntilTheAnswerIsUsable(t *testing.T) {
	dir := t.TempDir()
	args := append(
		initArgsWithRunnerModesWithoutAI(dir),
		"--write-mcp-config=true",
		"--shell-permission", "ask",
	)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		args,
		strings.NewReader("9\ncodex,codex\n2 1\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Unsupported AI family "9"; choose any of codex, claude.`,
		`"codex" is named twice.`,
	} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Fatalf("AI family question lacks %q:\n%s", want, diagnostics.String())
		}
	}
	if families := initRecordedAIFamilies(t, dir); !slices.Equal(
		families,
		[]string{"codex", "claude"},
	) {
		t.Fatalf("repeated question recorded AI families = %#v, want codex and claude", families)
	}
}

// TestInitChoosesAIFamiliesAgainWhenTheRecordedOnesAreNotRecognized covers the
// upgrade from a manifest written before the family list: init names the
// manifest it could not use and what is wrong with it, then offers the defaults
// as in a new workspace instead of refusing to continue.
func TestInitChoosesAIFamiliesAgainWhenTheRecordedOnesAreNotRecognized(t *testing.T) {
	dir := t.TempDir()
	args := append(
		initArgsWithRunnerModesWithoutAI(dir),
		"--write-mcp-config=true",
		"--shell-permission", "ask",
	)
	if err := initCommandWithIO(
		false,
		append(slices.Clone(args), "--ai", "claude"),
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, ".just-mcp-work", "managed.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "ai_families")
	document["ai_family"] = "unknown"
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	var diagnostics bytes.Buffer
	if err = initCommandWithIO(
		false,
		args,
		strings.NewReader("\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"The AI families recorded by an earlier init cannot be used; choose them again:",
		manifestPath,
		"no AI families are recorded",
		"AI families [codex,claude, default]:",
	} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Fatalf("unrecognized AI families prompt lacks %q:\n%s", want, diagnostics.String())
		}
	}
	if families := initRecordedAIFamilies(t, dir); !slices.Equal(
		families,
		[]string{"codex", "claude"},
	) {
		t.Fatalf("re-chosen AI families = %#v, want codex and claude", families)
	}
}

// TestInitRunnerQuestionTakesAModeNameOnly keeps the numbered AI-families
// question from spreading to the runner question: its choices carry no numbers,
// a typed number is not a mode, and neither is an answer that names two.
func TestInitRunnerQuestionTakesAModeNameOnly(t *testing.T) {
	catalog, err := runnerCatalog()
	if err != nil {
		t.Fatal(err)
	}
	request := catalog.PermissionRequests()[0]
	if len(request.Choices) < 2 {
		t.Fatalf("runner %q offers %d choices, want at least 2", request.Name, len(request.Choices))
	}
	firstMode := string(request.Choices[0].Mode)
	secondMode := string(request.Choices[1].Mode)

	var closedOutput bytes.Buffer
	closedConsole := initConsole{
		input:  bufio.NewReader(strings.NewReader("1")),
		output: &closedOutput,
	}
	_, err = closedConsole.askRunnerMode(request, singleOffer(string(request.Default), false))
	if err == nil || !strings.Contains(err.Error(), `unsupported mode "1"`) {
		t.Fatalf("numbered runner answer error = %v, want an unsupported mode", err)
	}
	if strings.Contains(closedOutput.String(), "1) "+firstMode) {
		t.Fatalf("runner question numbered its choices:\n%s", closedOutput.String())
	}

	var output bytes.Buffer
	console := initConsole{
		input: bufio.NewReader(
			strings.NewReader(firstMode + "," + secondMode + "\n" + secondMode + "\n"),
		),
		output: &output,
	}
	mode, err := console.askRunnerMode(request, singleOffer(string(request.Default), false))
	if err != nil {
		t.Fatal(err)
	}
	if mode != request.Choices[1].Mode {
		t.Fatalf("mode after a refused answer = %q, want %q", mode, request.Choices[1].Mode)
	}
	wantPrompt := fmt.Sprintf("Unsupported mode %q", firstMode+","+secondMode)
	if !strings.Contains(output.String(), wantPrompt) {
		t.Fatalf("single-choice question lacks %q:\n%s", wantPrompt, output.String())
	}
}

func initMCPConfigArgs(t *testing.T, dir string) []string {
	t.Helper()
	config, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Servers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err = json.Unmarshal(config, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Servers["just-mcp-work"].Args
}

func initRecordedAIFamilies(t *testing.T, dir string) []string {
	t.Helper()
	manifest, err := os.ReadFile(filepath.Join(dir, ".just-mcp-work", "managed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		AIFamilies []string `json:"ai_families"`
	}
	if err = json.Unmarshal(manifest, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.AIFamilies
}

func TestInitOffersRecordedAIFamiliesAsCurrent(t *testing.T) {
	dir := t.TempDir()
	firstArgs := append(
		initArgsWithRunnerModesWithoutAI(dir),
		"--write-mcp-config=true",
		"--shell-permission", "ask",
		"--ai", "claude",
	)
	if err := initCommandWithIO(
		false,
		firstArgs,
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}

	secondArgs := append(
		initArgsWithRunnerModesWithoutAI(dir),
		"--write-mcp-config=true",
		"--shell-permission", "ask",
	)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		secondArgs,
		strings.NewReader("\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  1) codex (default) - ",
		"  2) claude (current, default) - ",
		"AI families [claude, current]:",
	} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Fatalf("recorded AI family prompt lacks %q:\n%s", want, diagnostics.String())
		}
	}
	wantMCPArgs := []string{"serve", "--root", dir, "--ai", "claude"}
	if got := initMCPConfigArgs(t, dir); !slices.Equal(got, wantMCPArgs) {
		t.Fatalf("accepting the current AI families changed managed args = %#v", got)
	}
	if families := initRecordedAIFamilies(t, dir); !slices.Equal(families, []string{"claude"}) {
		t.Fatalf("accepting the current AI families recorded %#v", families)
	}
}

func TestInitRejectsUnusableAIFamiliesBeforeWriting(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "unsupported", value: "gemini", want: "must be one of codex, claude"},
		{
			name:  "placeholder of older releases",
			value: "unknown",
			want:  `unsupported AI family "unknown"`,
		},
		{name: "no family named", value: " ", want: "no AI family is selected"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			err := initCommandWithIO(
				false,
				[]string{
					"--dir", dir,
					"--instructions-target", "workspace",
					"--ai", testCase.value,
				},
				strings.NewReader(""),
				io.Discard,
				io.Discard,
			)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("init --ai %q error = %v, want %q", testCase.value, err, testCase.want)
			}
			if _, statErr := os.Stat(filepath.Join(dir, ".just-mcp-work")); !os.IsNotExist(statErr) {
				t.Fatalf("unusable AI families wrote workspace state: %v", statErr)
			}
		})
	}
}

// TestInitExplicitAIFamiliesRefuseAnUnreadableManifest holds the explicit --ai
// route to the same refusal the console route gives. The recorded document
// carries the surfaces init must carry forward and the modes it must refuse to
// change; when this binary cannot decode it, init stops rather than rewrite the
// workspace from state it cannot see, and the file is left as it was.
func TestInitExplicitAIFamiliesRefuseAnUnreadableManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, ".just-mcp-work", "managed.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	before := []byte("{not json")
	if err := os.WriteFile(manifestPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	args := append(initArgsWithRunnerModesWithoutAI(dir), "--ai", "claude")
	err := initCommandWithIO(false, args, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "decode managed manifest") {
		t.Fatalf("init over an unreadable manifest error = %v, want a decode refusal", err)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil || !bytes.Equal(after, before) {
		t.Fatalf("refused init rewrote the manifest: %q, %v", after, readErr)
	}
}

func initSnippetArgs(t *testing.T, output string) []string {
	t.Helper()
	start := strings.Index(output, "{")
	if start < 0 {
		t.Fatalf("init output has no MCP snippet:\n%s", output)
	}
	var snippet struct {
		Servers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(output[start:]), &snippet); err != nil {
		t.Fatalf("decode MCP snippet: %v\n%s", err, output)
	}
	return snippet.Servers["just-mcp-work"].Args
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
		false,
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
	// No --ai answers the family question with its default, both families,
	// so the .mcp.json snippet declares claude.
	want := []string{"serve", "--root", worktreeDir, "--ai", "claude"}
	if !slices.Equal(args, want) {
		t.Fatalf("snippet args = %#v, want %#v", args, want)
	}
}

func TestInitQuestionsUseDefaultsAndPersistCanonicalSelections(t *testing.T) {
	dir := t.TempDir()
	var output bytes.Buffer
	err := initCommandWithIO(
		false,
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
		{Name: "agent", Mode: runner.ModeSafe},
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
	for _, name := range []string{"just", "agent", "cmake", "docker", "go", "make"} {
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
		false,
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
	// The first line is invalid for Just, and the sixth line is a valid Go mode
	// typed in the wrong case, which must be rejected literally rather than
	// silently lowercased. The remaining lines answer the repeated Just
	// question, the other runner questions, and the Claude confirmation.
	input := strings.NewReader("\nsafe\nall\nsafe\nall\nall\nSAFE\nsafe\nall\n\ny\n")
	err := initCommandWithIO(
		false,
		[]string{
			"--dir", dir,
			"--instructions-target", "workspace",
			"--agents", "claude",
		},
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
		false,
		[]string{
			"--dir", dir,
			"--instructions-target", "workspace",
			"--agents", "codex",
			"--ai", "codex",
		},
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
		false,
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
		false,
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
		false,
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
		false,
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
		false,
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
		false,
		[]string{"--dir", dir, "--agents", "codex"},
		defaultRunnerInput(),
		io.Discard,
		&diagnostics,
	); initErr != nil {
		t.Fatal(initErr)
	}
	want := []runner.Selection{
		{Name: "just", Mode: runner.ModeDisabled},
		{Name: "agent", Mode: runner.ModeSafe},
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
			err := initCommandWithIO(false, args, defaultRunnerInput(), &result, &diagnostics)
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

func TestInitBetaTestCommandUsesInitFlagsAndOwnName(t *testing.T) {
	dir := t.TempDir()
	if err := initCommandWithIO(
		true,
		[]string{
			"--dir", dir,
			"--agents", "codex",
			"--write-mcp-config=false",
			"--runner-mode", "go=safe",
		},
		defaultRunnerInput(),
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "This workspace beta-tests just-mcp-work (JMW) itself.") {
		t.Fatal("init-beta-test did not write the beta paragraph")
	}

	var result bytes.Buffer
	var diagnostics bytes.Buffer
	err = initCommandWithIO(
		true,
		[]string{"unexpected"},
		defaultRunnerInput(),
		&result,
		&diagnostics,
	)
	if err == nil || !strings.Contains(err.Error(), "init-beta-test accepts no positional arguments") {
		t.Fatalf("positional error = %v", err)
	}
	if result.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatalf("positional output = %q, %q", result.String(), diagnostics.String())
	}

	err = initCommandWithIO(
		true,
		[]string{"--help"},
		defaultRunnerInput(),
		&result,
		&diagnostics,
	)
	if err != nil || !strings.Contains(diagnostics.String(), "Usage: just-mcp-work init-beta-test") {
		t.Fatalf("help error = %v, output = %q", err, diagnostics.String())
	}
}

func TestInitInstructionsPointerFlagWritesOnlyThePointer(t *testing.T) {
	for _, betaTest := range []bool{false, true} {
		t.Run(fmt.Sprintf("beta=%t", betaTest), func(t *testing.T) {
			dir := t.TempDir()
			if err := initCommandWithIO(
				betaTest,
				[]string{
					"--dir", dir,
					"--agents", "codex",
					"--instructions-pointer",
					"--write-mcp-config=false",
					"--runner-mode", "go=safe",
				},
				defaultRunnerInput(),
				io.Discard,
				io.Discard,
			); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
			if err != nil {
				t.Fatal(err)
			}
			const pointer = "This workspace uses just-mcp-work (JMW) for its runnable tasks; " +
				"the JMW MCP\nserver itself carries the full usage rules."
			const betaPointer = "This workspace beta-tests just-mcp-work (JMW) itself."
			if strings.Count(string(data), pointer) != 1 ||
				strings.Contains(string(data), "Core rule:") ||
				strings.Contains(string(data), "Report any JMW bug") ||
				strings.Contains(string(data), betaPointer) != betaTest {
				t.Fatalf("--instructions-pointer wrote unexpected guidance:\n%s", data)
			}
		})
	}
}

func TestInitInstructionsPointerFlagAppearsInBothHelpForms(t *testing.T) {
	for _, betaTest := range []bool{false, true} {
		t.Run(fmt.Sprintf("beta=%t", betaTest), func(t *testing.T) {
			var diagnostics bytes.Buffer
			if err := initCommandWithIO(
				betaTest,
				[]string{"--help"},
				strings.NewReader(""),
				io.Discard,
				&diagnostics,
			); err != nil {
				t.Fatal(err)
			}
			output := diagnostics.String()
			if !strings.Contains(output, "[--instructions-pointer]") ||
				!strings.Contains(output, "-instructions-pointer") ||
				!strings.Contains(output, "as a pointer to the server's instructions") {
				t.Fatalf("init help omits --instructions-pointer:\n%s", output)
			}
		})
	}
}

func TestInitInstructionsPointerUsesExplicitOrRecordedChoice(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		explicit    string
		hasRecorded bool
		recorded    bool
		nested      bool
		want        bool
	}{
		{name: "fresh omitted"},
		{
			name:        "recorded pointer omitted from nested directory",
			hasRecorded: true,
			recorded:    true,
			nested:      true,
			want:        true,
		},
		{name: "recorded full omitted", hasRecorded: true},
		{
			name:        "explicit full overrides recorded pointer",
			hasRecorded: true,
			recorded:    true,
			explicit:    "--instructions-pointer=false",
		},
		{
			name:        "explicit pointer overrides recorded full",
			hasRecorded: true,
			explicit:    "--instructions-pointer=true",
			want:        true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			if testCase.hasRecorded {
				initialArgs := append(
					initArgsWithoutQuestions(dir),
					"--write-mcp-config=true",
					"--shell-permission=ask",
					fmt.Sprintf("--instructions-pointer=%t", testCase.recorded),
				)
				if err := initCommandWithIO(
					false,
					initialArgs,
					strings.NewReader(""),
					io.Discard,
					io.Discard,
				); err != nil {
					t.Fatal(err)
				}
			}

			targetDir := dir
			if testCase.nested {
				targetDir = filepath.Join(dir, "nested", "directory")
				if err := os.MkdirAll(targetDir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			args := initArgsWithoutQuestions(targetDir)
			if testCase.explicit != "" {
				args = append(args, testCase.explicit)
			}
			if err := initCommandWithIO(
				false,
				args,
				strings.NewReader(""),
				io.Discard,
				io.Discard,
			); err != nil {
				t.Fatal(err)
			}
			assertWorkspaceInstructionsPointer(t, dir, testCase.want)
		})
	}
}

func TestInitLeavesRecordedBetaWorkspaceWhenConfirmed(t *testing.T) {
	dir := t.TempDir()
	initializeWorkspaceMode(t, dir, true)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		initArgsWithoutQuestions(dir),
		strings.NewReader("yes\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Thank you for volunteering to help improve JMW.",
		"Leave beta testing and continue with plain init?",
	} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Fatalf("leave-beta-test question does not contain %q: %q", want, diagnostics.String())
		}
	}
	assertWorkspaceBetaTest(t, dir, false)
}

func TestInitAsksWhenRecordedBetaModeCannotBeRead(t *testing.T) {
	// A manifest of a schema this binary does not support still decodes, so init
	// asks and continues. A malformed one does not, and the question is followed
	// by the refusal to plan from a document that cannot be read.
	for _, name := range []string{"malformed", "unsupported schema"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			initializeWorkspaceMode(t, dir, true)
			manifestPath := filepath.Join(dir, ".just-mcp-work", "managed.json")
			before, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			after := []byte(strings.Replace(
				string(before),
				`"schema_version": 1`,
				`"schema_version": 2`,
				1,
			))
			if name == "malformed" {
				after = []byte("{not json")
			}
			if bytes.Equal(after, before) {
				t.Fatal("manifest rewrite did not change the document")
			}
			// #nosec G703 -- manifestPath is inside this test's temporary directory.
			if err := os.WriteFile(manifestPath, after, 0o600); err != nil {
				t.Fatal(err)
			}

			var diagnostics bytes.Buffer
			initErr := initCommandWithIO(
				false,
				initArgsWithoutQuestions(dir),
				strings.NewReader("yes\n"),
				io.Discard,
				&diagnostics,
			)
			for _, want := range []string{
				"beta-test mode could not be read",
				"Plain init may remove beta feedback guidance.",
			} {
				if !strings.Contains(diagnostics.String(), want) {
					t.Fatalf("unknown-mode question does not contain %q: %q", want, diagnostics.String())
				}
			}
			if name == "malformed" {
				if initErr == nil ||
					!strings.Contains(initErr.Error(), "decode managed manifest") {
					t.Fatalf("init over a malformed manifest error = %v, want a decode refusal", initErr)
				}
				return
			}
			if initErr != nil {
				t.Fatal(initErr)
			}
			assertWorkspaceBetaTest(t, dir, false)
		})
	}
}

func TestInitKeepsRecordedBetaWorkspaceWhenDeclined(t *testing.T) {
	dir := t.TempDir()
	initializeWorkspaceMode(t, dir, true)
	paths := []string{
		filepath.Join(dir, "AGENTS.md"),
		filepath.Join(dir, ".just-mcp-work", "managed.json"),
		policy.Path(dir),
	}
	before := make(map[string][]byte, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = data
	}

	err := initCommandWithIO(
		false,
		initArgsWithoutQuestions(dir),
		strings.NewReader("no\n"),
		io.Discard,
		io.Discard,
	)
	const wantGuidance = "re-run the same command with init-beta-test in place of init"
	if err == nil || !strings.Contains(err.Error(), wantGuidance) {
		t.Fatalf("declined leave-beta-test error = %v, want %q", err, wantGuidance)
	}
	for path, want := range before {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("declined leave-beta-test confirmation changed %s", path)
		}
	}
}

func TestInitLeavesRecordedBetaWorkspaceAtEndOfInput(t *testing.T) {
	dir := t.TempDir()
	initializeWorkspaceMode(t, dir, true)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		initArgsWithoutQuestions(dir),
		strings.NewReader(""),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"No answer was given",
		"the workspace is leaving beta testing",
		"same command with init-beta-test in place of init",
	} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Fatalf("end-of-input notice does not contain %q: %q", want, diagnostics.String())
		}
	}
	assertWorkspaceBetaTest(t, dir, false)
}

func TestInitDryRunDoesNotAskToLeaveBeta(t *testing.T) {
	dir := t.TempDir()
	initializeWorkspaceMode(t, dir, true)
	args := append(initArgsWithoutQuestions(dir), "--dry-run")
	var result, diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		args,
		erroringReader{err: errors.New("dry-run read input")},
		&result,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostics.String(), "Leave beta testing") {
		t.Fatalf("dry-run asked to leave beta testing: %q", diagnostics.String())
	}
	if !strings.Contains(result.String(), "This workspace beta-tests just-mcp-work") {
		t.Fatalf("dry-run diff does not show removed beta guidance: %q", result.String())
	}
	assertWorkspaceBetaTest(t, dir, true)
}

func TestInitPlainWorkspaceDoesNotAskToLeaveBeta(t *testing.T) {
	dir := t.TempDir()
	initializeWorkspaceMode(t, dir, false)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		initArgsWithoutQuestions(dir),
		strings.NewReader("no\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostics.String(), "Leave beta testing") {
		t.Fatalf("plain init asked to leave beta testing: %q", diagnostics.String())
	}
	assertWorkspaceBetaTest(t, dir, false)
}

func TestInitBetaTestWorkspaceDoesNotAskToLeaveBeta(t *testing.T) {
	dir := t.TempDir()
	initializeWorkspaceMode(t, dir, true)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		true,
		initArgsWithoutQuestions(dir),
		strings.NewReader("no\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostics.String(), "Leave beta testing") {
		t.Fatalf("init-beta-test asked to leave beta testing: %q", diagnostics.String())
	}
	assertWorkspaceBetaTest(t, dir, true)
}

func TestInitRepairsEditedManagedBlockInPlainWorkspace(t *testing.T) {
	dir := t.TempDir()
	initializeWorkspaceMode(t, dir, false)
	editManagedInstructions(t, dir)

	if err := initCommandWithIO(
		false,
		initArgsWithoutQuestions(dir),
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatalf("init edited plain workspace: %v", err)
	}
	assertWorkspaceBetaTest(t, dir, false)
}

func TestInitRepairsEditedManagedBlockInRecordedBetaWorkspace(t *testing.T) {
	dir := t.TempDir()
	initializeWorkspaceMode(t, dir, true)
	editManagedInstructions(t, dir)
	var diagnostics bytes.Buffer

	if err := initCommandWithIO(
		false,
		initArgsWithoutQuestions(dir),
		strings.NewReader("yes\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatalf("init edited beta workspace: %v", err)
	}
	if !strings.Contains(diagnostics.String(), "Leave beta testing and continue with plain init?") {
		t.Fatalf("edited beta workspace question missing: %q", diagnostics.String())
	}
	assertWorkspaceBetaTest(t, dir, false)
}

func TestRunSelectsTheRequestedManagedBlock(t *testing.T) {
	tests := []struct {
		command      string
		name         string
		wantBetaText bool
	}{
		{
			command:      "init-beta-test",
			name:         "beta",
			wantBetaText: true,
		},
		{
			command:      "init",
			name:         "plain",
			wantBetaText: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			// run reads os.Stdin, so every declared runner must be answered by
			// flag or the console question is left unanswered at end of input.
			if err := run([]string{
				test.command,
				"--dir",
				dir,
				"--instructions-target",
				"workspace",
				"--agents",
				"codex",
				"--write-mcp-config=false",
				"--ai",
				"codex",
				"--runner-mode",
				"just=all",
				"--runner-mode",
				"agent=safe",
				"--runner-mode",
				"cmake=all",
				"--runner-mode",
				"docker=all",
				"--runner-mode",
				"go=safe",
				"--runner-mode",
				"make=all",
			}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
			if err != nil {
				t.Fatal(err)
			}
			gotBetaText := strings.Contains(
				string(data),
				"This workspace beta-tests just-mcp-work (JMW) itself.",
			)
			if gotBetaText != test.wantBetaText {
				t.Fatalf("beta text = %t, want %t", gotBetaText, test.wantBetaText)
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
				false,
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
			err := initCommandWithIO(false, args, defaultRunnerInput(), io.Discard, io.Discard)
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

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	defer func() {
		os.Stderr = original
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
	options, _, err := parseServeOptions(nil)
	if err != nil || options.SyncDeadline != 90*time.Second {
		t.Fatalf("environment deadline = %v, %v, want 90s", options.SyncDeadline, err)
	}
	options, _, err = parseServeOptions([]string{"--sync-deadline", "5s"})
	if err != nil || options.SyncDeadline != 5*time.Second {
		t.Fatalf("flag deadline = %v, %v, want 5s", options.SyncDeadline, err)
	}
	t.Setenv("JMW_SYNC_DEADLINE", "not-a-duration")
	options, _, err = parseServeOptions(nil)
	if err != nil || options.SyncDeadline != time.Minute {
		t.Fatalf("fallback deadline = %v, %v, want 1m", options.SyncDeadline, err)
	}
	if _, _, err := parseServeOptions([]string{"unexpected"}); err == nil {
		t.Fatal("positional arguments must be rejected")
	}
}

func TestParseServeOptionsTracksExplicitRoot(t *testing.T) {
	t.Setenv("JMW_ROOT", "")
	options, _, err := parseServeOptions(nil)
	if err != nil || options.RootExplicit {
		t.Fatalf("default root options = %#v, %v", options, err)
	}
	options, _, err = parseServeOptions([]string{"--root", "."})
	if err != nil || !options.RootExplicit {
		t.Fatalf("flag root options = %#v, %v", options, err)
	}
	t.Setenv("JMW_ROOT", t.TempDir())
	options, _, err = parseServeOptions(nil)
	if err != nil || !options.RootExplicit {
		t.Fatalf("environment root options = %#v, %v", options, err)
	}
}

func TestParseServeOptionsSelectsAIProfile(t *testing.T) {
	options, _, err := parseServeOptions(nil)
	if err != nil || options.AIProfile.Declared() {
		t.Fatalf("default AI profile = %#v, %v, want none", options.AIProfile, err)
	}
	for _, family := range []string{"codex", "claude"} {
		options, _, err = parseServeOptions([]string{"--ai", family})
		if err != nil || string(options.AIProfile.Family) != family {
			t.Fatalf("--ai %s profile = %#v, %v", family, options.AIProfile, err)
		}
	}
	for _, family := range []string{"", "unknown", "Codex", "gemini"} {
		if _, _, err = parseServeOptions([]string{"--ai", family}); err == nil {
			t.Fatalf("--ai %q was accepted", family)
		}
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
	options, _, err := parseServeOptions([]string{"--timeout", "0"})
	if err != nil || !options.TimeoutUnlimited || options.Timeout != 0 {
		t.Fatalf("zero timeout options = %#v, %v", options, err)
	}
	if _, _, err := parseServeOptions([]string{"--timeout", "-1s"}); err == nil {
		t.Fatal("negative timeout must be rejected")
	}
	if _, _, err := parseServeOptions([]string{"--timeout", "500us"}); err == nil {
		t.Fatal("sub-millisecond timeout must be rejected")
	}
}

func TestServeRejectsRetiredRunnerModeWithMigrationMessage(t *testing.T) {
	t.Setenv("JMW_TIMEOUT", "")
	t.Setenv("JMW_SYNC_DEADLINE", "")
	t.Setenv("JMW_RETENTION", "")
	root := t.TempDir()
	manifestPath := filepath.Join(root, ".just-mcp-work", "managed.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--root", root, "--runner-mode", "just=all"}
	want := fmt.Sprintf(
		"--runner-mode is no longer accepted by serve; the runner policy now lives in %s; "+
			"run just-mcp-work init to write it",
		policy.Path(root),
	)

	err := serve(args)
	if err == nil || err.Error() != want {
		t.Fatalf("serve retired runner-mode error = %q, want %q", err, want)
	}
	failure := readStartupFailureRecord(t, root)
	if failure.Error != want {
		t.Fatalf("recorded startup error = %q, want %q", failure.Error, want)
	}
	if failure.Root != root {
		t.Errorf("recorded root = %q, want %q", failure.Root, root)
	}
	if !slices.Equal(failure.Args, args) {
		t.Errorf("recorded args = %q, want %q", failure.Args, args)
	}
	wantOptions := map[string]string{
		"root_explicit":     "true",
		"timeout":           "15m0s",
		"timeout_unlimited": "false",
		"sync_deadline":     "1m0s",
		"retention":         "72h0m0s",
		"exclude":           "",
	}
	if !reflect.DeepEqual(failure.Options, wantOptions) {
		t.Errorf("recorded options = %#v, want %#v", failure.Options, wantOptions)
	}
	if failure.Time.IsZero() {
		t.Error("recorded startup failure has zero time")
	}
	if failure.Version != version.Current().Display() || failure.Commit != version.Commit {
		t.Errorf(
			"recorded build = %q (%q), want %q (%q)",
			failure.Version,
			failure.Commit,
			version.Current().Display(),
			version.Commit,
		)
	}
}

func TestServeStartupFailureRecordsResolvedOptions(t *testing.T) {
	root := t.TempDir()
	args := []string{
		"--root", root,
		"--ai", "codex",
		"--timeout", "0",
		"--sync-deadline", "2s",
		"--retention", "3h",
		"--exclude", "vendor,tmp/*",
		"--runner-mode", "just=all",
	}
	if err := serve(args); err == nil {
		t.Fatal("serve accepted retired --runner-mode")
	}
	failure := readStartupFailureRecord(t, root)
	want := map[string]string{
		"root_explicit":     "true",
		"ai_profile":        "jmw/codex",
		"timeout":           "0s",
		"timeout_unlimited": "true",
		"sync_deadline":     "2s",
		"retention":         "3h0m0s",
		"exclude":           "vendor,tmp/*",
	}
	if !reflect.DeepEqual(failure.Options, want) {
		t.Fatalf("recorded options = %#v, want %#v", failure.Options, want)
	}
}

func TestServeStartupFailureRecordsNegativeTimeoutOptions(t *testing.T) {
	t.Setenv("JMW_TIMEOUT", "")
	t.Setenv("JMW_SYNC_DEADLINE", "")
	t.Setenv("JMW_RETENTION", "")
	t.Setenv("JMW_TIMEOUT", "-1s")
	root := t.TempDir()

	err := serve([]string{"--root", root})
	if err == nil || err.Error() != "timeout must not be negative" {
		t.Fatalf("serve negative timeout error = %q, want %q", err, "timeout must not be negative")
	}
	failure := readStartupFailureRecord(t, root)
	if got := failure.Options["timeout"]; got != "-1s" {
		t.Fatalf("recorded timeout option = %q, want %q", got, "-1s")
	}
}

func TestServeStartupFailureOmitsInvalidAIProfileOption(t *testing.T) {
	t.Setenv("JMW_TIMEOUT", "")
	t.Setenv("JMW_SYNC_DEADLINE", "")
	t.Setenv("JMW_RETENTION", "")
	root := t.TempDir()

	if err := serve([]string{"--root", root, "--ai", "invalid"}); err == nil {
		t.Fatal("serve accepted an invalid --ai value")
	}
	failure := readStartupFailureRecord(t, root)
	want := map[string]string{
		"root_explicit":     "true",
		"timeout":           "15m0s",
		"timeout_unlimited": "false",
		"sync_deadline":     "1m0s",
		"retention":         "72h0m0s",
		"exclude":           "",
	}
	if !reflect.DeepEqual(failure.Options, want) {
		t.Fatalf("recorded options = %#v, want %#v", failure.Options, want)
	}
}

func TestServeStartupFailureRecordsOptionsForPositionalArgument(t *testing.T) {
	t.Setenv("JMW_TIMEOUT", "")
	t.Setenv("JMW_SYNC_DEADLINE", "")
	t.Setenv("JMW_RETENTION", "")
	root := t.TempDir()

	err := serve([]string{"--root", root, "unexpected"})
	if err == nil || err.Error() != "serve accepts no positional arguments" {
		t.Fatalf(
			"serve positional-argument error = %q, want %q",
			err,
			"serve accepts no positional arguments",
		)
	}
	failure := readStartupFailureRecord(t, root)
	want := map[string]string{
		"root_explicit":     "true",
		"timeout":           "15m0s",
		"timeout_unlimited": "false",
		"sync_deadline":     "1m0s",
		"retention":         "72h0m0s",
		"exclude":           "",
	}
	if !reflect.DeepEqual(failure.Options, want) {
		t.Fatalf("recorded options = %#v, want %#v", failure.Options, want)
	}
}

func TestServeWritesStartupFailureForFlagParseError(t *testing.T) {
	root := t.TempDir()
	args := []string{"--root", root, "--undefined"}
	err := serve(args)
	if err == nil {
		t.Fatal("serve accepted an undefined flag")
	}
	failure := readStartupFailureRecord(t, root)
	if failure.Error != err.Error() {
		t.Fatalf("recorded startup error = %q, want %q", failure.Error, err)
	}
	if failure.Root != root {
		t.Errorf("recorded root = %q, want %q", failure.Root, root)
	}
	if !slices.Equal(failure.Args, args) {
		t.Errorf("recorded args = %q, want %q", failure.Args, args)
	}
	if failure.Options != nil {
		t.Fatalf("flag-parse failure options = %#v, want nil", failure.Options)
	}
	path := filepath.Join(root, ".just-mcp-work", "log", "startup-error.json")
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if bytes.Contains(data, []byte(`"options"`)) {
		t.Fatalf("flag-parse failure JSON contains options: %s", data)
	}
}

func TestParseServeOptionsReportsBestKnownRootOnFailure(t *testing.T) {
	t.Setenv("JMW_ROOT", "")
	_, root, err := parseServeOptions([]string{"--undefined"})
	if err == nil || root != "." {
		t.Fatalf("default failure root = %q, %v", root, err)
	}

	environmentRoot := t.TempDir()
	t.Setenv("JMW_ROOT", environmentRoot)
	_, root, err = parseServeOptions([]string{"--undefined", "--root", "unparsed"})
	if err == nil || root != environmentRoot {
		t.Fatalf("environment failure root = %q, %v", root, err)
	}

	flagRoot := t.TempDir()
	_, root, err = parseServeOptions([]string{"--root", flagRoot, "--undefined"})
	if err == nil || root != flagRoot {
		t.Fatalf("parsed flag failure root = %q, %v", root, err)
	}
}

func TestServeHelpDoesNotWriteStartupFailure(t *testing.T) {
	root := t.TempDir()
	if err := serve([]string{"--root", root, "--help"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".just-mcp-work", "log", "startup-error.json")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("serve --help wrote a startup failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".just-mcp-work")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("serve --help created state directories: %v", err)
	}
}

func TestServeRecordWriteFailureDoesNotMaskStartupError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	root := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, ".just-mcp-work")); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(
		"--runner-mode is no longer accepted by serve; the runner policy now lives in %s; "+
			"run just-mcp-work init to write it",
		policy.Path(root),
	)
	var serveErr error
	output := captureStderr(t, func() {
		serveErr = serve([]string{"--root", root, "--runner-mode", "just=all"})
	})
	if serveErr == nil || serveErr.Error() != want {
		t.Fatalf("serve error = %q, want %q", serveErr, want)
	}
	if !strings.Contains(output, "just-mcp-work: could not write startup failure record:") {
		t.Fatalf("record-write diagnostic missing from stderr: %q", output)
	}
	if _, err := os.Stat(filepath.Join(target, "log", "startup-error.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup failure was written through symlink: %v", err)
	}
}

func readStartupFailureRecord(t *testing.T, root string) runstore.StartupFailure {
	t.Helper()
	path := filepath.Join(root, ".just-mcp-work", "log", "startup-error.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var failure runstore.StartupFailure
	if err := json.Unmarshal(data, &failure); err != nil {
		t.Fatal(err)
	}
	return failure
}

func writeStaleStartupFailure(t *testing.T, root string) {
	t.Helper()
	if err := runstore.WriteStartupFailure(
		root,
		runstore.StartupFailure{Error: "stale refusal"},
	); err != nil {
		t.Fatal(err)
	}
}

func TestServeVerifiesManagedSurfacesBeforeRunnerRegistry(t *testing.T) {
	root := t.TempDir()
	if err := initCommandWithIO(
		false,
		[]string{"--dir", root, "--agents", "codex"},
		defaultRunnerInput(),
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, resolveErr := filepath.EvalSymlinks(root)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	managedPath := filepath.Join(resolvedRoot, "AGENTS.md")
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
	wantNames := []string{"just", "agent", "cmake", "docker", "go", "make"}
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
	if len(requests) != 6 {
		t.Fatalf("permission requests = %#v", requests)
	}
	for _, index := range []int{0, 2, 3, 5} {
		request := requests[index]
		if request.Reviewed || request.Default != runner.ModeAll || len(request.Choices) != 2 ||
			request.Choices[0].Mode != runner.ModeAll ||
			request.Choices[1].Mode != runner.ModeDisabled {
			t.Errorf("unreviewed permission request = %#v", request)
		}
	}
	if request := requests[4]; request.Name != "go" || !request.Reviewed ||
		request.Default != runner.ModeSafe {
		t.Fatalf("Go permission request = %#v", request)
	}
}

func TestProductionCatalogDeclaresAgentPermission(t *testing.T) {
	catalog, err := runnerCatalog()
	if err != nil {
		t.Fatal(err)
	}
	requests := catalog.PermissionRequests()
	request := requests[1]
	if request.Name != "agent" || !request.Reviewed || request.Default != runner.ModeSafe ||
		len(request.Choices) != 2 || request.Choices[0].Mode != runner.ModeSafe ||
		request.Choices[1].Mode != runner.ModeDisabled {
		t.Fatalf("agent permission request = %#v", request)
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
				`{"name":"docker","mode":"all"},` +
				`{"name":"go","mode":"safe"},` +
				`{"name":"make","mode":"all"}]}`,
			wantError: "agent",
		},
		{
			name: "unknown runner",
			document: `{"version":1,"runners":[` +
				`{"name":"just","mode":"all"},` +
				`{"name":"agent","mode":"safe"},` +
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
				`{"name":"agent","mode":"disabled"},` +
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
		false,
		[]string{
			"--dir", dir,
			"--agents", "claude",
			"--claude-permissions", "yes",
			"--shell-permission", "allow",
		},
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
		false,
		[]string{
			"--dir", dir,
			"--agents", "claude",
			"--claude-permissions", "no",
			"--shell-permission", "ask",
		},
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
		false,
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
		false,
		[]string{
			"--dir", dir,
			"--agents", "claude",
			"--runner-mode", "just=all",
			"--runner-mode", "agent=safe",
			"--runner-mode", "cmake=all",
			"--runner-mode", "docker=all",
			"--runner-mode", "go=safe",
			"--runner-mode", "make=all",
			"--shell-permission", "ask",
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

func TestInitShellPermissionFlagSkipsPrompt(t *testing.T) {
	for _, testCase := range []struct {
		args func(string) []string
		name string
	}{
		{
			name: "Claude settings surface",
			args: initArgsWithClaudeShellQuestion,
		},
		{
			name: "Codex config surface",
			args: func(dir string) []string {
				return append(initArgsWithRunnerModes(dir), "--write-mcp-config=true")
			},
		},
		{
			name: "no permission surface",
			args: initArgsWithRunnerModes,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			var diagnostics bytes.Buffer
			args := append(testCase.args(dir), "--shell-permission", "ask")
			err := initCommandWithIO(
				false,
				args,
				erroringReader{err: errors.New("input must not be read")},
				io.Discard,
				&diagnostics,
			)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(diagnostics.String(), "How should the Claude permission lists") {
				t.Fatalf("--shell-permission path prompted:\n%s", diagnostics.String())
			}
		})
	}
}

func TestInitRunnerChoiceLineMatchesPermissionRequest(t *testing.T) {
	catalog, err := runnerCatalog()
	if err != nil {
		t.Fatal(err)
	}
	request := catalog.PermissionRequests()[1]
	choice := request.Choices[0]
	offer := singleOffer(string(choice.Mode), true)
	var output bytes.Buffer
	console := initConsole{
		input:  bufio.NewReader(strings.NewReader("\n")),
		output: &output,
	}
	mode, err := console.askRunnerMode(request, offer)
	if err != nil {
		t.Fatal(err)
	}
	if mode != choice.Mode {
		t.Fatalf("selected runner mode = %q, want %q", mode, choice.Mode)
	}
	wantLine := fmt.Sprintf(
		"  %s%s - %s: %s\n",
		choice.Mode,
		enumeratedChoiceLabel(string(choice.Mode), []string{string(request.Default)}, offer),
		choice.Label,
		choice.Description,
	)
	if !strings.Contains(output.String(), wantLine) {
		t.Fatalf("runner choice line missing %q:\n%s", wantLine, output.String())
	}
}

func TestInitNormalizesAgentsBeforeAskingShellPermission(t *testing.T) {
	dir := t.TempDir()
	args := append(
		initArgsWithRunnerModes(dir),
		"--agents", "Claude",
		"--claude-permissions", "yes",
	)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		args,
		strings.NewReader("\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatalf("mixed-case Claude init error = %v, want nil", err)
	}
	if !strings.Contains(diagnostics.String(), "Shell permission") {
		t.Fatalf("mixed-case Claude init did not ask for shell permission:\n%s", diagnostics.String())
	}
}

func TestInitEmptyAgentSelectionOffersCurrentShellPermission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	runRule := agentinit.ClaudeToolPrefix + "run_shell_command"
	startRule := agentinit.ClaudeToolPrefix + "start_shell_command"
	settings := `{"permissions":{"allow":["` + runRule + `","` + startRule + `"]}}`
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append(
		initArgsWithRunnerModes(dir),
		"--agents", "",
		"--claude-permissions", "yes",
	)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		args,
		strings.NewReader("\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"allow (current)", "Shell permission [allow, current]:"} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Fatalf("empty agent selection prompt lacks %q:\n%s", want, diagnostics.String())
		}
	}
}

func TestInitClaudePermissionNoWithoutMCPConfigDoesNotAskShellPermission(t *testing.T) {
	dir := t.TempDir()
	args := append(
		initArgsWithRunnerModes(dir),
		"--agents", "claude",
		"--claude-permissions", "no",
	)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		args,
		erroringReader{err: errors.New("input must not be read")},
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostics.String(), "How should the Claude permission lists") {
		t.Fatalf("Claude permission cleanup asked for shell permission:\n%s", diagnostics.String())
	}
}

func TestInitClaudeConfirmationShowsResolvedShellPermission(t *testing.T) {
	dir := t.TempDir()
	args := append(
		initArgsWithRunnerModes(dir),
		"--agents",
		"claude",
		"--shell-permission",
		"allow",
	)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		args,
		strings.NewReader("yes\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	shellRule := agentinit.ClaudeToolPrefix + "run_shell_command"
	allowStart := strings.Index(diagnostics.String(), "  allow: ")
	if allowStart < 0 || !strings.Contains(diagnostics.String()[allowStart:], shellRule) {
		t.Fatalf(
			"Claude confirmation does not show the shell rule under allow:\n%s",
			diagnostics.String(),
		)
	}
	if strings.Contains(diagnostics.String(), "\n  ask:   \n") ||
		strings.Contains(diagnostics.String(), "\n  ask:") {
		t.Fatalf("Claude confirmation shows an empty ask list:\n%s", diagnostics.String())
	}
}

func TestInitInteractiveShellPermissionTakesDefaultAndReprompts(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		input      string
		want       agentinit.ShellPermission
		wantOutput string
	}{
		{
			name:  "empty answer takes ask default",
			input: "\n",
			want:  agentinit.ShellPermissionAsk,
		},
		{
			name:       "garbage is rejected before allow",
			input:      "garbage\nallow\n",
			want:       agentinit.ShellPermissionAllow,
			wantOutput: `Unsupported shell permission "garbage"; choose one of allow, ask.`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			var diagnostics bytes.Buffer
			if err := initCommandWithIO(
				false,
				initArgsWithClaudeShellQuestion(dir),
				strings.NewReader(testCase.input),
				io.Discard,
				&diagnostics,
			); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, ".just-mcp-work", "managed.json"))
			if err != nil {
				t.Fatal(err)
			}
			wantField := `"shell_permission": "` + string(testCase.want) + `"`
			if !bytes.Contains(data, []byte(wantField)) {
				t.Fatalf("manifest does not contain %s:\n%s", wantField, data)
			}
			if !strings.Contains(diagnostics.String(), "ask (default)") ||
				!strings.Contains(diagnostics.String(), testCase.wantOutput) {
				t.Fatalf("shell permission prompt = %q", diagnostics.String())
			}
		})
	}
}

func TestInitInteractiveShellPermissionErrorsAtEndOfInput(t *testing.T) {
	dir := t.TempDir()
	err := initCommandWithIO(
		false,
		initArgsWithClaudeShellQuestion(dir),
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "--shell-permission") {
		t.Fatalf("init error = %v, want --shell-permission end-of-input guidance", err)
	}
	if _, statErr := os.Stat(policy.Path(dir)); !os.IsNotExist(statErr) {
		t.Fatalf("unanswered shell permission wrote policy: %v", statErr)
	}
}

func TestInitInteractiveShellPermissionOffersCurrentChoice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	runRule := agentinit.ClaudeToolPrefix + "run_shell_command"
	startRule := agentinit.ClaudeToolPrefix + "start_shell_command"
	settings := `{"permissions":{"allow":["` + runRule + `","` + startRule + `"],"ask":[]}}`
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		initArgsWithClaudeShellQuestion(dir),
		strings.NewReader("\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"allow (current)",
		"ask (default)",
		"Shell permission [allow, current]:",
	} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Fatalf("current shell permission prompt lacks %q:\n%s", want, diagnostics.String())
		}
	}
}

func TestInitCodexOnlyShellPermissionRoundTripKeepsRecordedAllow(t *testing.T) {
	dir := t.TempDir()
	firstArgs := append(
		initArgsWithRunnerModes(dir),
		"--write-mcp-config=true",
		"--shell-permission", "allow",
	)
	if err := initCommandWithIO(
		false,
		firstArgs,
		erroringReader{err: errors.New("input must not be read")},
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	secondArgs := append(initArgsWithRunnerModes(dir), "--write-mcp-config=true")
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		secondArgs,
		strings.NewReader("\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"How should the Claude permission lists and Codex approval modes handle",
		"use the Claude allow list and Codex approve mode",
		"use the Claude ask list and Codex prompt mode",
		"allow (current)",
	} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Fatalf("Codex-only shell permission prompt lacks %q:\n%s", want, diagnostics.String())
		}
	}
	configPath := filepath.Join(dir, ".codex", "config.toml")
	// #nosec G304 -- path is created in this test's temporary directory.
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(
		config,
		[]byte(`tools.run_shell_command.approval_mode = "approve"`),
	) {
		t.Fatalf("Codex shell approval was not kept at allow:\n%s", config)
	}
}

func TestInitCodexOnlyIgnoresOutOfScopeMalformedClaudeSettings(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append(initArgsWithRunnerModes(dir), "--write-mcp-config=true")
	if err := initCommandWithIO(
		false,
		args,
		strings.NewReader("\n"),
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatalf("Codex-only init read out-of-scope Claude settings: %v", err)
	}
}

func TestInitInteractiveShellPermissionDoesNotOfferSplitChoiceAsCurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	runRule := agentinit.ClaudeToolPrefix + "run_shell_command"
	startRule := agentinit.ClaudeToolPrefix + "start_shell_command"
	settings := `{"permissions":{"allow":["` + runRule + `"],"ask":["` + startRule + `"]}}`
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		initArgsWithClaudeShellQuestion(dir),
		strings.NewReader("\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostics.String(), "current") ||
		!strings.Contains(diagnostics.String(), "Shell permission [ask, default]:") {
		t.Fatalf("split shell permissions were offered as current:\n%s", diagnostics.String())
	}
}

func TestInitCodexOnlyWithoutMCPConfigDoesNotAskShellPermission(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		initArgsWithRunnerModes(dir),
		strings.NewReader(""),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatalf("Codex-only config-free init error = %v, want nil", err)
	}
	if strings.Contains(diagnostics.String(), "How should the Claude permission lists") {
		t.Fatalf("Codex-only init asked for a shell permission:\n%s", diagnostics.String())
	}
	manifest, err := os.ReadFile(filepath.Join(dir, ".just-mcp-work", "managed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(manifest, []byte("shell_permission")) {
		t.Fatalf("Codex-only manifest records a shell permission:\n%s", manifest)
	}
}

func TestInitInstructionsTargetFlagWorksWithClosedConsole(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := initArgsWithRunnerModesWithoutAI(project)
	args[3] = string(agentinit.InstructionsTargetProject)
	args = append(args, "--ai", "codex")
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		args,
		strings.NewReader(""),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, "AGENTS.md")); err != nil {
		t.Fatalf("project target did not write project instructions: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("project target wrote workspace instructions: %v", err)
	}
	if !strings.Contains(
		diagnostics.String(),
		"Agent instructions target project resolves to directory "+project,
	) {
		t.Fatalf("target diagnostic missing:\n%s", diagnostics.String())
	}
}

func TestInitMachineTargetRejectsUnsupportedAgentsBeforeLaterQuestions(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		args        func(string) []string
		input       string
		wantChoices bool
	}{
		{
			name: "flag",
			args: func(dir string) []string {
				return []string{"--dir", dir, "--instructions-target", "machine"}
			},
		},
		{
			name: "console",
			args: func(dir string) []string {
				return []string{"--dir", dir}
			},
			input:       "machine\n",
			wantChoices: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			var diagnostics bytes.Buffer
			err := initCommandWithIO(
				false,
				testCase.args(root),
				strings.NewReader(testCase.input),
				io.Discard,
				&diagnostics,
			)
			if err == nil {
				t.Fatal("machine target with default agents error = nil")
			}
			for _, want := range []string{
				"--agents \"claude,codex,cursor\"",
				"cursor",
				"--agents claude,codex,windsurf",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("early machine-agent error does not contain %q: %v", want, err)
				}
			}
			if strings.Contains(err.Error(), "AI families") ||
				strings.Contains(diagnostics.String(), "Which AI families") {
				t.Fatalf("machine-agent validation ran after AI question: %v\n%s", err, diagnostics.String())
			}
			if testCase.wantChoices &&
				!strings.Contains(
					diagnostics.String(),
					"machine-wide instruction files for claude, codex, and windsurf",
				) {
				t.Fatalf("machine choice omitted supported agents:\n%s", diagnostics.String())
			}
			if _, statErr := os.Stat(filepath.Join(root, ".just-mcp-work")); !os.IsNotExist(statErr) {
				t.Fatalf("early machine-agent refusal wrote workspace state: %v", statErr)
			}
		})
	}
}

func TestInitMachineTargetDiagnosticAndPlanShareCanonicalHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	actualHome := t.TempDir()
	linkRoot := t.TempDir()
	homeLink := filepath.Join(linkRoot, "home")
	if err := os.Symlink(actualHome, homeLink); err != nil {
		t.Skipf("create home symlink: %v", err)
	}
	t.Setenv("HOME", homeLink)
	t.Setenv("USERPROFILE", homeLink)
	root := t.TempDir()
	args := initArgsWithRunnerModesWithoutAI(root)
	args[3] = string(agentinit.InstructionsTargetMachine)
	args[5] = "claude"
	args = append(args, "--ai", "codex", "--shell-permission", "ask", "--dry-run")
	var result bytes.Buffer
	var diagnostics bytes.Buffer

	if err := initCommandWithIO(
		false,
		args,
		strings.NewReader(""),
		&result,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	canonicalHome, err := filepath.EvalSymlinks(actualHome)
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath := filepath.Join(canonicalHome, ".claude", "CLAUDE.md")
	if !strings.Contains(
		diagnostics.String(),
		"machine resolves to directory "+canonicalHome,
	) {
		t.Fatalf("machine target diagnostic did not use canonical home:\n%s", diagnostics.String())
	}
	if !strings.Contains(result.String(), "+++ "+resolvedPath) {
		t.Fatalf("machine dry-run plan did not use resolved diagnostic directory:\n%s", result.String())
	}
	if strings.Contains(diagnostics.String(), "directory "+homeLink) ||
		strings.Contains(result.String(), homeLink) {
		t.Fatalf("machine target exposed divergent lexical home:\n%s\n%s", diagnostics.String(), result.String())
	}
}

func TestInitInstructionsTargetQuestionRequiresAnAnswerAtEOF(t *testing.T) {
	dir := t.TempDir()
	var diagnostics bytes.Buffer
	err := initCommandWithIO(
		false,
		[]string{"--dir", dir, "--ai", "codex"},
		strings.NewReader(""),
		io.Discard,
		&diagnostics,
	)
	if err == nil || !strings.Contains(
		err.Error(),
		"--instructions-target project|workspace|machine",
	) {
		t.Fatalf("unanswered instructions-target error = %v", err)
	}
	if !strings.Contains(diagnostics.String(), "Where should the managed agent-instruction block") ||
		strings.Contains(diagnostics.String(), "Which AI families") {
		t.Fatalf("instructions target was not asked before AI families:\n%s", diagnostics.String())
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".just-mcp-work")); !os.IsNotExist(statErr) {
		t.Fatalf("unanswered instructions target wrote workspace state: %v", statErr)
	}
}

func TestInitRejectsUnsupportedInstructionsTargetBeforeQuestion(t *testing.T) {
	dir := t.TempDir()
	var diagnostics bytes.Buffer
	err := initCommandWithIO(
		false,
		[]string{"--dir", dir, "--instructions-target", "elsewhere"},
		erroringReader{err: errors.New("console was read")},
		io.Discard,
		&diagnostics,
	)
	if err == nil || !strings.Contains(err.Error(), "unsupported instructions target") {
		t.Fatalf("unsupported instructions target error = %v", err)
	}
	if strings.Contains(diagnostics.String(), "Where should") {
		t.Fatalf("unsupported target asked a question:\n%s", diagnostics.String())
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".just-mcp-work")); !os.IsNotExist(statErr) {
		t.Fatalf("unsupported instructions target wrote workspace state: %v", statErr)
	}
}

func TestInitOffersRecordedInstructionsTargetAsCurrent(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := initArgsWithRunnerModesWithoutAI(project)
	args[3] = string(agentinit.InstructionsTargetProject)
	args = append(args, "--ai", "codex")
	if err := initCommandWithIO(false, args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	args = append(args[:2], args[4:]...)
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		args,
		strings.NewReader("\n"),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostics.String(), "project (current)") ||
		!strings.Contains(diagnostics.String(), "Instructions target [project, current]:") {
		t.Fatalf("recorded instructions target was not offered as current:\n%s", diagnostics.String())
	}
}

func TestInitWorkspaceTargetNamesDirectoryOutsideDirInDryRun(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append(initArgsWithRunnerModesWithoutAI(project), "--ai", "codex", "--dry-run")
	var diagnostics bytes.Buffer
	if err := initCommandWithIO(
		false,
		args,
		strings.NewReader(""),
		io.Discard,
		&diagnostics,
	); err != nil {
		t.Fatal(err)
	}
	text := diagnostics.String()
	if !strings.Contains(text, "workspace resolves to directory "+root) ||
		!strings.Contains(text, "not in the directory --dir named ("+project+")") {
		t.Fatalf("workspace target diagnostics:\n%s", text)
	}
}

func TestInitDryRunPrintsStaleMachineNoteWithoutWritingHome(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	args := initArgsWithRunnerModesWithoutAI(root)
	args[3] = string(agentinit.InstructionsTargetMachine)
	args = append(args, "--ai", "codex")
	if err := initCommandWithIO(false, args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	path, err := filepath.EvalSymlinks(filepath.Join(home, ".codex", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	args[3] = string(agentinit.InstructionsTargetWorkspace)
	args = append(args, "--dry-run")
	var diagnostics bytes.Buffer
	if initErr := initCommandWithIO(
		false,
		args,
		strings.NewReader(""),
		io.Discard,
		&diagnostics,
	); initErr != nil {
		t.Fatal(initErr)
	}
	if !strings.Contains(diagnostics.String(), path) ||
		!strings.Contains(diagnostics.String(), "stale") {
		t.Fatalf("dry-run stale note missing:\n%s", diagnostics.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("dry-run rewrote stale machine file %s", path)
	}
}

func TestInitRejectsUnsupportedShellPermission(t *testing.T) {
	err := initCommand([]string{"--dir", t.TempDir(), "--shell-permission", "maybe"})
	if err == nil || !strings.Contains(err.Error(), "unsupported shell permission") {
		t.Fatalf("initCommand error = %v", err)
	}
}
