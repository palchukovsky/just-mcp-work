// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
	"github.com/palchukovsky/just-mcp-work/internal/workspace"
)

// catalogRunner stands for a project with more tasks than any single answer
// should carry, including a private helper and a documented parameterized task.
type catalogRunner struct{}

func (catalogRunner) Name() string { return "catalog" }

func (catalogRunner) Detect(projectDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(projectDir, "justfile"))
	return err == nil, nil
}

const longTaskDescription = "Run the release gate: formatting, dependency metadata, strict lint, " +
	"vet, race-enabled tests, and the smoke flow, then report a single verdict for the whole tree.\n" +
	"The second line documents the exit codes."

func (catalogRunner) ListTasks(context.Context, string) ([]runner.Task, error) {
	tasks := []runner.Task{
		{Name: "build", Description: "Build every package in the module."},
		{Name: "check-debug", Description: "Run the debug gate."},
		{
			Name:        "check-debug-dev",
			Description: "Run the debug gate against the dev profile.",
			Params: []runner.Param{
				{Name: "target", Kind: runner.ParamSingular, Doc: "profile name"},
			},
		},
		{Name: "check-release", Description: longTaskDescription},
		{Name: "_helper", Description: "Internal helper.", Private: true},
		{Name: "_helper-debug", Description: "Internal DEBUG helper.", Private: true},
	}
	for index := range tasks {
		tasks[index].Runner = "catalog"
		tasks[index].ID = "catalog:" + tasks[index].Name
		tasks[index].Meta = map[string]any{
			"aliases": nil,
			"group":   "",
			// A real runner reports its whole module dump here, which is the
			// bulk a compact listing exists to drop.
			"modules": map[string]any{"nested": "a large runner-specific payload"},
		}
	}
	return tasks, nil
}

func (catalogRunner) BuildCommand(
	ctx context.Context,
	projectDir string,
	_ runner.Task,
	_ []string,
) (*exec.Cmd, error) {
	// #nosec G204,G702 -- the test re-executes its own fixed helper process.
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestMCPServerHelperProcess")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "JMW_TEST_HELPER_PROCESS=1")
	return cmd, nil
}

func newCatalogTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "justfile"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	runners, err := runner.NewRegistry(catalogRunner{})
	if err != nil {
		t.Fatal(err)
	}
	workspaceRegistry, err := workspace.NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(workspaceRegistry, runners, store, Config{
		Timeout:   5 * time.Second,
		Retention: time.Hour,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func listedTaskIDs(tasks []taskOutput) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func TestListTasksDescriptionExplainsCompactMode(t *testing.T) {
	description := listTasksDescription()
	for _, expected := range []string{
		"first 160 runes of the first description line",
		"drops runner metadata and run statistics",
	} {
		if !strings.Contains(description, expected) {
			t.Errorf("list_tasks description does not mention %q", expected)
		}
	}
}

// TestListTasksSelectorsSelectServerSide pins the matching semantics of every
// task selector, so an agent can trust one call instead of fetching the whole
// catalog and filtering it again on its own side.
func TestListTasksSelectorsSelectServerSide(t *testing.T) {
	server := newCatalogTestServer(t)
	for name, testCase := range map[string]struct {
		input        listTasksInput
		wantIDs      []string
		wantUnknown  []string
		wantPruned   taskPruned
		wantReturned int
	}{
		"legacy request returns the whole catalog": {
			input: listTasksInput{ProjectPath: "."},
			wantIDs: []string{
				"catalog:build",
				"catalog:check-debug",
				"catalog:check-debug-dev",
				"catalog:check-release",
				"catalog:_helper",
				"catalog:_helper-debug",
			},
			wantReturned: 6,
		},
		"one exact name": {
			input:        listTasksInput{ProjectPath: ".", Names: []string{"check-debug-dev"}},
			wantIDs:      []string{"catalog:check-debug-dev"},
			wantPruned:   taskPruned{Name: 5},
			wantReturned: 1,
		},
		"exact task ID": {
			input:        listTasksInput{ProjectPath: ".", Names: []string{"catalog:check-debug"}},
			wantIDs:      []string{"catalog:check-debug"},
			wantPruned:   taskPruned{Name: 5},
			wantReturned: 1,
		},
		"same task requested by name and ID": {
			input: listTasksInput{
				ProjectPath: ".",
				Names:       []string{"check-debug", "catalog:check-debug"},
			},
			wantIDs:      []string{"catalog:check-debug"},
			wantPruned:   taskPruned{Name: 5},
			wantReturned: 1,
		},
		"several exact names": {
			input: listTasksInput{
				ProjectPath: ".",
				Names:       []string{" check-debug ", "check-debug-dev", "check-debug"},
			},
			wantIDs:      []string{"catalog:check-debug", "catalog:check-debug-dev"},
			wantPruned:   taskPruned{Name: 4},
			wantReturned: 2,
		},
		"unknown exact name is reported next to the matches": {
			input: listTasksInput{
				ProjectPath: ".",
				Names:       []string{"check-debug", "check-nothing"},
			},
			wantIDs:      []string{"catalog:check-debug"},
			wantUnknown:  []string{"check-nothing"},
			wantPruned:   taskPruned{Name: 5},
			wantReturned: 1,
		},
		"no exact name matches": {
			input:       listTasksInput{ProjectPath: ".", Names: []string{"absent"}},
			wantIDs:     []string{},
			wantUnknown: []string{"absent"},
			wantPruned:  taskPruned{Name: 6},
		},
		"name prefix": {
			input:        listTasksInput{ProjectPath: ".", NamePrefix: "check-"},
			wantIDs:      []string{"catalog:check-debug", "catalog:check-debug-dev", "catalog:check-release"},
			wantPruned:   taskPruned{Name: 3},
			wantReturned: 3,
		},
		"name prefix is case sensitive": {
			input:      listTasksInput{ProjectPath: ".", NamePrefix: "Check-"},
			wantIDs:    []string{},
			wantPruned: taskPruned{Name: 6},
		},
		"query matches the name or the description without case": {
			input: listTasksInput{ProjectPath: ".", Query: "debug"},
			wantIDs: []string{
				"catalog:check-debug",
				"catalog:check-debug-dev",
				"catalog:_helper-debug",
			},
			wantPruned:   taskPruned{Name: 3},
			wantReturned: 3,
		},
		"public visibility drops the private helpers": {
			input: listTasksInput{ProjectPath: ".", Visibility: taskVisibilityPublic},
			wantIDs: []string{
				"catalog:build",
				"catalog:check-debug",
				"catalog:check-debug-dev",
				"catalog:check-release",
			},
			wantPruned:   taskPruned{Visibility: 2},
			wantReturned: 4,
		},
		"private visibility keeps only the private helpers": {
			input:        listTasksInput{ProjectPath: ".", Visibility: taskVisibilityPrivate},
			wantIDs:      []string{"catalog:_helper", "catalog:_helper-debug"},
			wantPruned:   taskPruned{Visibility: 4},
			wantReturned: 2,
		},
		"visibility and a selector narrow together": {
			input: listTasksInput{
				ProjectPath: ".",
				Query:       "debug",
				Visibility:  taskVisibilityPublic,
			},
			wantIDs:      []string{"catalog:check-debug", "catalog:check-debug-dev"},
			wantPruned:   taskPruned{Visibility: 2, Name: 2},
			wantReturned: 2,
		},
		"visibility does not make an exact name unknown": {
			input: listTasksInput{
				ProjectPath: ".",
				Names:       []string{"_helper"},
				Visibility:  taskVisibilityPublic,
			},
			wantIDs:    []string{},
			wantPruned: taskPruned{Visibility: 2, Name: 4},
		},
		"runner filter does not make an exact name unknown": {
			input:      listTasksInput{ProjectPath: ".", Runner: "absent", Names: []string{"build"}},
			wantIDs:    []string{},
			wantPruned: taskPruned{Runner: 6},
		},
	} {
		t.Run(name, func(t *testing.T) {
			result, output, err := server.listTasks(context.Background(), nil, testCase.input)
			if err != nil || result != nil || output.Error != nil {
				t.Fatalf("list_tasks = %#v, %#v, %v", result, output, err)
			}
			if got := listedTaskIDs(output.Tasks); !reflect.DeepEqual(got, testCase.wantIDs) {
				t.Fatalf("task IDs = %#v, want %#v", got, testCase.wantIDs)
			}
			if !reflect.DeepEqual(output.AppliedFilter.UnknownNames, testCase.wantUnknown) {
				t.Errorf(
					"unknown names = %#v, want %#v",
					output.AppliedFilter.UnknownNames,
					testCase.wantUnknown,
				)
			}
			if output.AppliedFilter.Pruned != testCase.wantPruned {
				t.Errorf("pruned = %#v, want %#v", output.AppliedFilter.Pruned, testCase.wantPruned)
			}
			if output.AppliedFilter.Returned != testCase.wantReturned {
				t.Errorf(
					"returned = %d, want %d",
					output.AppliedFilter.Returned,
					testCase.wantReturned,
				)
			}
			if output.AppliedFilter.Discovered != 6 {
				t.Errorf("discovered = %d, want 6", output.AppliedFilter.Discovered)
			}
		})
	}
}

// TestListTasksAppliedFilterReportsTheDefaults keeps the diagnostics of an
// unfiltered request explicit, the way list_projects reports its own defaults.
func TestListTasksAppliedFilterReportsTheDefaults(t *testing.T) {
	server := newCatalogTestServer(t)
	_, output, err := server.listTasks(context.Background(), nil, listTasksInput{ProjectPath: "."})
	if err != nil {
		t.Fatal(err)
	}
	want := appliedTaskFilterOutput{
		ProjectPath:     ".",
		Names:           []string{},
		Visibility:      taskVisibilityAll,
		Detail:          taskDetailFull,
		IncludeStats:    true,
		IncludeMetadata: true,
		DefaultsApplied: []string{
			"runner",
			"names",
			"name_prefix",
			"query",
			"visibility",
			"detail",
			"include_stats",
			"include_metadata",
		},
		Discovered: 6,
		Returned:   6,
	}
	if !reflect.DeepEqual(output.AppliedFilter, want) {
		t.Fatalf("applied filter = %#v, want %#v", output.AppliedFilter, want)
	}
}

// seedTaskRun gives a task the run history a full listing reports, so the two
// detail modes can be compared on a task that actually has statistics.
func seedTaskRun(t *testing.T, server *Server, taskID string) {
	t.Helper()
	_, receipt, err := server.runTask(context.Background(), nil, runTaskInput{
		ProjectPath: ".",
		TaskID:      taskID,
	})
	if err != nil || !receipt.OK {
		t.Fatalf("seed run = %#v, %v", receipt, err)
	}
}

// TestListTasksFullDetailKeepsItsShape holds the default listing on the answer
// it gave before server-side filtering existed.
func TestListTasksFullDetailKeepsItsShape(t *testing.T) {
	server := newCatalogTestServer(t)
	seedTaskRun(t, server, "catalog:check-debug-dev")
	_, full, err := server.listTasks(context.Background(), nil, listTasksInput{
		ProjectPath: ".",
		Names:       []string{"check-debug-dev"},
	})
	if err != nil || len(full.Tasks) != 1 {
		t.Fatalf("full list_tasks = %#v, %v", full, err)
	}
	if full.Tasks[0].Meta == nil || full.Tasks[0].Stats == nil {
		t.Fatalf("full task = %#v, want metadata and statistics", full.Tasks[0])
	}
	if full.Tasks[0].Description != "Run the debug gate against the dev profile." {
		t.Errorf("full description = %q", full.Tasks[0].Description)
	}
}

// TestListTasksCompactDetailStaysRunnable keeps the compact listing on the
// fields an agent needs to pick a task and to invoke it with its parameters.
func TestListTasksCompactDetailStaysRunnable(t *testing.T) {
	server := newCatalogTestServer(t)
	seedTaskRun(t, server, "catalog:check-debug-dev")
	_, compact, err := server.listTasks(context.Background(), nil, listTasksInput{
		ProjectPath: ".",
		Names:       []string{"check-debug-dev"},
		Detail:      taskDetailCompact,
	})
	if err != nil || len(compact.Tasks) != 1 {
		t.Fatalf("compact list_tasks = %#v, %v", compact, err)
	}
	task := compact.Tasks[0]
	if task.Meta != nil || task.Stats != nil {
		t.Fatalf("compact task = %#v, want no metadata and no statistics", task)
	}
	if task.ID != "catalog:check-debug-dev" ||
		task.Runner != "catalog" ||
		task.Name != "check-debug-dev" ||
		task.Private {
		t.Fatalf("compact task identity = %#v", task)
	}
	if len(task.Params) != 1 || task.Params[0].Name != "target" {
		t.Fatalf("compact parameters = %#v, want the parameters needed to invoke the task", task.Params)
	}
	_, receipt, err := server.runTask(context.Background(), nil, runTaskInput{
		ProjectPath: ".",
		TaskID:      task.ID,
		Arguments:   []string{"dev"},
	})
	if err != nil || !receipt.OK {
		t.Fatalf("run the compactly listed task = %#v, %v", receipt, err)
	}
}

// TestListTasksCompactExtrasAreOptIn keeps the bulky parts of a listing
// available without giving up the compact default.
func TestListTasksCompactExtrasAreOptIn(t *testing.T) {
	server := newCatalogTestServer(t)
	enabled := true
	_, output, err := server.listTasks(context.Background(), nil, listTasksInput{
		ProjectPath:     ".",
		Names:           []string{"check-debug"},
		Detail:          taskDetailCompact,
		IncludeMetadata: &enabled,
	})
	if err != nil || len(output.Tasks) != 1 {
		t.Fatalf("compact list_tasks = %#v, %v", output, err)
	}
	if output.Tasks[0].Meta == nil {
		t.Fatalf("opted-in metadata = %#v", output.Tasks[0])
	}
	if !output.AppliedFilter.IncludeMetadata || output.AppliedFilter.IncludeStats {
		t.Fatalf("applied filter = %#v", output.AppliedFilter)
	}
	if slices.Contains(output.AppliedFilter.DefaultsApplied, "include_metadata") {
		t.Errorf("defaults applied = %#v", output.AppliedFilter.DefaultsApplied)
	}

	disabled := false
	_, plain, err := server.listTasks(context.Background(), nil, listTasksInput{
		ProjectPath:     ".",
		Names:           []string{"check-debug"},
		IncludeMetadata: &disabled,
		IncludeStats:    &disabled,
	})
	if err != nil || len(plain.Tasks) != 1 {
		t.Fatalf("full list_tasks without extras = %#v, %v", plain, err)
	}
	if plain.Tasks[0].Meta != nil || plain.Tasks[0].Stats != nil {
		t.Fatalf("opted-out extras = %#v", plain.Tasks[0])
	}
}

// TestListTasksCompactShortensADescription keeps a long task doc from spending
// the context a compact listing was asked to save.
func TestListTasksCompactShortensADescription(t *testing.T) {
	server := newCatalogTestServer(t)
	_, output, err := server.listTasks(context.Background(), nil, listTasksInput{
		ProjectPath: ".",
		Names:       []string{"check-release"},
		Detail:      taskDetailCompact,
	})
	if err != nil || len(output.Tasks) != 1 {
		t.Fatalf("compact list_tasks = %#v, %v", output, err)
	}
	description := output.Tasks[0].Description
	if strings.Contains(description, "\n") || strings.Contains(description, "exit codes") {
		t.Fatalf("compact description keeps more than its first line: %q", description)
	}
	if !strings.HasSuffix(description, "...") ||
		len([]rune(description)) > compactDescriptionRunes+len("...") {
		t.Fatalf("compact description = %q", description)
	}
}

func TestShortTaskDescription(t *testing.T) {
	for name, testCase := range map[string]struct {
		description string
		want        string
	}{
		"empty":              {description: "", want: ""},
		"single line":        {description: "Build the module.", want: "Build the module."},
		"first line only":    {description: "Build the module.\nAnd more.", want: "Build the module."},
		"carriage return":    {description: "Build the module.\r\nAnd more.", want: "Build the module."},
		"multi-byte is kept": {description: strings.Repeat("ä", 10), want: strings.Repeat("ä", 10)},
		"truncated": {
			description: strings.Repeat("a", compactDescriptionRunes+10),
			want:        strings.Repeat("a", compactDescriptionRunes) + "...",
		},
		"trailing whitespace is trimmed before the ellipsis": {
			description: strings.Repeat("a", compactDescriptionRunes-1) + "\t" + strings.Repeat("b", 10),
			want:        strings.Repeat("a", compactDescriptionRunes-1) + "...",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := shortTaskDescription(testCase.description); got != testCase.want {
				t.Fatalf("shortTaskDescription = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestListTasksRejectsContradictorySelectors keeps an ambiguous request an
// explicit error instead of a silently guessed listing.
func TestListTasksRejectsContradictorySelectors(t *testing.T) {
	server := newCatalogTestServer(t)
	for name, testCase := range map[string]struct {
		input   listTasksInput
		message string
	}{
		"names and prefix": {
			input: listTasksInput{
				ProjectPath: ".",
				Names:       []string{"check-debug"},
				NamePrefix:  "check-",
			},
			message: "names and name_prefix must not be combined; use one task selector per request",
		},
		"names and query": {
			input:   listTasksInput{ProjectPath: ".", Names: []string{"check-debug"}, Query: "check"},
			message: "names and query must not be combined; use one task selector per request",
		},
		"prefix and query": {
			input:   listTasksInput{ProjectPath: ".", NamePrefix: "check-", Query: "check"},
			message: "name_prefix and query must not be combined; use one task selector per request",
		},
		"every selector": {
			input: listTasksInput{
				ProjectPath: ".",
				Names:       []string{"check-debug"},
				NamePrefix:  "check-",
				Query:       "check",
			},
			message: "names and name_prefix and query must not be combined; " +
				"use one task selector per request",
		},
		"empty names": {
			input:   listTasksInput{ProjectPath: ".", Names: []string{}},
			message: "names must not be empty",
		},
		"blank name": {
			input:   listTasksInput{ProjectPath: ".", Names: []string{"check-debug", "  "}},
			message: "names must not contain a blank entry",
		},
		"blank prefix": {
			input:   listTasksInput{ProjectPath: ".", NamePrefix: "  "},
			message: "name_prefix must not be blank",
		},
		"blank query": {
			input:   listTasksInput{ProjectPath: ".", Query: " "},
			message: "query must not be blank",
		},
		"unknown visibility": {
			input:   listTasksInput{ProjectPath: ".", Visibility: "hidden"},
			message: "visibility must be public, private, or all",
		},
		"unknown detail": {
			input:   listTasksInput{ProjectPath: ".", Detail: "short"},
			message: "detail must be compact or full",
		},
	} {
		t.Run(name, func(t *testing.T) {
			result, output, err := server.listTasks(context.Background(), nil, testCase.input)
			if err != nil {
				t.Fatalf("list_tasks returned a transport error: %v", err)
			}
			if result == nil || !result.IsError || output.Error == nil {
				t.Fatalf("invalid request = %#v, %#v", result, output)
			}
			if output.Error.Message != testCase.message {
				t.Fatalf("error = %q, want %q", output.Error.Message, testCase.message)
			}
			if len(output.Tasks) != 0 {
				t.Fatalf("invalid request returned tasks: %#v", output.Tasks)
			}
			if output.AppliedFilter.ProjectPath != testCase.input.ProjectPath ||
				output.AppliedFilter.Visibility != taskVisibilityAll ||
				output.AppliedFilter.Detail != taskDetailFull {
				t.Fatalf("invalid request lost its applied filter: %#v", output.AppliedFilter)
			}
		})
	}
}

// TestListTasksRunnerFilterStillNarrowsTheIssues keeps the runner filter and
// the new task selectors independent.
func TestListTasksRunnerFilterStillNarrowsTheIssues(t *testing.T) {
	server := newCatalogTestServer(t)
	_, output, err := server.listTasks(context.Background(), nil, listTasksInput{
		ProjectPath: ".",
		Runner:      "catalog",
		NamePrefix:  "check-debug",
		Detail:      taskDetailCompact,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"catalog:check-debug", "catalog:check-debug-dev"}
	if got := listedTaskIDs(output.Tasks); !reflect.DeepEqual(got, want) {
		t.Fatalf("task IDs = %#v, want %#v", got, want)
	}
	if output.AppliedFilter.Runner != "catalog" ||
		slices.Contains(output.AppliedFilter.DefaultsApplied, "runner") {
		t.Fatalf("applied filter = %#v", output.AppliedFilter)
	}
	if output.AppliedFilter.Discovered != 6 || output.AppliedFilter.Returned != 2 {
		t.Fatalf("applied filter counters = %#v", output.AppliedFilter)
	}
}

// TestListTasksReportsTheFilterOfAFailedLookup keeps the diagnostics readable
// when the project itself cannot be resolved.
func TestListTasksReportsTheFilterOfAFailedLookup(t *testing.T) {
	server := newCatalogTestServer(t)
	result, output, err := server.listTasks(context.Background(), nil, listTasksInput{
		ProjectPath: "../outside",
		Names:       []string{"check-debug"},
	})
	if err != nil || result == nil || !result.IsError || output.Error == nil {
		t.Fatalf("list_tasks outside the workspace = %#v, %#v, %v", result, output, err)
	}
	if !reflect.DeepEqual(output.AppliedFilter.Names, []string{"check-debug"}) {
		t.Fatalf("applied filter = %#v", output.AppliedFilter)
	}
}
