// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package cmake implements CMake preset and configured-target task discovery.
package cmake

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

const (
	runnerName                = "cmake"
	cmakeListsName            = "CMakeLists.txt"
	presetsName               = "CMakePresets.json"
	userPresetsName           = "CMakeUserPresets.json"
	configurePreset           = "configure"
	buildPreset               = "build"
	testPreset                = "test"
	packagePreset             = "package"
	workflowPreset            = "workflow"
	targetKind                = "target"
	cmakeCacheName            = "CMakeCache.txt"
	cacheSourceEntry          = "CMAKE_HOME_DIRECTORY:INTERNAL="
	cacheGeneratorEntry       = "CMAKE_GENERATOR:INTERNAL="
	ninjaGenerator            = "Ninja"
	ninjaMultiConfigGenerator = "Ninja Multi-Config"
	ninjaBuildFileName        = "build.ninja"
	cmakeObjectOrderPrefix    = "cmake_object_order_depends_target_"
	maxBuildDirDepth          = 3
	ctestBinary               = "ctest"
	cpackBinary               = "cpack"
)

// Runner executes CMake presets and targets from configured build trees.
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

// Registration explicitly keeps the unreviewed CMake command policy enabled.
func Registration(binary string) runner.Registration {
	return runner.NewRegistration(
		runnerName,
		runner.UnreviewedPermissions(),
		func(runner.Mode) (runner.Runner, error) { return New(binary), nil },
	)
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
	_, err := runner.FindRegularFile(projectDir, cmakeListsName)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find %s in %q: %w", cmakeListsName, projectDir, err)
	}
	return true, nil
}

// ListTasks lists presets and targets without configuring or regenerating the project.
func (r *Runner) ListTasks(ctx context.Context, projectDir string) ([]runner.Task, error) {
	return r.listTasks(ctx, projectDir, r.listBuildTargets)
}

func (r *Runner) listTasks(
	ctx context.Context,
	projectDir string,
	listTargets func(context.Context, string) ([]buildTarget, error),
) ([]runner.Task, error) {
	tasks := make([]runner.Task, 0)
	var warning error
	var failure error

	hasPresets, err := hasPresetFile(projectDir)
	if err != nil {
		return nil, err
	}
	if hasPresets {
		presets, listErr := r.listPresets(ctx, projectDir)
		presetWarning, presetFailure := runner.SplitIssues(listErr)
		warning = errors.Join(warning, presetWarning)
		failure = errors.Join(failure, presetFailure)
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
	}

	targets, listErr := listTargets(ctx, projectDir)
	targetWarning, targetFailure := runner.SplitIssues(listErr)
	warning = errors.Join(warning, targetWarning)
	failure = errors.Join(failure, targetFailure)
	if len(targets) > 0 && !hasPresets {
		// A binary absent from this host is only a warning. A binary that is present
		// but unusable is a real environment misconfiguration and remains a failure.
		_, lookupErr := exec.LookPath(r.binary)
		lookupWarning, lookupFailure := runner.SplitIssues(
			runner.MarkMissingTool(r.binary, lookupErr),
		)
		warning = errors.Join(warning, lookupWarning)
		failure = errors.Join(failure, lookupFailure)
	}
	for _, target := range targets {
		tasks = append(tasks, runner.Task{
			ID: runnerName + ":" + targetKind + ":" +
				url.QueryEscape(target.buildDir) + ":" + url.QueryEscape(target.name),
			Runner:      runnerName,
			Name:        target.name,
			Description: fmt.Sprintf("Build target in CMake tree %q.", target.buildDir),
			Meta: map[string]any{
				"kind":      targetKind,
				"build_dir": target.buildDir,
				"target":    target.name,
			},
		})
	}

	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})
	return tasks, errors.Join(warning, failure)
}

