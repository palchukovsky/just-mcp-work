// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package agentrunner exposes fixed tasks for supported CLI coding agents.
package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

const (
	runnerName = "agent"
	gitPath    = ".git"
)

type taskSpec struct {
	id               string
	binary           string
	stateDir         string
	stateDirEnv      string
	name             string
	description      string
	argv             []string
	scopedArgvPrefix []string
	params           []taskParam
}

type taskParam struct {
	runner.Param
	argv          []string
	allowedValues []string
}

// Runner launches the fixed task table for CLI coding agents.
type Runner struct {
	specs []taskSpec
}

// Registration returns the reviewed registration for CLI coding agents.
// Empty binary values use the respective default command names.
func Registration(codexBinary, claudeBinary string) runner.Registration {
	return registration(taskSpecs(codexBinary, claudeBinary))
}

func taskSpecs(codexBinary, claudeBinary string) []taskSpec {
	if codexBinary == "" {
		codexBinary = "codex"
	}
	if claudeBinary == "" {
		claudeBinary = "claude"
	}
	empty := ""
	// These are CLI-wide vocabularies; individual models may support fewer levels.
	codexEfforts := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	claudeEfforts := []string{"low", "medium", "high", "xhigh", "max"}
	return []taskSpec{
		{
			id:               "codex",
			binary:           codexBinary,
			stateDir:         ".codex",
			stateDirEnv:      "CODEX_HOME",
			name:             "codex",
			description:      "Run Codex with a prompt in this checkout.",
			argv:             []string{"exec"},
			scopedArgvPrefix: []string{"--sandbox", "danger-full-access"},
			params: []taskParam{
				{Param: runner.Param{Name: "prompt", Kind: runner.ParamSingular, Doc: "task prompt"}},
				{Param: runner.Param{Name: "model", Kind: runner.ParamSingular, Default: &empty, Doc: "optional model"}, argv: []string{"-m", "<model>"}},
				{Param: runner.Param{Name: "effort", Kind: runner.ParamSingular, Default: &empty, Doc: "optional model reasoning effort"}, argv: []string{"-c", "model_reasoning_effort=<effort>"}, allowedValues: codexEfforts},
			},
		},
		{
			id:          "claude",
			binary:      claudeBinary,
			stateDir:    ".claude",
			stateDirEnv: "CLAUDE_CONFIG_DIR",
			name:        "claude",
			description: "Run Claude with a prompt in this checkout.",
			argv:        []string{"-p"},
			params: []taskParam{
				{Param: runner.Param{Name: "prompt", Kind: runner.ParamSingular, Doc: "task prompt"}},
				{Param: runner.Param{Name: "model", Kind: runner.ParamSingular, Default: &empty, Doc: "optional model"}, argv: []string{"--model", "<model>"}},
				{Param: runner.Param{Name: "effort", Kind: runner.ParamSingular, Default: &empty, Doc: "optional effort"}, argv: []string{"--effort", "<effort>"}, allowedValues: claudeEfforts},
			},
		},
	}
}

func registration(specs []taskSpec) runner.Registration {
	permissions := runner.ReviewedPermissions(
		"Choose coding-agent access.",
		"This runner launches another coding agent, which then acts with your own permissions in this checkout. JMW does not sandbox it.",
		runner.ModeSafe,
		runner.PermissionChoice{
			Mode:        runner.ModeSafe,
			Label:       "Enabled (safe default)",
			Description: "Expose only the fixed Codex and Claude prompt tasks; no extra CLI flags are accepted.",
			Warning:     "The launched coding agent can act with your permissions in this checkout; JMW does not sandbox it.",
		},
		runner.PermissionChoice{
			Mode:        runner.ModeDisabled,
			Label:       "Disabled",
			Description: "Do not expose the coding-agent runner or its tasks.",
		},
	)
	return runner.NewRegistration(
		runnerName,
		permissions,
		func(mode runner.Mode) (runner.Runner, error) {
			if mode != runner.ModeSafe {
				return nil, fmt.Errorf("unsupported agent mode %q", mode)
			}
			if err := validateTaskSpecs(specs); err != nil {
				return nil, err
			}
			return newRunner(specs), nil
		},
	)
}

