// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

func TestDefineShellBlockAndRunByID(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	server := newShellTestServer(t, root)
	command := shellBlockMarkerCommand("recorded-block-marker")
	block := defineShellBlockForTest(t, server, command, "nested/.")
	if block.WorkingDirectory != "nested" {
		t.Fatalf("canonical block working directory = %q, want nested", block.WorkingDirectory)
	}
	assertNoShellBlockRuns(t, server)

	result, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{BlockID: block.BlockID},
	)
	if err != nil || result != nil || !receipt.OK || receipt.Status != runstore.StatusOK {
		t.Fatalf("runShellCommand by block_id = %#v, %#v, %v", result, receipt, err)
	}
	if _, err := os.Stat(filepath.Join(root, "nested", "recorded-block-marker")); err != nil {
		t.Fatalf("recorded command did not run in recorded directory: %v", err)
	}
	assertShellBlockRunMeta(t, server, receipt.RunID, command, "nested", root)
}

func TestStartShellCommandByBlockID(t *testing.T) {
	root := t.TempDir()
	server := newShellTestServer(t, root)
	command := shellOutputCommand()
	block := defineShellBlockForTest(t, server, command, "")

	result, receipt, err := server.startShellCommand(
		context.Background(),
		nil,
		startShellCommandInput{BlockID: block.BlockID},
	)
	if err != nil || result != nil || receipt.RunID == "" || receipt.Error != nil {
		t.Fatalf("startShellCommand by block_id = %#v, %#v, %v", result, receipt, err)
	}
	_, status, err := server.waitRun(
		context.Background(),
		nil,
		waitRunInput{RunID: receipt.RunID},
	)
	if err != nil || !status.OK || status.Status != runstore.StatusOK {
		t.Fatalf("waitRun for shell block = %#v, %v", status, err)
	}
	assertShellBlockRunMeta(t, server, receipt.RunID, command, ".", root)
	_, stdout, err := server.getRunLogs(
		context.Background(),
		nil,
		getRunLogsInput{RunID: receipt.RunID, Stream: "stdout"},
	)
	if err != nil || !strings.Contains(stdout.Data, "shell-output") {
		t.Fatalf("started shell block stdout = %#v, %v", stdout, err)
	}
}

func TestArgvShellBlockPassesArgumentsVerbatim(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	server := newShellTestServer(t, root)
	baseArgv := append(shellArgvHelperBase("echo"), "fixed")
	block := defineArgvShellBlockForTest(t, server, baseArgv, "nested/.")
	arguments := []string{
		"with spaces",
		`single' and "double" quotes`,
		"$dollar",
		"`backticks`",
		"line one\nline two",
		"*.go",
		"-leading",
		"",
	}

	fullArgv := append(append([]string(nil), baseArgv...), arguments...)
	result, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{BlockID: block.BlockID, Arguments: fullArgv},
	)
	if err != nil || result != nil || !receipt.OK || receipt.Status != runstore.StatusOK {
		t.Fatalf("argv run = %#v, %#v, %v", result, receipt, err)
	}
	_, stdout, err := server.getRunLogs(
		context.Background(),
		nil,
		getRunLogsInput{RunID: receipt.RunID, Stream: "stdout"},
	)
	wantPayload := append([]string{"fixed"}, arguments...)
	wantStdout := strings.Join(wantPayload, "\x00") + "\x00"
	if err != nil || stdout.Data != wantStdout {
		t.Fatalf("argv stdout = %q, %v; want exact bytes %q", stdout.Data, err, wantStdout)
	}
	assertArgvShellRunMeta(
		t,
		server,
		receipt.RunID,
		fullArgv,
		"nested",
		root,
	)
	_, listed, err := server.listRuns(context.Background(), nil, listRunsInput{TaskID: "shell:command"})
	if err != nil || len(listed.Runs) != 1 || !slices.Equal(listed.Runs[0].Args, fullArgv) {
		t.Fatalf("listed argv = %#v, %v; want %#v", listed.Runs, err, fullArgv)
	}
}

