// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package gorunner implements fixed Go module commands.
package gorunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

const (
	runnerName = "go"
	moduleFile = "go.mod"
)

type taskSpec struct {
	id          string
	name        string
	description string
	argv        []string
	modes       []runner.Mode
	arbitrary   bool
}

func taskSpecs() []taskSpec {
	return []taskSpec{
		{
			id:          "build",
			name:        "build",
			description: "Build every package in the Go module.",
			argv:        []string{"build", "./..."},
			modes:       []runner.Mode{runner.ModeSafe, runner.ModeAll},
		},
		{
			id:          "test",
			name:        "test",
			description: "Test every package in the Go module.",
			argv:        []string{"test", "./..."},
			modes:       []runner.Mode{runner.ModeSafe, runner.ModeAll},
		},
		{
			id:          "vet",
			name:        "vet",
			description: "Run the Go analyzer on every package in the module.",
			argv:        []string{"vet", "./..."},
			modes:       []runner.Mode{runner.ModeSafe, runner.ModeAll},
		},
		{
			id:          "mod:download",
			name:        "mod download",
			description: "Download dependencies declared by the Go module.",
			argv:        []string{"mod", "download"},
			modes:       []runner.Mode{runner.ModeSafe, runner.ModeAll},
		},
		{
			id:          "fmt",
			name:        "fmt",
			description: "Format every package in the Go module.",
			argv:        []string{"fmt", "./..."},
			modes:       []runner.Mode{runner.ModeAll},
		},
		{
			id:          "mod:tidy",
			name:        "mod tidy",
			description: "Add missing and remove unused Go module requirements.",
			argv:        []string{"mod", "tidy"},
			modes:       []runner.Mode{runner.ModeAll},
		},
		{
			id:          "any",
			name:        "any",
			description: "Run arbitrary Go argv exactly as supplied; this is unrestricted and risky.",
			modes:       []runner.Mode{runner.ModeAll},
			arbitrary:   true,
		},
	}
}

// Runner executes fixed commands for a Go module.
type Runner struct {
	binary string
	mode   runner.Mode
	specs  []taskSpec
}

// New constructs a safe Go runner. An empty binary uses "go" from PATH.
func New(binary string) *Runner {
	return newRunner(binary, runner.ModeSafe, taskSpecs())
}

func newRunner(binary string, mode runner.Mode, specs []taskSpec) *Runner {
	if binary == "" {
		binary = runnerName
	}
	return &Runner{binary: binary, mode: mode, specs: cloneTaskSpecs(specs)}
}

// Registration declares the reviewed safe, all, and disabled Go policies.
func Registration(binary string) runner.Registration {
	return registration(binary, taskSpecs())
}

func registration(binary string, specs []taskSpec) runner.Registration {
	permissions := runner.ReviewedPermissions(
		"Choose Go command access.",
		"The Go runner has a reviewed, table-driven command surface.",
		runner.ModeSafe,
		runner.PermissionChoice{
			Mode:  runner.ModeSafe,
			Label: "Reduced access (safe default)",
			Description: "Allow only go build ./..., go test ./..., go vet ./..., and " +
				"go mod download. These fixed tasks reject all caller arguments.",
			Warning: "Runner modes reduce exposed commands; they are not a sandbox. go test " +
				"executes code from the checkout, Go may run toolchains and helper programs, and " +
				"go mod download may use the network and write the module cache.",
		},
		runner.PermissionChoice{
			Mode:        runner.ModeAll,
			Label:       "All commands",
			Description: "Add go fmt ./..., go mod tidy, and go:any to the safe commands.",
			Warning: "Includes every safe-mode risk and also exposes go:any, which forwards " +
				"arbitrary Go argv; exec and tool hooks can execute external programs as your user.",
		},
		runner.PermissionChoice{
			Mode:        runner.ModeDisabled,
			Label:       "Disabled",
			Description: "Do not expose the Go runner or any Go tasks.",
		},
	)
	return runner.NewRegistration(
		runnerName,
		permissions,
		func(mode runner.Mode) (runner.Runner, error) {
			if err := validateTaskSpecs(specs); err != nil {
				return nil, err
			}
			return newRunner(binary, mode, specs), nil
		},
	)
}

// Name returns the stable runner name.
func (*Runner) Name() string { return runnerName }

