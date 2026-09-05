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

func TestDefineShellBlockRejectsInvalidInput(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	for _, testCase := range []struct {
		name             string
		command          string
		workingDirectory string
		message          string
	}{
		{
			name:    "empty command",
			command: " \n\t",
			message: "command must not be empty",
		},
		{
			name:             "outside workspace",
			command:          shellOutputCommand(),
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
		command, workingDirectory, err := server.resolveShellCommand("", block.BlockID, "")
		if err != nil {
			return fmt.Errorf("lookup shell block: %w", err)
		}
		if command != shellOutputCommand() || workingDirectory != "." {
			return fmt.Errorf("lookup = %q, %q", command, workingDirectory)
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
		"server session",
		"earlier session is an error",
	} {
		if !strings.Contains(description, expected) {
			t.Errorf("define_shell_block description does not mention %q", expected)
		}
	}
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