func TestStartArgvShellBlockPassesArguments(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	baseArgv := shellArgvHelperBase("stdout")
	block := defineArgvShellBlockForTest(t, server, baseArgv, "")
	result, receipt, err := server.startShellCommand(
		context.Background(),
		nil,
		startShellCommandInput{
			BlockID:   block.BlockID,
			Arguments: append(append([]string(nil), baseArgv...), "async-output"),
		},
	)
	if err != nil || result != nil || receipt.RunID == "" || receipt.Error != nil {
		t.Fatalf("start argv block = %#v, %#v, %v", result, receipt, err)
	}
	_, status, err := server.waitRun(
		context.Background(),
		nil,
		waitRunInput{RunID: receipt.RunID},
	)
	if err != nil || !status.OK || status.StdoutTail != "async-output" {
		t.Fatalf("waitRun for argv block = %#v, %v", status, err)
	}
	assertArgvShellRunMeta(
		t,
		server,
		receipt.RunID,
		append(append([]string(nil), baseArgv...), "async-output"),
		".",
		server.workspace.Root(),
	)
}

func TestDefineArgvShellBlockCopiesInput(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	argv := []string{"executable", "", "fixed"}
	block := defineArgvShellBlockForTest(t, server, argv, "")
	argv[0] = "mutated"
	argv[1] = "mutated"

	server.shellBlocksMu.Lock()
	stored := server.shellBlocks[block.BlockID]
	server.shellBlocksMu.Unlock()
	if !slices.Equal(stored.argv, []string{"executable", "", "fixed"}) {
		t.Fatalf("stored argv = %#v, want defensive copy", stored.argv)
	}
}

func TestShellBlockCanRunTwiceByID(t *testing.T) {
	root := t.TempDir()
	server := newShellTestServer(t, root)
	command := shellBlockAppendCommand("shell-block-runs")
	block := defineShellBlockForTest(t, server, command, "")
	runIDs := make([]string, 0, 2)
	for range 2 {
		_, receipt, err := server.runShellCommand(
			context.Background(),
			nil,
			runShellCommandInput{BlockID: block.BlockID},
		)
		if err != nil || !receipt.OK {
			t.Fatalf("reused shell block = %#v, %v", receipt, err)
		}
		runIDs = append(runIDs, receipt.RunID)
	}
	if runIDs[0] == runIDs[1] {
		t.Fatalf("reused shell block returned the same run_id %q", runIDs[0])
	}
	data, err := os.ReadFile(filepath.Join(root, "shell-block-runs"))
	if err != nil || !slices.Equal(strings.Fields(string(data)), []string{"run", "run"}) {
		t.Fatalf("reused shell block output = %q, %v", data, err)
	}
	for _, runID := range runIDs {
		assertShellBlockRunMeta(t, server, runID, command, ".", root)
	}
}

//nolint:gocyclo // The integration flow keeps scoped and unrestricted launches together.
func TestShellBlockWriteScopeIsPerLaunch(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("write scope enforcement is available only on darwin")
	}
	fixtureRoot := t.TempDir()
	root := filepath.Join(fixtureRoot, "worktree")
	temporaryRoot := filepath.Join(fixtureRoot, "tmp")
	for _, path := range []string{filepath.Join(root, ".git"), temporaryRoot} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", temporaryRoot)
	if err := os.Mkdir(filepath.Join(root, "allowed"), 0o750); err != nil {
		t.Fatal(err)
	}
	server := newShellTestServer(t, root)
	command := shellBlockAppendCommand(filepath.Join(root, "allowed", "runs"))
	block := defineShellBlockForTest(t, server, command, "")

	result, scoped, err := server.startShellCommand(
		context.Background(),
		nil,
		startShellCommandInput{BlockID: block.BlockID, WriteScope: []string{"allowed"}},
	)
	if err != nil || result != nil || scoped.RunID == "" || len(scoped.WriteScope) == 0 {
		t.Fatalf("scoped block launch = %#v, %#v, %v", result, scoped, err)
	}
	_, waited, err := server.waitRun(context.Background(), nil, waitRunInput{RunID: scoped.RunID})
	if err != nil || !waited.OK {
		t.Fatalf("wait scoped block = %#v, %v", waited, err)
	}

	_, unrestricted, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{BlockID: block.BlockID},
	)
	if err != nil || !unrestricted.OK || unrestricted.WriteScope != nil {
		t.Fatalf("unrestricted block relaunch = %#v, %v", unrestricted, err)
	}
	meta, err := server.store.Get(unrestricted.RunID)
	if err != nil || meta.WriteScope != nil {
		t.Fatalf("unrestricted block metadata = %#v, %v", meta, err)
	}
}