// BuildCommand creates an argv-only invocation of a selected preset or target.
// Configured-target arguments stay before --target because a bare word after it
// becomes an extra target. Native-tool arguments remain after CMake's --.
func (r *Runner) BuildCommand(
	ctx context.Context,
	projectDir string,
	task runner.Task,
	args []string,
) (*exec.Cmd, error) {
	if strings.HasPrefix(task.ID, runnerName+":"+targetKind+":") {
		buildDir, target, err := taskBuildTarget(task)
		if err != nil {
			return nil, err
		}
		argv := []string{
			"--build",
			filepath.Join(projectDir, filepath.FromSlash(buildDir)),
		}
		targetArgs := args
		var nativeArgs []string
		if separator := slices.Index(args, "--"); separator >= 0 {
			targetArgs = args[:separator]
			nativeArgs = args[separator:]
		}
		argv = append(argv, targetArgs...)
		argv = append(argv, "--target", target)
		argv = append(argv, nativeArgs...)
		// #nosec G204 -- task and argv come from discovered runner metadata, not a shell.
		cmd := exec.CommandContext(ctx, r.binary, argv...)
		cmd.Dir = projectDir
		return cmd, nil
	}

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

type buildTarget struct {
	buildDir string
	name     string
}

type configuredBuildTree struct {
	path      string
	generator string
}

type cmakeCache struct {
	sourceDir string
	generator string
}

type ninjaBuildEdge struct {
	outputs         []string
	rule            string
	inputs          []string
	orderOnlyInputs []string
}

func (r *Runner) listBuildTargets(
	ctx context.Context,
	projectDir string,
) ([]buildTarget, error) {
	buildTrees, err := configuredBuildDirs(ctx, projectDir)
	if err != nil {
		return nil, err
	}

	targets := make([]buildTarget, 0)
	var warning error
	for _, buildTree := range buildTrees {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return targets, fmt.Errorf(
				"list configured CMake targets: %w",
				ctxErr,
			)
		}
		absoluteDir := filepath.Join(
			projectDir,
			filepath.FromSlash(buildTree.path),
		)
		names, listErr := r.listBuildDirTargets(
			ctx,
			absoluteDir,
			buildTree.generator,
		)
		if listErr != nil {
			if errors.Is(listErr, os.ErrNotExist) {
				continue
			}
			if errors.Is(listErr, fs.ErrPermission) {
				warning = errors.Join(
					warning,
					runner.MarkWarning(listErr),
				)
				continue
			}
			treeWarning, treeFailure := runner.SplitIssues(listErr)
			warning = errors.Join(warning, treeWarning)
			if treeFailure != nil {
				return targets, errors.Join(warning, treeFailure)
			}
			continue
		}
		for _, name := range names {
			targets = append(targets, buildTarget{
				buildDir: buildTree.path,
				name:     name,
			})
		}
	}
	return targets, warning
}

