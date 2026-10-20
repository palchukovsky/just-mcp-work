// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package mcpserver

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/palchukovsky/just-mcp-work/internal/runner"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

func TestRunToolsTaskTimeoutOverrides(t *testing.T) {
	cases := []struct {
		value     any
		name      string
		present   bool
		unlimited bool
		wantMS    int64
	}{
		{name: "omitted", wantMS: 5000},
		{name: "null", present: true, wantMS: 5000},
		{name: "longer", value: int64(60000), present: true, wantMS: 60000},
		{name: "disabled", value: int64(0), present: true},
		{name: "inherit unlimited", unlimited: true},
		{name: "enable limit", value: int64(60000), present: true, unlimited: true, wantMS: 60000},
		{
			name:    "largest duration",
			value:   int64((1<<63 - 1) / time.Millisecond),
			present: true,
			wantMS:  int64((1<<63 - 1) / time.Millisecond),
		},
	}
	for _, tool := range taskTimeoutTools() {
		for _, tc := range cases {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				server := newTaskTimeoutTestServer(t)
				if tc.unlimited {
					server.config.Timeout = 0
					server.config.TimeoutUnlimited = true
				}
				withMCPClientSession(t, server, func(session *mcp.ClientSession) {
					args := taskTimeoutArguments(tool)
					if tc.present {
						args["task_timeout_ms"] = tc.value
					}
					receipt := callTaskTimeoutTool(t, session, tool, args)
					runID := taskTimeoutRunID(t, receipt)
					assertTaskTimeout(t, receipt, tc.wantMS)
					meta, err := server.store.Get(runID)
					if err != nil {
						t.Fatal(err)
					}
					if tc.wantMS == 0 {
						if meta.TaskTimeoutMS != nil {
							t.Fatalf("unlimited persisted timeout = %v", *meta.TaskTimeoutMS)
						}
					} else if meta.TaskTimeoutMS == nil || *meta.TaskTimeoutMS != tc.wantMS {
						t.Fatalf("persisted timeout = %v, want %d", meta.TaskTimeoutMS, tc.wantMS)
					}
				})
				wantTimeout := 5 * time.Second
				if tc.unlimited {
					wantTimeout = 0
				}
				if server.config.Timeout != wantTimeout || server.config.TimeoutUnlimited != tc.unlimited {
					t.Fatalf("operation changed the server timeout: %#v", server.config)
				}
			})
		}
	}
}

func TestRunToolsTaskTimeoutExecution(t *testing.T) {
	cases := []struct {
		name      string
		want      runstore.Status
		serverMS  int64
		value     int64
		present   bool
		unlimited bool
	}{
		{name: "inherit", serverMS: 1, want: runstore.StatusTimeout},
		{name: "shorten", serverMS: 5000, value: 1, present: true, want: runstore.StatusTimeout},
		{name: "extend", serverMS: 1, value: 5000, present: true, want: runstore.StatusRunning},
		{name: "disable", serverMS: 1, present: true, want: runstore.StatusRunning},
		{name: "enable", value: 1, present: true, unlimited: true, want: runstore.StatusTimeout},
	}
	for _, tool := range taskTimeoutTools() {
		for _, tc := range cases {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				server := newTaskTimeoutTestServer(t)
				server.config.Timeout = time.Duration(tc.serverMS) * time.Millisecond
				server.config.TimeoutUnlimited = tc.unlimited
				withMCPClientSession(t, server, func(session *mcp.ClientSession) {
					args := taskTimeoutArguments(tool)
					if tc.present {
						args["task_timeout_ms"] = tc.value
					}
					receipt := callTaskTimeoutTool(t, session, tool, args)
					runID := taskTimeoutRunID(t, receipt)
					if tc.want == runstore.StatusRunning {
						// The inherited 1ms limit must not terminate an overridden run.
						time.Sleep(20 * time.Millisecond)
						receipt = callTaskTimeoutTool(t, session, "get_run_status", map[string]any{
							"run_id": runID,
						})
					} else {
						receipt = callTaskTimeoutTool(t, session, "wait_run", map[string]any{
							"run_id":      runID,
							"max_wait_ms": 5000,
						})
					}
					if receipt["status"] != string(tc.want) {
						t.Fatalf("run after timeout = %#v, want %s", receipt, tc.want)
					}
					if tc.want == runstore.StatusTimeout {
						message, ok := receipt["message"].(string)
						if !ok || !strings.Contains(message, "task_timeout_ms") {
							t.Fatalf("timeout diagnostic = %#v", receipt["message"])
						}
					}
				})
			})
		}
	}
}

