// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/palchukovsky/just-mcp-work/internal/runner"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
	"github.com/palchukovsky/just-mcp-work/internal/updatecheck"
	"github.com/palchukovsky/just-mcp-work/internal/version"
	"github.com/palchukovsky/just-mcp-work/internal/workspace"
)

//nolint:gocyclo // This direct tool-flow test intentionally covers all MCP outcomes together.
func TestDirectHandlerFlow(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "justfile"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	runners, err := runner.NewRegistry(handlerRunner{})
	if err != nil {
		t.Fatal(err)
	}
	workspaceRegistry, err := workspace.NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(workspaceRegistry, runners, store, Config{
		Timeout:   5 * time.Second,
		Retention: time.Hour,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, projects, err := server.listProjects(context.Background(), nil, listProjectsInput{})
	if err != nil || len(projects.Projects) != 1 {
		t.Fatalf("listProjects = %#v, %v", projects, err)
	}
	projectPath := projects.Projects[0].RelPath
	_, tasks, err := server.listTasks(context.Background(), nil, listTasksInput{ProjectPath: projectPath})
	if err != nil || len(tasks.Tasks) != 1 || tasks.Tasks[0].ID != "fake:echo" {
		t.Fatalf("listTasks = %#v, %v", tasks, err)
	}

	_, receipt, err := server.runTask(context.Background(), nil, runTaskInput{
		ProjectPath: projectPath,
		TaskID:      "fake:echo",
		Arguments:   []string{"one", "two"},
	})
	if err != nil || !receipt.OK || receipt.RunID == "" || receipt.Status != runstore.StatusOK {
		t.Fatalf("runTask = %#v, %v", receipt, err)
	}
	_, run, err := server.getRun(context.Background(), nil, getRunInput{RunID: receipt.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if run.Run.ProjectPath != projectPath ||
		run.Run.Runner != "fake" ||
		run.Run.TaskID != "fake:echo" ||
		run.Run.Status != runstore.StatusOK {
		t.Fatalf("getRun metadata = %#v", run.Run)
	}
	_, stdout, err := server.getRunLogs(
		context.Background(),
		nil,
		getRunLogsInput{RunID: receipt.RunID, Stream: "stdout"},
	)
	if err != nil ||
		stdout.Data != "helper stdout" ||
		stdout.NextOffset != int64(len("helper stdout")) {
		t.Fatalf("stdout logs = %#v, %v", stdout, err)
	}
	_, stderr, err := server.getRunLogs(
		context.Background(),
		nil,
		getRunLogsInput{
			RunID:    receipt.RunID,
			Stream:   "stderr",
			Encoding: "base64",
		},
	)
	if err != nil || stderr.Data != base64.StdEncoding.EncodeToString([]byte("helper stderr")) {
		t.Fatalf("stderr logs = %#v, %v", stderr, err)
	}

	_, rejected, err := server.runTask(
		context.Background(),
		nil,
		runTaskInput{ProjectPath: projectPath, TaskID: "fake:missing"},
	)
	if err != nil ||
		rejected.OK ||
		rejected.RunID == "" ||
		rejected.Status != runstore.StatusSpawnError {
		t.Fatalf("rejected runTask = %#v, %v", rejected, err)
	}
	_, rejectedRun, err := server.getRun(context.Background(), nil, getRunInput{RunID: rejected.RunID})
	if err != nil || rejectedRun.Run.Status != runstore.StatusSpawnError {
		t.Fatalf("rejected getRun = %#v, %v", rejectedRun, err)
	}

	result, missing, err := server.getRun(
		context.Background(),
		nil,
		getRunInput{RunID: "not-a-run"},
	)
	if err != nil ||
		result == nil ||
		!result.IsError ||
		missing.Error == nil ||
		missing.Error.Message == "" {
		t.Fatalf("structured missing getRun error = %#v, %#v, %v", result, missing, err)
	}
}

//nolint:gocyclo // This direct handler test deliberately pins the filtering contract end to end.
func TestListProjectsFilterDefaultsDoNotLimitTaskLookup(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		"justfile",
		"top/justfile",
		"top/deeper/justfile",
		".hidden/justfile",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runners, err := runner.NewRegistry(handlerRunner{})
	if err != nil {
		t.Fatal(err)
	}
	workspaceRegistry, err := workspace.NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(workspaceRegistry, runners, store, Config{
		Timeout:   5 * time.Second,
		Retention: time.Hour,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, listed, err := server.listProjects(context.Background(), nil, listProjectsInput{})
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(listed.Projects))
	for _, project := range listed.Projects {
		paths = append(paths, project.RelPath)
	}
	if want := []string{".", "top"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("default projects = %#v, want %#v", paths, want)
	}
	if got, want := listed.AppliedFilter, (appliedFilterOutput{
		Path:            ".",
		MaxDepth:        1,
		Runners:         []string{},
		IncludeHidden:   false,
		DefaultsApplied: []string{"path", "max_depth", "runners", "include_hidden"},
		Pruned:          workspace.Pruned{Depth: 1, Hidden: 1, Excluded: 1},
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("applied filter = %#v, want %#v", got, want)
	}
	_, tasks, err := server.listTasks(
		context.Background(),
		nil,
		listTasksInput{ProjectPath: "top/deeper"},
	)
	if err != nil || len(tasks.Tasks) != 1 {
		t.Fatalf("listTasks below default depth = %#v, %v", tasks, err)
	}
	_, receipt, err := server.runTask(
		context.Background(),
		nil,
		runTaskInput{ProjectPath: "top/deeper", TaskID: "fake:echo"},
	)
	if err != nil || !receipt.OK {
		t.Fatalf("runTask below default depth = %#v, %v", receipt, err)
	}
}

//nolint:gocyclo // The cases assert one byte-paging sequence and its invalid boundaries.
func TestGetRunLogsPreservesUTF8ByteOffsets(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	handle, err := server.store.Begin(runstore.Meta{TaskID: "test:utf8-log"})
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("a€b")
	if _, err = handle.Stdout().Write(content); err != nil {
		t.Fatal(err)
	}
	if err = handle.Finish(runstore.StatusOK, 0, "", false, false); err != nil {
		t.Fatal(err)
	}

	_, first, err := server.getRunLogs(
		context.Background(),
		nil,
		getRunLogsInput{RunID: handle.Meta.RunID, Stream: "stdout", Limit: 2},
	)
	if err != nil || first.Data != "a" || first.Offset != 0 || first.NextOffset != 1 {
		t.Fatalf("first UTF-8 page = %#v, %v", first, err)
	}
	_, second, err := server.getRunLogs(
		context.Background(),
		nil,
		getRunLogsInput{
			RunID:  handle.Meta.RunID,
			Stream: "stdout",
			Offset: first.NextOffset,
			Limit:  4,
		},
	)
	if err != nil || second.Data != "€b" || second.Offset != 1 || second.NextOffset != 5 {
		t.Fatalf("second UTF-8 page = %#v, %v", second, err)
	}

	result, invalid, err := server.getRunLogs(
		context.Background(),
		nil,
		getRunLogsInput{RunID: handle.Meta.RunID, Stream: "stdout", Offset: 2, Limit: 2},
	)
	if err != nil || result == nil || !result.IsError || invalid.Error == nil {
		t.Fatalf("mid-rune UTF-8 page = %#v, %#v, %v", result, invalid, err)
	}
	result, incomplete, err := server.getRunLogs(
		context.Background(),
		nil,
		getRunLogsInput{RunID: handle.Meta.RunID, Stream: "stdout", Offset: 1, Limit: 1},
	)
	if err != nil || result == nil || !result.IsError || incomplete.Error == nil {
		t.Fatalf("incomplete UTF-8 page = %#v, %#v, %v", result, incomplete, err)
	}

	_, exact, err := server.getRunLogs(
		context.Background(),
		nil,
		getRunLogsInput{
			RunID:    handle.Meta.RunID,
			Stream:   "stdout",
			Offset:   1,
			Limit:    1,
			Encoding: "base64",
		},
	)
	if err != nil ||
		exact.Data != base64.StdEncoding.EncodeToString(content[1:2]) ||
		exact.NextOffset != 2 {
		t.Fatalf("base64 byte page = %#v, %v", exact, err)
	}
}

func TestMCPServerHelperProcess(_ *testing.T) {
	switch os.Getenv("JMW_TEST_HELPER_PROCESS") {
	case "1":
		//nolint:errcheck // The helper exits immediately when test output is unavailable.
		_, _ = os.Stdout.WriteString("helper stdout")
		//nolint:errcheck // The helper exits immediately when test output is unavailable.
		_, _ = os.Stderr.WriteString("helper stderr")
		os.Exit(0)
	case "sleep":
		for {
			time.Sleep(time.Hour)
		}
	}
}

func TestRunShellCommandFromNonProjectDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	server := newShellTestServer(t, root)
	command := shellOutputCommand()
	_, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: command, WorkingDirectory: "nested"},
	)
	if err != nil || !receipt.OK || receipt.Status != runstore.StatusOK {
		t.Fatalf("runShellCommand = %#v, %v", receipt, err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || strings.Contains(string(encoded), "completed") || strings.Contains(string(encoded), "promoted") {
		t.Fatalf("fast receipt changed = %s, %v", encoded, err)
	}
	assertShellRun(t, server, receipt.RunID, root)
	_, rejected, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: command, WorkingDirectory: "../outside"},
	)
	if err != nil || rejected.OK || rejected.Status != runstore.StatusSpawnError {
		t.Fatalf("rejected shell command = %#v, %v", rejected, err)
	}
}

func TestRunShellCommandDefaultsToWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	server := newShellTestServer(t, root)
	_, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellOutputCommand()},
	)
	if err != nil || !receipt.OK || receipt.Status != runstore.StatusOK {
		t.Fatalf("runShellCommand = %#v, %v", receipt, err)
	}
	_, run, err := server.getRun(context.Background(), nil, getRunInput{RunID: receipt.RunID})
	if err != nil || run.Run.ProjectPath != "." || run.Run.CWD != root {
		t.Fatalf("root shell run metadata = %#v, %v", run.Run, err)
	}
}

