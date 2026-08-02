// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package cmake_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestListTasksMapsConfiguredBuildTargets(t *testing.T) {
	requireCMake(t)
	if _, err := exec.LookPath("ninja"); err != nil {
		t.Skip("ninja is not installed")
	}

	dir := t.TempDir()
	writeCMakeProject(t, dir)
	addCMakeTargetSubdirectory(t, dir)
	buildDir := filepath.Join(dir, "build", "Debug")
	configure := exec.CommandContext(
		t.Context(),
		"cmake",
		"-S",
		dir,
		"-B",
		buildDir,
		"-G",
		"Ninja",
	)
	if output, err := configure.CombinedOutput(); err != nil {
		t.Fatalf("configure CMake build tree: %v\n%s", err, output)
	}

	r := cmakerunner.New("")
	tasks, err := r.ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	target := taskID(t, tasks, "cmake:target:build%2FDebug:greeting")
	if target.Name != "greeting" ||
		target.Meta["kind"] != "target" ||
		target.Meta["build_dir"] != "build/Debug" ||
		target.Meta["target"] != "greeting" {
		t.Fatalf("build target task = %#v", target)
	}
	_ = taskID(t, tasks, "cmake:target:build%2FDebug:widget")
	for _, task := range tasks {
		switch task.Name {
		case "widget.txt", "widget.stamp", "lib/widget.txt", "lib/widget.stamp":
			t.Fatalf("generated output leaked into task list: %#v", task)
		case "lib/all", "lib/install", "lib/test":
			t.Fatalf("per-directory target leaked into task list: %#v", task)
		}
	}

	command, err := r.BuildCommand(context.Background(), dir, target, nil)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run CMake target: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "hello from CMake") {
		t.Fatalf("target output = %q", output)
	}
}

func TestListTasksKeepsConfiguredTargetsWhenPresetListingFails(t *testing.T) {
	binary, executableErr := os.Executable()
	if executableErr != nil {
		t.Fatal(executableErr)
	}
	// The test binary rejects CMake's capabilities and family-listing flags.
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	writeCMakeProject(t, dir)
	writeConfiguredNinjaTree(t, dir, "alpha")
	if err := os.WriteFile(
		filepath.Join(dir, "CMakePresets.json"),
		// The file enables preset discovery; the fake binary owns the failure.
		[]byte("{\n  \"version\": 6\n}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	tasks, err := cmakerunner.New(binary).ListTasks(context.Background(), dir)
	warning, failure := runner.SplitIssues(err)
	const degradedText = "CMake capabilities query failed"
	if warning == nil || !strings.Contains(warning.Error(), degradedText) {
		t.Fatalf("ListTasks warning = %v, want degraded preset discovery", warning)
	}
	if failure != nil {
		t.Fatalf("ListTasks failure = %v, want none", failure)
	}
	_ = taskID(t, tasks, "cmake:target:build%2FDebug:alpha")
}

func TestListTasksFailsWhenVersionedPresetListingFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires POSIX shell fixtures")
	}
	dir := t.TempDir()
	writeCMakeProject(t, dir)
	writePresets(t, dir)
	writeConfiguredNinjaTree(t, dir, "alpha")

	binaryDir := t.TempDir()
	binary := filepath.Join(binaryDir, "cmake")
	writeExecutableFixture(t, binary, `#!/bin/sh
case "$1" in
-E)
	printf '%s\n' '{"version":{"major":3,"minor":25}}'
	;;
--list-presets)
	printf '%s\n' 'Available configure presets:' '  "versioned-configure"'
	;;
--list-presets=build)
	printf '%s\n' 'versioned build listing broke' >&2
	exit 7
	;;
--list-presets=workflow)
	printf '%s\n' 'Available workflow presets:' '  "versioned-workflow"'
	;;
*)
	printf '%s\n' "unexpected arguments: $*" >&2
	exit 8
	;;
esac
`)
	writeExecutableFixture(t, filepath.Join(binaryDir, "ctest"), `#!/bin/sh
printf '%s\n' 'Available test presets:' '  "versioned-test"'
`)
	writeExecutableFixture(t, filepath.Join(binaryDir, "cpack"), `#!/bin/sh
printf '%s\n' 'Available package presets:' '  "versioned-package"'
`)
	t.Setenv("PATH", binaryDir)

	tasks, err := cmakerunner.New(binary).ListTasks(context.Background(), dir)
	warning, failure := runner.SplitIssues(err)
	if warning != nil {
		t.Fatalf("ListTasks warning = %v, want none", warning)
	}
	if failure == nil ||
		!strings.Contains(failure.Error(), "list CMake presets for build") ||
		!strings.Contains(failure.Error(), "versioned build listing broke") {
		t.Fatalf("ListTasks failure = %v, want contextual build preset failure", failure)
	}
	_ = taskID(t, tasks, "cmake:target:build%2FDebug:alpha")
}