func newRunner(specs []taskSpec) *Runner {
	return &Runner{specs: cloneTaskSpecs(specs)}
}

func (*Runner) Name() string {
	return runnerName
}

func (*Runner) Detect(projectDir string) (bool, error) {
	info, err := os.Lstat(filepath.Join(projectDir, gitPath))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lstat %s in %q: %w", gitPath, projectDir, err)
	}
	return info.IsDir() || info.Mode().IsRegular(), nil
}

func (r *Runner) ListTasks(_ context.Context, _ string) ([]runner.Task, error) {
	tasks := make([]runner.Task, 0, len(r.specs))
	issues := make([]error, 0, len(r.specs))
	for _, spec := range r.specs {
		if _, err := exec.LookPath(spec.binary); err != nil {
			issues = append(issues, runner.MarkMissingTool(spec.binary, err))
			continue
		}
		tasks = append(tasks, canonicalTask(spec))
	}
	return tasks, errors.Join(issues...)
}

func (r *Runner) BuildCommand(
	ctx context.Context,
	projectDir string,
	task runner.Task,
	args []string,
) (*exec.Cmd, error) {
	return r.buildCommand(ctx, projectDir, task, args, false)
}

// BuildScopedCommand builds an agent command for JMW to wrap in a write boundary.
func (r *Runner) BuildScopedCommand(
	ctx context.Context,
	projectDir string,
	task runner.Task,
	args []string,
) (*exec.Cmd, error) {
	return r.buildCommand(ctx, projectDir, task, args, true)
}

func (r *Runner) buildCommand(
	ctx context.Context,
	projectDir string,
	task runner.Task,
	args []string,
	scoped bool,
) (*exec.Cmd, error) {
	spec, argv, err := r.commandArgs(task, args)
	if err != nil {
		return nil, err
	}
	if scoped {
		argv = slices.Insert(argv, len(spec.argv), spec.scopedArgvPrefix...)
	}
	detected, err := r.Detect(projectDir)
	if err != nil {
		return nil, err
	}
	if !detected {
		return nil, fmt.Errorf("find %s in %q: %w", gitPath, projectDir, fs.ErrNotExist)
	}
	// #nosec G204 -- binary and flags come from the fixed task table; values are validated and passed as separate argv elements.
	cmd := exec.CommandContext(ctx, spec.binary, argv...)
	cmd.Dir = projectDir
	return cmd, nil
}

// TaskWriteScope reports the CLI state directory required by task.
func (r *Runner) TaskWriteScope(task runner.Task) ([]string, error) {
	spec, err := r.taskSpecFor(task)
	if err != nil {
		return nil, err
	}
	if override := os.Getenv(spec.stateDirEnv); override != "" {
		return []string{override}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home directory for task %q: %w", task.ID, err)
	}
	return []string{filepath.Join(home, spec.stateDir)}, nil
}

func (r *Runner) ValidateTaskInput(task runner.Task, args []string) error {
	_, _, err := r.commandArgs(task, args)
	return err
}

func (r *Runner) commandArgs(task runner.Task, args []string) (taskSpec, []string, error) {
	spec, err := r.taskSpecFor(task)
	if err != nil {
		return taskSpec{}, nil, err
	}
	if len(args) == 0 {
		return taskSpec{}, nil, fmt.Errorf("task %q requires a prompt argument", task.ID)
	}
	if len(args) > len(spec.params) {
		return taskSpec{}, nil, fmt.Errorf("task %q accepts at most %d arguments", task.ID, len(spec.params))
	}
	if strings.TrimSpace(args[0]) == "" {
		return taskSpec{}, nil, fmt.Errorf("task %q requires a non-empty prompt", task.ID)
	}
	for index, value := range args[1:] {
		param := spec.params[index+1]
		if value != strings.TrimSpace(value) {
			return taskSpec{}, nil, fmt.Errorf("task %q parameter %q must not have surrounding whitespace", task.ID, param.Name)
		}
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, "-") {
			return taskSpec{}, nil, fmt.Errorf("task %q parameter %q must be a value, not a flag", task.ID, param.Name)
		}
		if len(param.allowedValues) > 0 && !slices.Contains(param.allowedValues, value) {
			return taskSpec{}, nil, fmt.Errorf("task %q parameter %q must be one of %s", task.ID, param.Name, strings.Join(param.allowedValues, ", "))
		}
	}

	argv := slices.Clone(spec.argv)
	for index, param := range spec.params[1:] {
		argumentIndex := index + 1
		if len(args) <= argumentIndex || args[argumentIndex] == "" {
			continue
		}
		placeholder := "<" + param.Name + ">"
		for _, element := range param.argv {
			argv = append(argv, strings.ReplaceAll(element, placeholder, args[argumentIndex]))
		}
	}
	argv = append(argv, "--", args[0])
	return spec, argv, nil
}