func TestRunShellCommandCanonicalizesWorkingDirectoryBeforeRecordingHistory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	server := newShellTestServer(t, root)
	_, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellOutputCommand(), WorkingDirectory: "nested/.."},
	)
	if err != nil || !receipt.OK {
		t.Fatalf("runShellCommand = %#v, %v", receipt, err)
	}
	_, stored, err := server.getRun(context.Background(), nil, getRunInput{RunID: receipt.RunID})
	if err != nil || stored.Run.ProjectPath != "." || stored.Run.CWD != root {
		t.Fatalf("canonical shell metadata = %#v, %v", stored.Run, err)
	}
}

func TestStartShellCommandReportsLedgerCreationFailureAsMCPError(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	if err := os.RemoveAll(server.store.LogRoot()); err != nil {
		t.Fatal(err)
	}
	result, output, err := server.startShellCommand(
		context.Background(),
		nil,
		startShellCommandInput{Command: shellOutputCommand()},
	)
	if err != nil || result == nil || !result.IsError || output.Error == nil {
		t.Fatalf("start after ledger removal = %#v, %#v, %v", result, output, err)
	}
}

func TestRunShellCommandCancellationIsOwnedByExecutor(t *testing.T) {
	root := t.TempDir()
	server := newShellTestServer(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, receipt, err := server.runShellCommand(
		ctx,
		nil,
		runShellCommandInput{Command: shellSleepCommand()},
	)
	if err != nil || receipt.Status != runstore.StatusCancelled {
		t.Fatalf("cancelled shell command = %#v, %v", receipt, err)
	}
}

//nolint:gocyclo // This test deliberately verifies the whole recovery sequence.
func TestRunShellCommandPromotionCanBeRecoveredAndStopped(t *testing.T) {
	root := t.TempDir()
	server := newShellTestServer(t, root)
	maxWait := int64(10)
	_, receipt, err := server.runShellCommand(
		context.Background(),
		nil,
		runShellCommandInput{Command: shellLongOutputCommand(), MaxWaitMS: &maxWait},
	)
	if err != nil ||
		receipt.RunID == "" ||
		receipt.Status != runstore.StatusRunning ||
		!receipt.Promoted ||
		receipt.Completed == nil ||
		*receipt.Completed {
		t.Fatalf("promoted shell command = %#v, %v", receipt, err)
	}
	_, persisted, err := server.getRun(context.Background(), nil, getRunInput{RunID: receipt.RunID})
	if err != nil ||
		persisted.Run.ProjectPath != "." ||
		persisted.Run.TaskID != "shell:command" ||
		persisted.Run.CWD != root ||
		persisted.Run.PID == 0 {
		t.Fatalf("running metadata = %#v, %v", persisted, err)
	}
	_, listed, err := server.listRuns(
		context.Background(),
		nil,
		listRunsInput{Status: []string{"running"}, ProjectPath: ".", TaskID: "shell:command"},
	)
	if err != nil || len(listed.Runs) == 0 || listed.Runs[0].RunID != receipt.RunID {
		t.Fatalf("running runs = %#v, %v", listed, err)
	}
	wait := int64(1)
	_, waiting, err := server.waitRun(
		context.Background(),
		nil,
		waitRunInput{RunID: receipt.RunID, MaxWaitMS: &wait},
	)
	if err != nil ||
		waiting.Completed == nil ||
		*waiting.Completed ||
		waiting.Status != runstore.StatusRunning {
		t.Fatalf("wait timeout = %#v, %v", waiting, err)
	}
	_, stopped, err := server.stopRun(
		context.Background(),
		nil,
		stopRunInput{RunID: receipt.RunID},
	)
	if err != nil ||
		stopped.Completed == nil ||
		!*stopped.Completed ||
		stopped.Status != runstore.StatusCancelled {
		t.Fatalf("stopped run = %#v, %v", stopped, err)
	}
}

//nolint:gocyclo // This test keeps pagination and input-contract assertions together.
func TestListRunsPaginatesAndExplainsStatusValues(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	for range 3 {
		handle, err := server.store.Begin(runstore.Meta{ProjectPath: ".", TaskID: "just:page"})
		if err != nil {
			t.Fatal(err)
		}
		if err := handle.Finish(runstore.StatusOK, 0, "", false, false); err != nil {
			t.Fatal(err)
		}
	}
	limit := 2
	_, first, err := server.listRuns(context.Background(), nil, listRunsInput{Limit: &limit})
	if err != nil || len(first.Runs) != 2 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first list page = %#v, %v", first, err)
	}
	_, second, err := server.listRuns(
		context.Background(),
		nil,
		listRunsInput{Limit: &limit, Cursor: first.NextCursor},
	)
	if err != nil || len(second.Runs) != 1 || second.Truncated {
		t.Fatalf("second list page = %#v, %v", second, err)
	}
	if second.Runs[0].RunID == first.Runs[0].RunID || second.Runs[0].RunID == first.Runs[1].RunID {
		t.Fatalf("list cursor repeated a run: first=%#v second=%#v", first.Runs, second.Runs)
	}
	result, invalid, err := server.listRuns(
		context.Background(),
		nil,
		listRunsInput{Status: []string{"bad"}},
	)
	if err != nil || result == nil || !result.IsError || invalid.Error == nil ||
		!strings.Contains(invalid.Error.Message, "spawn_error") {
		t.Fatalf("invalid status = %#v, %#v, %v", result, invalid, err)
	}
}