func (*Runner) listBuildDirTargets(
	ctx context.Context,
	buildDir string,
	generator string,
) ([]string, error) {
	if !usesNinjaGenerator(generator) {
		return nil, fmt.Errorf(
			"report unsupported configured CMake generator: %w",
			runner.MarkWarning(
				fmt.Errorf(
					"configured CMake target discovery supports only Ninja and Ninja Multi-Config; generator %q is unsupported",
					generator,
				),
			),
		)
	}

	buildPath, err := runner.FindRegularFile(buildDir, ninjaBuildFileName)
	if err != nil {
		return nil, fmt.Errorf("find Ninja build file in %q: %w", buildDir, err)
	}
	// #nosec G304 -- buildPath is a regular file in the discovered build tree.
	buildFile, err := os.Open(buildPath)
	if err != nil {
		return nil, fmt.Errorf("open Ninja build file %q: %w", buildPath, err)
	}
	targets, parseErr := parseBuildTargets(ctx, buildFile)
	closeErr := buildFile.Close()
	if parseErr != nil {
		return nil, fmt.Errorf("parse Ninja targets in %q: %w", buildDir, parseErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close Ninja build file %q: %w", buildPath, closeErr)
	}
	return targets, nil
}

func usesNinjaGenerator(generator string) bool {
	return generator == ninjaGenerator ||
		generator == ninjaMultiConfigGenerator
}

func parseBuildTargets(
	ctx context.Context,
	input io.Reader,
) ([]string, error) {
	declaredTargets := make(map[string]struct{})
	phonyTargets := make(map[string]struct{})
	nonPhonyTargets := make(map[string]struct{})
	aliasesByInput := make(map[string][]string)
	implementationInputs := make(map[string]struct{})
	if err := readNinjaBuildEdges(ctx, input, func(edge ninjaBuildEdge) {
		addNinjaDeclaredTargets(declaredTargets, edge)
		addNinjaTargetAliases(aliasesByInput, edge)
		addNinjaImplementationInputs(implementationInputs, edge)
		addNinjaCandidateTargets(phonyTargets, nonPhonyTargets, edge)
	}); err != nil {
		return nil, err
	}

	aliasedInputs := make(map[string]struct{})
	for input := range aliasesByInput {
		if _, declared := declaredTargets[input]; declared {
			delete(aliasesByInput, input)
			continue
		}
		aliasedInputs[input] = struct{}{}
	}
	for input := range implementationInputs {
		if _, declared := declaredTargets[input]; declared {
			delete(implementationInputs, input)
		}
	}
	artifactAliases := ninjaArtifactAliases(aliasesByInput)

	seen := make(map[string]struct{})
	for name := range phonyTargets {
		if _, artifactAlias := artifactAliases[name]; !artifactAlias {
			seen[name] = struct{}{}
		}
	}
	for name := range nonPhonyTargets {
		if _, aliased := aliasedInputs[name]; aliased {
			continue
		}
		if _, implementationInput := implementationInputs[name]; implementationInput {
			continue
		}
		seen[name] = struct{}{}
	}

	targets := make([]string, 0, len(seen))
	for name := range seen {
		targets = append(targets, name)
	}
	sort.Strings(targets)
	return targets, nil
}

func addNinjaDeclaredTargets(
	declaredTargets map[string]struct{},
	edge ninjaBuildEdge,
) {
	for _, output := range edge.outputs {
		if edge.rule == "phony" && logicalNinjaTarget(output) {
			declaredTargets[output] = struct{}{}
		}
		name, found := strings.CutPrefix(output, cmakeObjectOrderPrefix)
		if found && validBuildTargetName(name) {
			declaredTargets[name] = struct{}{}
		}
	}
}

func addNinjaTargetAliases(
	aliasesByInput map[string][]string,
	edge ninjaBuildEdge,
) {
	if edge.rule != "phony" ||
		len(edge.outputs) != 1 ||
		len(edge.inputs) != 1 ||
		edge.outputs[0] == "all" ||
		!logicalNinjaTarget(edge.outputs[0]) {
		return
	}
	input := edge.inputs[0]
	aliasesByInput[input] = append(
		aliasesByInput[input],
		edge.outputs[0],
	)
}

func addNinjaImplementationInputs(
	implementationInputs map[string]struct{},
	edge ninjaBuildEdge,
) {
	if edge.rule == "phony" && cmakeFilesOutput(edge.outputs) {
		for _, input := range edge.inputs {
			implementationInputs[input] = struct{}{}
		}
	}
	for _, input := range edge.orderOnlyInputs {
		implementationInputs[input] = struct{}{}
	}
}

func addNinjaCandidateTargets(
	phonyTargets map[string]struct{},
	nonPhonyTargets map[string]struct{},
	edge ninjaBuildEdge,
) {
	for _, name := range edge.outputs {
		if !logicalNinjaTarget(name) {
			continue
		}
		if edge.rule == "phony" {
			phonyTargets[name] = struct{}{}
			continue
		}
		nonPhonyTargets[name] = struct{}{}
	}
}

func ninjaArtifactAliases(aliasesByInput map[string][]string) map[string]struct{} {
	artifactAliases := make(map[string]struct{})
	for input, aliases := range aliasesByInput {
		if len(aliases) < 2 {
			continue
		}
		artifactName := path.Base(input)
		for _, alias := range aliases {
			if alias == artifactName {
				artifactAliases[alias] = struct{}{}
			}
		}
	}
	return artifactAliases
}

func readNinjaBuildEdges(
	ctx context.Context,
	input io.Reader,
	visit func(ninjaBuildEdge),
) error {
	err := readNinjaLogicalLines(ctx, input, func(line string) {
		edge, found := parseNinjaBuildEdge(line)
		if found {
			visit(edge)
		}
	})
	if err != nil {
		return fmt.Errorf("read Ninja build file: %w", err)
	}
	return nil
}

func cmakeFilesOutput(outputs []string) bool {
	for _, output := range outputs {
		if slices.Contains(strings.Split(output, "/"), "CMakeFiles") {
			return true
		}
	}
	return false
}

func validBuildTargetName(name string) bool {
	if name == "" ||
		name == ninjaBuildFileName ||
		strings.HasPrefix(name, "-") ||
		path.IsAbs(name) ||
		strings.ContainsAny(name, " \t\r\n\x00\\$") {
		return false
	}
	clean := path.Clean(name)
	if clean == "." ||
		clean == ".." ||
		strings.HasPrefix(clean, "../") ||
		clean != name {
		return false
	}
	if !strings.Contains(name, "/") {
		return true
	}
	return directoryScopedCMakeTarget(name)
}

// directoryScopedCMakeTarget keeps slash-bearing CMake targets that do not
// repeat per source directory. CMake 4.4+ Ninja generators can create
// test_prep/<name> when CMAKE_TEST_BUILD_DEPENDS is enabled, so dropping it
// would hide a task CMake makes available. Per-directory variants such as
// "lib/all" and "src/app/install" are deliberately left out because they
// would bury the targets a caller looks for.
func directoryScopedCMakeTarget(name string) bool {
	if name == "install/local" ||
		name == "install/parallel" ||
		name == "install/strip" {
		return true
	}
	testName, found := strings.CutPrefix(name, "test_prep/")
	return found && testName != "" && !strings.Contains(testName, "/")
}

func readNinjaLogicalLines(
	ctx context.Context,
	input io.Reader,
	visit func(string),
) error {
	var logical strings.Builder
	err := readLines(
		ctx,
		input,
		func(line string) {
			if logical.Len() > 0 {
				line = strings.TrimLeft(line, " \t")
			}
			if ninjaLineContinues(line) {
				logical.WriteString(line[:len(line)-1])
				return
			}
			logical.WriteString(line)
			visit(logical.String())
			logical.Reset()
		},
	)
	if err != nil {
		return err
	}
	if logical.Len() > 0 {
		visit(logical.String())
	}
	return nil
}

func readLines(
	ctx context.Context,
	input io.Reader,
	visit func(string),
) error {
	reader := bufio.NewReader(input)
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("read lines: %w", ctxErr)
		}
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			visit(trimTextLineEnding(line))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read line: %w", err)
		}
	}
}