func (r *Runner) taskSpecFor(task runner.Task) (taskSpec, error) {
	prefix := runnerName + ":"
	if task.Runner != runnerName || !strings.HasPrefix(task.ID, prefix) {
		return taskSpec{}, fmt.Errorf("task %q does not belong to the %s runner", task.ID, runnerName)
	}
	for _, spec := range r.specs {
		if task.ID != prefix+spec.id {
			continue
		}
		if !reflect.DeepEqual(task, canonicalTask(spec)) {
			return taskSpec{}, fmt.Errorf("task %q has invalid agent metadata", task.ID)
		}
		return spec, nil
	}
	return taskSpec{}, fmt.Errorf("task %q has an unsupported agent command", task.ID)
}

func canonicalTask(spec taskSpec) runner.Task {
	params := make([]runner.Param, len(spec.params))
	for index, param := range spec.params {
		params[index] = param.Param
		if param.Default != nil {
			value := *param.Default
			params[index].Default = &value
		}
	}
	command := slices.Clone(spec.argv)
	for _, param := range spec.params[1:] {
		command = append(command, "["+strings.Join(param.argv, " ")+"]")
	}
	command = append(command, "--", "<"+spec.params[0].Name+">")
	return runner.Task{
		ID:          runnerName + ":" + spec.id,
		Runner:      runnerName,
		Name:        spec.name,
		Description: spec.description,
		Params:      params,
		Meta:        map[string]any{"command": strings.Join(command, " ")},
	}
}

//nolint:gocyclo // Direct validation keeps the fixed task table requirements together.
func validateTaskSpecs(specs []taskSpec) error {
	if len(specs) == 0 {
		return fmt.Errorf("agent command table must not be empty")
	}
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if spec.id == "" || spec.binary == "" || spec.stateDir == "" || spec.stateDirEnv == "" ||
			spec.name == "" || spec.description == "" {
			return fmt.Errorf("agent command table contains an incomplete task")
		}
		if _, duplicate := seen[spec.id]; duplicate {
			return fmt.Errorf("agent command table contains duplicate task %q", spec.id)
		}
		seen[spec.id] = struct{}{}
		if len(spec.argv) == 0 {
			return fmt.Errorf("agent task %q has no argv", spec.id)
		}
		if len(spec.params) == 0 || spec.params[0].Name != "prompt" || spec.params[0].Default != nil {
			return fmt.Errorf("agent task %q has no required prompt parameter", spec.id)
		}
		for _, param := range spec.params[1:] {
			if len(param.argv) == 0 {
				return fmt.Errorf("agent task %q parameter %q has no renderer", spec.id, param.Name)
			}
		}
	}
	return nil
}

func cloneTaskSpecs(specs []taskSpec) []taskSpec {
	cloned := make([]taskSpec, len(specs))
	for index, spec := range specs {
		cloned[index] = spec
		cloned[index].argv = slices.Clone(spec.argv)
		cloned[index].scopedArgvPrefix = slices.Clone(spec.scopedArgvPrefix)
		cloned[index].params = make([]taskParam, len(spec.params))
		for paramIndex, param := range spec.params {
			cloned[index].params[paramIndex] = param
			cloned[index].params[paramIndex].argv = slices.Clone(param.argv)
			cloned[index].params[paramIndex].allowedValues = slices.Clone(param.allowedValues)
			if param.Default != nil {
				value := *param.Default
				cloned[index].params[paramIndex].Default = &value
			}
		}
	}
	return cloned
}
