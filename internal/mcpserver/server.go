// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package mcpserver exposes workspace tasks through the local STDIO MCP transport.
package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/palchukovsky/just-mcp-work/internal/executor"
	"github.com/palchukovsky/just-mcp-work/internal/runmanager"
	"github.com/palchukovsky/just-mcp-work/internal/runner"
	"github.com/palchukovsky/just-mcp-work/internal/runstats"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
	"github.com/palchukovsky/just-mcp-work/internal/updatecheck"
	"github.com/palchukovsky/just-mcp-work/internal/version"
	"github.com/palchukovsky/just-mcp-work/internal/workspace"
)

// Config controls server-side execution defaults.
//
//nolint:govet // Field order groups process settings before the logger dependency.
type Config struct {
	Timeout          time.Duration
	TimeoutUnlimited bool
	SyncDeadline     time.Duration
	Retention        time.Duration
	Grace            time.Duration
	Logger           *slog.Logger
	Updates          *updatecheck.Checker
}

// Server owns MCP handlers and their workspace dependencies.
type Server struct {
	workspace *workspace.Registry
	runners   *runner.Registry
	store     *runstore.Store
	manager   *runmanager.Manager
	stats     *runstats.Collector
	updates   *updatecheck.Checker
	config    Config
}

// New creates an MCP server facade.
func New(
	workspaceRegistry *workspace.Registry,
	runners *runner.Registry,
	store *runstore.Store,
	config Config,
) (*Server, error) {
	if workspaceRegistry == nil || runners == nil || store == nil {
		return nil, fmt.Errorf("workspace registry, runner registry, and run store are required")
	}
	if err := validateTaskTimeout(config); err != nil {
		return nil, err
	}
	if config.Timeout == 0 && !config.TimeoutUnlimited {
		config.Timeout = 15 * time.Minute
	}
	if config.TimeoutUnlimited {
		config.Timeout = 0
	}
	if config.Retention <= 0 {
		config.Retention = 72 * time.Hour
	}
	if config.Grace <= 0 {
		config.Grace = 2 * time.Second
	}
	if config.SyncDeadline <= 0 {
		config.SyncDeadline = time.Minute
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Updates == nil {
		config.Updates = updatecheck.New(
			updatecheck.Config{
				StatePath:      filepath.Join(store.StateRoot(), "version.json"),
				CurrentVersion: version.Current(),
				Logger:         config.Logger,
			},
		)
	}
	stats := runstats.New(store)
	return &Server{
		workspace: workspaceRegistry,
		runners:   runners,
		store:     store,
		manager:   runmanager.New(stats.Invalidate, runmanager.MaxConcurrentRuns),
		stats:     stats,
		updates:   config.Updates,
		config:    config,
	}, nil
}

func validateTaskTimeout(config Config) error {
	if config.Timeout < 0 {
		return fmt.Errorf("task timeout must not be negative")
	}
	if !config.TimeoutUnlimited && config.Timeout > 0 && config.Timeout < time.Millisecond {
		return fmt.Errorf("task timeout must be zero or at least 1ms")
	}
	return nil
}

func shutdownBudget(grace time.Duration) time.Duration {
	return 2*grace + time.Second
}

// Run serves only STDIO MCP traffic. Logs are handled by the configured logger.
func (s *Server) Run(ctx context.Context) error {
	s.updates.Start(ctx)
	defer s.updates.Close()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			shutdownBudget(s.config.Grace),
		)
		defer cancel()
		s.manager.Shutdown(shutdownCtx)
	}()
	server := mcp.NewServer(
		&mcp.Implementation{Name: "just-mcp-work", Version: version.Current().Display()},
		&mcp.ServerOptions{Logger: s.config.Logger},
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name: "list_projects",
			Description: "List task projects. By default, scans depth 0-1 below the workspace root " +
				"without dot-directories; use path to choose a subtree, max_depth or include_hidden to widen " +
				"directory coverage, and runners to restrict projects. The depth, hidden, and excluded pruned " +
				"counters count skipped directory subtrees; runner_mismatch counts inspected projects removed by runners. " +
				"Excluded paths " +
				"are configured by the operator and cannot be widened.",
		},
		recoverTool(withUpdateNotification(s, s.listProjects)),
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        "list_tasks",
			Description: "List runner-neutral tasks for one discovered project.",
		},
		recoverTool(withUpdateNotification(s, s.listTasks)),
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name: "run_task",
			Description: "Run one discovered task. A running receipt with promoted: true is normal: " +
				"use its run_id with wait_run or get_run_status, never start the task again.",
		},
		recoverTool(withUpdateNotification(s, s.runTask)),
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        "start_task",
			Description: "Start one discovered task asynchronously and return its run_id immediately.",
		},
		recoverTool(withUpdateNotification(s, s.startTask)),
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name: "run_shell_command",
			Description: "Run a shell command. A running receipt with promoted: true is normal: follow its " +
				"run_id instead of retrying the command.",
		},
		recoverTool(withUpdateNotification(s, s.runShellCommand)),
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        "start_shell_command",
			Description: "Start a shell command asynchronously and return its run_id immediately.",
		},
		recoverTool(withUpdateNotification(s, s.startShellCommand)),
	)
	mcp.AddTool(
		server,
		&mcp.Tool{Name: "get_run", Description: "Get persisted metadata for one task run."},
		recoverTool(withUpdateNotification(s, s.getRun)),
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        "get_run_logs",
			Description: "Read a paged stdout or stderr range from a persisted task run.",
		},
		recoverTool(withUpdateNotification(s, s.getRunLogs)),
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        "get_run_status",
			Description: "Get a non-blocking run snapshot, including liveness, recent output, and duration stats.",
		},
		recoverTool(withUpdateNotification(s, s.getRunStatus)),
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        "wait_run",
			Description: "Wait for a run without stopping it when the wait timeout expires.",
		},
		recoverTool(withUpdateNotification(s, s.waitRun)),
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        "stop_run",
			Description: "Stop a run owned by this server process and return its final status.",
		},
		recoverTool(withUpdateNotification(s, s.stopRun)),
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        "list_runs",
			Description: "List recent persisted runs, newest first, with optional status, project, or task filters.",
		},
		recoverTool(withUpdateNotification(s, s.listRuns)),
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        "version_status",
			Description: "Check the installed version against the latest stable GitHub release tag.",
		},
		recoverTool(s.versionStatus),
	)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("run MCP transport: %w", err)
	}
	return nil
}

type versionStatusInput struct{}

func (s *Server) versionStatus(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	_ versionStatusInput,
) (*mcp.CallToolResult, updatecheck.Status, error) {
	return nil, s.updates.CheckNow(ctx), nil
}