func trimTextLineEnding(line string) string {
	line = strings.TrimSuffix(line, "\n")
	return strings.TrimSuffix(line, "\r")
}

func ninjaLineContinues(line string) bool {
	trailingDollars := 0
	for index := len(line) - 1; index >= 0 && line[index] == '$'; index-- {
		trailingDollars++
	}
	return trailingDollars%2 == 1
}

func parseNinjaBuildEdge(line string) (ninjaBuildEdge, bool) {
	body, found := strings.CutPrefix(strings.TrimSpace(line), "build ")
	if !found {
		return ninjaBuildEdge{}, false
	}
	separator := ninjaBuildSeparator(body)
	if separator < 0 {
		return ninjaBuildEdge{}, false
	}
	outputTokens, valid := ninjaTokens(body[:separator])
	if !valid {
		return ninjaBuildEdge{}, false
	}
	statementTokens, valid := ninjaTokens(body[separator+1:])
	if !valid || len(statementTokens) == 0 {
		return ninjaBuildEdge{}, false
	}
	outputs := explicitNinjaOutputs(outputTokens)
	if len(outputs) == 0 {
		return ninjaBuildEdge{}, false
	}
	inputTokens := statementTokens[1:]
	return ninjaBuildEdge{
		outputs:         outputs,
		rule:            statementTokens[0],
		inputs:          explicitNinjaInputs(inputTokens),
		orderOnlyInputs: orderOnlyNinjaInputs(inputTokens),
	}, true
}

func ninjaBuildSeparator(line string) int {
	for index := 0; index < len(line); index++ {
		if line[index] == '$' {
			if index+1 < len(line) {
				index++
			}
			continue
		}
		if line[index] == ':' {
			return index
		}
	}
	return -1
}

func ninjaTokens(value string) ([]string, bool) {
	tokens := make([]string, 0)
	var token strings.Builder
	flush := func() {
		if token.Len() == 0 {
			return
		}
		tokens = append(tokens, token.String())
		token.Reset()
	}

	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '$' {
			if index+1 >= len(value) {
				return nil, false
			}
			next := value[index+1]
			switch next {
			case ' ', ':', '$':
				token.WriteByte(next)
				index++
			default:
				token.WriteByte(character)
			}
			continue
		}
		if character == ' ' || character == '\t' {
			flush()
			continue
		}
		token.WriteByte(character)
	}
	flush()
	return tokens, true
}

func explicitNinjaOutputs(tokens []string) []string {
	outputs := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token == "|" {
			break
		}
		outputs = append(outputs, token)
	}
	return outputs
}

func explicitNinjaInputs(tokens []string) []string {
	inputs := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token == "|" || token == "||" || token == "|@" {
			break
		}
		inputs = append(inputs, token)
	}
	return inputs
}

func orderOnlyNinjaInputs(tokens []string) []string {
	inputs := make([]string, 0)
	foundSeparator := false
	for _, token := range tokens {
		switch token {
		case "||":
			foundSeparator = true
		case "|@":
			return inputs
		default:
			if foundSeparator {
				inputs = append(inputs, token)
			}
		}
	}
	return inputs
}

func logicalNinjaTarget(name string) bool {
	return validBuildTargetName(name) && !internalCMakeTarget(name)
}

func internalCMakeTarget(name string) bool {
	switch name {
	case "CMakeCache.txt",
		"cmake_install.cmake",
		"help",
		"install_manifest.txt",
		"edit_cache",
		"rebuild_cache",
		"list_install_components":
		return true
	default:
		return ctestDashboardTarget(name) ||
			strings.HasPrefix(name, cmakeObjectOrderPrefix)
	}
}

func ctestDashboardTarget(name string) bool {
	for _, mode := range []string{
		"Continuous",
		"Experimental",
		"Nightly",
	} {
		for _, phase := range []string{
			"",
			"Start",
			"Update",
			"Configure",
			"Build",
			"Test",
			"Coverage",
			"MemCheck",
			"Submit",
		} {
			if name == mode+phase {
				return true
			}
		}
	}
	return name == "NightlyMemoryCheck"
}

func skippableBuildTreeError(err error) bool {
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, fs.ErrPermission)
}

type buildDirCandidate struct {
	path  string
	depth int
}

