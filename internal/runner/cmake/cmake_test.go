// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package cmake_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
	cmakerunner "github.com/palchukovsky/just-mcp-work/internal/runner/cmake"
)

func TestDetectRecognizesOnlyRegularCMakeLists(t *testing.T) {
	dir := t.TempDir()
	r := cmakerunner.New("")

	detected, err := r.Detect(dir)
	if err != nil {
		t.Fatalf("Detect without CMakeLists.txt: %v", err)
	}
	if detected {
		t.Fatal("Detect found a project without CMakeLists.txt")
	}

	writeCMakeProject(t, dir)
	detected, err = r.Detect(dir)
	if err != nil {
		t.Fatalf("Detect with CMakeLists.txt: %v", err)
	}
	if !detected {
		t.Fatal("Detect did not find CMakeLists.txt")
	}
}

func TestListTasksMapsCMakePresets(t *testing.T) {
	requireCMake(t)
	dir := t.TempDir()
	writeCMakeProject(t, dir)
	writePresets(t, dir)

	tasks, err := cmakerunner.New("").ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if got, want := taskIDs(tasks), []string{
		"cmake:build:debug",
		"cmake:configure:debug",
		"cmake:package:debug",
		"cmake:test:debug",
		"cmake:workflow:all",
	}; !sameStrings(got, want) {
		t.Fatalf("task IDs = %#v, want %#v", got, want)
	}
	build := taskID(t, tasks, "cmake:build:debug")
	if build.Name != "debug" ||
		build.Meta["kind"] != "build" ||
		build.Meta["preset"] != "debug" {
		t.Fatalf("build task = %#v", build)
	}
}

func TestListTasksReturnsNoTasksWithoutPresets(t *testing.T) {
	dir := t.TempDir()
	writeCMakeProject(t, dir)

	tasks, err := cmakerunner.New("").ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("tasks without presets = %#v", tasks)
	}
}

func TestBuildCommandExecutesCMakeBuildPreset(t *testing.T) {
	requireCMake(t)
	dir := t.TempDir()
	writeCMakeProject(t, dir)
	writePresets(t, dir)
	r := cmakerunner.New("")
	tasks, err := r.ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}

	configure, err := r.BuildCommand(
		context.Background(),
		dir,
		taskID(t, tasks, "cmake:configure:debug"),
		nil,
	)
	if err != nil {
		t.Fatalf("BuildCommand(configure): %v", err)
	}
	if output, runErr := configure.CombinedOutput(); runErr != nil {
		t.Fatalf("run configure preset: %v\n%s", runErr, output)
	}

	build, err := r.BuildCommand(
		context.Background(),
		dir,
		taskID(t, tasks, "cmake:build:debug"),
		nil,
	)
	if err != nil {
		t.Fatalf("BuildCommand(build): %v", err)
	}
	output, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("run build preset: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "hello from CMake") {
		t.Fatalf("build output = %q", output)
	}
}

func TestBuildCommandRejectsMismatchedPresetMetadata(t *testing.T) {
	dir := t.TempDir()
	writeCMakeProject(t, dir)
	task := runner.Task{
		ID:     "cmake:build:debug",
		Runner: "cmake",
		Meta:   map[string]any{"preset": "release"},
	}
	if _, err := cmakerunner.New("").BuildCommand(context.Background(), dir, task, nil); err == nil {
		t.Fatal("BuildCommand accepted mismatched CMake preset metadata")
	}
}

func TestBuildCommandSelectsCMakePresetTool(t *testing.T) {
	dir := t.TempDir()
	writeCMakeProject(t, dir)
	r := cmakerunner.New("")
	tests := []struct {
		name string
		task runner.Task
		want []string
	}{
		{
			name: "test",
			task: runner.Task{ID: "cmake:test:debug", Runner: "cmake"},
			want: []string{"ctest", "--preset", "debug"},
		},
		{
			name: "package",
			task: runner.Task{ID: "cmake:package:debug", Runner: "cmake"},
			want: []string{"cpack", "--preset", "debug"},
		},
		{
			name: "workflow",
			task: runner.Task{ID: "cmake:workflow:all", Runner: "cmake"},
			want: []string{"cmake", "--workflow", "--preset", "all"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd, err := r.BuildCommand(context.Background(), dir, test.task, nil)
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			if !sameStrings(cmd.Args, test.want) {
				t.Fatalf("command arguments = %#v, want %#v", cmd.Args, test.want)
			}
		})
	}
}

func writeCMakeProject(t *testing.T, dir string) {
	t.Helper()
	contents := "cmake_minimum_required(VERSION 3.23)\n" +
		"project(cmake_runner_fixture NONE)\n" +
		"add_custom_target(greeting COMMAND ${CMAKE_COMMAND} -E echo hello from CMake)\n" +
		"enable_testing()\n" +
		"add_test(NAME smoke COMMAND ${CMAKE_COMMAND} -E true)\n"
	if err := os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writePresets(t *testing.T, dir string) {
	t.Helper()
	contents := `{
  "version": 6,
  "configurePresets": [
    {
      "name": "debug",
      "binaryDir": "${sourceDir}/build/${presetName}"
    }
  ],
  "buildPresets": [
    {
      "name": "debug",
      "configurePreset": "debug",
      "targets": ["greeting"]
    }
  ],
  "testPresets": [
    {
      "name": "debug",
      "configurePreset": "debug"
    }
  ],
  "packagePresets": [
    {
      "name": "debug",
      "configurePreset": "debug"
    }
  ],
  "workflowPresets": [
    {
      "name": "all",
      "steps": [
        {"type": "configure", "name": "debug"},
        {"type": "build", "name": "debug"},
        {"type": "test", "name": "debug"}
      ]
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(dir, "CMakePresets.json"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireCMake(t *testing.T) {
	t.Helper()
	for _, binary := range []string{"cmake", "ctest", "cpack"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is not installed", binary)
		}
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

func sameStrings(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