//nolint:govet // Field order follows the MCP request shape.
type listProjectsInput struct {
	Path          *string  `json:"path,omitempty" jsonschema:"workspace-relative subtree to search, default ."`
	MaxDepth      *int     `json:"max_depth,omitempty" jsonschema:"relative scan depth: default 1, -1 unlimited"`
	Runners       []string `json:"runners,omitempty" jsonschema:"keep projects exposing one of these runners"`
	IncludeHidden *bool    `json:"include_hidden,omitempty" jsonschema:"include dot-directories, default false"`
}

//nolint:govet // Field order follows the stable MCP JSON response shape.
type projectOutput struct {
	RelPath string            `json:"rel_path"`
	Runners []string          `json:"runners"`
	Status  string            `json:"status"`
	Errors  map[string]string `json:"errors,omitempty"`
}

//nolint:govet // Field order follows the stable MCP JSON response shape.
type listProjectsOutput struct {
	Projects      []projectOutput     `json:"projects"`
	AppliedFilter appliedFilterOutput `json:"applied_filter"`
	Error         *toolError          `json:"error,omitempty"`
}

//nolint:govet // Field order follows the stable MCP JSON response shape.
type appliedFilterOutput struct {
	Path            string           `json:"path"`
	MaxDepth        int              `json:"max_depth"`
	Runners         []string         `json:"runners"`
	IncludeHidden   bool             `json:"include_hidden"`
	DefaultsApplied []string         `json:"defaults_applied"`
	Pruned          workspace.Pruned `json:"pruned"`
}

func (s *Server) listProjects(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input listProjectsInput,
) (*mcp.CallToolResult, listProjectsOutput, error) {
	filter, applied := projectFilter(input)
	projects, pruned, err := s.workspace.Discover(ctx, filter)
	if err != nil {
		return toolErrorResult(err), listProjectsOutput{Error: newToolError(err)}, nil
	}
	applied.Pruned = pruned
	output := listProjectsOutput{
		Projects:      make([]projectOutput, 0, len(projects)),
		AppliedFilter: applied,
	}
	for _, project := range projects {
		output.Projects = append(
			output.Projects,
			projectOutput{
				RelPath: project.RelPath,
				Runners: project.Runners,
				Status:  project.Status,
				Errors:  project.Errors,
			},
		)
	}
	return nil, output, nil
}

func projectFilter(input listProjectsInput) (workspace.Filter, appliedFilterOutput) {
	filter := workspace.Filter{Runners: append([]string(nil), input.Runners...)}
	applied := appliedFilterOutput{Runners: append([]string{}, input.Runners...)}
	if input.Path == nil {
		filter.Path = "."
		applied.Path = "."
		applied.DefaultsApplied = append(applied.DefaultsApplied, "path")
	} else {
		filter.Path = *input.Path
		applied.Path = *input.Path
	}
	if input.MaxDepth == nil {
		filter.MaxDepth = 1
		applied.MaxDepth = 1
		applied.DefaultsApplied = append(applied.DefaultsApplied, "max_depth")
	} else {
		filter.MaxDepth = *input.MaxDepth
		applied.MaxDepth = *input.MaxDepth
	}
	if input.Runners == nil {
		applied.DefaultsApplied = append(applied.DefaultsApplied, "runners")
	}
	if input.IncludeHidden == nil {
		applied.DefaultsApplied = append(applied.DefaultsApplied, "include_hidden")
	} else {
		filter.IncludeHidden = *input.IncludeHidden
		applied.IncludeHidden = *input.IncludeHidden
	}
	return filter, applied
}

type listTasksInput struct {
	ProjectPath string `json:"project_path" jsonschema:"relative path returned by list_projects"`
	Runner      string `json:"runner,omitempty" jsonschema:"optional runner name to filter"`
}

//nolint:govet // Field order follows the stable MCP JSON response shape.
type listTasksOutput struct {
	Tasks []taskOutput `json:"tasks"`
	Error *toolError   `json:"error,omitempty"`
}

type taskOutput struct {
	runner.Task
	Stats *runstats.Aggregate `json:"stats,omitempty"`
}

func (s *Server) listTasks(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input listTasksInput,
) (*mcp.CallToolResult, listTasksOutput, error) {
	s.repairTerminalMetadata()
	project, err := s.workspace.Find(ctx, input.ProjectPath)
	if err != nil {
		return toolErrorResult(err), listTasksOutput{Error: newToolError(err)}, nil
	}
	if input.Runner != "" {
		return nil,
			listTasksOutput{Tasks: s.taskOutputs(project.RelPath, project.Tasks[input.Runner])},
			nil
	}
	result := listTasksOutput{}
	for _, name := range project.Runners {
		result.Tasks = append(result.Tasks, s.taskOutputs(project.RelPath, project.Tasks[name])...)
	}
	return nil, result, nil
}

func (s *Server) taskOutputs(projectPath string, tasks []runner.Task) []taskOutput {
	output := make([]taskOutput, 0, len(tasks))
	for _, task := range tasks {
		stats, err := s.stats.Task(projectPath, task.ID)
		if err != nil {
			s.config.Logger.Warn("read task duration statistics failed", "task_id", task.ID, "error", err)
		}
		output = append(output, taskOutput{Task: task, Stats: stats})
	}
	return output
}

//nolint:govet // Field order follows the MCP request shape.
type runTaskInput struct {
	ProjectPath string   `json:"project_path" jsonschema:"relative path returned by list_projects"`
	TaskID      string   `json:"task_id" jsonschema:"task ID returned by list_tasks"`
	Arguments   []string `json:"arguments,omitempty" jsonschema:"arguments passed to the selected task"`
	MaxWaitMS   *int64   `json:"max_wait_ms,omitempty" jsonschema:"wait up to this many milliseconds; 0 starts immediately, -1 waits for completion"`
}

//nolint:govet // Embedded result precedes the structured MCP error by contract.
type runTaskOutput struct {
	executor.Result
	*runDetails
	Error *toolError `json:"error,omitempty"`
}