type projectPathIdentity struct {
	info fs.FileInfo
	path string
}

func configuredBuildDirs(
	ctx context.Context,
	projectDir string,
) ([]configuredBuildTree, error) {
	projectIdentity, err := resolveProjectPath(projectDir)
	if err != nil {
		return nil, err
	}
	buildTrees := make([]configuredBuildTree, 0)
	cache, configured, err := isConfiguredBuildDir(
		ctx,
		projectIdentity,
		projectDir,
	)
	if err != nil && !skippableBuildTreeError(err) {
		return nil, err
	}
	if err == nil && configured {
		buildTrees = append(buildTrees, configuredBuildTree{
			path:      ".",
			generator: cache.generator,
		})
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("find configured CMake directories: %w", ctxErr)
	}
	queue, err := initialBuildDirCandidates(projectDir)
	if err != nil {
		return nil, err
	}
	nested, err := configuredBuildDirsFromCandidates(
		ctx,
		projectDir,
		projectIdentity,
		queue,
	)
	if err != nil {
		return nil, err
	}
	buildTrees = append(buildTrees, nested...)
	sort.Slice(buildTrees, func(i, j int) bool {
		return buildTrees[i].path < buildTrees[j].path
	})
	return buildTrees, nil
}

func initialBuildDirCandidates(projectDir string) ([]buildDirCandidate, error) {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil, fmt.Errorf("read CMake project directory %q: %w", projectDir, err)
	}
	candidates := make([]buildDirCandidate, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			candidates = append(candidates, buildDirCandidate{
				path:  filepath.Join(projectDir, entry.Name()),
				depth: 1,
			})
		}
	}
	return candidates, nil
}

func configuredBuildDirsFromCandidates(
	ctx context.Context,
	projectDir string,
	projectIdentity projectPathIdentity,
	queue []buildDirCandidate,
) ([]configuredBuildTree, error) {
	buildTrees := make([]configuredBuildTree, 0)
	for len(queue) > 0 {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return buildTrees, fmt.Errorf(
				"scan configured CMake directories: %w",
				ctxErr,
			)
		}
		candidate := queue[0]
		queue = queue[1:]
		cache, configured, err := isConfiguredBuildDir(
			ctx,
			projectIdentity,
			candidate.path,
		)
		if err != nil {
			if skippableBuildTreeError(err) {
				continue
			}
			return buildTrees, err
		}
		if configured {
			relative, relativeErr := filepath.Rel(projectDir, candidate.path)
			if relativeErr != nil {
				return buildTrees, fmt.Errorf(
					"make CMake build directory %q relative: %w",
					candidate.path,
					relativeErr,
				)
			}
			buildTrees = append(buildTrees, configuredBuildTree{
				path:      filepath.ToSlash(relative),
				generator: cache.generator,
			})
			continue
		}
		if candidate.depth == 1 && !isBuildRootName(filepath.Base(candidate.path)) {
			continue
		}
		if candidate.depth >= maxBuildDirDepth {
			continue
		}

		children, readErr := os.ReadDir(candidate.path)
		if readErr != nil {
			if skippableBuildTreeError(readErr) {
				continue
			}
			return buildTrees, fmt.Errorf(
				"read CMake build directory candidate %q: %w",
				candidate.path,
				readErr,
			)
		}
		for _, child := range children {
			if child.IsDir() && !skipBuildDir(child.Name()) {
				queue = append(queue, buildDirCandidate{
					path:  filepath.Join(candidate.path, child.Name()),
					depth: candidate.depth + 1,
				})
			}
		}
	}
	return buildTrees, nil
}

func isBuildRootName(name string) bool {
	name = strings.ToLower(name)
	return name == "out" ||
		name == "_build" ||
		name == "build" ||
		strings.HasPrefix(name, "build-") ||
		strings.HasPrefix(name, "build_") ||
		strings.HasPrefix(name, "cmake-build-")
}

func skipBuildDir(name string) bool {
	return strings.HasPrefix(name, ".") ||
		name == "CMakeFiles" ||
		name == "node_modules"
}

func isConfiguredBuildDir(
	ctx context.Context,
	projectIdentity projectPathIdentity,
	buildDir string,
) (cmakeCache, bool, error) {
	cache, err := readCMakeCache(ctx, buildDir)
	if errors.Is(err, os.ErrNotExist) {
		return cmakeCache{}, false, nil
	}
	if err != nil {
		return cmakeCache{}, false, err
	}
	if !sameProjectPath(
		projectIdentity,
		cache.sourceDir,
	) {
		return cmakeCache{}, false, nil
	}
	return cache, true, nil
}