func TestShellBlockSelectorsRejectBeforeRunStart(t *testing.T) {
	root := t.TempDir()
	server := newShellTestServer(t, root)
	blockMarker := filepath.Join(root, "block-selector-marker")
	directMarker := filepath.Join(root, "direct-selector-marker")
	block := defineShellBlockForTest(
		t,
		server,
		shellBlockMarkerCommand(blockMarker),
		"",
	)
	staleServer := newShellTestServer(t, t.TempDir())
	stale := defineShellBlockForTest(t, staleServer, shellOutputCommand(), "")

	testCases := []struct {
		name    string
		input   runShellCommandInput
		message string
	}{
		{
			name:    "missing",
			input:   runShellCommandInput{},
			message: "command or block_id is required",
		},
		{
			name:    "whitespace-only command",
			input:   runShellCommandInput{Command: " \n\t"},
			message: "command must not be empty",
		},
		{
			name: "ambiguous",
			input: runShellCommandInput{
				Command: shellBlockMarkerCommand(directMarker),
				BlockID: block.BlockID,
			},
			message: "command and block_id must not be combined; " +
				"use one shell command selector per request",
		},
		{
			name: "working directory with block",
			input: runShellCommandInput{
				BlockID:          block.BlockID,
				WorkingDirectory: ".",
			},
			message: "working_directory must not be combined with block_id; " +
				"the block already carries its working directory",
		},
		{
			name:  "stale block ID",
			input: runShellCommandInput{BlockID: stale.BlockID},
			message: fmt.Sprintf(
				"unknown block_id %q; shell blocks live only for one server session, "+
					"so define the block again",
				stale.BlockID,
			),
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result, receipt, err := server.runShellCommand(
				context.Background(),
				nil,
				testCase.input,
			)
			if err != nil || result == nil || !result.IsError || receipt.Error == nil ||
				receipt.Error.Message != testCase.message {
				t.Fatalf("selector rejection = %#v, %#v, %v", result, receipt, err)
			}
			if receipt.RunID != "" {
				t.Fatalf("selector rejection created run %q", receipt.RunID)
			}
			if receipt.runDetails == nil ||
				receipt.WorktreeRoot != server.store.WorktreeRoot() {
				t.Fatalf("selector rejection details = %#v", receipt.runDetails)
			}
			assertNoShellBlockRuns(t, server)
		})
	}
	for _, marker := range []string{blockMarker, directMarker} {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("selector rejection started a process for marker %q: %v", marker, err)
		}
	}
}

// TestArgvBlockRunRejectsArgumentsThatDoNotRepeatTheBlockArgv pins the property
// the argv block would otherwise lose: one client-visible call carries the whole
// command line, so no execution is approved from a block id and a tail of values
// whose executable never appears in the request.
func TestArgvBlockRunRejectsArgumentsThatDoNotRepeatTheBlockArgv(t *testing.T) {
	root := t.TempDir()
	server := newShellTestServer(t, root)
	baseArgv := append(shellArgvHelperBase("echo"), "fixed")
	block := defineArgvShellBlockForTest(t, server, baseArgv, "")
	for _, testCase := range []struct {
		name      string
		message   string
		arguments []string
	}{
		{
			name:      "added values alone",
			arguments: []string{"added"},
			message: fmt.Sprintf(
				"arguments must be the full argv and start with the %d elements of block_id %q, but carry 1; "+
					"repeat the block's own argv before the added values",
				len(baseArgv),
				block.BlockID,
			),
		},
		{
			name:      "different executable",
			arguments: append([]string{"rm"}, baseArgv[1:]...),
			message: fmt.Sprintf(
				"arguments[0] is %q, but block_id %q fixes %q there; "+
					"arguments must be the full argv and start with the block's own argv",
				"rm",
				block.BlockID,
				baseArgv[0],
			),
		},
		{
			name: "edited fixed tail",
			arguments: append(
				append([]string(nil), baseArgv[:len(baseArgv)-1]...),
				"replaced",
				"added",
			),
			message: fmt.Sprintf(
				"arguments[%d] is %q, but block_id %q fixes %q there; "+
					"arguments must be the full argv and start with the block's own argv",
				len(baseArgv)-1,
				"replaced",
				block.BlockID,
				"fixed",
			),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, receipt, err := server.runShellCommand(
				context.Background(),
				nil,
				runShellCommandInput{BlockID: block.BlockID, Arguments: testCase.arguments},
			)
			if err != nil || result == nil || !result.IsError ||
				receipt.Error == nil || receipt.Error.Message != testCase.message {
				t.Fatalf("argv prefix rejection = %#v, %#v, %v", result, receipt, err)
			}
			if receipt.RunID != "" || receipt.runDetails == nil {
				t.Fatalf("argv prefix rejection details = %#v", receipt)
			}
		})
	}
	assertNoShellBlockRuns(t, server)
}

