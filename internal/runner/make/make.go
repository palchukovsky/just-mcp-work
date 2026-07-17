// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package makerunner implements the GNU Make task runner.
package makerunner

import (
	"bufio"
	"context"
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
	runnerName      = "make"
	gnuMakefileName = "GNUmakefile"
	makefileName    = "Makefile"
	lowerMakefile   = "makefile"
)

// Runner executes targets using the installed GNU Make binary.
type Runner struct {
	binary string
}

// New constructs a Make runner. An empty binary uses "make" from PATH.
func New(binary string) *Runner {
	if binary == "" {
		binary = runnerName
	}
	return &Runner{binary: binary}
}

// Name returns the stable runner name.
func (*Runner) Name() string { return runnerName }

// RunnerVersion reports the installed Make version for run metadata.
func (r *Runner) RunnerVersion(ctx context.Context) (string, error) {
	// #nosec G204 -- binary is configured locally, never supplied over MCP.
	output, err := exec.CommandContext(ctx, r.binary, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("get Make version: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Detect reports whether a supported Makefile exists in projectDir.
func (*Runner) Detect(projectDir string) (bool, error) {
	_, err := findMakefile(projectDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// ListTasks discovers explicit Make targets without running their recipes.
func (r *Runner) ListTasks(ctx context.Context, projectDir string) ([]runner.Task, error) {
	makefile, err := findMakefile(projectDir)
	if err != nil {
		return nil, err
	}
	// #nosec G204 -- binary and Makefile are local workspace configuration.
	cmd := exec.CommandContext(ctx, r.binary, "-rRpn", "-f", makefile)
	cmd.Dir = projectDir
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("list Make targets: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("start Make target listing: %w", err)
	}
	tasks, err := tasksFromDatabase(string(output), makefile)
	if err != nil {
		return nil, fmt.Errorf("parse Make target listing: %w", err)
	}
	return tasks, nil
}

// BuildCommand creates an argv-only invocation of a discovered Make target.
func (r *Runner) BuildCommand(
	ctx context.Context,
	projectDir string,
	task runner.Task,
	args []string,
) (*exec.Cmd, error) {
	target, err := taskTarget(task)
	if err != nil {
		return nil, err
	}
	makefile, err := findMakefile(projectDir)
	if err != nil {
		return nil, err
	}
	argv := []string{"-f", makefile, "--", target}
	argv = append(argv, args...)
	// #nosec G204 -- task and argv come from discovered runner metadata, not a shell.
	cmd := exec.CommandContext(ctx, r.binary, argv...)
	cmd.Dir = projectDir
	return cmd, nil
}

func tasksFromDatabase(database string, makefile string) ([]runner.Task, error) {
	scanner := bufio.NewScanner(strings.NewReader(database))
	inFiles := false
	notTarget := false
	targets := make(map[string]struct{})
	for scanner.Scan() {
		line := scanner.Text()
		if line == "# Files" {
			inFiles = true
			continue
		}
		if inFiles && line == "# Implicit Rules" {
			break
		}
		if !inFiles {
			continue
		}
		if line == "# Not a target:" {
			notTarget = true
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "\t") {
			continue
		}
		target, ok := databaseTarget(line)
		if !ok {
			continue
		}
		if notTarget {
			notTarget = false
			continue
		}
		if !isTaskTarget(target, makefile) {
			continue
		}
		targets[target] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan Make database: %w", err)
	}

	names := make([]string, 0, len(targets))
	for target := range targets {
		names = append(names, target)
	}
	sort.Strings(names)
	tasks := make([]runner.Task, 0, len(names))
	for _, target := range names {
		tasks = append(tasks, runner.Task{
			ID:     runnerName + ":" + target,
			Runner: runnerName,
			Name:   target,
			Meta:   map[string]any{"target": target},
		})
	}
	return tasks, nil
}

func databaseTarget(line string) (string, bool) {
	target, _, found := strings.Cut(line, ":")
	if !found || target == "" || strings.ContainsAny(target, " \t") {
		return "", false
	}
	return target, true
}

func isTaskTarget(target string, makefile ...string) bool {
	if strings.HasPrefix(target, ".") || strings.Contains(target, "%") {
		return false
	}
	for _, path := range makefile {
		if filepath.Clean(target) == filepath.Clean(path) {
			return false
		}
	}
	if target == gnuMakefileName || target == makefileName || target == lowerMakefile {
		return false
	}
	return true
}

func taskTarget(task runner.Task) (string, error) {
	prefix := runnerName + ":"
	if task.Runner != runnerName || !strings.HasPrefix(task.ID, prefix) {
		return "", fmt.Errorf("task %q does not belong to the %s runner", task.ID, runnerName)
	}
	target := strings.TrimPrefix(task.ID, prefix)
	if !isTaskTarget(target) {
		return "", fmt.Errorf("task %q has an invalid Make target", task.ID)
	}
	if value, ok := task.Meta["target"]; ok {
		metadataTarget, valid := value.(string)
		if !valid || !isTaskTarget(metadataTarget) {
			return "", fmt.Errorf("task %q has an invalid Make target metadata", task.ID)
		}
		if metadataTarget != target {
			return "", fmt.Errorf("task %q does not match Make target %q", task.ID, metadataTarget)
		}
	}
	return target, nil
}

func findMakefile(projectDir string) (string, error) {
	for _, name := range makefileNames() {
		path := filepath.Join(projectDir, name)
		info, err := os.Lstat(path)
		if err == nil {
			if info.Mode().IsRegular() {
				return path, nil
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect Makefile: %w", err)
		}
	}
	return "", os.ErrNotExist
}

func makefileNames() []string {
	return []string{gnuMakefileName, makefileName, lowerMakefile}
}