func readCMakeCache(
	ctx context.Context,
	buildDir string,
) (cmakeCache, error) {
	cachePath, err := runner.FindRegularFile(buildDir, cmakeCacheName)
	if err != nil {
		return cmakeCache{}, fmt.Errorf(
			"find CMake cache in %q: %w",
			buildDir,
			err,
		)
	}
	// #nosec G304 -- cachePath is a regular file in the discovered build tree.
	cacheFile, err := os.Open(cachePath)
	if err != nil {
		return cmakeCache{}, fmt.Errorf(
			"open CMake cache %q: %w",
			cachePath,
			err,
		)
	}
	cache, parseErr := parseCMakeCache(ctx, cacheFile)
	closeErr := cacheFile.Close()
	if parseErr != nil {
		return cmakeCache{}, fmt.Errorf(
			"parse CMake cache %q: %w",
			cachePath,
			parseErr,
		)
	}
	if closeErr != nil {
		return cmakeCache{}, fmt.Errorf(
			"close CMake cache %q: %w",
			cachePath,
			closeErr,
		)
	}
	return cache, nil
}

func parseCMakeCache(
	ctx context.Context,
	input io.Reader,
) (cmakeCache, error) {
	var cache cmakeCache
	err := readLines(ctx, input, func(line string) {
		if sourceDir, found := strings.CutPrefix(line, cacheSourceEntry); found {
			cache.sourceDir = strings.TrimSpace(sourceDir)
		}
		if generator, found := strings.CutPrefix(line, cacheGeneratorEntry); found {
			cache.generator = strings.TrimSpace(generator)
		}
	})
	if err != nil {
		return cmakeCache{}, err
	}
	return cache, nil
}

func resolveProjectPath(projectDir string) (projectPathIdentity, error) {
	projectPath, err := filepath.Abs(projectDir)
	if err != nil {
		return projectPathIdentity{}, fmt.Errorf(
			"make CMake project directory %q absolute: %w",
			projectDir,
			err,
		)
	}
	projectPath = filepath.Clean(projectPath)
	projectInfo, statErr := os.Stat(projectPath)
	if statErr != nil {
		// File identity is optional because exact cleaned lexical paths still match.
		projectInfo = nil
	}
	return projectPathIdentity{
		path: projectPath,
		info: projectInfo,
	}, nil
}

func sameProjectPath(
	projectIdentity projectPathIdentity,
	cacheSourceDir string,
) bool {
	if projectIdentity.path == "" || !filepath.IsAbs(cacheSourceDir) {
		return false
	}
	cacheSourceDir = filepath.Clean(cacheSourceDir)
	if projectIdentity.path == cacheSourceDir {
		return true
	}
	if projectIdentity.info == nil {
		return false
	}
	cacheInfo, err := os.Stat(cacheSourceDir)
	if err != nil {
		// An alternate cache path that cannot be statted intentionally does not match.
		return false
	}
	return os.SameFile(projectIdentity.info, cacheInfo)
}

func taskBuildTarget(task runner.Task) (string, string, error) {
	prefix := runnerName + ":" + targetKind + ":"
	if task.Runner != runnerName || !strings.HasPrefix(task.ID, prefix) {
		return "", "", fmt.Errorf("task %q does not belong to the %s runner", task.ID, runnerName)
	}
	encodedBuildDir, encodedTarget, found := strings.Cut(
		strings.TrimPrefix(task.ID, prefix),
		":",
	)
	if !found {
		return "", "", fmt.Errorf("task %q has an invalid CMake target ID", task.ID)
	}
	buildDir, err := url.QueryUnescape(encodedBuildDir)
	if err != nil || !validBuildDir(buildDir) {
		return "", "", fmt.Errorf("task %q has an invalid CMake build directory", task.ID)
	}
	target, err := url.QueryUnescape(encodedTarget)
	if err != nil || !validBuildTargetName(target) {
		return "", "", fmt.Errorf("task %q has an invalid CMake target", task.ID)
	}
	if err := validateBuildTargetMetadata(task, buildDir, target); err != nil {
		return "", "", err
	}
	return buildDir, target, nil
}

func validateBuildTargetMetadata(task runner.Task, buildDir string, target string) error {
	if value, ok := task.Meta["kind"]; ok {
		kind, valid := value.(string)
		if !valid || kind != targetKind {
			return fmt.Errorf("task %q has an invalid CMake target kind", task.ID)
		}
	}
	if value, ok := task.Meta["build_dir"]; ok {
		metadataBuildDir, valid := value.(string)
		if !valid || metadataBuildDir != buildDir {
			return fmt.Errorf(
				"task %q does not match its CMake build directory metadata",
				task.ID,
			)
		}
	}
	if value, ok := task.Meta["target"]; ok {
		metadataTarget, valid := value.(string)
		if !valid || metadataTarget != target {
			return fmt.Errorf(
				"task %q does not match its CMake target metadata",
				task.ID,
			)
		}
	}
	return nil
}

