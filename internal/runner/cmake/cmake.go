// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package cmake implements the CMake preset task runner.
package cmake

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

const (
	runnerName      = "cmake"
	cmakeListsName  = "CMakeLists.txt"
	presetsName     = "CMakePresets.json"
	userPresetsName = "CMakeUserPresets.json"
	configurePreset = "configure"
	buildPreset     = "build"
	testPreset      = "test"
	packagePreset   = "package"
	workflowPreset  = "workflow"
	ctestBinary     = "ctest"
	cpackBinary     = "cpack"
)

// Runner executes CMake tasks declared by CMake Presets.
type Runner struct {
	binary string
}

// New constructs a CMake runner. An empty binary uses "cmake" from PATH.
func New(binary string) *Runner {
	if binary == "" {
		binary = runnerName
	}
	return &Runner{binary: binary}
}

// Name returns the stable runner name.
func (*Runner) Name() string { return runnerName }

// RunnerVersion reports the installed CMake version for run metadata.
func (r *Runner) RunnerVersion(ctx context.Context) (string, error) {
	// #nosec G204 -- binary is configured locally, never supplied over MCP.
	output, err := exec.CommandContext(ctx, r.binary, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("get CMake version: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Detect reports whether a CMake project file exists in projectDir.
func (*Runner) Detect(projectDir string) (bool, error) {
	_, err := findRegularFile(projectDir, cmakeListsName)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// ListTasks lists every enabled CMake preset without configuring the project.
func (r *Runner) ListTasks(ctx context.Context, projectDir string) ([]runner.Task, error) {
	hasPresets, err := hasPresetFile(projectDir)
	if err != nil {
		return nil, err
	}
	if !hasPresets {
		return nil, nil
	}

	presets, err := r.listPresets(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	tasks := make([]runner.Task, 0, len(presets))
	for _, preset := range presets {
		tasks = append(tasks, runner.Task{
			ID:          runnerName + ":" + preset.kind + ":" + preset.name,
			Runner:      runnerName,
			Name:        preset.name,
			Description: preset.description,
			Meta: map[string]any{
				"kind":   preset.kind,
				"preset": preset.name,
			},
		})
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})
	return tasks, nil
}

// BuildCommand creates an argv-only invocation of the selected CMake preset.
func (r *Runner) BuildCommand(
	ctx context.Context,
	projectDir string,
	task runner.Task,
	args []string,
) (*exec.Cmd, error) {
	kind, preset, err := taskPreset(task)
	if err != nil {
		return nil, err
	}

	binary := r.binary
	argv := []string{"--preset", preset}
	switch kind {
	case configurePreset:
	case buildPreset:
		argv = []string{"--build", "--preset", preset}
	case testPreset:
		binary = ctestBinary
	case packagePreset:
		binary = cpackBinary
	case workflowPreset:
		argv = []string{"--workflow", "--preset", preset}
	default:
		return nil, fmt.Errorf("task %q has an unsupported CMake preset kind %q", task.ID, kind)
	}
	argv = append(argv, args...)
	// #nosec G204 -- task and argv come from discovered runner metadata, not a shell.
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Dir = projectDir
	return cmd, nil
}

type preset struct {
	kind        string
	name        string
	description string
}

func (r *Runner) listPresets(ctx context.Context, projectDir string) ([]preset, error) {
	commands := []presetCommand{
		{binary: r.binary, args: []string{"--list-presets=configure"}},
		{binary: r.binary, args: []string{"--list-presets=build"}},
		{binary: ctestBinary, args: []string{"--list-presets"}},
		{binary: cpackBinary, args: []string{"--list-presets"}},
		{binary: r.binary, args: []string{"--list-presets=workflow"}},
	}
	presets := make([]preset, 0)
	for _, command := range commands {
		listed, err := command.list(ctx, projectDir)
		if err != nil {
			return nil, err
		}
		presets = append(presets, listed...)
	}
	return presets, nil
}

type presetCommand struct {
	binary string
	args   []string
}

func (c presetCommand) list(ctx context.Context, projectDir string) ([]preset, error) {
	// #nosec G204 -- binary and preset file are local workspace configuration.
	cmd := exec.CommandContext(ctx, c.binary, c.args...)
	cmd.Dir = projectDir
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("list CMake presets: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("start CMake preset listing: %w", err)
	}
	presets, err := parsePresets(string(output))
	if err != nil {
		return nil, fmt.Errorf("parse CMake preset listing: %w", err)
	}
	return presets, nil
}

func parsePresets(output string) ([]preset, error) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	presets := make([]preset, 0)
	kind := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if headerKind, ok := presetHeaderKind(line); ok {
			kind = headerKind
			continue
		}
		if kind == "" || !strings.HasPrefix(line, "\"") {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(line))
		var name string
		if err := decoder.Decode(&name); err != nil {
			return nil, fmt.Errorf("decode %s preset: %w", kind, err)
		}
		description := strings.TrimSpace(line[decoder.InputOffset():])
		description = strings.TrimSpace(strings.TrimPrefix(description, "-"))
		presets = append(presets, preset{kind: kind, name: name, description: description})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan CMake preset listing: %w", err)
	}
	return presets, nil
}

func presetHeaderKind(line string) (string, bool) {
	for _, kind := range []string{
		configurePreset,
		buildPreset,
		testPreset,
		packagePreset,
		workflowPreset,
	} {
		if line == "Available "+kind+" presets:" {
			return kind, true
		}
	}
	return "", false
}

func taskPreset(task runner.Task) (string, string, error) {
	prefix := runnerName + ":"
	if task.Runner != runnerName || !strings.HasPrefix(task.ID, prefix) {
		return "", "", fmt.Errorf("task %q does not belong to the %s runner", task.ID, runnerName)
	}
	kind, preset, found := strings.Cut(strings.TrimPrefix(task.ID, prefix), ":")
	if !found || kind == "" || preset == "" {
		return "", "", fmt.Errorf("task %q has an invalid CMake preset ID", task.ID)
	}
	if value, ok := task.Meta["kind"]; ok {
		metadataKind, valid := value.(string)
		if !valid || metadataKind == "" {
			return "", "", fmt.Errorf("task %q has an invalid CMake preset kind", task.ID)
		}
		if metadataKind != kind {
			return "", "", fmt.Errorf("task %q does not match CMake preset kind %q", task.ID, metadataKind)
		}
	}
	if value, ok := task.Meta["preset"]; ok {
		metadataPreset, valid := value.(string)
		if !valid || metadataPreset == "" {
			return "", "", fmt.Errorf("task %q has an invalid CMake preset name", task.ID)
		}
		if metadataPreset != preset {
			return "", "", fmt.Errorf("task %q does not match CMake preset %q", task.ID, metadataPreset)
		}
	}
	return kind, preset, nil
}

func hasPresetFile(projectDir string) (bool, error) {
	for _, name := range []string{presetsName, userPresetsName} {
		_, err := findRegularFile(projectDir, name)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func findRegularFile(dir string, name string) (string, error) {
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode().IsRegular() {
			return path, nil
		}
		return "", os.ErrNotExist
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect %s: %w", name, err)
	}
	return "", os.ErrNotExist
}