//nolint:govet // Field order follows the stable MCP running receipt response shape.
type runDetails struct {
	Completed           *bool           `json:"completed,omitempty"`
	Promoted            bool            `json:"promoted,omitempty"`
	ProjectPath         string          `json:"project_path,omitempty"`
	Runner              string          `json:"runner,omitempty"`
	TaskID              string          `json:"task_id,omitempty"`
	Args                []string        `json:"args,omitempty"`
	CWD                 string          `json:"cwd,omitempty"`
	PID                 int             `json:"pid,omitempty"`
	OwnerPID            int             `json:"owner_pid,omitempty"`
	ProcessAlive        *bool           `json:"process_alive,omitempty"`
	OwnedByThis         *bool           `json:"owned_by_this_server,omitempty"`
	StartedAt           *time.Time      `json:"started_at,omitempty"`
	EndedAt             *time.Time      `json:"ended_at,omitempty"`
	LastOutputAt        *time.Time      `json:"last_output_at,omitempty"`
	LastOutputAgeMS     int64           `json:"last_output_age_ms,omitempty"`
	NoOutputYet         *bool           `json:"no_output_yet,omitempty"`
	StdoutBytes         int64           `json:"stdout_bytes,omitempty"`
	StderrBytes         int64           `json:"stderr_bytes,omitempty"`
	TaskTimeoutMS       *int64          `json:"task_timeout_ms,omitempty"`
	TimeToTaskTimeoutMS *int64          `json:"time_to_task_timeout_ms,omitempty"`
	TimeoutMS           int64           `json:"timeout_ms,omitempty"`
	TimeToTimeoutMS     *int64          `json:"time_to_timeout_ms,omitempty"`
	Stats               *runstats.Stats `json:"stats,omitempty"`
	AlreadyFinished     bool            `json:"already_finished,omitempty"`
}

func (s *Server) runTask(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input runTaskInput,
) (*mcp.CallToolResult, runTaskOutput, error) {
	wait, err := s.syncWaitDuration(input.MaxWaitMS)
	if err != nil {
		return toolErrorResult(err), runTaskOutput{Error: newToolError(err)}, nil
	}
	run, stats, output := s.startTaskRun(ctx, input)
	if run == nil {
		return mcpErrorFor(output), output, nil
	}
	return nil, s.waitForSyncReceipt(ctx, request, run, stats, wait), nil
}

type startTaskInput struct {
	ProjectPath string   `json:"project_path" jsonschema:"relative path returned by list_projects"`
	TaskID      string   `json:"task_id" jsonschema:"task ID returned by list_tasks"`
	Arguments   []string `json:"arguments,omitempty" jsonschema:"arguments passed to the selected task"`
}

func (s *Server) startTask(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input startTaskInput,
) (*mcp.CallToolResult, runTaskOutput, error) {
	run, stats, output := s.startTaskRun(ctx, runTaskInput{
		ProjectPath: input.ProjectPath,
		TaskID:      input.TaskID,
		Arguments:   input.Arguments,
	})
	if run == nil {
		return mcpErrorFor(output), output, nil
	}
	return nil, s.runningReceipt(run, stats, false), nil
}

func (s *Server) startTaskRun(
	ctx context.Context,
	input runTaskInput,
) (*executor.Run, *runstats.Stats, runTaskOutput) {
	go s.cleanup()
	s.repairTerminalMetadata()
	stats, statsErr := s.stats.For(input.ProjectPath, input.TaskID, input.Arguments)
	if statsErr != nil {
		s.config.Logger.Warn("read task duration statistics failed", "task_id", input.TaskID, "error", statsErr)
	}
	handle, err := s.store.Begin(
		runstore.Meta{ProjectPath: input.ProjectPath, TaskID: input.TaskID, Args: input.Arguments},
	)
	if err != nil {
		return nil, stats, runTaskOutput{Error: newToolError(err)}
	}
	s.configureTaskTimeout(&handle.Meta)
	project, err := s.workspace.Find(ctx, input.ProjectPath)
	if err != nil {
		return nil, stats, runTaskOutput{Result: s.reject(handle, err)}
	}
	runnerName, _, found := strings.Cut(input.TaskID, ":")
	if !found {
		return nil, stats, runTaskOutput{
			Result: s.reject(handle, fmt.Errorf("task_id must be namespaced as <runner>:<task>")),
		}
	}
	candidate, ok := s.runners.Get(runnerName)
	if !ok {
		return nil, stats, runTaskOutput{Result: s.reject(handle, fmt.Errorf("unknown runner %q", runnerName))}
	}
	task, ok := taskByID(project.Tasks[runnerName], input.TaskID)
	if !ok {
		return nil, stats, runTaskOutput{
			Result: s.reject(
				handle,
				fmt.Errorf("unknown task_id %q for project %q", input.TaskID, input.ProjectPath),
			),
		}
	}
	handle.Meta.Runner = runnerName
	handle.Meta.CWD = project.Dir
	if versionProvider, ok := candidate.(runner.VersionProvider); ok {
		if runnerVersion, versionErr := versionProvider.RunnerVersion(ctx); versionErr == nil {
			handle.Meta.RunnerVersion = runnerVersion
		}
	}
	if persistErr := handle.PersistRunning(); persistErr != nil {
		return nil, stats, runTaskOutput{Result: s.reject(handle, persistErr)}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, stats, runTaskOutput{Result: s.cancel(handle, ctxErr)}
	}
	cmd, err := candidate.BuildCommand(context.Background(), project.Dir, task, input.Arguments)
	if err != nil {
		return nil, stats, runTaskOutput{Result: s.reject(handle, err)}
	}
	return s.startRun(handle, cmd, stats)
}

//nolint:govet // Field order follows the MCP request shape.
type runShellCommandInput struct {
	Command          string `json:"command" jsonschema:"command text interpreted by the operating system shell"`
	WorkingDirectory string `json:"working_directory,omitempty" jsonschema:"workspace-relative directory, default root"`
	MaxWaitMS        *int64 `json:"max_wait_ms,omitempty" jsonschema:"wait up to this many milliseconds; 0 starts immediately, -1 waits for completion"`
}

func (s *Server) runShellCommand(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input runShellCommandInput,
) (*mcp.CallToolResult, runTaskOutput, error) {
	wait, err := s.syncWaitDuration(input.MaxWaitMS)
	if err != nil {
		return toolErrorResult(err), runTaskOutput{Error: newToolError(err)}, nil
	}
	run, stats, output := s.startShellRun(ctx, input)
	if run == nil {
		return mcpErrorFor(output), output, nil
	}
	return nil, s.waitForSyncReceipt(ctx, request, run, stats, wait), nil
}

type startShellCommandInput struct {
	Command          string `json:"command" jsonschema:"command text interpreted by the operating system shell"`
	WorkingDirectory string `json:"working_directory,omitempty" jsonschema:"workspace-relative directory, default root"`
}

func (s *Server) startShellCommand(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input startShellCommandInput,
) (*mcp.CallToolResult, runTaskOutput, error) {
	run, stats, output := s.startShellRun(ctx, runShellCommandInput{
		Command:          input.Command,
		WorkingDirectory: input.WorkingDirectory,
	})
	if run == nil {
		return mcpErrorFor(output), output, nil
	}
	return nil, s.runningReceipt(run, stats, false), nil
}