func validBuildDir(buildDir string) bool {
	if buildDir == "" {
		return false
	}
	native := filepath.FromSlash(buildDir)
	if filepath.IsAbs(native) {
		return false
	}
	clean := filepath.Clean(native)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	return filepath.ToSlash(clean) == buildDir
}

type preset struct {
	kind        string
	name        string
	description string
}

func (r *Runner) listPresets(ctx context.Context, projectDir string) ([]preset, error) {
	version, err := queryCMakeVersion(ctx, r.binary, projectDir)
	if err != nil {
		var capabilitiesErr *unusableCMakeCapabilitiesError
		if !errors.As(err, &capabilitiesErr) {
			return nil, fmt.Errorf("discover CMake preset support: %w", err)
		}
		commands := bestEffortPresetCommands(r.binary)
		presets, listErr := collectPresets(ctx, commands, func(command presetCommand) (
			[]preset,
			error,
		) {
			return command.list(ctx, projectDir)
		})
		capabilitiesWarning := runner.MarkWarning(fmt.Errorf(
			"CMake capabilities query failed; listing all preset families best-effort: %w",
			err,
		))
		return presets, errors.Join(capabilitiesWarning, listErr)
	}
	commands := presetCommandsForVersion(r.binary, version)
	if len(commands) == 0 {
		return nil, fmt.Errorf(
			"list CMake presets: %w",
			runner.MarkWarning(fmt.Errorf(
				"CMake %d.%d has no preset support; CMake 3.19 or newer is required",
				version.major,
				version.minor,
			)),
		)
	}
	return collectPresets(ctx, commands, func(command presetCommand) ([]preset, error) {
		return command.list(ctx, projectDir)
	})
}

type cmakeVersion struct {
	major int
	minor int
}

type unusableCMakeCapabilitiesError struct {
	err error
}

func (e *unusableCMakeCapabilitiesError) Error() string {
	return e.err.Error()
}

func (e *unusableCMakeCapabilitiesError) Unwrap() error {
	return e.err
}

func (v cmakeVersion) atLeast(major int, minor int) bool {
	return v.major > major || v.major == major && v.minor >= minor
}

func queryCMakeVersion(
	ctx context.Context,
	binary string,
	projectDir string,
) (cmakeVersion, error) {
	// #nosec G204 -- binary is configured locally, never supplied over MCP.
	cmd := exec.CommandContext(ctx, binary, "-E", "capabilities")
	cmd.Dir = projectDir
	output, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return cmakeVersion{}, fmt.Errorf(
				"query CMake capabilities: %w",
				ctxErr,
			)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			details := strings.TrimSpace(string(exitErr.Stderr))
			if details == "" {
				return cmakeVersion{}, &unusableCMakeCapabilitiesError{
					err: fmt.Errorf(
						"CMake capabilities query failed: %w",
						exitErr,
					),
				}
			}
			return cmakeVersion{}, &unusableCMakeCapabilitiesError{
				err: fmt.Errorf(
					"CMake capabilities query failed: %s: %w",
					details,
					exitErr,
				),
			}
		}
		return cmakeVersion{}, fmt.Errorf(
			"start CMake capabilities query: %w",
			runner.MarkMissingTool(binary, err),
		)
	}
	version, err := parseCMakeCapabilitiesVersion(output)
	if err != nil {
		return cmakeVersion{}, &unusableCMakeCapabilitiesError{
			err: fmt.Errorf("parse CMake capabilities: %w", err),
		}
	}
	return version, nil
}

func parseCMakeCapabilitiesVersion(output []byte) (cmakeVersion, error) {
	var capabilities struct {
		Version *struct {
			Major *int `json:"major"`
			Minor *int `json:"minor"`
		} `json:"version"`
	}
	if err := json.Unmarshal(output, &capabilities); err != nil {
		return cmakeVersion{}, fmt.Errorf("decode capabilities JSON: %w", err)
	}
	if capabilities.Version == nil {
		return cmakeVersion{}, errors.New("capabilities JSON is missing version")
	}
	if capabilities.Version.Major == nil {
		return cmakeVersion{}, errors.New("capabilities JSON is missing version.major")
	}
	if capabilities.Version.Minor == nil {
		return cmakeVersion{}, errors.New("capabilities JSON is missing version.minor")
	}
	if *capabilities.Version.Major < 0 || *capabilities.Version.Minor < 0 {
		return cmakeVersion{}, errors.New("capabilities JSON has a negative version")
	}
	return cmakeVersion{
		major: *capabilities.Version.Major,
		minor: *capabilities.Version.Minor,
	}, nil
}

func presetCommandsForVersion(
	binary string,
	version cmakeVersion,
) []presetCommand {
	commands := presetCommandTable(binary, false)
	available := make([]presetCommand, 0, len(commands))
	for _, command := range commands {
		minimum := command.minimumVersion
		if version.atLeast(minimum.major, minimum.minor) {
			available = append(available, command)
		}
	}
	return available
}