// TestShellCommandAcceptsAnEmptyArgumentsList covers the client that serialises
// an absent list as []: an empty arguments list carries no argument, so it must
// not be refused as one combined with command.
func TestShellCommandAcceptsAnEmptyArgumentsList(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	result, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellOutputCommand(), Arguments: []string{}},
	)
	if err != nil || result != nil || receipt.Error != nil || !receipt.OK {
		t.Fatalf("empty arguments with command = %#v, %#v, %v", result, receipt, err)
	}
}

// TestArgvBlockRunWithoutArgumentsRunsTheDefinedArgv keeps the other half of the
// contract: a run that adds nothing needs no repetition, because the definition
// call already showed that argv in full.
func TestArgvBlockRunWithoutArgumentsRunsTheDefinedArgv(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	baseArgv := append(shellArgvHelperBase("echo"), "only-fixed")
	block := defineArgvShellBlockForTest(t, server, baseArgv, "")
	for _, arguments := range [][]string{nil, {}} {
		result, receipt, err := server.runShellCommand(
			context.Background(),
			nil,
			runShellCommandInput{BlockID: block.BlockID, Arguments: arguments},
		)
		if err != nil || result != nil || !receipt.OK || receipt.Status != runstore.StatusOK {
			t.Fatalf("argv run without arguments = %#v, %#v, %v", result, receipt, err)
		}
		_, stdout, logErr := server.getRunLogs(
			context.Background(),
			nil,
			getRunLogsInput{RunID: receipt.RunID, Stream: "stdout"},
		)
		if logErr != nil || stdout.Data != "only-fixed\x00" {
			t.Fatalf("argv run without arguments stdout = %q, %v", stdout.Data, logErr)
		}
	}
}

func TestShellArgumentsRejectWithoutArgvBlockBeforeRunStart(t *testing.T) {
	for _, async := range []bool{false, true} {
		name := "run"
		if async {
			name = "start"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			server := newShellTestServer(t, root)
			textBlock := defineShellBlockForTest(t, server, shellOutputCommand(), "")
			marker := filepath.Join(root, "arguments-marker")
			for _, testCase := range []struct {
				name      string
				command   string
				blockID   string
				message   string
				arguments []string
			}{
				{
					name:      "direct command",
					command:   shellBlockMarkerCommand(marker),
					arguments: []string{"value"},
					message: "arguments must not be combined with command; " +
						"arguments require block_id naming an argv block",
				},
				{
					name:      "no selector",
					arguments: []string{"value"},
					message:   "arguments require block_id naming an argv block",
				},
				{
					name:      "shell-text block",
					blockID:   textBlock.BlockID,
					arguments: []string{"value"},
					message: fmt.Sprintf(
						"arguments require an argv block; block_id %q names a shell-text block",
						textBlock.BlockID,
					),
				},
			} {
				t.Run(testCase.name, func(t *testing.T) {
					var result *mcp.CallToolResult
					var receipt runTaskOutput
					var err error
					if async {
						result, receipt, err = server.startShellCommand(
							context.Background(),
							nil,
							startShellCommandInput{
								Command:   testCase.command,
								BlockID:   testCase.blockID,
								Arguments: testCase.arguments,
							},
						)
					} else {
						var syncReceipt runShellCommandOutput
						result, syncReceipt, err = server.runShellCommand(
							context.Background(),
							nil,
							runShellCommandInput{
								Command:   testCase.command,
								BlockID:   testCase.blockID,
								Arguments: testCase.arguments,
							},
						)
						receipt = syncReceipt.runTaskOutput
					}
					if err != nil || result == nil || !result.IsError ||
						receipt.Error == nil || receipt.Error.Message != testCase.message {
						t.Fatalf("arguments rejection = %#v, %#v, %v", result, receipt, err)
					}
					if receipt.RunID != "" || receipt.runDetails == nil ||
						receipt.WorktreeRoot != server.store.WorktreeRoot() {
						t.Fatalf("arguments rejection details = %#v", receipt)
					}
				})
			}
			assertNoShellBlockRuns(t, server)
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("arguments rejection started a process: %v", err)
			}
		})
	}
}

