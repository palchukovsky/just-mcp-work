// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package mcpserver

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
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

func TestMCPServerHelperProcess(_ *testing.T) {
	if os.Getenv("JMW_TEST_HELPER_PROCESS") != "1" {
		return
	}
	//nolint:errcheck // The helper exits immediately when test output is unavailable.
	_, _ = os.Stdout.WriteString("helper stdout")
	//nolint:errcheck // The helper exits immediately when test output is unavailable.
	_, _ = os.Stderr.WriteString("helper stderr")
	os.Exit(0)
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
