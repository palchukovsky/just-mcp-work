// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package just_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
	justrunner "github.com/palchukovsky/just-mcp-work/internal/runner/just"
)

func TestListTasksMapsRealDump(t *testing.T) {
	requireJust(t)

	r := justrunner.New("")
	root := filepath.Join("..", "..", "..", "testdata", "workspace")
	rootTasks, err := r.ListTasks(context.Background(), filepath.Join(root, "root"))
	if err != nil {
		t.Fatalf("ListTasks(root): %v", err)
	}
	hello := taskNamed(t, rootTasks, "hello")
	if hello.ID != "just:hello" || hello.Runner != "just" {
		t.Fatalf("unexpected task identity: %#v", hello)
	}
	if len(hello.Params) != 1 ||
		hello.Params[0].Kind != runner.ParamSingular ||
		hello.Params[0].Default == nil ||
		*hello.Params[0].Default != "world" {
		t.Fatalf("unexpected hello parameters: %#v", hello.Params)
	}
	aliases, ok := hello.Meta["aliases"].([]string)
	if !ok || !reflect.DeepEqual(aliases, []string{"h"}) {
		t.Fatalf("unexpected aliases metadata: %#v", hello.Meta["aliases"])
	}
	if !taskNamed(t, rootTasks, "private-task").Private {
		t.Fatal("private-task was not marked private")
	}

	nestedTasks, err := r.ListTasks(context.Background(), filepath.Join(root, "nested"))
	if err != nil {
		t.Fatalf("ListTasks(nested): %v", err)
	}
	if got := taskNamed(t, nestedTasks, "many").Params[0].Kind; got != runner.ParamPlus {
		t.Fatalf("many parameter kind = %q, want plus", got)
	}
	if got := taskNamed(t, nestedTasks, "optional").Params[0].Kind; got != runner.ParamStar {
		t.Fatalf("optional parameter kind = %q, want star", got)
	}
}

func TestListTasksMapsObjectAttributesFromRealDump(t *testing.T) {
	requireJust(t)

	dir := t.TempDir()
	justfile := "[group('checks')]\n[confirm]\ncheck:\n    @echo checked\n"
	if err := os.WriteFile(filepath.Join(dir, "justfile"), []byte(justfile), 0o600); err != nil {
		t.Fatal(err)
	}
	tasks, err := justrunner.New("").ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	check := taskNamed(t, tasks, "check")
	if got := check.Meta["confirm"]; got != true {
		t.Fatalf("confirm metadata = %#v, want true", got)
	}
	if got := check.Meta["group"]; got != "checks" {
		t.Fatalf("group metadata = %#v, want checks", got)
	}
}

//nolint:govet // The test keeps each decoded metadata assertion adjacent to its fixture.
func TestListTasksDiscoversModulesFromRealDump(t *testing.T) {
	requireJust(t)

	dir := filepath.Join("..", "..", "..", "testdata", "workspace", "modules")
	tasks, err := justrunner.New("").ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if got, want := taskIDs(tasks), []string{
		"just:root",
		"just:tools::check",
		"just:tools::nested::nested-task",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("task IDs = %#v, want %#v", got, want)
	}

	check := taskID(t, tasks, "just:tools::check")
	if check.Name != "check" {
		t.Fatalf("module task name = %q, want check", check.Name)
	}
	if got := check.Meta["namepath"]; got != "tools::check" {
		t.Fatalf("namepath metadata = %#v, want tools::check", got)
	}
	if got := check.Meta["aliases"]; !reflect.DeepEqual(got, []string{"c"}) {
		t.Fatalf("aliases metadata = %#v, want []string{\"c\"}", got)
	}
	if got := check.Meta["confirm"]; got != true {
		t.Fatalf("confirm metadata = %#v, want true", got)
	}
	if got := check.Meta["group"]; got != "quality" {
		t.Fatalf("group metadata = %#v, want quality", got)
	}
	modules, ok := check.Meta["modules"].(map[string]any)
	if !ok {
		t.Fatalf("modules metadata = %#v, want object", check.Meta["modules"])
	}
	if _, ok := modules["nested"]; !ok {
		t.Fatalf("modules metadata = %#v, want nested module", modules)
	}

	rootTask := taskID(t, tasks, "just:root")
	rootModules, ok := rootTask.Meta["modules"].(map[string]any)
	if !ok {
		t.Fatalf("root modules metadata = %#v, want object", rootTask.Meta["modules"])
	}
	if _, ok := rootModules["tools"]; !ok {
		t.Fatalf("root modules metadata = %#v, want tools module", rootModules)
	}
}

func TestIncludedProjectDirsUsesDumpSources(t *testing.T) {
	requireJust(t)

	dir := filepath.Join("..", "..", "..", "testdata", "workspace", "included")
	dirs, err := justrunner.New("").IncludedProjectDirs(context.Background(), dir)
	if err != nil {
		t.Fatalf("IncludedProjectDirs: %v", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dirs, []string{absDir, filepath.Join(absDir, "nested")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("included directories = %#v, want %#v", got, want)
	}
}

func TestBuildCommandExecutesModuleRecipe(t *testing.T) {
	requireJust(t)

	dir := filepath.Join("..", "..", "..", "testdata", "workspace", "modules")
	r := justrunner.New("")
	tasks, err := r.ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	task := taskID(t, tasks, "just:tools::nested::nested-task")
	cmd, err := r.BuildCommand(context.Background(), dir, task, []string{"from-module"})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run module command: %v", err)
	}
	if got, want := strings.TrimSpace(string(output)), "from-module"; got != want {
		t.Fatalf("module output = %q, want %q", got, want)
	}
}

func TestBuildCommandRejectsMismatchedNamepath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "justfile"),
		[]byte("safe:\n    @echo safe\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	task := runner.Task{
		ID:     "just:safe",
		Runner: "just",
		Name:   "safe",
		Meta:   map[string]any{"namepath": "other"},
	}
	if _, err := justrunner.New("").BuildCommand(context.Background(), dir, task, nil); err == nil {
		t.Fatal("BuildCommand accepted a task ID that did not match its namepath")
	}
}

func requireJust(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("just"); err != nil {
		t.Skip("just is not installed")
	}
}

func taskNamed(t *testing.T, tasks []runner.Task, name string) runner.Task {
	t.Helper()
	for _, task := range tasks {
		if task.Name == name {
			return task
		}
	}
	t.Fatalf("task %q not found in %#v", name, tasks)
	return runner.Task{}
}

func taskID(t *testing.T, tasks []runner.Task, id string) runner.Task {
	t.Helper()
	for _, task := range tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("task ID %q not found in %#v", id, tasks)
	return runner.Task{}
}

func taskIDs(tasks []runner.Task) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}
