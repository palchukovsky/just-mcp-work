// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package gorunner_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
	gorunner "github.com/palchukovsky/just-mcp-work/internal/runner/go"
)

func TestRegistrationDeclaresExactReviewedInitPrompt(t *testing.T) {
	catalog, err := runner.NewCatalog(gorunner.Registration(""))
	if err != nil {
		t.Fatal(err)
	}
	requests := catalog.PermissionRequests()
	if len(requests) != 1 {
		t.Fatalf("permission requests = %#v", requests)
	}
	request := requests[0]
	if request.Name != "go" || !request.Reviewed || request.Default != runner.ModeSafe ||
		request.Question != "Choose Go command access." {
		t.Fatalf("Go permission request = %#v", request)
	}
	want := []runner.PermissionChoice{
		{
			Mode:  runner.ModeSafe,
			Label: "Reduced access (safe default)",
			Description: "Allow only go build ./..., go test ./..., go vet ./..., and " +
				"go mod download. These fixed tasks reject all caller arguments.",
			Warning: "Runner modes reduce exposed commands; they are not a sandbox. go test " +
				"executes code from the checkout, Go may run toolchains and helper programs, and " +
				"go mod download may use the network and write the module cache.",
		},
		{
			Mode:        runner.ModeAll,
			Label:       "All commands",
			Description: "Add go fmt ./..., go mod tidy, and go:any to the safe commands.",
			Warning: "Includes every safe-mode risk and also exposes go:any, which forwards " +
				"arbitrary Go argv; exec and tool hooks can execute external programs as your user.",
		},
		{
			Mode:        runner.ModeDisabled,
			Label:       "Disabled",
			Description: "Do not expose the Go runner or any Go tasks.",
		},
	}
	if !reflect.DeepEqual(request.Choices, want) {
		t.Fatalf("Go permission choices = %#v, want %#v", request.Choices, want)
	}
}

func TestDetectAcceptsOnlyRegularGoMod(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)

	detected, err := gorunner.New("").Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !detected {
		t.Fatal("Detect did not find go.mod")
	}

	linkDir := t.TempDir()
	if symlinkErr := os.Symlink(filepath.Join(dir, "go.mod"), filepath.Join(linkDir, "go.mod")); symlinkErr != nil {
		t.Skipf("symlinks unavailable: %v", symlinkErr)
	}
	detected, err = gorunner.New("").Detect(linkDir)
	if err != nil {
		t.Fatalf("Detect symlink: %v", err)
	}
	if detected {
		t.Fatal("Detect accepted a symlinked go.mod")
	}
}