func (s *Server) startShellRun(
	ctx context.Context,
	input runShellCommandInput,
) (*executor.Run, *runstats.Stats, runTaskOutput) {
	go s.cleanup()
	workingDirectory := canonicalWorkingDirectory(input.WorkingDirectory)
	s.repairTerminalMetadata()
	stats, statsErr := s.stats.For(workingDirectory, "shell:command", []string{input.Command})
	if statsErr != nil {
		s.config.Logger.Warn("read shell duration statistics failed", "error", statsErr)
	}
	handle, err := s.store.Begin(
		runstore.Meta{
			ProjectPath: workingDirectory,
			TaskID:      "shell:command",
			Args:        []string{input.Command},
		},
	)
	if err != nil {
		return nil, stats, runTaskOutput{Error: newToolError(err)}
	}
	s.configureTaskTimeout(&handle.Meta)
	dir, err := s.workspace.ResolveDir(workingDirectory)
	if err != nil {
		return nil, stats, runTaskOutput{Result: s.reject(handle, err)}
	}
	handle.Meta.Runner = "shell"
	handle.Meta.CWD = dir
	if persistErr := handle.PersistRunning(); persistErr != nil {
		return nil, stats, runTaskOutput{Result: s.reject(handle, persistErr)}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, stats, runTaskOutput{Result: s.cancel(handle, ctxErr)}
	}
	cmd, err := shellCommand(dir, input.Command)
	if err != nil {
		return nil, stats, runTaskOutput{Result: s.reject(handle, err)}
	}
	return s.startRun(handle, cmd, stats)
}

func (s *Server) configureTaskTimeout(meta *runstore.Meta) {
	if s.config.TimeoutUnlimited {
		return
	}
	timeout := s.config.Timeout.Milliseconds()
	meta.TaskTimeoutMS = &timeout
}

func (s *Server) startRun(
	handle *runstore.Handle,
	cmd *exec.Cmd,
	stats *runstats.Stats,
) (*executor.Run, *runstats.Stats, runTaskOutput) {
	if err := s.manager.Reserve(handle.Meta.RunID); err != nil {
		return nil, stats, runTaskOutput{Result: s.reject(handle, err)}
	}
	reserved := true
	defer func() {
		if reserved {
			s.manager.Release(handle.Meta.RunID)
		}
	}()
	run, startErr := executor.Start(
		cmd,
		handle,
		executor.Config{
			Timeout:          s.config.Timeout,
			TimeoutUnlimited: s.config.TimeoutUnlimited,
			Grace:            s.config.Grace,
		},
	)
	if run == nil {
		return nil, stats, runTaskOutput{Error: newToolError(startErr)}
	}
	if startErr != nil {
		if manageErr := s.manager.Start(run); manageErr != nil {
			s.config.Logger.Warn("track failed task run failed", "error", manageErr)
		}
		reserved = false
		s.config.Logger.Error(
			"start task process failed",
			"run_id",
			run.Snapshot().RunID,
			"error",
			startErr,
		)
		return nil, stats, runTaskOutput{Result: run.Snapshot()}
	}
	if err := s.manager.Start(run); err != nil {
		if stopErr := run.StopWithReason(err.Error()); stopErr != nil {
			s.config.Logger.Warn("reject excess asynchronous run failed", "error", stopErr)
		}
		result := run.Snapshot()
		result.Message = err.Error()
		return nil, stats, runTaskOutput{Result: result}
	}
	reserved = false
	return run, stats, runTaskOutput{}
}

type syncWait struct {
	duration        time.Duration
	untilCompletion bool
}

func (s *Server) syncWaitDuration(value *int64) (syncWait, error) {
	if value == nil {
		return syncWait{duration: s.config.SyncDeadline}, nil
	}
	if *value == -1 {
		return syncWait{untilCompletion: true}, nil
	}
	if *value < -1 {
		return syncWait{}, fmt.Errorf("max_wait_ms must be -1 or greater")
	}
	if *value > (1<<63-1)/int64(time.Millisecond) {
		return syncWait{}, fmt.Errorf("max_wait_ms is too large")
	}
	return syncWait{duration: time.Duration(*value) * time.Millisecond}, nil
}

// Request cancellation only owns a run until this handler returns. A promoted run is server-owned afterwards.
func (s *Server) waitForSyncReceipt(
	ctx context.Context,
	request *mcp.CallToolRequest,
	run *executor.Run,
	stats *runstats.Stats,
	wait syncWait,
) runTaskOutput {
	stopProgress := s.progressReporter(ctx, request, run, stats)
	defer stopProgress()
	if !wait.untilCompletion && wait.duration == 0 {
		return s.runningReceipt(run, stats, true)
	}
	var timeout <-chan time.Time
	var timer *time.Timer
	if !wait.untilCompletion {
		timer = time.NewTimer(wait.duration)
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case <-run.Done():
		if err := run.Err(); err != nil {
			s.config.Logger.Error("task ledger finalization failed", "run_id", run.Snapshot().RunID, "error", err)
		}
		s.stats.Invalidate()
		return finishedReceipt(run.Snapshot(), stats)
	case <-ctx.Done():
		if err := run.Stop(); err != nil {
			s.config.Logger.Error("cancel task run failed", "run_id", run.Snapshot().RunID, "error", err)
		}
		s.stats.Invalidate()
		return finishedReceipt(run.Snapshot(), stats)
	case <-timeout:
		return s.runningReceipt(run, stats, true)
	}
}

// finishedReceipt keeps the completed synchronous receipt shape stable and only
// adds the pre-run duration statistics, so a caller can compare this run with
// its own history without paging any other tool.
func finishedReceipt(result executor.Result, stats *runstats.Stats) runTaskOutput {
	if stats == nil {
		return runTaskOutput{Result: result}
	}
	return runTaskOutput{Result: result, runDetails: &runDetails{Stats: stats}}
}

func (s *Server) runningReceipt(
	run *executor.Run,
	stats *runstats.Stats,
	promoted bool,
) runTaskOutput {
	result := run.Snapshot()
	details, err := s.detailsFor(result.RunID, stats)
	if err != nil {
		s.config.Logger.Warn("read running task status failed", "run_id", result.RunID, "error", err)
		return runTaskOutput{Result: result}
	}
	if result.Status == runstore.StatusRunning {
		details.Promoted = promoted
		if promoted {
			result.Message = "Still running as a background run; poll get_run_status or call wait_run with this run_id."
		}
		if stdout, tailErr := s.store.ReadLogTail(result.RunID, "stdout", 4096); tailErr == nil {
			result.StdoutTail = string(stdout)
		}
		if stderr, tailErr := s.store.ReadLogTail(result.RunID, "stderr", 4096); tailErr == nil {
			result.StderrTail = string(stderr)
		}
	}
	return runTaskOutput{Result: result, runDetails: details}
}