// TestDefineShellBlockAcceptsCommandWithAnEmptyArgv covers the client that
// serialises an absent list as []: an empty argv names no executable, so it
// must not be read as a second selector combined with command.
func TestDefineShellBlockAcceptsCommandWithAnEmptyArgv(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	result, output, err := server.defineShellBlock(
		context.Background(),
		nil,
		defineShellBlockInput{Command: shellOutputCommand(), Argv: []string{}},
	)
	if err != nil || result != nil || output.Error != nil || output.BlockID == "" {
		t.Fatalf("command with an empty argv = %#v, %#v, %v", result, output, err)
	}
	command, argv, workingDirectory, resolveErr := server.resolveShellCommand(
		"",
		output.BlockID,
		"",
		nil,
	)
	if resolveErr != nil || command != shellOutputCommand() || argv != nil ||
		workingDirectory != "." {
		t.Fatalf(
			"empty-argv block resolved to %q, %#v, %q, %v",
			command,
			argv,
			workingDirectory,
			resolveErr,
		)
	}
}

func TestDefineShellBlockRejectsInvalidInput(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	for _, testCase := range []struct {
		name             string
		command          string
		workingDirectory string
		message          string
		argv             []string
	}{
		{
			name:    "missing selector",
			message: "command or argv is required",
		},
		{
			name:    "empty command",
			command: " \n\t",
			message: "command must not be empty",
		},
		{
			name:    "combined selectors",
			command: shellOutputCommand(),
			argv:    []string{os.Args[0]},
			message: "command and argv must not be combined; " +
				"use one shell block selector per request",
		},
		{
			name:    "empty argv selects nothing",
			argv:    []string{},
			message: "command or argv is required",
		},
		{
			name:    "blank argv executable",
			argv:    []string{" \n\t", "kept"},
			message: "argv[0] must not be empty",
		},
		{
			name:             "outside workspace",
			command:          shellOutputCommand(),
			workingDirectory: "../outside",
			message:          "invalid working directory",
		},
		{
			name:             "argv outside workspace",
			argv:             []string{os.Args[0]},
			workingDirectory: "../outside",
			message:          "invalid working directory",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, output, err := server.defineShellBlock(
				context.Background(),
				nil,
				defineShellBlockInput{
					Command:          testCase.command,
					Argv:             testCase.argv,
					WorkingDirectory: testCase.workingDirectory,
				},
			)
			if err != nil || result == nil || !result.IsError || output.Error == nil ||
				!strings.Contains(output.Error.Message, testCase.message) {
				t.Fatalf("defineShellBlock rejection = %#v, %#v, %v", result, output, err)
			}
		})
	}
	assertNoShellBlockRuns(t, server)
}

