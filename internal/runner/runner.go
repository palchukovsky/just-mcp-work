// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package runner defines task backends used by the workspace registry.
package runner

import (
	"context"
	"os/exec"
)

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