func (s *Server) progressReporter(
	ctx context.Context,
	request *mcp.CallToolRequest,
	run *executor.Run,
	stats *runstats.Stats,
) func() {
	if request == nil || request.Session == nil || request.Params.GetProgressToken() == nil {
		return func() {}
	}
	token := request.Params.GetProgressToken()
	report := func() {
		result := run.Snapshot()
		details, err := s.detailsFor(result.RunID, stats)
		if err != nil {
			s.config.Logger.Debug("read task progress state failed", "run_id", result.RunID, "error", err)
			return
		}
		total := float64(0)
		if details.Stats != nil &&
			details.Stats.Exact != nil &&
			details.Stats.Exact.AvgDurationMS != nil {
			total = float64(*details.Stats.Exact.AvgDurationMS) / 1000
		}
		if err := request.Session.NotifyProgress(
			ctx,
			&mcp.ProgressNotificationParams{
				ProgressToken: token,
				Message: fmt.Sprintf(
					"run_id=%s elapsed=%ds last_output_age_ms=%d stdout_bytes=%d stderr_bytes=%d",
					result.RunID,
					result.DurationMS/1000,
					details.LastOutputAgeMS,
					details.StdoutBytes,
					details.StderrBytes,
				),
				Progress: float64(result.DurationMS) / 1000,
				Total:    total,
			},
		); err != nil {
			s.config.Logger.Debug("send task progress notification failed", "run_id", result.RunID, "error", err)
		}
	}
	report()
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				report()
			}
		}
	}()
	return func() { once.Do(func() { close(stop) }) }
}

func (s *Server) cancel(handle *runstore.Handle, reason error) executor.Result {
	//nolint:errcheck // The compact cancellation receipt is still returned if ledger finalization fails.
	_ = handle.Finish(runstore.StatusCancelled, -1, reason.Error(), false, false)
	return executor.Result{
		RunID:      handle.Meta.RunID,
		ExitCode:   -1,
		DurationMS: handle.Meta.DurationMS,
		Message:    "Task cancelled",
		Status:     runstore.StatusCancelled,
		LogsReady:  true,
	}
}

func shellCommand(dir, command string) (*exec.Cmd, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command must not be empty")
	}
	if runtime.GOOS == "windows" {
		shell := os.Getenv("ComSpec")
		if shell == "" {
			shell = "cmd.exe"
		}
		// #nosec G702 -- command text is intentionally interpreted by the requested shell tool.
		cmd := exec.CommandContext(context.Background(), shell, "/D", "/S", "/C", command)
		cmd.Dir = dir
		return cmd, nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	// #nosec G702 -- command text is intentionally interpreted by the requested shell tool.
	cmd := exec.CommandContext(context.Background(), shell, "-c", command)
	cmd.Dir = dir
	return cmd, nil
}

func (s *Server) reject(handle *runstore.Handle, reason error) executor.Result {
	//nolint:errcheck // The compact error receipt is still returned if ledger finalization fails.
	_ = handle.Finish(runstore.StatusSpawnError, -1, reason.Error(), false, false)
	return executor.Result{
		RunID:      handle.Meta.RunID,
		OK:         false,
		ExitCode:   -1,
		DurationMS: handle.Meta.DurationMS,
		Message:    reason.Error(),
		Status:     runstore.StatusSpawnError,
		LogsReady:  true,
	}
}

func taskByID(tasks []runner.Task, wanted string) (runner.Task, bool) {
	for _, task := range tasks {
		if task.ID == wanted {
			return task, true
		}
	}
	return runner.Task{}, false
}

type getRunInput struct {
	RunID string `json:"run_id" jsonschema:"run ID returned by run_task"`
}

//nolint:govet // Field order follows the stable MCP JSON response shape.
type getRunOutput struct {
	Run   runstore.Meta `json:"run"`
	Error *toolError    `json:"error,omitempty"`
}

func (s *Server) getRun(
	_ context.Context,
	_ *mcp.CallToolRequest,
	input getRunInput,
) (*mcp.CallToolResult, getRunOutput, error) {
	meta, _, err := s.metaFor(input.RunID)
	if err != nil {
		return toolErrorResult(err), getRunOutput{Error: newToolError(err)}, nil
	}
	return nil, getRunOutput{Run: meta}, nil
}

//nolint:govet // Field order follows the stable MCP JSON request shape.
type getRunLogsInput struct {
	RunID    string `json:"run_id" jsonschema:"run ID returned by run_task"`
	Stream   string `json:"stream" jsonschema:"stdout or stderr"`
	Offset   int64  `json:"offset,omitempty" jsonschema:"raw byte offset, default zero"`
	Limit    int64  `json:"limit,omitempty" jsonschema:"maximum raw bytes, default 65536"`
	Encoding string `json:"encoding,omitempty" jsonschema:"utf8 or base64, default utf8"`
}

//nolint:govet // Field order follows the stable MCP JSON response shape.
type getRunLogsOutput struct {
	RunID      string     `json:"run_id"`
	Stream     string     `json:"stream"`
	Offset     int64      `json:"offset"`
	NextOffset int64      `json:"next_offset"`
	Encoding   string     `json:"encoding"`
	Data       string     `json:"data"`
	Error      *toolError `json:"error,omitempty"`
}

func (s *Server) getRunLogs(
	_ context.Context,
	_ *mcp.CallToolRequest,
	input getRunLogsInput,
) (*mcp.CallToolResult, getRunLogsOutput, error) {
	encoding := input.Encoding
	if encoding == "" {
		encoding = "utf8"
	}
	if encoding != "utf8" && encoding != "base64" {
		err := fmt.Errorf("encoding must be utf8 or base64")
		return toolErrorResult(err), getRunLogsOutput{Error: newToolError(err)}, nil
	}
	data, err := s.store.ReadLog(input.RunID, input.Stream, input.Offset, input.Limit)
	if err != nil {
		return toolErrorResult(err), getRunLogsOutput{Error: newToolError(err)}, nil
	}
	if encoding == "utf8" {
		data, err = validUTF8Page(data)
		if err != nil {
			return toolErrorResult(err), getRunLogsOutput{Error: newToolError(err)}, nil
		}
	}
	output := getRunLogsOutput{
		RunID:      input.RunID,
		Stream:     input.Stream,
		Offset:     input.Offset,
		NextOffset: input.Offset + int64(len(data)),
		Encoding:   encoding,
	}
	if encoding == "base64" {
		output.Data = base64.StdEncoding.EncodeToString(data)
	} else {
		output.Data = string(data)
	}
	return nil, output, nil
}

//nolint:govet // Field order follows the MCP request shape.
type getRunStatusInput struct {
	RunID     string `json:"run_id" jsonschema:"run ID returned by a task tool"`
	TailBytes *int64 `json:"tail_bytes,omitempty" jsonschema:"bytes from each log tail, default 4096, zero disables tails"`
}