func TestShellBlockRegistryConcurrentDefinitionsAndLookups(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	known := defineShellBlockForTest(t, server, shellOutputCommand(), "")
	const (
		writers          = 64
		readers          = 8
		readerIterations = 2000
	)
	start := make(chan struct{})
	definitions := make(chan concurrentShellBlockDefinition, writers)
	lookups := make(chan error, readers)
	var group sync.WaitGroup
	for index := range writers {
		group.Go(func() {
			definitions <- defineConcurrentShellBlock(server, index, start)
		})
	}
	for range readers {
		group.Go(func() {
			lookups <- lookupShellBlockRepeatedly(
				server,
				known,
				readerIterations,
				start,
			)
		})
	}
	close(start)
	group.Wait()
	close(definitions)
	close(lookups)
	for err := range lookups {
		if err != nil {
			t.Fatal(err)
		}
	}
	unique := make(map[string]struct{}, writers)
	for definition := range definitions {
		if definition.err != nil {
			t.Fatal(definition.err)
		}
		if _, exists := unique[definition.id]; exists {
			t.Fatalf("duplicate concurrent block_id %q", definition.id)
		}
		unique[definition.id] = struct{}{}
	}
	if len(unique) != writers {
		t.Fatalf("concurrent definitions = %d, want %d", len(unique), writers)
	}
	server.shellBlocksMu.Lock()
	count := len(server.shellBlocks)
	server.shellBlocksMu.Unlock()
	if count != writers+1 {
		t.Fatalf("registered shell blocks = %d, want %d", count, writers+1)
	}
}

type concurrentShellBlockDefinition struct {
	err error
	id  string
}

func defineConcurrentShellBlock(
	server *Server,
	index int,
	start <-chan struct{},
) concurrentShellBlockDefinition {
	<-start
	_, output, err := server.defineShellBlock(
		context.Background(),
		nil,
		defineShellBlockInput{Command: fmt.Sprintf("echo concurrent-%d", index)},
	)
	if err != nil {
		return concurrentShellBlockDefinition{err: err}
	}
	if output.Error != nil {
		return concurrentShellBlockDefinition{
			err: fmt.Errorf("define shell block: %s", output.Error.Message),
		}
	}
	return concurrentShellBlockDefinition{id: output.BlockID}
}

func lookupShellBlockRepeatedly(
	server *Server,
	block defineShellBlockOutput,
	iterations int,
	start <-chan struct{},
) error {
	<-start
	for range iterations {
		command, argv, workingDirectory, err := server.resolveShellCommand("", block.BlockID, "", nil)
		if err != nil {
			return fmt.Errorf("lookup shell block: %w", err)
		}
		if command != shellOutputCommand() || argv != nil || workingDirectory != "." {
			return fmt.Errorf("lookup = %q, %#v, %q", command, argv, workingDirectory)
		}
		runtime.Gosched()
	}
	return nil
}

func TestDefineShellBlockDescriptionExplainsSessionScope(t *testing.T) {
	description := defineShellBlockDescription()
	for _, expected := range []string{
		"long ad-hoc",
		"block_id",
		"run_shell_command",
		"start_shell_command",
		"command and argv",
		"arguments",
		"full argv",
		"bypasses the shell",
		"server session",
		"earlier session is an error, not a silent miss",
	} {
		if !strings.Contains(description, expected) {
			t.Errorf("define_shell_block description does not mention %q", expected)
		}
	}
}

func defineArgvShellBlockForTest(
	t *testing.T,
	server *Server,
	argv []string,
	workingDirectory string,
) defineShellBlockOutput {
	t.Helper()
	result, output, err := server.defineShellBlock(
		context.Background(),
		nil,
		defineShellBlockInput{Argv: argv, WorkingDirectory: workingDirectory},
	)
	if err != nil || result != nil || output.Error != nil || output.BlockID == "" {
		t.Fatalf("define argv shell block = %#v, %#v, %v", result, output, err)
	}
	return output
}

func defineShellBlockForTest(
	t *testing.T,
	server *Server,
	command string,
	workingDirectory string,
) defineShellBlockOutput {
	t.Helper()
	result, output, err := server.defineShellBlock(
		context.Background(),
		nil,
		defineShellBlockInput{Command: command, WorkingDirectory: workingDirectory},
	)
	if err != nil || result != nil || output.Error != nil || output.BlockID == "" {
		t.Fatalf("defineShellBlock = %#v, %#v, %v", result, output, err)
	}
	id, err := uuid.Parse(output.BlockID)
	if err != nil || id.Version() != 7 {
		t.Fatalf("shell block ID = %q, %v; want UUIDv7", output.BlockID, err)
	}
	return output
}