func TestRunShellCommandPreCancelledDoesNotStartProcess(t *testing.T) {
	root := t.TempDir()
	server := newShellTestServer(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, receipt, err := server.runShellCommand(
		ctx,
		nil,
		runShellCommandInput{Command: "echo started > marker"},
	)
	if err != nil || receipt.Status != runstore.StatusCancelled {
		t.Fatalf("pre-cancelled shell command = %#v, %v", receipt, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "marker")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("marker stat error = %v, want not exist", statErr)
	}
}

func TestNewRejectsSubMillisecondTaskTimeout(t *testing.T) {
	root := t.TempDir()
	runners, err := runner.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	workspaceRegistry, err := workspace.NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = New(workspaceRegistry, runners, store, Config{Timeout: 500 * time.Microsecond}); err == nil {
		t.Fatal("sub-millisecond task timeout must be rejected")
	}
}

func newShellTestServer(t *testing.T, root string) *Server {
	t.Helper()
	runners, err := runner.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	workspaceRegistry, err := workspace.NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(workspaceRegistry, runners, store, Config{
		Timeout:   5 * time.Second,
		Retention: time.Hour,
		Grace:     20 * time.Millisecond,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func shellOutputCommand() string {
	command := "printf shell-output"
	if runtime.GOOS == "windows" {
		command = "echo shell-output"
	}
	return command
}

func shellSleepCommand() string {
	if runtime.GOOS == "windows" {
		return "ping -n 30 127.0.0.1 >NUL"
	}
	return "sleep 30"
}

func shellLongOutputCommand() string {
	if runtime.GOOS == "windows" {
		return "echo started & ping -n 30 127.0.0.1 >NUL"
	}
	return "printf started; sleep 30"
}

func assertShellRun(t *testing.T, server *Server, runID, root string) {
	t.Helper()
	_, run, err := server.getRun(context.Background(), nil, getRunInput{RunID: runID})
	if err != nil ||
		run.Run.Runner != "shell" ||
		run.Run.ProjectPath != "nested" ||
		run.Run.CWD != filepath.Join(root, "nested") {
		t.Fatalf("shell run metadata = %#v, %v", run.Run, err)
	}
	_, stdout, err := server.getRunLogs(
		context.Background(),
		nil,
		getRunLogsInput{RunID: runID, Stream: "stdout"},
	)
	if err != nil || !strings.Contains(stdout.Data, "shell-output") {
		t.Fatalf("shell stdout = %#v, %v", stdout, err)
	}
}

//nolint:gocyclo // This test keeps the normal-result and status-tool contracts together.
func TestUpdateNoticeKeepsPrimaryToolResultAndStatusIsFresh(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "justfile"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	runners, err := runner.NewRegistry(handlerRunner{})
	if err != nil {
		t.Fatal(err)
	}
	workspaceRegistry, err := workspace.NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	client := &versionTagClient{responses: [][]string{{"v0.2.0"}, {"v0.1.1"}, {"v0.1.1"}}}
	updates := updatecheck.New(updatecheck.Config{
		StatePath:      filepath.Join(store.StateRoot(), "version.json"),
		Endpoint:       "https://example.invalid/tags",
		CurrentVersion: version.Detect("v0.1.0", "(devel)"),
		Client:         client,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	updates.CheckNow(context.Background())
	server, err := New(workspaceRegistry, runners, store, Config{
		Timeout:   time.Second,
		Retention: time.Hour,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Updates:   updates,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, output, err := withUpdateNotification(server, server.listProjects)(
		context.Background(),
		nil,
		listProjectsInput{},
	)
	if err != nil || result == nil || len(result.Content) != 2 || len(output.Projects) != 1 {
		t.Fatalf("regular response = %#v, %#v, %v", result, output, err)
	}
	primary, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(primary.Text, "projects") {
		t.Fatalf("primary response content = %#v", result.Content[0])
	}
	notice, ok := result.Content[1].(*mcp.TextContent)
	if !ok || !strings.Contains(notice.Text, "IMPORTANT FOR THE AGENT") {
		t.Fatalf("update notice content = %#v", result.Content[1])
	}

	_, firstStatus, err := server.versionStatus(context.Background(), nil, versionStatusInput{})
	if err != nil || firstStatus.UpdateType != "patch" {
		t.Fatalf("first version_status = %#v, %v", firstStatus, err)
	}
	_, secondStatus, err := server.versionStatus(context.Background(), nil, versionStatusInput{})
	if err != nil || secondStatus.UpdateType != "patch" || client.Calls() != 3 {
		t.Fatalf("second version_status = %#v, calls %d, %v", secondStatus, client.Calls(), err)
	}
}

func TestGitHubFailureDoesNotBreakRegularTool(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "justfile"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	runners, err := runner.NewRegistry(handlerRunner{})
	if err != nil {
		t.Fatal(err)
	}
	workspaceRegistry, err := workspace.NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	updates := updatecheck.New(updatecheck.Config{
		StatePath:      filepath.Join(store.StateRoot(), "version.json"),
		Endpoint:       "https://example.invalid/tags",
		CurrentVersion: version.Detect("v0.1.0", "(devel)"),
		Client:         errorHTTPClient{},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	updates.CheckNow(context.Background())
	server, err := New(workspaceRegistry, runners, store, Config{Updates: updates})
	if err != nil {
		t.Fatal(err)
	}
	result, output, err := withUpdateNotification(server, server.listProjects)(
		context.Background(),
		nil,
		listProjectsInput{},
	)
	if err != nil || result != nil || len(output.Projects) != 1 {
		t.Fatalf("regular tool after GitHub failure = %#v, %#v, %v", result, output, err)
	}
}

type versionTagClient struct {
	responses [][]string
	mu        sync.Mutex
	calls     int
}

func (c *versionTagClient) Do(*http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	index := c.calls
	c.calls++
	if index >= len(c.responses) {
		index = len(c.responses) - 1
	}
	tags := make([]map[string]string, 0, len(c.responses[index]))
	for _, tag := range c.responses[index] {
		tags = append(tags, map[string]string{"name": tag})
	}
	payload, err := json.Marshal(tags)
	if err != nil {
		return nil, fmt.Errorf("encode test tags: %w", err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(payload))),
	}, nil
}

func (c *versionTagClient) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type errorHTTPClient struct{}

func (errorHTTPClient) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline")
}

type handlerRunner struct{}

func (handlerRunner) Name() string { return "fake" }

func (handlerRunner) Detect(projectDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(projectDir, "justfile"))
	return err == nil, nil
}

func (handlerRunner) ListTasks(context.Context, string) ([]runner.Task, error) {
	return []runner.Task{{ID: "fake:echo", Runner: "fake", Name: "echo"}}, nil
}

func (handlerRunner) BuildCommand(
	ctx context.Context,
	projectDir string,
	task runner.Task,
	_ []string,
) (*exec.Cmd, error) {
	if task.ID != "fake:echo" {
		return nil, errors.New("unknown helper task")
	}
	// #nosec G204,G702 -- the test re-executes its own fixed helper process.
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestMCPServerHelperProcess")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "JMW_TEST_HELPER_PROCESS=1")
	return cmd, nil
}