// TestListTasksDoesNotRegenerateConfiguredBuildTree sabotages the project file
// of a configured checkout and then lists it. The fixture carries presets on
// purpose, so listing really does start CMake: without that, listing would
// spawn no CMake at all and the sabotage could never fire.
func TestListTasksDoesNotRegenerateConfiguredBuildTree(t *testing.T) {
	requireCMake(t)
	if _, err := exec.LookPath("ninja"); err != nil {
		t.Skip("ninja is not installed")
	}

	dir := t.TempDir()
	writeCMakeProject(t, dir)
	writePresets(t, dir)
	buildDir := filepath.Join(dir, "build", "Debug")
	configure := exec.CommandContext(
		t.Context(),
		"cmake",
		"-S",
		dir,
		"-B",
		buildDir,
		"-G",
		"Ninja",
	)
	if output, err := configure.CombinedOutput(); err != nil {
		t.Fatalf("configure CMake build tree: %v\n%s", err, output)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "CMakeLists.txt"),
		[]byte("message(FATAL_ERROR \"listing regenerated the project\")\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	tasks, err := cmakerunner.New("").ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks regenerated CMake: %v", err)
	}
	// The preset task proves CMake ran over the sabotaged checkout, and the
	// target task proves the configured tree was read without regenerating it.
	_ = taskID(t, tasks, "cmake:configure:debug")
	_ = taskID(t, tasks, "cmake:target:build%2FDebug:greeting")
}