func assertShellBlockRunMeta(
	t *testing.T,
	server *Server,
	runID string,
	command string,
	projectPath string,
	root string,
) {
	t.Helper()
	_, stored, err := server.getRun(context.Background(), nil, getRunInput{RunID: runID})
	if err != nil ||
		stored.Run.TaskID != "shell:command" ||
		stored.Run.Runner != "shell" ||
		!slices.Equal(stored.Run.Args, []string{command}) ||
		stored.Run.ProjectPath != projectPath ||
		stored.Run.CWD != filepath.Join(root, filepath.FromSlash(projectPath)) {
		t.Fatalf("shell block run metadata = %#v, %v", stored.Run, err)
	}
}

func assertArgvShellRunMeta(
	t *testing.T,
	server *Server,
	runID string,
	argv []string,
	projectPath string,
	root string,
) {
	t.Helper()
	_, stored, err := server.getRun(context.Background(), nil, getRunInput{RunID: runID})
	if err != nil ||
		stored.Run.TaskID != "shell:command" ||
		stored.Run.Runner != "shell" ||
		!slices.Equal(stored.Run.Args, argv) ||
		stored.Run.ProjectPath != projectPath ||
		stored.Run.CWD != filepath.Join(root, filepath.FromSlash(projectPath)) {
		t.Fatalf("argv shell block run metadata = %#v, %v", stored.Run, err)
	}
}

const shellArgvHelperMarker = "jmw-shell-argv-helper"

func shellArgvHelperBase(mode string) []string {
	return []string{
		os.Args[0],
		"-test.run=^TestArgvShellBlockHelperProcess$",
		"--",
		shellArgvHelperMarker,
		mode,
	}
}

func TestArgvShellBlockHelperProcess(_ *testing.T) {
	marker := slices.Index(os.Args, shellArgvHelperMarker)
	if marker < 0 {
		return
	}
	if marker+1 >= len(os.Args) {
		os.Exit(2)
	}
	payload := os.Args[marker+2:]
	switch os.Args[marker+1] {
	case "echo":
		for _, argument := range payload {
			//nolint:errcheck // A closed test pipe terminates useful helper output.
			_, _ = os.Stdout.WriteString(argument)
			//nolint:errcheck // A closed test pipe terminates useful helper output.
			_, _ = os.Stdout.Write([]byte{0})
		}
	case "stdout":
		if len(payload) != 1 {
			os.Exit(2)
		}
		//nolint:errcheck // A closed test pipe terminates useful helper output.
		_, _ = os.Stdout.WriteString(payload[0])
	case "oversize":
		//nolint:errcheck // A closed test pipe terminates useful helper output.
		_, _ = os.Stdout.WriteString(strings.Repeat("x", int(stdoutJSONLimit+1)))
	case "json-limit":
		data := `"` + strings.Repeat("x", int(stdoutJSONLimit-2)) + `"`
		//nolint:errcheck // A closed test pipe terminates useful helper output.
		_, _ = os.Stdout.WriteString(data)
	case "invalid-utf8":
		//nolint:errcheck // A closed test pipe terminates useful helper output.
		_, _ = os.Stdout.Write([]byte{'{', '"', 'v', '"', ':', '"', 0xff, '"', '}'})
	case "truncate":
		data := strings.Repeat("x", int(stdoutJSONLimit+1))
		//nolint:errcheck // A closed test pipe terminates useful helper output.
		_, _ = os.Stdout.WriteString(data)
		//nolint:errcheck // A closed test pipe terminates useful helper output.
		_, _ = os.Stderr.WriteString(data)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func assertNoShellBlockRuns(t *testing.T, server *Server) {
	t.Helper()
	page, err := server.store.ListRecent(1)
	if err != nil || len(page.Runs) != 0 {
		t.Fatalf("shell block rejection created ledger entries = %#v, %v", page.Runs, err)
	}
}

func shellBlockMarkerCommand(marker string) string {
	if runtime.GOOS == "windows" {
		return "echo started > \"" + marker + "\""
	}
	return "printf started > " + strconv.Quote(marker)
}

func shellBlockAppendCommand(path string) string {
	if runtime.GOOS == "windows" {
		return "echo run>>\"" + path + "\""
	}
	return "printf 'run\\n' >> " + strconv.Quote(path)
}