//nolint:govet // Field order follows the MCP request shape.
type waitRunInput struct {
	RunID     string `json:"run_id" jsonschema:"run ID returned by a task tool"`
	MaxWaitMS *int64 `json:"max_wait_ms,omitempty" jsonschema:"wait duration in milliseconds, default 30000, maximum 600000"`
	TimeoutMS *int64 `json:"timeout_ms,omitempty" jsonschema:"deprecated alias for max_wait_ms"`
	TailBytes *int64 `json:"tail_bytes,omitempty" jsonschema:"bytes from each log tail, default 4096, zero disables tails"`
}

//nolint:govet // Field order follows the MCP request shape.
type stopRunInput struct {
	RunID     string `json:"run_id" jsonschema:"run ID returned by a task tool"`
	TailBytes *int64 `json:"tail_bytes,omitempty" jsonschema:"bytes from each log tail, default 4096, zero disables tails"`
}

//nolint:govet // Embedded result precedes the status details by contract.
type runStatusOutput struct {
	executor.Result
	*runDetails
	Error *toolError `json:"error,omitempty"`
}

func (s *Server) getRunStatus(
	_ context.Context,
	_ *mcp.CallToolRequest,
	input getRunStatusInput,
) (*mcp.CallToolResult, runStatusOutput, error) {
	tailBytes, err := statusTailBytes(input.TailBytes)
	if err != nil {
		return toolErrorResult(err), runStatusOutput{Error: newToolError(err)}, nil
	}
	output, err := s.statusOutput(input.RunID, tailBytes, nil)
	if err != nil {
		return toolErrorResult(err), runStatusOutput{Error: newToolError(err)}, nil
	}
	return nil, output, nil
}

func (s *Server) waitRun(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input waitRunInput,
) (*mcp.CallToolResult, runStatusOutput, error) {
	tailBytes, err := statusTailBytes(input.TailBytes)
	if err != nil {
		return toolErrorResult(err), runStatusOutput{Error: newToolError(err)}, nil
	}
	wait, err := waitRunDuration(input)
	if err != nil {
		return toolErrorResult(err), runStatusOutput{Error: newToolError(err)}, nil
	}
	if run, live := s.manager.Get(input.RunID); live {
		waitCtx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		run.Wait(waitCtx)
		s.stats.Invalidate()
		output, statusErr := s.statusOutput(input.RunID, tailBytes, nil)
		if statusErr != nil {
			return toolErrorResult(statusErr), runStatusOutput{Error: newToolError(statusErr)}, nil
		}
		return nil, output, nil
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		output, statusErr := s.statusOutput(input.RunID, tailBytes, nil)
		if statusErr != nil {
			return toolErrorResult(statusErr), runStatusOutput{Error: newToolError(statusErr)}, nil
		}
		if output.Completed != nil && *output.Completed {
			return nil, output, nil
		}
		select {
		case <-ctx.Done():
			return nil, output, nil
		case <-deadline.C:
			return nil, output, nil
		case <-ticker.C:
		}
	}
}

func (s *Server) stopRun(
	_ context.Context,
	_ *mcp.CallToolRequest,
	input stopRunInput,
) (*mcp.CallToolResult, runStatusOutput, error) {
	tailBytes, err := statusTailBytes(input.TailBytes)
	if err != nil {
		return toolErrorResult(err), runStatusOutput{Error: newToolError(err)}, nil
	}
	meta, _, err := s.metaFor(input.RunID)
	if err != nil {
		return toolErrorResult(err), runStatusOutput{Error: newToolError(err)}, nil
	}
	if meta.Status != runstore.StatusRunning {
		output, statusErr := s.statusOutput(input.RunID, tailBytes, nil)
		if statusErr != nil {
			return toolErrorResult(statusErr), runStatusOutput{Error: newToolError(statusErr)}, nil
		}
		output.AlreadyFinished = true
		return nil, output, nil
	}
	run, live := s.manager.Get(input.RunID)
	if !live {
		// Either the run belongs to another server process, or it finished
		// between the two reads above. Re-read before blaming the owner.
		output, statusErr := s.statusOutput(input.RunID, tailBytes, nil)
		if statusErr != nil {
			return toolErrorResult(statusErr), runStatusOutput{Error: newToolError(statusErr)}, nil
		}
		if output.Completed != nil && *output.Completed {
			output.AlreadyFinished = true
			return nil, output, nil
		}
		err := fmt.Errorf(
			"run %q is owned by PID %d and cannot be stopped by this server",
			input.RunID,
			meta.OwnerPID,
		)
		return toolErrorResult(err), runStatusOutput{Error: newToolError(err)}, nil
	}
	if stopErr := run.Stop(); stopErr != nil {
		s.config.Logger.Error("stop task run failed", "run_id", input.RunID, "error", stopErr)
	}
	s.stats.Invalidate()
	output, statusErr := s.statusOutput(input.RunID, tailBytes, nil)
	if statusErr != nil {
		return toolErrorResult(statusErr), runStatusOutput{Error: newToolError(statusErr)}, nil
	}
	return nil, output, nil
}

//nolint:govet // Field order follows the MCP request shape.
type listRunsInput struct {
	Status      []string `json:"status,omitempty" jsonschema:"optional statuses: running, ok, nonzero, timeout, cancelled, spawn_error"`
	ProjectPath string   `json:"project_path,omitempty" jsonschema:"optional project path filter"`
	TaskID      string   `json:"task_id,omitempty" jsonschema:"optional task ID filter"`
	Limit       *int     `json:"limit,omitempty" jsonschema:"maximum runs, default 20, maximum 200"`
	Cursor      string   `json:"cursor,omitempty" jsonschema:"exclusive run cursor from next_cursor"`
}

//nolint:govet // Field order follows the stable MCP JSON response shape.
type runListEntry struct {
	RunID           string          `json:"run_id"`
	Status          runstore.Status `json:"status"`
	ProjectPath     string          `json:"project_path,omitempty"`
	TaskID          string          `json:"task_id,omitempty"`
	Args            []string        `json:"args,omitempty"`
	StartedAt       time.Time       `json:"started_at"`
	DurationMS      int64           `json:"duration_ms"`
	LastOutputAgeMS *int64          `json:"last_output_age_ms,omitempty"`
}

//nolint:govet // Field order follows the stable MCP response shape.
type listRunsOutput struct {
	Runs       []runListEntry `json:"runs"`
	Scanned    int            `json:"scanned"`
	Truncated  bool           `json:"truncated,omitempty"`
	NextCursor string         `json:"next_cursor,omitempty"`
	Error      *toolError     `json:"error,omitempty"`
}

// listRunsScanLimit bounds how many ledger entries one listing reads before
// applying filters. next_cursor resumes from the oldest inspected entry.
const listRunsScanLimit = 2000