func TestListTasksReturnsModeTaskSets(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	tests := []struct {
		name string
		mode runner.Mode
		want []string
	}{
		{
			name: "safe",
			mode: runner.ModeSafe,
			want: []string{"go:build", "go:test", "go:vet", "go:mod:download"},
		},
		{
			name: "all",
			mode: runner.ModeAll,
			want: []string{
				"go:build",
				"go:test",
				"go:vet",
				"go:mod:download",
				"go:fmt",
				"go:mod:tidy",
				"go:any",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tasks := listTasks(t, resolvedGoRunner(t, test.mode), dir)
			if got := taskIDs(tasks); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("task IDs = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSafeBuildCommandUsesExactFixedArgv(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	r := resolvedGoRunner(t, runner.ModeSafe)
	tasks := listTasks(t, r, dir)
	tests := []struct {
		name string
		id   string
		want []string
	}{
		{name: "build", id: "go:build", want: []string{"go", "build", "./..."}},
		{name: "test", id: "go:test", want: []string{"go", "test", "./..."}},
		{name: "vet", id: "go:vet", want: []string{"go", "vet", "./..."}},
		{name: "mod download", id: "go:mod:download", want: []string{"go", "mod", "download"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd, err := r.BuildCommand(
				context.Background(),
				dir,
				taskID(t, tasks, test.id),
				nil,
			)
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			if got := cmd.Args; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("command args = %#v, want %#v", got, test.want)
			}
			if cmd.Dir != dir {
				t.Fatalf("command dir = %q, want %q", cmd.Dir, dir)
			}
		})
	}
}

func TestAllBuildCommandUsesExactFixedArgv(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	r := resolvedGoRunner(t, runner.ModeAll)
	wantByID := map[string][]string{
		"go:build":        {"go", "build", "./..."},
		"go:test":         {"go", "test", "./..."},
		"go:vet":          {"go", "vet", "./..."},
		"go:mod:download": {"go", "mod", "download"},
		"go:fmt":          {"go", "fmt", "./..."},
		"go:mod:tidy":     {"go", "mod", "tidy"},
	}
	built := 0
	for _, task := range listTasks(t, r, dir) {
		if task.ID == "go:any" {
			continue
		}
		want, found := wantByID[task.ID]
		if !found {
			t.Fatalf("fixed all-mode task %q has no argv expectation", task.ID)
		}
		cmd, err := r.BuildCommand(context.Background(), dir, task, nil)
		if err != nil {
			t.Fatalf("BuildCommand(%s): %v", task.ID, err)
		}
		if !reflect.DeepEqual(cmd.Args, want) {
			t.Fatalf("command args for %s = %#v, want %#v", task.ID, cmd.Args, want)
		}
		built++
	}
	if built != len(wantByID) {
		t.Fatalf("built %d fixed all-mode tasks, want %d", built, len(wantByID))
	}
}

func TestFixedTasksRejectEveryCallerArgument(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	for _, mode := range []runner.Mode{runner.ModeSafe, runner.ModeAll} {
		r := resolvedGoRunner(t, mode)
		for _, task := range listTasks(t, r, dir) {
			if task.ID == "go:any" {
				continue
			}
			if _, err := r.BuildCommand(
				context.Background(),
				dir,
				task,
				[]string{"-x"},
			); err == nil {
				t.Fatalf("%s in %s mode accepted caller arguments", task.ID, mode)
			}
		}
	}
}

func TestAllModeAnyForwardsNonEmptyArgvExactly(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	r := resolvedGoRunner(t, runner.ModeAll)
	task := taskID(t, listTasks(t, r, dir), "go:any")
	if !strings.Contains(strings.ToLower(task.Description), "risky") || task.Meta["risk"] == nil {
		t.Fatalf("go:any does not expose its risk: %#v", task)
	}
	if _, err := r.BuildCommand(context.Background(), dir, task, nil); err == nil {
		t.Fatal("go:any accepted empty argv")
	}
	args := []string{"test", "-exec", "custom tool", "./pkg/..."}
	cmd, err := r.BuildCommand(context.Background(), dir, task, args)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	want := append([]string{"go"}, args...)
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("command args = %#v, want %#v", cmd.Args, want)
	}
}

func TestSafeModeRejectsHiddenAndTamperedTasksAtExecution(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	safe := resolvedGoRunner(t, runner.ModeSafe)
	allTasks := listTasks(t, resolvedGoRunner(t, runner.ModeAll), dir)
	for _, id := range []string{"go:fmt", "go:mod:tidy", "go:any"} {
		if _, err := safe.BuildCommand(
			context.Background(),
			dir,
			taskID(t, allTasks, id),
			nil,
		); err == nil {
			t.Fatalf("safe mode executed hidden task %q", id)
		}
	}
	safeTask := taskID(t, listTasks(t, safe, dir), "go:build")
	safeTask.Meta["command"] = "fmt ./..."
	if _, err := safe.BuildCommand(context.Background(), dir, safeTask, nil); err == nil {
		t.Fatal("safe mode accepted tampered task metadata")
	}
}

func TestBuildCommandRejectsUnsupportedOrMismatchedTask(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	r := resolvedGoRunner(t, runner.ModeSafe)

	for name, task := range map[string]runner.Task{
		"foreign runner":  {ID: "make:build", Runner: "make"},
		"unknown command": {ID: "go:run", Runner: "go"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := r.BuildCommand(context.Background(), dir, task, nil); err == nil {
				t.Fatal("BuildCommand accepted an invalid task")
			}
		})
	}
}

func TestListTasksWarnsWhenGoBinaryIsMissing(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	r := resolvedGoRunner(t, runner.ModeSafe, "jmw-absent-go-fixture")
	tasks, err := r.ListTasks(context.Background(), dir)
	if len(tasks) != 0 {
		t.Fatalf("tasks = %#v, want none", tasks)
	}
	warning, failure := runner.SplitIssues(err)
	if warning == nil || !errors.Is(warning, runner.ErrToolUnavailable) || failure != nil {
		t.Fatalf("ListTasks issue = warning %v, failure %v", warning, failure)
	}
}

func resolvedGoRunner(t *testing.T, mode runner.Mode, binary ...string) runner.Runner {
	t.Helper()
	configuredBinary := ""
	if len(binary) > 0 {
		configuredBinary = binary[0]
	}
	catalog, err := runner.NewCatalog(gorunner.Registration(configuredBinary))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := catalog.Resolve([]runner.Selection{{Name: "go", Mode: mode}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, found := registry.Get("go")
	if !found {
		t.Fatal("Go runner is absent")
	}
	return resolved
}

func listTasks(t *testing.T, r runner.Runner, dir string) []runner.Task {
	t.Helper()
	tasks, err := r.ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	return tasks
}

func writeGoMod(t *testing.T, dir string) {
	t.Helper()
	contents := "module example.com/project\n\n" + "go 1.25.0\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func taskID(t *testing.T, tasks []runner.Task, id string) runner.Task {
	t.Helper()
	for _, task := range tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("task %q not found in %#v", id, tasks)
	return runner.Task{}
}

func taskIDs(tasks []runner.Task) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}
