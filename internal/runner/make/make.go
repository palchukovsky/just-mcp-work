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

// ListTasks discovers literal explicit Make targets without evaluating the Makefile.
func (*Runner) ListTasks(ctx context.Context, projectDir string) ([]runner.Task, error) {
	makefile, err := findMakefile(projectDir)
	if err != nil {
		return nil, err
	}
	// #nosec G304 -- makefile is one of the fixed regular filenames below projectDir.
	source, err := os.ReadFile(makefile)
	if err != nil {
		return nil, fmt.Errorf("read Makefile for safe discovery: %w", err)
	}
	tasks, err := tasksFromMakefile(ctx, string(source), makefile)
	if err != nil {
		return nil, fmt.Errorf("safe Make discovery: %w", err)
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

const maxMakefileLine = 1 << 20

type makefileParser struct {
	ctx      context.Context
	targets  map[string]struct{}
	makefile string
}

func tasksFromMakefile(
	ctx context.Context,
	source string,
	makefile string,
) ([]runner.Task, error) {
	parser := makefileParser{
		ctx:      ctx,
		makefile: makefile,
		targets:  make(map[string]struct{}),
	}
	scanner := bufio.NewScanner(strings.NewReader(source))
	scanner.Buffer(make([]byte, 64<<10), maxMakefileLine)
	parts := make([]string, 0, 2)
	lineNumber := 0
	logicalLine := 0
	recipeAllowed := false
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if len(parts) == 0 {
			logicalLine = lineNumber
		}
		if makeLineContinues(line) {
			parts = append(parts, line[:len(line)-1])
			continue
		}
		parts = append(parts, line)
		var err error
		recipeAllowed, err = parser.parseLine(
			strings.Join(parts, " "),
			logicalLine,
			recipeAllowed,
		)
		if err != nil {
			return nil, err
		}
		parts = parts[:0]
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan Makefile: %w", err)
	}
	if len(parts) > 0 {
		if _, err := parser.parseLine(
			strings.Join(parts, " "),
			logicalLine,
			recipeAllowed,
		); err != nil {
			return nil, err
		}
	}
	return makeTasks(parser.targets), nil
}

func (p *makefileParser) parseLine(
	line string,
	lineNumber int,
	recipeAllowed bool,
) (bool, error) {
	if err := p.ctx.Err(); err != nil {
		return false, fmt.Errorf("parse Makefile: %w", err)
	}
	if strings.HasPrefix(line, "	") {
		if !recipeAllowed {
			return false, fmt.Errorf("line %d: recipe has no preceding literal rule", lineNumber)
		}
		return true, nil
	}
	line = strings.TrimSpace(stripMakeComment(line))
	if line == "" {
		return recipeAllowed, nil
	}
	if directive, unsupported := unsupportedMakeDirective(line); unsupported {
		return false, fmt.Errorf(
			"line %d: %s directives are unsupported",
			lineNumber,
			directive,
		)
	}
	if strings.Contains(line, "$(eval") || strings.Contains(line, "${eval") {
		return false, fmt.Errorf("line %d: eval expansion is unsupported", lineNumber)
	}
	if name, assignment := makeAssignmentName(line); assignment {
		if name == ".RECIPEPREFIX" {
			return false, fmt.Errorf("line %d: custom recipe prefixes are unsupported", lineNumber)
		}
		return false, nil
	}
	if makeEnvironmentDirective(line) {
		return false, nil
	}
	return p.parseRule(line, lineNumber)
}

func (p *makefileParser) parseRule(line string, lineNumber int) (bool, error) {
	left, prerequisites, found := strings.Cut(line, ":")
	if !found {
		return false, fmt.Errorf("line %d: unsupported top-level statement", lineNumber)
	}
	left = strings.TrimSpace(left)
	if left == "" {
		return false, fmt.Errorf("line %d: rule has no target", lineNumber)
	}
	if left == ".PHONY" {
		prerequisites = strings.TrimLeft(prerequisites, ":")
		if index := strings.IndexAny(prerequisites, ";|"); index >= 0 {
			prerequisites = prerequisites[:index]
		}
		if err := p.addTargets(prerequisites, lineNumber); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := p.addTargets(left, lineNumber); err != nil {
		return false, err
	}
	return true, nil
}

func (p *makefileParser) addTargets(expression string, lineNumber int) error {
	expression = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(expression), "&"))
	if expression == "" {
		return fmt.Errorf("line %d: rule has no literal target", lineNumber)
	}
	if strings.ContainsAny(expression, "$\\") {
		return fmt.Errorf("line %d: dynamic or escaped targets are unsupported", lineNumber)
	}
	if strings.ContainsAny(expression, "*?[") {
		return fmt.Errorf("line %d: wildcard targets are unsupported", lineNumber)
	}
	for target := range strings.FieldsSeq(expression) {
		if strings.Contains(target, "%") || !isTaskTarget(target, p.makefile) {
			continue
		}
		p.targets[target] = struct{}{}
	}
	return nil
}

func makeTasks(targets map[string]struct{}) []runner.Task {
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
	return tasks
}

func makeLineContinues(line string) bool {
	backslashes := 0
	for index := len(line) - 1; index >= 0 && line[index] == '\\'; index-- {
		backslashes++
	}
	return backslashes%2 != 0
}

func stripMakeComment(line string) string {
	backslashes := 0
	for index, character := range line {
		if character == '#' && backslashes%2 == 0 {
			return line[:index]
		}
		if character == '\\' {
			backslashes++
		} else {
			backslashes = 0
		}
	}
	return line
}

func unsupportedMakeDirective(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", false
	}
	index := 0
	if fields[index] == "override" || fields[index] == "private" {
		index++
		if index == len(fields) {
			return fields[0], true
		}
	}
	switch fields[index] {
	case "include", "-include", "sinclude",
		"define", "endef",
		"ifeq", "ifneq", "ifdef", "ifndef", "else", "endif",
		"load":
		return fields[index], true
	default:
		return "", false
	}
}

func makeAssignmentName(line string) (string, bool) {
	line = strings.TrimSpace(line)
	for {
		modifier := ""
		for _, candidate := range []string{"export", "override", "private"} {
			if makeKeywordPrefix(line, candidate) {
				modifier = candidate
				break
			}
		}
		if modifier == "" {
			break
		}
		line = strings.TrimLeft(line[len(modifier):], " \t")
	}
	equal := strings.IndexByte(line, '=')
	if equal < 0 {
		return "", false
	}
	operator := equal
	for operator > 0 && strings.ContainsRune(":+?!", rune(line[operator-1])) {
		operator--
	}
	if colon := strings.IndexByte(line, ':'); colon >= 0 && colon < operator {
		return "", false
	}
	name := strings.TrimSpace(line[:operator])
	return name, name != ""
}

func makeEnvironmentDirective(line string) bool {
	for _, directive := range []string{"export", "unexport", "undefine", "vpath"} {
		if makeKeywordPrefix(line, directive) {
			return true
		}
	}
	return false
}

func makeKeywordPrefix(line string, keyword string) bool {
	if !strings.HasPrefix(line, keyword) {
		return false
	}
	if len(line) == len(keyword) {
		return true
	}
	separator := line[len(keyword)]
	return separator == ' ' || separator == '\t'
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
	path, err := runner.FindRegularFile(projectDir, makefileNames()...)
	if err != nil {
		return "", fmt.Errorf("find Makefile in %q: %w", projectDir, err)
	}
	return path, nil
}

func makefileNames() []string {
	return []string{gnuMakefileName, makefileName, lowerMakefile}
}