func TestRunToolsTaskTimeoutRejectsBeforeRunStart(t *testing.T) {
	for _, tool := range taskTimeoutTools() {
		for _, value := range []int64{-1, int64((1<<63-1)/time.Millisecond) + 1, 1 << 53} {
			t.Run(tool+"/"+strconv.FormatInt(value, 10), func(t *testing.T) {
				server := newTaskTimeoutTestServer(t)
				withMCPClientSession(t, server, func(session *mcp.ClientSession) {
					args := taskTimeoutArguments(tool)
					args["task_timeout_ms"] = value
					result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
						Name: tool, Arguments: args,
					})
					if err != nil || !result.IsError {
						t.Fatalf("invalid timeout = %#v, %v", result, err)
					}
					output, ok := result.StructuredContent.(map[string]any)
					if !ok || output["worktree_root"] != server.workspace.WorktreeRoot() {
						t.Fatalf("invalid timeout structured content = %#v", result.StructuredContent)
					}
					toolErr, ok := output["error"].(map[string]any)
					if !ok {
						t.Fatalf("invalid timeout error = %#v", output["error"])
					}
					message, ok := toolErr["message"].(string)
					if !ok || !strings.Contains(message, "task_timeout_ms") {
						t.Fatalf("invalid timeout error = %#v", output["error"])
					}
				})
				page, err := server.store.ListRecent(1)
				if err != nil || len(page.Runs) != 0 {
					t.Fatalf("runs after invalid timeout = %#v, %v", page, err)
				}
			})
		}
	}
}

func TestTaskTimeoutOverrideIsIsolatedFromConcurrentRuns(t *testing.T) {
	server := newTaskTimeoutTestServer(t)
	server.config.Timeout = 20 * time.Millisecond
	withMCPClientSession(t, server, func(session *mcp.ClientSession) {
		args := taskTimeoutArguments("start_task")
		args["task_timeout_ms"] = 0
		unlimited := callTaskTimeoutTool(t, session, "start_task", args)
		inherited := callTaskTimeoutTool(t, session, "start_shell_command",
			taskTimeoutArguments("start_shell_command"))
		finished := callTaskTimeoutTool(t, session, "wait_run", map[string]any{
			"run_id": inherited["run_id"], "max_wait_ms": 5000,
		})
		if finished["status"] != string(runstore.StatusTimeout) {
			t.Fatalf("inherited timeout = %#v", finished)
		}
		stillRunning := callTaskTimeoutTool(t, session, "get_run_status", map[string]any{
			"run_id": unlimited["run_id"],
		})
		if stillRunning["status"] != string(runstore.StatusRunning) {
			t.Fatalf("concurrent unlimited run = %#v", stillRunning)
		}
		assertTaskTimeout(t, stillRunning, 0)
	})
}

func TestSynchronousTaskTimeoutIsIndependentFromWait(t *testing.T) {
	server := newTaskTimeoutTestServer(t)
	withMCPClientSession(t, server, func(session *mcp.ClientSession) {
		for _, tool := range []string{"run_task", "run_shell_command"} {
			args := taskTimeoutArguments(tool)
			args["task_timeout_ms"] = 1
			args["max_wait_ms"] = -1
			finished := callTaskTimeoutTool(t, session, tool, args)
			if finished["status"] != string(runstore.StatusTimeout) {
				t.Fatalf("synchronous overridden timeout = %#v", finished)
			}
		}
	})
}

func taskTimeoutTools() []string {
	return []string{"run_task", "start_task", "run_shell_command", "start_shell_command"}
}

func taskTimeoutArguments(tool string) map[string]any {
	args := map[string]any{}
	if strings.HasSuffix(tool, "task") {
		args["project_path"] = "."
		args["task_id"] = "fake:echo"
	} else {
		args["command"] = shellSleepCommand()
	}
	if strings.HasPrefix(tool, "run_") {
		args["max_wait_ms"] = 0
	}
	return args
}

func callTaskTimeoutTool(
	t *testing.T,
	session *mcp.ClientSession,
	name string,
	args map[string]any,
) map[string]any {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: name, Arguments: args,
	})
	if err != nil || result.IsError {
		t.Fatalf("%s = %#v, %v", name, result, err)
	}
	output, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("%s structured content = %#v", name, result.StructuredContent)
	}
	return output
}

func assertTaskTimeout(t *testing.T, receipt map[string]any, wantMS int64) {
	t.Helper()
	value, present := receipt["task_timeout_ms"]
	if wantMS == 0 {
		if present {
			t.Fatalf("unlimited receipt has task_timeout_ms: %#v", receipt)
		}
	} else if !present || value != float64(wantMS) {
		t.Fatalf("receipt timeout = %#v, want %d", value, wantMS)
	}
}

func taskTimeoutRunID(t *testing.T, receipt map[string]any) string {
	t.Helper()
	runID, ok := receipt["run_id"].(string)
	if !ok || runID == "" {
		t.Fatalf("receipt has no run ID: %#v", receipt)
	}
	return runID
}

func newTaskTimeoutTestServer(t *testing.T) *Server {
	t.Helper()
	server := newRunnerTestServer(t, t.TempDir(), taskTimeoutRunner{})
	server.config.Grace = 5 * time.Millisecond
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.manager.Shutdown(ctx)
		if err := ctx.Err(); err != nil {
			t.Errorf("stop timeout test runs: %v", err)
		}
	})
	return server
}

type taskTimeoutRunner struct{ handlerRunner }

func (candidate taskTimeoutRunner) BuildCommand(
	ctx context.Context,
	projectDir string,
	task runner.Task,
	args []string,
) (*exec.Cmd, error) {
	cmd, err := candidate.handlerRunner.BuildCommand(ctx, projectDir, task, args)
	if err != nil {
		return nil, err
	}
	cmd.Env = append(cmd.Env, "JMW_TEST_HELPER_PROCESS=sleep")
	return cmd, nil
}