//nolint:gocyclo // Each optional run-list filter has a separate validation path.
func (s *Server) listRuns(
	_ context.Context,
	_ *mcp.CallToolRequest,
	input listRunsInput,
) (*mcp.CallToolResult, listRunsOutput, error) {
	limit, err := listLimit(input.Limit)
	if err != nil {
		return toolErrorResult(err), listRunsOutput{Error: newToolError(err)}, nil
	}
	statuses := make(map[runstore.Status]struct{}, len(input.Status))
	for _, value := range input.Status {
		status := runstore.Status(value)
		if !validRunStatus(status) {
			statusErr := fmt.Errorf(
				"unknown run status %q; expected running, ok, nonzero, timeout, cancelled, or spawn_error",
				value,
			)
			return toolErrorResult(statusErr), listRunsOutput{Error: newToolError(statusErr)}, nil
		}
		statuses[status] = struct{}{}
	}
	runs, more, err := s.store.ListRecentPage(listRunsScanLimit, input.Cursor)
	if err != nil {
		return toolErrorResult(err), listRunsOutput{Error: newToolError(err)}, nil
	}
	output := listRunsOutput{
		Runs: make([]runListEntry, 0, min(limit, len(runs))),
	}
	for index, meta := range runs {
		output.Scanned = index + 1
		if current, _, metaErr := s.metaFor(meta.RunID); metaErr == nil {
			meta = current
		} else {
			s.config.Logger.Warn(
				"resolve listed run metadata failed",
				"run_id",
				meta.RunID,
				"error",
				metaErr,
			)
		}
		if len(statuses) > 0 {
			if _, found := statuses[meta.Status]; !found {
				continue
			}
		}
		if input.ProjectPath != "" && meta.ProjectPath != input.ProjectPath {
			continue
		}
		if input.TaskID != "" && meta.TaskID != input.TaskID {
			continue
		}
		entry := runListEntry{
			RunID:       meta.RunID,
			Status:      meta.Status,
			ProjectPath: meta.ProjectPath,
			TaskID:      meta.TaskID,
			Args:        meta.Args,
			StartedAt:   meta.StartedAt,
			DurationMS:  durationFor(meta),
		}
		if meta.Status == runstore.StatusRunning {
			if logState, stateErr := s.store.LogState(meta.RunID); stateErr == nil {
				last := logState.LastOutputAt
				if last.IsZero() {
					last = meta.StartedAt
				}
				age := maxDuration(time.Since(last), 0).Milliseconds()
				entry.LastOutputAgeMS = &age
			}
		}
		output.Runs = append(output.Runs, entry)
		if len(output.Runs) == limit {
			if index+1 < len(runs) || more {
				output.Truncated = true
				output.NextCursor = meta.RunID
			}
			return nil, output, nil
		}
	}
	if more && len(runs) > 0 {
		output.Truncated = true
		output.NextCursor = runs[len(runs)-1].RunID
	}
	return nil, output, nil
}

func (s *Server) statusOutput(
	runID string,
	tailBytes int64,
	predicted *runstats.Stats,
) (runStatusOutput, error) {
	meta, run, err := s.metaFor(runID)
	if err != nil {
		return runStatusOutput{}, fmt.Errorf("read run metadata: %w", err)
	}
	result := resultForMeta(meta)
	if run != nil {
		result = run.Snapshot()
	}
	result.StdoutTail = ""
	result.StderrTail = ""
	if tailBytes > 0 {
		if stdout, tailErr := s.store.ReadLogTail(runID, "stdout", tailBytes); tailErr == nil {
			result.StdoutTail = string(stdout)
		}
		if stderr, tailErr := s.store.ReadLogTail(runID, "stderr", tailBytes); tailErr == nil {
			result.StderrTail = string(stderr)
		}
	}
	details, err := s.runDetails(meta, predicted)
	if err != nil {
		return runStatusOutput{}, err
	}
	return runStatusOutput{Result: result, runDetails: details}, nil
}

func (s *Server) detailsFor(runID string, predicted *runstats.Stats) (*runDetails, error) {
	meta, _, err := s.metaFor(runID)
	if err != nil {
		return nil, fmt.Errorf("read run metadata: %w", err)
	}
	return s.runDetails(meta, predicted)
}

// metaFor prefers the local authoritative snapshot while a run is live or its
// terminal metadata is being repaired. This prevents a failed final write from
// making a completed local run appear to run forever.
func (s *Server) metaFor(runID string) (runstore.Meta, *executor.Run, error) {
	if run, live := s.manager.Get(runID); live {
		return run.Meta(), run, nil
	}
	if run, fallback := s.manager.Terminal(runID); fallback {
		if err := run.RepairFinalMetadata(); err != nil {
			s.config.Logger.Warn(
				"repair terminal task metadata failed",
				"run_id",
				runID,
				"error",
				err,
			)
		} else {
			s.manager.ReleaseTerminal(runID, run)
			s.stats.Invalidate()
		}
		return run.Meta(), run, nil
	}
	meta, err := s.store.Get(runID)
	if err != nil {
		return runstore.Meta{}, nil, fmt.Errorf("get run metadata: %w", err)
	}
	return meta, nil, nil
}

func (s *Server) repairTerminalMetadata() {
	repaired, err := s.manager.RepairTerminals()
	if repaired > 0 {
		s.stats.Invalidate()
	}
	if err != nil {
		s.config.Logger.Warn("repair terminal task metadata failed", "error", err)
	}
}

// runDetails derives the status view from one already-read metadata snapshot, so
// the receipt and its details can never describe two different ledger states.
func (s *Server) runDetails(meta runstore.Meta, predicted *runstats.Stats) (*runDetails, error) {
	runID := meta.RunID
	logState, err := s.store.LogState(runID)
	if err != nil {
		return nil, fmt.Errorf("read run log state: %w", err)
	}
	lastOutput := logState.LastOutputAt
	if lastOutput.IsZero() {
		lastOutput = meta.StartedAt
	}
	age := maxDuration(time.Since(lastOutput), 0).Milliseconds()
	// Ownership is a property of the ledger entry, not of the live run map: a
	// finished run this server started still reports true while its process
	// identity remains the same.
	owned := meta.OwnerPID == os.Getpid() &&
		runstore.ProcessMatches(meta.OwnerPID, meta.OwnerIdentity)
	completed := meta.Status != runstore.StatusRunning
	processAlive := !completed && runstore.ProcessMatches(meta.PID, meta.ProcessIdentity)
	startedAt := meta.StartedAt
	details := &runDetails{
		Completed:       boolPointer(completed),
		ProjectPath:     meta.ProjectPath,
		Runner:          meta.Runner,
		TaskID:          meta.TaskID,
		Args:            meta.Args,
		CWD:             meta.CWD,
		PID:             meta.PID,
		OwnerPID:        meta.OwnerPID,
		ProcessAlive:    boolPointer(processAlive),
		OwnedByThis:     boolPointer(owned),
		StartedAt:       &startedAt,
		LastOutputAt:    &lastOutput,
		LastOutputAgeMS: age,
		NoOutputYet:     boolPointer(logState.NoOutputYet),
		StdoutBytes:     logState.StdoutBytes,
		StderrBytes:     logState.StderrBytes,
		Stats:           predicted,
	}
	if meta.TaskTimeoutMS != nil {
		timeout := *meta.TaskTimeoutMS
		details.TaskTimeoutMS = &timeout
		details.TimeoutMS = timeout
	}
	if !meta.EndedAt.IsZero() {
		ended := meta.EndedAt
		details.EndedAt = &ended
	}
	if !completed && meta.TaskTimeoutMS != nil {
		remaining := maxDuration(
			time.Duration(*meta.TaskTimeoutMS)*time.Millisecond-time.Since(meta.StartedAt),
			0,
		).Milliseconds()
		details.TimeToTaskTimeoutMS = &remaining
		details.TimeToTimeoutMS = &remaining
	}
	if details.Stats == nil {
		s.repairTerminalMetadata()
		if stats, statsErr := s.stats.For(meta.ProjectPath, meta.TaskID, meta.Args); statsErr == nil {
			details.Stats = stats
		} else {
			s.config.Logger.Warn(
				"read task duration statistics failed",
				"task_id",
				meta.TaskID,
				"error",
				statsErr,
			)
		}
	}
	return details, nil
}