func TestListTasksIgnoresBuildTreeForAnotherProject(t *testing.T) {
	dir := t.TempDir()
	writeCMakeProject(t, dir)
	buildDir := filepath.Join(dir, "build", "Debug")
	if err := os.MkdirAll(buildDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cache := "CMAKE_HOME_DIRECTORY:INTERNAL=" + filepath.Join(dir, "other") + "\n"
	if err := os.WriteFile(filepath.Join(buildDir, "CMakeCache.txt"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}

	tasks, err := cmakerunner.New("jmw-absent-cmake-fixture").ListTasks(
		context.Background(),
		dir,
	)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("tasks from foreign build tree = %#v", tasks)
	}
}

// TestListTasksReportsMissingCMakeAsWarning keeps a checkout with presets
// usable on a host that lacks the CMake family: the gap belongs to the machine,
// not to the project, so it must not turn the project into an error.
func TestListTasksReportsMissingCMakeAsWarning(t *testing.T) {
	dir := t.TempDir()
	writeCMakeProject(t, dir)
	writePresets(t, dir)

	_, err := cmakerunner.New("jmw-absent-cmake-fixture").ListTasks(context.Background(), dir)
	warning, failure := runner.SplitIssues(err)
	if warning == nil || !errors.Is(warning, runner.ErrToolUnavailable) ||
		!errors.Is(warning, exec.ErrNotFound) {
		t.Fatalf("ListTasks warning = %v, want missing CMake binary", warning)
	}
	if failure != nil {
		t.Fatalf("ListTasks failure = %v, want none", failure)
	}
}

func TestListTasksWarnsWhenCMakeHasNoPresetSupport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires a POSIX shell fixture")
	}
	dir := t.TempDir()
	writeCMakeProject(t, dir)
	writePresets(t, dir)
	binaryDir := t.TempDir()
	callsPath := filepath.Join(t.TempDir(), "preset-family-calls")
	binary := filepath.Join(binaryDir, "cmake")
	writeExecutableFixture(t, binary, `#!/bin/sh
if [ "$1" = "-E" ] && [ "$2" = "capabilities" ]; then
	printf '%s\n' '{"version":{"major":3,"minor":18}}'
	exit 0
fi
printf '%s\n' "$0 $*" >> "$JMW_CMAKE_CALLS"
exit 1
`)
	familyBinary := `#!/bin/sh
printf '%s\n' "$0 $*" >> "$JMW_CMAKE_CALLS"
exit 1
`
	writeExecutableFixture(t, filepath.Join(binaryDir, "ctest"), familyBinary)
	writeExecutableFixture(t, filepath.Join(binaryDir, "cpack"), familyBinary)
	t.Setenv("PATH", binaryDir)
	t.Setenv("JMW_CMAKE_CALLS", callsPath)

	tasks, err := cmakerunner.New(binary).ListTasks(context.Background(), dir)
	warning, failure := runner.SplitIssues(err)
	if warning == nil ||
		!strings.Contains(warning.Error(), "CMake 3.18 has no preset support") {
		t.Fatalf("ListTasks warning = %v, want unsupported CMake 3.18 warning", warning)
	}
	if failure != nil {
		t.Fatalf("ListTasks failure = %v, want none", failure)
	}
	if len(tasks) != 0 {
		t.Fatalf("ListTasks tasks = %#v, want none", tasks)
	}
	if _, statErr := os.Stat(callsPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("preset family calls marker error = %v, want not exist", statErr)
	}
}

func TestListTasksFallsBackToBestEffortPresetDiscovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires POSIX shell fixtures")
	}
	dir := t.TempDir()
	writeCMakeProject(t, dir)
	writePresets(t, dir)
	binaryDir := t.TempDir()
	binary := filepath.Join(binaryDir, "cmake")
	writeExecutableFixture(t, binary, `#!/bin/sh
case "$1" in
-E)
	printf '%s\n' 'wrapper banner' '{"version":{"major":3,"minor":25}}'
	;;
--list-presets)
	printf '%s\n' 'Available configure presets:' '  "fallback-configure"'
	;;
--list-presets=build)
	printf '%s\n' 'build listing broke' >&2
	exit 7
	;;
--list-presets=workflow)
	printf '%s\n' 'Available workflow presets:' '  "fallback-workflow"'
	;;
*)
	printf '%s\n' "unexpected arguments: $*" >&2
	exit 8
	;;
esac
`)
	writeExecutableFixture(t, filepath.Join(binaryDir, "ctest"), `#!/bin/sh
printf '%s\n' 'Available test presets:' '  "fallback-test"'
`)
	writeExecutableFixture(t, filepath.Join(binaryDir, "cpack"), `#!/bin/sh
printf '%s\n' 'Available package presets:' '  "fallback-package"'
`)
	t.Setenv("PATH", binaryDir)

	tasks, err := cmakerunner.New(binary).ListTasks(context.Background(), dir)
	warning, failure := runner.SplitIssues(err)
	if warning == nil ||
		!strings.Contains(warning.Error(), "CMake capabilities query failed") ||
		!strings.Contains(warning.Error(), "listing all preset families best-effort") ||
		!strings.Contains(warning.Error(), "list CMake presets for build") ||
		!strings.Contains(warning.Error(), "build listing broke") {
		t.Fatalf("ListTasks warning = %v, want capabilities and build warnings", warning)
	}
	if failure != nil {
		t.Fatalf("ListTasks failure = %v, want none", failure)
	}
	if got, want := taskIDs(tasks), []string{
		"cmake:configure:fallback-configure",
		"cmake:package:fallback-package",
		"cmake:test:fallback-test",
		"cmake:workflow:fallback-workflow",
	}; !sameStrings(got, want) {
		t.Fatalf("task IDs = %#v, want %#v", got, want)
	}
}

