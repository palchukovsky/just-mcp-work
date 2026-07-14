// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package just implements the just task runner.
package just

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

const runnerName = "just"

// Runner executes tasks using the installed just binary.
type Runner struct {
	binary string
}

// New constructs a just runner. An empty binary uses "just" from PATH.
func New(binary string) *Runner {
	if binary == "" {
		binary = runnerName
	}
	return &Runner{binary: binary}
}

func (*Runner) Name() string { return runnerName }

// RunnerVersion reports the installed just version for run metadata.
func (r *Runner) RunnerVersion(ctx context.Context) (string, error) {
	// #nosec G204 -- binary is configured locally, never supplied over MCP.
	output, err := exec.CommandContext(ctx, r.binary, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("get just version: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Detect reports whether a supported justfile exists in projectDir.
func (*Runner) Detect(projectDir string) (bool, error) {
	_, err := findJustfile(projectDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// ListTasks uses just's JSON dump instead of parsing a justfile itself.
func (r *Runner) ListTasks(ctx context.Context, projectDir string) ([]runner.Task, error) {
	dump, err := r.dump(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	tasks, err := tasksFromDump(dump)
	if err != nil {
		return nil, err
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})
	return tasks, nil
}

// IncludedProjectDirs returns directories of justfiles reached through imports
// or modules, so their tasks are exposed only through the parent project.
func (r *Runner) IncludedProjectDirs(ctx context.Context, projectDir string) ([]string, error) {
	dump, err := r.dump(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	sources := make(map[string]struct{})
	collectSources(dump, sources)
	dirs := make([]string, 0, len(sources))
	for source := range sources {
		dirs = append(dirs, filepath.Dir(source))
	}
	sort.Strings(dirs)
	return dirs, nil
}

func (r *Runner) dump(ctx context.Context, projectDir string) (justDump, error) {
	justfile, err := findJustfile(projectDir)
	if err != nil {
		return justDump{}, err
	}

	// #nosec G204 -- binary and justfile are local workspace configuration.
	cmd := exec.CommandContext(ctx, r.binary, "-f", justfile, "--dump", "--dump-format", "json")
	cmd.Dir = projectDir
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return justDump{}, fmt.Errorf("just dump: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return justDump{}, fmt.Errorf("start just dump: %w", err)
	}

	var dump justDump
	if err := json.Unmarshal(output, &dump); err != nil {
		return justDump{}, fmt.Errorf("decode just dump: %w", err)
	}
	return dump, nil
}

func collectSources(dump justDump, sources map[string]struct{}) {
	if dump.Source != "" {
		sources[filepath.Clean(dump.Source)] = struct{}{}
	}
	for _, raw := range dump.Modules {
		var module justDump
		if json.Unmarshal(raw, &module) == nil {
			collectSources(module, sources)
		}
	}
}

func tasksFromDump(dump justDump) ([]runner.Task, error) {
	aliases := aliasesByTarget(dump.Aliases)
	names := sortedKeys(dump.Recipes)
	tasks := make([]runner.Task, 0, len(names))
	for _, name := range names {
		recipe := dump.Recipes[name]
		params := make([]runner.Param, 0, len(recipe.Parameters))
		for _, param := range recipe.Parameters {
			kind := runner.ParamKind(param.Kind)
			switch kind {
			case runner.ParamSingular, runner.ParamPlus, runner.ParamStar:
			default:
				kind = runner.ParamSingular
			}
			params = append(params, runner.Param{
				Name:    param.Name,
				Kind:    kind,
				Default: param.Default,
				Doc:     stringOrEmpty(param.Help),
			})
		}

		group := attributeString(recipe.Attributes, "group")
		if group == "" {
			group = groupForRecipe(dump.Groups, recipe.Name)
		}
		namepath := recipe.Namepath
		if namepath == "" {
			namepath = joinNamepath(dump.ModulePath, recipe.Name)
		}
		metadata := map[string]any{
			"aliases":  aliases[recipe.Name],
			"confirm":  hasAttribute(recipe.Attributes, "confirm"),
			"group":    group,
			"modules":  jsonValue(dump.Modules),
			"namepath": namepath,
		}
		tasks = append(tasks, runner.Task{
			ID:          runnerName + ":" + namepath,
			Runner:      runnerName,
			Name:        recipe.Name,
			Description: stringOrEmpty(recipe.Doc),
			Params:      params,
			Private:     recipe.Private,
			Meta:        metadata,
		})
	}

	for _, moduleName := range sortedModuleNames(dump.Modules) {
		var moduleDump justDump
		if err := json.Unmarshal(dump.Modules[moduleName], &moduleDump); err != nil {
			return nil, fmt.Errorf(
				"decode just module %q: %w",
				joinNamepath(dump.ModulePath, moduleName),
				err,
			)
		}
		moduleTasks, err := tasksFromDump(moduleDump)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, moduleTasks...)
	}
	return tasks, nil
}

// BuildCommand creates an argv-only invocation of the real just binary.
func (r *Runner) BuildCommand(
	ctx context.Context,
	projectDir string,
	task runner.Task,
	args []string,
) (*exec.Cmd, error) {
	prefix := runnerName + ":"
	if task.Runner != runnerName || !strings.HasPrefix(task.ID, prefix) {
		return nil, fmt.Errorf("task %q does not belong to the %s runner", task.ID, runnerName)
	}
	justfile, err := findJustfile(projectDir)
	if err != nil {
		return nil, err
	}
	namepath := strings.TrimPrefix(task.ID, prefix)
	if namepath == "" {
		return nil, fmt.Errorf("task %q has an empty just namepath", task.ID)
	}
	if value, ok := task.Meta["namepath"]; ok {
		metadataNamepath, valid := value.(string)
		if !valid || metadataNamepath == "" {
			return nil, fmt.Errorf("task %q has an invalid just namepath", task.ID)
		}
		if metadataNamepath != namepath {
			return nil, fmt.Errorf("task %q does not match just namepath %q", task.ID, metadataNamepath)
		}
	}
	argv := []string{"-f", justfile, "--", namepath}
	argv = append(argv, args...)
	// #nosec G204 -- task and argv come from discovered runner metadata, not a shell.
	cmd := exec.CommandContext(ctx, r.binary, argv...)
	cmd.Dir = projectDir
	return cmd, nil
}

func findJustfile(projectDir string) (string, error) {
	for _, name := range []string{"justfile", "Justfile", ".justfile"} {
		path := filepath.Join(projectDir, name)
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return path, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect justfile: %w", err)
		}
	}
	return "", os.ErrNotExist
}

//nolint:govet // Field order mirrors the just JSON dump for readable decoding.
type justDump struct {
	Aliases map[string]struct {
		Target string `json:"target"`
	} `json:"aliases"`
	Groups     json.RawMessage            `json:"groups"`
	ModulePath string                     `json:"module_path"`
	Modules    map[string]json.RawMessage `json:"modules"`
	Recipes    map[string]justRecipe      `json:"recipes"`
	Source     string                     `json:"source"`
}

type justRecipe struct {
	Attributes []json.RawMessage `json:"attributes"`
	Doc        *string           `json:"doc"`
	Name       string            `json:"name"`
	Namepath   string            `json:"namepath"`
	Parameters []struct {
		Default *string `json:"default"`
		Help    *string `json:"help"`
		Kind    string  `json:"kind"`
		Name    string  `json:"name"`
	} `json:"parameters"`
	Private bool `json:"private"`
}

func aliasesByTarget(aliases map[string]struct {
	Target string "json:\"target\""
}) map[string][]string {
	result := make(map[string][]string)
	for name, alias := range aliases {
		result[alias.Target] = append(result[alias.Target], name)
	}
	for _, names := range result {
		sort.Strings(names)
	}
	return result
}

func groupForRecipe(raw json.RawMessage, recipe string) string {
	var groups []struct {
		Name    string   `json:"name"`
		Recipes []string `json:"recipes"`
	}
	if json.Unmarshal(raw, &groups) == nil {
		for _, group := range groups {
			if slices.Contains(group.Recipes, recipe) {
				return group.Name
			}
		}
	}
	var byName map[string]json.RawMessage
	if json.Unmarshal(raw, &byName) == nil {
		for name, group := range byName {
			if strings.Contains(string(group), "\""+recipe+"\"") {
				return name
			}
		}
	}
	return ""
}

func jsonValue(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return string(raw)
	}
	return decoded
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedModuleNames(modules map[string]json.RawMessage) []string {
	return sortedKeys(modules)
}

func joinNamepath(parts ...string) string {
	nonempty := parts[:0]
	for _, part := range parts {
		if part != "" {
			nonempty = append(nonempty, part)
		}
	}
	return strings.Join(nonempty, "::")
}

func hasAttribute(attributes []json.RawMessage, wanted string) bool {
	for _, attribute := range attributes {
		var name string
		if json.Unmarshal(attribute, &name) == nil && name == wanted {
			return true
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(attribute, &object) == nil {
			if _, ok := object[wanted]; ok {
				return true
			}
		}
	}
	return false
}

func attributeString(attributes []json.RawMessage, wanted string) string {
	for _, attribute := range attributes {
		var object map[string]string
		if json.Unmarshal(attribute, &object) == nil {
			if value, ok := object[wanted]; ok {
				return value
			}
		}
	}
	return ""
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