// RunnerVersion reports the installed Go version for run metadata.
func (r *Runner) RunnerVersion(ctx context.Context) (string, error) {
	// #nosec G204 -- binary is configured locally, never supplied over MCP.
	output, err := exec.CommandContext(ctx, r.binary, "version").Output()
	if err != nil {
		return "", fmt.Errorf("get Go version: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Detect reports whether projectDir contains a regular Go module file.
func (*Runner) Detect(projectDir string) (bool, error) {
	_, err := findModule(projectDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// ListTasks returns the fixed commands authorized by the selected mode.
func (r *Runner) ListTasks(_ context.Context, projectDir string) ([]runner.Task, error) {
	if _, err := findModule(projectDir); err != nil {
		return nil, err
	}
	if _, err := exec.LookPath(r.binary); err != nil {
		return nil, fmt.Errorf(
			"locate Go executable %q: %w",
			r.binary,
			runner.MarkMissingTool(r.binary, err),
		)
	}
	tasks := make([]runner.Task, 0, len(r.specs))
	for _, spec := range r.specs {
		if slices.Contains(spec.modes, r.mode) {
			tasks = append(tasks, canonicalTask(spec))
		}
	}
	return tasks, nil
}

// BuildCommand creates an argv-only invocation of a fixed Go module command.
func (r *Runner) BuildCommand(
	ctx context.Context,
	projectDir string,
	task runner.Task,
	args []string,
) (*exec.Cmd, error) {
	argv, err := r.commandArgs(task, args)
	if err != nil {
		return nil, err
	}
	if _, err := findModule(projectDir); err != nil {
		return nil, err
	}
	// #nosec G204 -- argv is either fixed table metadata or explicitly authorized
	// all-mode go:any input; every argument remains separate and no shell is used.
	cmd := exec.CommandContext(ctx, r.binary, argv...)
	cmd.Dir = projectDir
	return cmd, nil
}

// ValidateTaskInput performs the side-effect-free authorization check used by
// the MCP server before it collects runner metadata or starts any process.
func (r *Runner) ValidateTaskInput(task runner.Task, args []string) error {
	_, err := r.commandArgs(task, args)
	return err
}

func (r *Runner) commandArgs(task runner.Task, args []string) ([]string, error) {
	spec, err := r.taskSpecFor(task)
	if err != nil {
		return nil, err
	}
	if spec.arbitrary {
		if len(args) == 0 {
			return nil, fmt.Errorf("task %q requires non-empty Go argv", task.ID)
		}
		return slices.Clone(args), nil
	}
	if len(args) != 0 {
		return nil, fmt.Errorf("task %q does not accept arguments", task.ID)
	}
	return slices.Clone(spec.argv), nil
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
		if !slices.Contains(spec.modes, r.mode) {
			return taskSpec{}, fmt.Errorf("task %q is not authorized in Go mode %q", task.ID, r.mode)
		}
		if !reflect.DeepEqual(task, canonicalTask(spec)) {
			return taskSpec{}, fmt.Errorf("task %q has invalid Go metadata", task.ID)
		}
		return spec, nil
	}
	return taskSpec{}, fmt.Errorf("task %q has an unsupported Go command", task.ID)
}

func canonicalTask(spec taskSpec) runner.Task {
	metadata := map[string]any{"command": strings.Join(spec.argv, " ")}
	if spec.arbitrary {
		metadata = map[string]any{
			"arguments": "forwarded exactly as supplied",
			"risk":      "unrestricted arbitrary Go command execution",
		}
	}
	return runner.Task{
		ID:          runnerName + ":" + spec.id,
		Runner:      runnerName,
		Name:        spec.name,
		Description: spec.description,
		Meta:        metadata,
	}
}

func validateTaskSpecs(specs []taskSpec) error {
	if len(specs) == 0 {
		return fmt.Errorf("go command table must not be empty")
	}
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if err := validateTaskIdentity(spec, seen); err != nil {
			return err
		}
		modes, err := validateTaskModes(spec)
		if err != nil {
			return err
		}
		if err := validateTaskCommand(spec, modes); err != nil {
			return err
		}
	}
	return nil
}

func validateTaskIdentity(spec taskSpec, seen map[string]struct{}) error {
	if spec.id == "" || spec.name == "" || spec.description == "" {
		return fmt.Errorf("go command table contains an incomplete task")
	}
	if _, duplicate := seen[spec.id]; duplicate {
		return fmt.Errorf("go command table contains duplicate task %q", spec.id)
	}
	seen[spec.id] = struct{}{}
	return nil
}

func validateTaskModes(spec taskSpec) (map[runner.Mode]struct{}, error) {
	if len(spec.modes) == 0 {
		return nil, fmt.Errorf("go task %q has no modes", spec.id)
	}
	modes := make(map[runner.Mode]struct{}, len(spec.modes))
	for _, mode := range spec.modes {
		if mode != runner.ModeSafe && mode != runner.ModeAll {
			return nil, fmt.Errorf("go task %q has invalid mode %q", spec.id, mode)
		}
		if _, duplicate := modes[mode]; duplicate {
			return nil, fmt.Errorf("go task %q has duplicate mode %q", spec.id, mode)
		}
		modes[mode] = struct{}{}
	}
	if _, safe := modes[runner.ModeSafe]; safe {
		if _, all := modes[runner.ModeAll]; !all {
			return nil, fmt.Errorf("safe Go task %q must also be available in all mode", spec.id)
		}
	}
	return modes, nil
}

func validateTaskCommand(spec taskSpec, modes map[runner.Mode]struct{}) error {
	if spec.arbitrary {
		if len(spec.argv) != 0 {
			return fmt.Errorf("arbitrary Go task %q must not have fixed argv", spec.id)
		}
		if _, all := modes[runner.ModeAll]; len(modes) != 1 || !all {
			return fmt.Errorf("arbitrary Go task %q must be available only in all mode", spec.id)
		}
		return nil
	}
	if len(spec.argv) == 0 {
		return fmt.Errorf("fixed Go task %q must have argv", spec.id)
	}
	return nil
}

func cloneTaskSpecs(specs []taskSpec) []taskSpec {
	cloned := make([]taskSpec, len(specs))
	for index, spec := range specs {
		cloned[index] = spec
		cloned[index].argv = slices.Clone(spec.argv)
		cloned[index].modes = slices.Clone(spec.modes)
	}
	return cloned
}

func findModule(projectDir string) (string, error) {
	module, err := runner.FindRegularFile(projectDir, moduleFile)
	if err != nil {
		return "", fmt.Errorf("find %s in %q: %w", moduleFile, projectDir, err)
	}
	return module, nil
}
