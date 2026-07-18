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

func TestListTasksParsesLiteralTargetsWithoutMake(t *testing.T) {
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

func TestListTasksAcceptsMakeWhitespaceInEnvironmentDirectives(t *testing.T) {
	for _, test := range []struct {
		name      string
		directive string
	}{
		{name: "export with tab", directive: "export\tFOO\n"},
		{name: "unexport with mixed whitespace", directive: "unexport \t FOO\n"},
		{name: "undefine with tabs", directive: "undefine\t\tFOO\n"},
		{name: "vpath with mixed whitespace", directive: "vpath  \t%.c src\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			contents := test.directive + ".PHONY: hello\nhello:\n\t@echo hello\n"
			if err := os.WriteFile(
				filepath.Join(dir, "Makefile"),
				[]byte(contents),
				0o600,
			); err != nil {
				t.Fatal(err)
			}

			tasks, err := makerunner.New("").ListTasks(context.Background(), dir)
			if err != nil {
				t.Fatalf("ListTasks: %v", err)
			}
			if got, want := taskIDs(tasks), []string{"make:hello"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("task IDs = %#v, want %#v", got, want)
			}
		})
	}
}

func TestListTasksAcceptsMakeWhitespaceAfterAssignmentModifiers(t *testing.T) {
	for _, test := range []struct {
		name       string
		assignment string
	}{
		{name: "export with tab", assignment: "export\tFOO := value\n"},
		{name: "override with mixed whitespace", assignment: "override \t FOO := value\n"},
		{name: "private with tabs", assignment: "private\t\tFOO := value\n"},
		{
			name:       "multiple modifiers with mixed whitespace",
			assignment: "override \t private\texport  FOO := value\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			contents := test.assignment + ".PHONY: hello\nhello:\n\t@echo hello\n"
			if err := os.WriteFile(
				filepath.Join(dir, "Makefile"),
				[]byte(contents),
				0o600,
			); err != nil {
				t.Fatal(err)
			}

			tasks, err := makerunner.New("").ListTasks(context.Background(), dir)
			if err != nil {
				t.Fatalf("ListTasks: %v", err)
			}
			if got, want := taskIDs(tasks), []string{"make:hello"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("task IDs = %#v, want %#v", got, want)
			}
		})
	}
}

func TestListTasksDoesNotEvaluateMakefile(t *testing.T) {
	dir := t.TempDir()
	contents := "DISCOVERY_SIDE_EFFECT := $(shell echo side-effect > discovery-ran.txt)\n" +
		".PHONY: hello\n" +
		"hello:\n" +
		"\t@echo hello\n"
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	tasks, err := makerunner.New("").ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if got, want := taskIDs(tasks), []string{"make:hello"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("task IDs = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "discovery-ran.txt")); !os.IsNotExist(err) {
		t.Fatalf("Makefile evaluation created a marker: %v", err)
	}
}

func TestListTasksRejectsUnsafeIntrospection(t *testing.T) {
	for _, test := range []struct {
		name     string
		contents string
		error    string
	}{
		{
			name:     "dynamic target",
			contents: "TARGET := hello\n$(TARGET):\n\t@echo hello\n",
			error:    "dynamic or escaped targets are unsupported",
		},
		{
			name:     "wildcard target",
			contents: "*.generated:\n\t@echo generated\n",
			error:    "wildcard targets are unsupported",
		},
		{
			name:     "include",
			contents: "include tasks.mk\n",
			error:    "include directives are unsupported",
		},
		{
			name:     "conditional",
			contents: "ifeq ($(OS),Windows_NT)\nhello:\nendif\n",
			error:    "ifeq directives are unsupported",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(
				filepath.Join(dir, "Makefile"),
				[]byte(test.contents),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			_, err := makerunner.New("").ListTasks(context.Background(), dir)
			if err == nil || !strings.Contains(err.Error(), test.error) {
				t.Fatalf("ListTasks error = %v, want %q", err, test.error)
			}
		})
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