func resultForMeta(meta runstore.Meta) executor.Result {
	message := statusMessage(meta.Status)
	if meta.Status == runstore.StatusTimeout && meta.Error != "" {
		message = meta.Error
	}
	return executor.Result{
		RunID:      meta.RunID,
		OK:         meta.Status == runstore.StatusOK,
		ExitCode:   meta.ExitCode,
		DurationMS: durationFor(meta),
		Message:    message,
		Status:     meta.Status,
		LogsReady:  true,
	}
}

func durationFor(meta runstore.Meta) int64 {
	if meta.Status == runstore.StatusRunning {
		return maxDuration(time.Since(meta.StartedAt), 0).Milliseconds()
	}
	return meta.DurationMS
}

func statusMessage(status runstore.Status) string {
	switch status {
	case runstore.StatusOK:
		return "OK"
	case runstore.StatusRunning:
		return "Task is running"
	case runstore.StatusNonzero:
		return "Task exited with a non-zero status"
	case runstore.StatusTimeout:
		return "Task timed out"
	case runstore.StatusCancelled:
		return "Task cancelled"
	case runstore.StatusSpawnError:
		return "Failed to start task"
	default:
		return "Unknown task status"
	}
}

func statusTailBytes(value *int64) (int64, error) {
	if value == nil {
		return 4096, nil
	}
	if *value < 0 || *value > 65536 {
		return 0, fmt.Errorf("tail_bytes must be between 0 and 65536")
	}
	return *value, nil
}

func waitDuration(value *int64) (time.Duration, error) {
	if value == nil {
		return 30 * time.Second, nil
	}
	if *value < 0 || *value > 600000 {
		return 0, fmt.Errorf("max_wait_ms must be between 0 and 600000")
	}
	return time.Duration(*value) * time.Millisecond, nil
}

func waitRunDuration(input waitRunInput) (time.Duration, error) {
	if input.MaxWaitMS != nil && input.TimeoutMS != nil &&
		*input.MaxWaitMS != *input.TimeoutMS {
		return 0, fmt.Errorf("max_wait_ms and timeout_ms must not conflict")
	}
	value := input.MaxWaitMS
	if value == nil {
		value = input.TimeoutMS
	}
	return waitDuration(value)
}

func listLimit(value *int) (int, error) {
	if value == nil {
		return 20, nil
	}
	if *value <= 0 || *value > 200 {
		return 0, fmt.Errorf("limit must be between 1 and 200")
	}
	return *value, nil
}

func validRunStatus(status runstore.Status) bool {
	switch status {
	case runstore.StatusRunning,
		runstore.StatusOK,
		runstore.StatusNonzero,
		runstore.StatusTimeout,
		runstore.StatusCancelled,
		runstore.StatusSpawnError:
		return true
	default:
		return false
	}
}

func maxDuration(value, minimum time.Duration) time.Duration {
	if value < minimum {
		return minimum
	}
	return value
}

func boolPointer(value bool) *bool { return &value }

func canonicalWorkingDirectory(value string) string {
	if value == "" {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(value))
}

func validUTF8Page(data []byte) ([]byte, error) {
	if utf8.Valid(data) {
		return data, nil
	}
	for suffixSize := 1; suffixSize < utf8.UTFMax && suffixSize <= len(data); suffixSize++ {
		split := len(data) - suffixSize
		if utf8.Valid(data[:split]) && !utf8.FullRune(data[split:]) {
			if split == 0 {
				break
			}
			return data[:split], nil
		}
	}
	return nil, fmt.Errorf(
		"log range is not complete valid UTF-8; adjust offset or limit, or use base64",
	)
}

type toolError struct {
	Message string `json:"message"`
}

func newToolError(err error) *toolError { return &toolError{Message: err.Error()} }

func mcpErrorFor(output runTaskOutput) *mcp.CallToolResult {
	if output.Error == nil {
		return nil
	}
	return toolErrorResult(errors.New(output.Error.Message))
}

func toolErrorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			&mcp.TextContent{Text: err.Error()},
		},
	}
}

func (s *Server) cleanup() {
	if err := s.store.Cleanup(s.config.Retention); err != nil {
		s.config.Logger.Warn("run log cleanup failed", "error", err)
	}
}

func withUpdateNotification[In, Out any](
	s *Server,
	handler mcp.ToolHandlerFor[In, Out],
) mcp.ToolHandlerFor[In, Out] {
	return func(
		ctx context.Context,
		request *mcp.CallToolRequest,
		input In,
	) (*mcp.CallToolResult, Out, error) {
		s.updates.Observe()
		result, output, err := handler(ctx, request, input)
		if err != nil {
			return result, output, err
		}
		notification := s.updates.Notification()
		if notification == nil {
			return result, output, nil
		}
		if result == nil {
			data, marshalErr := json.Marshal(output)
			if marshalErr != nil {
				s.config.Logger.Warn("marshal primary MCP result for update notice failed", "error", marshalErr)
				return nil, output, nil
			}
			result = &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: string(data)},
				},
			}
		}
		result.Content = append(
			result.Content,
			&mcp.TextContent{Text: notification.Message()},
		)
		return result, output, nil
	}
}

func recoverTool[In, Out any](handler mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(
		ctx context.Context,
		request *mcp.CallToolRequest,
		input In,
	) (result *mcp.CallToolResult, output Out, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				result = &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{
						&mcp.TextContent{Text: "internal server error"},
					},
				}
				err = nil
			}
		}()
		return handler(ctx, request, input)
	}
}
