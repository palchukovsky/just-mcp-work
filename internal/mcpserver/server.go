// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package mcpserver exposes workspace tasks through the local STDIO MCP transport.
package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/palchukovsky/just-mcp-work/internal/executor"
	"github.com/palchukovsky/just-mcp-work/internal/runner"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
	"github.com/palchukovsky/just-mcp-work/internal/updatecheck"
	"github.com/palchukovsky/just-mcp-work/internal/version"
	"github.com/palchukovsky/just-mcp-work/internal/workspace"
)

// Config controls server-side execution defaults.
//
//nolint:govet // Field order groups process settings before the logger dependency.
type Config struct {
	Timeout   time.Duration
	Retention time.Duration
	Logger    *slog.Logger
	Updates   *updatecheck.Checker
}

// Server owns MCP handlers and their workspace dependencies.
type Server struct {
	workspace *workspace.Registry
	runners   *runner.Registry
	store     *runstore.Store
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
	if config.Timeout <= 0 {
		config.Timeout = 15 * time.Minute
	}
	if config.Retention <= 0 {
		config.Retention = 72 * time.Hour
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
	return &Server{
		workspace: workspaceRegistry,
		runners:   runners,
		store:     store,
		updates:   config.Updates,
		config:    config,
	}, nil
}

// Run serves only STDIO MCP traffic. Logs are handled by the configured logger.
func (s *Server) Run(ctx context.Context) error {
	s.updates.Start(ctx)
	defer s.updates.Close()
	server := mcp.NewServer(
		&mcp.Implementation{Name: "just-mcp-work", Version: version.Current().Display()},
		&mcp.ServerOptions{Logger: s.config.Logger},
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        "list_projects",
			Description: "List task projects discovered below the workspace root.",
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
			Description: "Run one discovered task with separate argv arguments and " +
				"return a compact receipt.",
		},
		recoverTool(withUpdateNotification(s, s.runTask)),
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

type listProjectsInput struct{}

//nolint:govet // Field order follows the stable MCP JSON response shape.
type projectOutput struct {
	RelPath string            `json:"rel_path"`
	Runners []string          `json:"runners"`
	Status  string            `json:"status"`
	Errors  map[string]string `json:"errors,omitempty"`
}

//nolint:govet // Field order follows the stable MCP JSON response shape.
type listProjectsOutput struct {
	Projects []projectOutput `json:"projects"`
	Error    *toolError      `json:"error,omitempty"`
}

func (s *Server) listProjects(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	_ listProjectsInput,
) (*mcp.CallToolResult, listProjectsOutput, error) {
	projects, err := s.workspace.Discover(ctx)
	if err != nil {
		return toolErrorResult(err), listProjectsOutput{Error: newToolError(err)}, nil
	}
	output := listProjectsOutput{Projects: make([]projectOutput, 0, len(projects))}
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

type listTasksInput struct {
	ProjectPath string `json:"project_path" jsonschema:"relative path returned by list_projects"`
	Runner      string `json:"runner,omitempty" jsonschema:"optional runner name to filter"`
}

//nolint:govet // Field order follows the stable MCP JSON response shape.
type listTasksOutput struct {
	Tasks []runner.Task `json:"tasks"`
	Error *toolError    `json:"error,omitempty"`
}

func (s *Server) listTasks(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input listTasksInput,
) (*mcp.CallToolResult, listTasksOutput, error) {
	project, err := s.workspace.Find(ctx, input.ProjectPath)
	if err != nil {
		return toolErrorResult(err), listTasksOutput{Error: newToolError(err)}, nil
	}
	if input.Runner != "" {
		return nil,
			listTasksOutput{Tasks: append([]runner.Task(nil), project.Tasks[input.Runner]...)},
			nil
	}
	result := listTasksOutput{}
	for _, name := range project.Runners {
		result.Tasks = append(result.Tasks, project.Tasks[name]...)
	}
	return nil, result, nil
}

type runTaskInput struct {
	ProjectPath string   `json:"project_path" jsonschema:"relative path returned by list_projects"`
	TaskID      string   `json:"task_id" jsonschema:"task ID returned by list_tasks"`
	Arguments   []string `json:"arguments,omitempty" jsonschema:"arguments passed to the selected task"`
}

//nolint:govet // Embedded result precedes the structured MCP error by contract.
type runTaskOutput struct {
	executor.Result
	Error *toolError `json:"error,omitempty"`
}

func (s *Server) runTask(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input runTaskInput,
) (*mcp.CallToolResult, runTaskOutput, error) {
	go s.cleanup()
	handle, err := s.store.Begin(
		runstore.Meta{ProjectPath: input.ProjectPath, TaskID: input.TaskID, Args: input.Arguments},
	)
	if err != nil {
		return toolErrorResult(err), runTaskOutput{Error: newToolError(err)}, nil
	}
	project, err := s.workspace.Find(ctx, input.ProjectPath)
	if err != nil {
		return nil, runTaskOutput{Result: s.reject(handle, err)}, nil
	}
	runnerName, _, found := strings.Cut(input.TaskID, ":")
	if !found {
		return nil,
			runTaskOutput{
				Result: s.reject(handle, fmt.Errorf("task_id must be namespaced as <runner>:<task>")),
			},
			nil
	}
	candidate, ok := s.runners.Get(runnerName)
	if !ok {
		return nil,
			runTaskOutput{Result: s.reject(handle, fmt.Errorf("unknown runner %q", runnerName))},
			nil
	}
	task, ok := taskByID(project.Tasks[runnerName], input.TaskID)
	if !ok {
		return nil,
			runTaskOutput{
				Result: s.reject(
					handle,
					fmt.Errorf("unknown task_id %q for project %q", input.TaskID, input.ProjectPath),
				),
			},
			nil
	}
	handle.Meta.Runner = runnerName
	handle.Meta.CWD = project.Dir
	if versionProvider, ok := candidate.(runner.VersionProvider); ok {
		if runnerVersion, versionErr := versionProvider.RunnerVersion(ctx); versionErr == nil {
			handle.Meta.RunnerVersion = runnerVersion
		}
	}
	if persistErr := handle.PersistRunning(); persistErr != nil {
		return nil, runTaskOutput{Result: s.reject(handle, persistErr)}, nil
	}
	cmd, err := candidate.BuildCommand(context.Background(), project.Dir, task, input.Arguments)
	if err != nil {
		return nil, runTaskOutput{Result: s.reject(handle, err)}, nil
	}
	result, executeErr := executor.Execute(
		ctx,
		cmd,
		handle,
		executor.Config{Timeout: s.config.Timeout},
	)
	if executeErr != nil {
		s.config.Logger.Error(
			"task ledger finalization failed",
			"run_id",
			result.RunID,
			"error",
			executeErr,
		)
	}
	return nil, runTaskOutput{Result: result}, nil
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
	meta, err := s.store.Get(input.RunID)
	if err != nil {
		return toolErrorResult(err), getRunOutput{Error: newToolError(err)}, nil
	}
	return nil, getRunOutput{Run: meta}, nil
}

//nolint:govet // Field order follows the stable MCP JSON request shape.
type getRunLogsInput struct {
	RunID    string `json:"run_id" jsonschema:"run ID returned by run_task"`
	Stream   string `json:"stream" jsonschema:"stdout or stderr"`
	Offset   int64  `json:"offset,omitempty" jsonschema:"byte offset, default zero"`
	Limit    int64  `json:"limit,omitempty" jsonschema:"maximum bytes, default 65536"`
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

type toolError struct {
	Message string `json:"message"`
}

func newToolError(err error) *toolError { return &toolError{Message: err.Error()} }

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
