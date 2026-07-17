// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package makerunner_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
	makerunner "github.com/palchukovsky/just-mcp-work/internal/runner/make"
)

func TestDetectRecognizesSupportedMakefileNames(t *testing.T) {
	for _, name := range []string{"GNUmakefile", "Makefile", "makefile"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeMakefile(t, dir, name)
			detected, err := makerunner.New("").Detect(dir)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if !detected {
				t.Fatalf("Detect did not find %s", name)
			}
		})
	}
}

func TestListTasksUsesMakeDatabase(t *testing.T) {
	requireMake(t)
	dir := t.TempDir()
	writeMakefile(t, dir, "Makefile")

	tasks, err := makerunner.New("").ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if got, want := taskIDs(tasks), []string{
		"make:all",
		"make:hello",
		"make:output.txt",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("task IDs = %#v, want %#v", got, want)
	}
	hello := taskID(t, tasks, "make:hello")
	if hello.Name != "hello" || hello.Meta["target"] != "hello" {
		t.Fatalf("hello task = %#v", hello)
	}
}

func TestBuildCommandExecutesMakeTarget(t *testing.T) {
	requireMake(t)
	dir := t.TempDir()
	writeMakefile(t, dir, "Makefile")
	r := makerunner.New("")
	tasks, err := r.ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	cmd, err := r.BuildCommand(context.Background(), dir, taskID(t, tasks, "make:hello"), nil)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run Make target: %v", err)
	}
	if got, want := strings.TrimSpace(string(output)), "hello from Make"; got != want {
		t.Fatalf("target output = %q, want %q", got, want)
	}
}

func TestBuildCommandRejectsMismatchedTargetMetadata(t *testing.T) {
	dir := t.TempDir()
	writeMakefile(t, dir, "Makefile")
	task := runner.Task{
		ID:     "make:hello",
		Runner: "make",
		Meta:   map[string]any{"target": "other"},
	}
	if _, err := makerunner.New("").BuildCommand(context.Background(), dir, task, nil); err == nil {
		t.Fatal("BuildCommand accepted mismatched Make target metadata")
	}
}

func writeMakefile(t *testing.T, dir string, name string) {
	t.Helper()
	contents := ".PHONY: all hello\n" +
		"all: hello\n" +
		"\n" +
		"hello:\n" +
		"\t@printf 'hello from Make\\n'\n" +
		"\n" +
		"output.txt:\n" +
		"\t@printf 'generated' > $@\n" +
		"\n" +
		"%.generated:\n" +
		"\t@printf 'pattern' > $@\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireMake(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
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