func TestListTasksDoesNotListPresetFamiliesWhenCMakeIsMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires POSIX shell fixtures")
	}
	dir := t.TempDir()
	writeCMakeProject(t, dir)
	writePresets(t, dir)
	binaryDir := t.TempDir()
	callsPath := filepath.Join(t.TempDir(), "preset-family-calls")
	familyBinary := `#!/bin/sh
printf '%s\n' "$0 $*" >> "$JMW_CMAKE_CALLS"
`
	writeExecutableFixture(t, filepath.Join(binaryDir, "ctest"), familyBinary)
	writeExecutableFixture(t, filepath.Join(binaryDir, "cpack"), familyBinary)
	t.Setenv("PATH", binaryDir)
	t.Setenv("JMW_CMAKE_CALLS", callsPath)

	tasks, err := cmakerunner.New(
		filepath.Join(binaryDir, "missing-cmake"),
	).ListTasks(context.Background(), dir)
	warning, failure := runner.SplitIssues(err)
	if warning == nil || !errors.Is(warning, runner.ErrToolUnavailable) ||
		!errors.Is(warning, os.ErrNotExist) {
		t.Fatalf("ListTasks warning = %v, want missing CMake binary", warning)
	}
	if failure != nil {
		t.Fatalf("ListTasks failure = %v, want none", failure)
	}
	if len(tasks) != 0 {
		t.Fatalf("ListTasks tasks = %#v, want none", tasks)
	}
	if _, statErr := os.Stat(callsPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("preset family calls marker error = %v, want not exist", statErr)
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

func TestBuildCommandRejectsInvalidTargetMetadata(t *testing.T) {
	dir := t.TempDir()
	writeCMakeProject(t, dir)
	r := cmakerunner.New("")

	tests := []struct {
		task runner.Task
		name string
	}{
		{
			name: "escaping build directory",
			task: runner.Task{
				ID:     "cmake:target:..:greeting",
				Runner: "cmake",
			},
		},
		{
			name: "non-string target metadata",
			task: runner.Task{
				ID:     "cmake:target:build%2FDebug:greeting",
				Runner: "cmake",
				Meta:   map[string]any{"kind": []string{"target"}},
			},
		},
		{
			name: "mismatched build directory",
			task: runner.Task{
				ID:     "cmake:target:build%2FDebug:greeting",
				Runner: "cmake",
				Meta:   map[string]any{"build_dir": "build/Release"},
			},
		},
		{
			name: "mismatched target",
			task: runner.Task{
				ID:     "cmake:target:build%2FDebug:greeting",
				Runner: "cmake",
				Meta:   map[string]any{"target": "other"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := r.BuildCommand(
				context.Background(),
				dir,
				test.task,
				nil,
			); err == nil {
				t.Fatalf("BuildCommand accepted task %#v", test.task)
			}
		})
	}
}

func TestBuildCommandPlacesArgumentsBeforeConfiguredTarget(t *testing.T) {
	dir := t.TempDir()
	task := runner.Task{
		ID:     "cmake:target:build%2FDebug:greeting",
		Runner: "cmake",
	}
	targetPath := filepath.Join(dir, "build", "Debug")
	r := cmakerunner.New("cmake")
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "no arguments",
			want: []string{
				"cmake", "--build", targetPath, "--target", "greeting",
			},
		},
		{
			name: "options",
			args: []string{"-j2", "--verbose"},
			want: []string{
				"cmake", "--build", targetPath, "-j2", "--verbose", "--target", "greeting",
			},
		},
		{
			name: "native tool arguments",
			args: []string{"-j2", "--", "-v"},
			want: []string{
				"cmake", "--build", targetPath, "-j2", "--target", "greeting", "--", "-v",
			},
		},
		{
			name: "empty native tool arguments",
			args: []string{"-j2", "--"},
			want: []string{
				"cmake", "--build", targetPath, "-j2", "--target", "greeting", "--",
			},
		},
		{
			name: "second separator belongs to native tool",
			args: []string{"-j2", "--", "-v", "--", "-d"},
			want: []string{
				"cmake", "--build", targetPath, "-j2", "--target", "greeting", "--", "-v", "--", "-d",
			},
		},
		{
			name: "leading separator",
			args: []string{"--", "-v"},
			want: []string{
				"cmake", "--build", targetPath, "--target", "greeting", "--", "-v",
			},
		},
		{
			name: "only separator",
			args: []string{"--"},
			want: []string{
				"cmake", "--build", targetPath, "--target", "greeting", "--",
			},
		},
		{
			// CMake has allowed -j/--parallel without a job count since 3.12.
			// A following option is not consumed as that optional value, and this
			// case pins the boundary.
			name: "option with an optional value",
			args: []string{"-j"},
			want: []string{
				"cmake", "--build", targetPath, "-j", "--target", "greeting",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage := make([]string, len(test.args)+4)
			copy(storage, test.args)
			copy(storage[len(test.args):], []string{
				"tail-sentinel-0",
				"tail-sentinel-1",
				"tail-sentinel-2",
				"tail-sentinel-3",
			})
			wantStorage := append([]string(nil), storage...)
			args := storage[:len(test.args)]
			cmd, err := r.BuildCommand(context.Background(), dir, task, args)
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			if !sameStrings(cmd.Args, test.want) {
				t.Fatalf("command arguments = %#v, want %#v", cmd.Args, test.want)
			}
			if !sameStrings(args, test.args) {
				t.Fatalf("caller arguments = %#v, want %#v", args, test.args)
			}
			if !sameStrings(storage, wantStorage) {
				t.Fatalf("caller argument storage = %#v, want %#v", storage, wantStorage)
			}
		})
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

func writeConfiguredNinjaTree(t *testing.T, dir string, target string) {
	t.Helper()
	buildDir := filepath.Join(dir, "build", "Debug")
	if err := os.MkdirAll(buildDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cache := "CMAKE_HOME_DIRECTORY:INTERNAL=" + dir + "\n" +
		"CMAKE_GENERATOR:INTERNAL=Ninja\n"
	if err := os.WriteFile(
		filepath.Join(buildDir, "CMakeCache.txt"),
		[]byte(cache),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(buildDir, "build.ninja"),
		[]byte("build "+target+": CXX_EXECUTABLE_LINKER__"+target+"_\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func addCMakeTargetSubdirectory(t *testing.T, dir string) {
	t.Helper()
	rootPath := filepath.Join(dir, "CMakeLists.txt")
	rootContents, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	rootContents = append(rootContents, "add_subdirectory(lib)\n"...)
	// #nosec G703 -- rootPath stays inside the test temporary directory.
	if err := os.WriteFile(rootPath, rootContents, 0o600); err != nil {
		t.Fatal(err)
	}

	libraryDir := filepath.Join(dir, "lib")
	if err := os.Mkdir(libraryDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- libraryDir stays inside the test temporary directory.
	if err := os.WriteFile(
		filepath.Join(libraryDir, "CMakeLists.txt"),
		[]byte(
			"add_custom_command(\n"+
				"  OUTPUT widget.txt widget.stamp\n"+
				"  COMMAND ${CMAKE_COMMAND} -E touch widget.txt widget.stamp\n"+
				")\n"+
				"add_custom_target(widget DEPENDS widget.txt widget.stamp)\n",
		),
		0o600,
	); err != nil {
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

func writeExecutableFixture(t *testing.T, filePath string, contents string) {
	t.Helper()
	// #nosec G306 -- the fixed test fixture must be executable.
	if err := os.WriteFile(filePath, []byte(contents), 0o700); err != nil {
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