func bestEffortPresetCommands(binary string) []presetCommand {
	return presetCommandTable(binary, true)
}

func presetCommandTable(binary string, bestEffort bool) []presetCommand {
	return []presetCommand{
		{
			kind:           configurePreset,
			binary:         binary,
			args:           []string{"--list-presets"},
			bestEffort:     bestEffort,
			minimumVersion: cmakeVersion{major: 3, minor: 19},
		},
		{
			kind:           buildPreset,
			binary:         binary,
			args:           []string{"--list-presets=build"},
			bestEffort:     bestEffort,
			minimumVersion: cmakeVersion{major: 3, minor: 20},
		},
		{
			kind:           testPreset,
			binary:         ctestBinary,
			args:           []string{"--list-presets"},
			bestEffort:     bestEffort,
			minimumVersion: cmakeVersion{major: 3, minor: 20},
		},
		{
			kind:           packagePreset,
			binary:         cpackBinary,
			args:           []string{"--list-presets"},
			bestEffort:     bestEffort,
			minimumVersion: cmakeVersion{major: 3, minor: 25},
		},
		{
			kind:           workflowPreset,
			binary:         binary,
			args:           []string{"--list-presets=workflow"},
			bestEffort:     bestEffort,
			minimumVersion: cmakeVersion{major: 3, minor: 25},
		},
	}
}

func collectPresets(
	ctx context.Context,
	commands []presetCommand,
	list func(presetCommand) ([]preset, error),
) ([]preset, error) {
	presets := make([]preset, 0)
	// A CMake installation does not have to ship ctest and cpack. Their absence
	// only removes the presets they own, so the configure and build presets are
	// still listed and the gap is reported as a warning instead of failing the
	// whole project.
	var missing error
	// Best-effort commands are used when CMake's capabilities output is unusable.
	// Their failures describe degraded discovery without making the project fail.
	var warning error
	// Preset families are independent, so one broken command must not hide the
	// presets exposed by commands that ran before or after it.
	var failure error
	for _, command := range commands {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return presets, presetCollectionCancellation(missing, warning, failure, ctxErr)
		}
		listed, err := list(command)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return presets, presetCollectionCancellation(missing, warning, failure, ctxErr)
		}
		if err != nil {
			if errors.Is(err, runner.ErrToolUnavailable) {
				missing = errors.Join(missing, err)
				continue
			}
			issue := fmt.Errorf(
				"list CMake presets for %s: %w",
				command.kind,
				err,
			)
			if command.bestEffort {
				warning = errors.Join(warning, runner.MarkWarning(issue))
			} else {
				failure = errors.Join(failure, issue)
			}
			continue
		}
		presets = append(presets, listed...)
	}
	return presets, errors.Join(missing, warning, failure)
}

func presetCollectionCancellation(
	missing error,
	warning error,
	failure error,
	ctxErr error,
) error {
	return errors.Join(
		missing,
		warning,
		failure,
		fmt.Errorf("list CMake presets: %w", ctxErr),
	)
}

type presetCommand struct {
	kind           string
	binary         string
	args           []string
	bestEffort     bool
	minimumVersion cmakeVersion
}

func (c presetCommand) list(ctx context.Context, projectDir string) ([]preset, error) {
	// #nosec G204 -- binary and preset file are local workspace configuration.
	cmd := exec.CommandContext(ctx, c.binary, c.args...)
	cmd.Dir = projectDir
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			details := strings.TrimSpace(string(exitErr.Stderr))
			if details == "" {
				return nil, fmt.Errorf("CMake preset listing failed: %w", exitErr)
			}
			return nil, fmt.Errorf(
				"CMake preset listing failed: %s: %w",
				details,
				exitErr,
			)
		}
		return nil, fmt.Errorf(
			"start CMake preset listing: %w",
			runner.MarkMissingTool(c.binary, err),
		)
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
			return "", "", fmt.Errorf(
				"task %q does not match CMake preset kind %q",
				task.ID,
				metadataKind,
			)
		}
	}
	if value, ok := task.Meta["preset"]; ok {
		metadataPreset, valid := value.(string)
		if !valid || metadataPreset == "" {
			return "", "", fmt.Errorf("task %q has an invalid CMake preset name", task.ID)
		}
		if metadataPreset != preset {
			return "", "", fmt.Errorf(
				"task %q does not match CMake preset %q",
				task.ID,
				metadataPreset,
			)
		}
	}
	return kind, preset, nil
}

func hasPresetFile(projectDir string) (bool, error) {
	_, err := runner.FindRegularFile(projectDir, presetsName, userPresetsName)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find CMake preset file in %q: %w", projectDir, err)
	}
	return true, nil
}
