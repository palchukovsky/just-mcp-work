// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package runner defines task backends used by the workspace registry.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
)

// ErrToolUnavailable reports that the build tool of a runner is missing on this
// host. A repository that merely carries a build file for a tool the machine
// does not have is not a broken project, so the workspace keeps such a runner
// visible as a warning instead of failing the project.
var ErrToolUnavailable = errors.New("runner tool is unavailable")

// MarkMissingTool reports err as ErrToolUnavailable when a command failed
// because its binary does not exist on this host. Every other failure is
// returned unchanged: a tool that is installed but broken is a real error, and
// hiding it behind a warning would leave the project silently taskless.
func MarkMissingTool(binary string, err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, exec.ErrNotFound) && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return fmt.Errorf("find the %s binary: %w: %w", binary, ErrToolUnavailable, err)
}

// ParamKind describes how a task parameter accepts values.
type ParamKind string

const (
	ParamSingular ParamKind = "singular"
	ParamPlus     ParamKind = "plus"
	ParamStar     ParamKind = "star"
)

// Param is a runner-neutral task parameter.
type Param struct {
	Name    string    `json:"name"`
	Kind    ParamKind `json:"kind"`
	Default *string   `json:"default,omitempty"`
	Doc     string    `json:"doc,omitempty"`
}

// Task is a task exposed by a runner.
//
//nolint:govet // Field order follows the stable MCP task response shape.
type Task struct {
	ID          string         `json:"task_id"`
	Runner      string         `json:"runner"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Params      []Param        `json:"parameters,omitempty"`
	Private     bool           `json:"private"`
	Meta        map[string]any `json:"metadata,omitempty"`
}

// Runner discovers and runs tasks for one build-tool format.
// Implementations must set Cmd.Dir and must not interpret task bodies.
//
// ListTasks may return the tasks it did discover together with an error that
// describes the part of the discovery that failed, so one unusable task file
// does not hide the tasks of the same runner that are perfectly usable.
type Runner interface {
	Name() string
	Detect(projectDir string) (bool, error)
	ListTasks(ctx context.Context, projectDir string) ([]Task, error)
	BuildCommand(ctx context.Context, projectDir string, task Task, args []string) (*exec.Cmd, error)
}

// VersionProvider is an optional runner capability used for run metadata.
// It is deliberately separate from Runner so new backends need only implement
// task discovery and execution to join the MCP API.
type VersionProvider interface {
	RunnerVersion(ctx context.Context) (string, error)
}

// IncludedProjectProvider reports directories whose task definitions are already
// reachable from a parent project.
type IncludedProjectProvider interface {
	IncludedProjectDirs(ctx context.Context, projectDir string) ([]string, error)
}
