// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package cmake

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

func TestParseBuildTargetsKeepsLogicalCMakeTargets(t *testing.T) {
	manifest := strings.Join([]string{
		"build cmake_object_order_depends_target_app: phony || CMakeFiles/app.dir",
		"build CMakeFiles/app.dir/main.o: CXX_COMPILER__app_ main.cpp",
		"build app: CXX_EXECUTABLE_LINKER__app_ CMakeFiles/app.dir/main.o",
		"build libfoo.a: CXX_STATIC_LIBRARY_LINKER__foo_ CMakeFiles/foo.dir/foo.o",
		"build foo: phony libfoo.a",
		"build CMakeCache.txt build.ninja: RERUN_CMAKE CMakeLists.txt",
		"build edit_cache: phony CMakeFiles/edit_cache.util",
		"build rebuild_cache: phony CMakeFiles/rebuild_cache.util",
		"build list_install_components: phony",
		"build install: phony CMakeFiles/install.util",
		"build clean: CLEAN",
		"build help: HELP",
		"build Continuous: phony",
		"build ContinuousBuild: phony",
		"build ExperimentalTest: phony",
		"build NightlySubmit: phony",
		"build all: phony app $",
		"    foo",
	}, "\n")

	targets, err := parseBuildTargets(
		context.Background(),
		strings.NewReader(manifest),
	)
	if err != nil {
		t.Fatalf("parseBuildTargets: %v", err)
	}
	want := []string{"all", "app", "clean", "foo", "install"}
	if !sameTargetNames(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestCTestDashboardTargetRecognizesGeneratedNames(t *testing.T) {
	for name, want := range map[string]bool{
		"ContinuousBuild":      true,
		"ExperimentalMemCheck": true,
		"NightlyMemCheck":      true,
		"NightlyMemoryCheck":   true,
		"ContinuousDone":       false,
		"NightlyDone":          false,
	} {
		if actual := ctestDashboardTarget(name); actual != want {
			t.Errorf("ctestDashboardTarget(%q) = %t, want %t", name, actual, want)
		}
	}
}

func TestParseBuildTargetsKeepsOnlyTargetBehindAll(t *testing.T) {
	manifest := strings.Join([]string{
		"build app: CXX_EXECUTABLE_LINKER__app_ main.o",
		"build all: phony app",
	}, "\n")

	targets, err := parseBuildTargets(
		context.Background(),
		strings.NewReader(manifest),
	)
	if err != nil {
		t.Fatalf("parseBuildTargets: %v", err)
	}
	want := []string{"all", "app"}
	if !sameTargetNames(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestParseBuildTargetsKeepsDeclaredTargetDependencies(t *testing.T) {
	manifest := strings.Join([]string{
		"build cmake_object_order_depends_target_app: phony || .",
		"build app: future_cmake_link_rule app.o",
		"build bundle: phony CMakeFiles/bundle app",
		"build CMakeFiles/bundle | ${cmake_ninja_workdir}CMakeFiles/bundle: phony app || app",
	}, "\n")

	targets, err := parseBuildTargets(
		context.Background(),
		strings.NewReader(manifest),
	)
	if err != nil {
		t.Fatalf("parseBuildTargets: %v", err)
	}
	want := []string{"app", "bundle"}
	if !sameTargetNames(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestParseBuildTargetsDropsMultiOutputCustomCommandArtifacts(t *testing.T) {
	manifest := strings.Join([]string{
		"build widget: phony CMakeFiles/widget generated.txt second.txt",
		"build CMakeFiles/widget | ${cmake_ninja_workdir}CMakeFiles/widget: phony generated.txt second.txt",
		"build generated.txt second.txt | ${cmake_ninja_workdir}generated.txt ${cmake_ninja_workdir}second.txt: future_cmake_custom_command",
	}, "\n")

	targets, err := parseBuildTargets(
		context.Background(),
		strings.NewReader(manifest),
	)
	if err != nil {
		t.Fatalf("parseBuildTargets: %v", err)
	}
	if !sameTargetNames(targets, []string{"widget"}) {
		t.Fatalf("targets = %#v, want widget", targets)
	}
}

func TestParseBuildTargetsDropsArtifactOnlyAlias(t *testing.T) {
	manifest := strings.Join([]string{
		"build lib/libwidget.a: CXX_STATIC_LIBRARY_LINKER__widget_ widget.o",
		"build widget: phony lib/libwidget.a",
		"build libwidget.a: phony lib/libwidget.a",
	}, "\n")

	targets, err := parseBuildTargets(
		context.Background(),
		strings.NewReader(manifest),
	)
	if err != nil {
		t.Fatalf("parseBuildTargets: %v", err)
	}
	if !sameTargetNames(targets, []string{"widget"}) {
		t.Fatalf("targets = %#v, want widget", targets)
	}
}

func TestParseBuildTargetsDoesNotDependOnCMakeRuleNames(t *testing.T) {
	targets, err := parseBuildTargets(
		context.Background(),
		strings.NewReader("build app: future_cmake_link_rule main.o\n"),
	)
	if err != nil {
		t.Fatalf("parseBuildTargets: %v", err)
	}
	if !sameTargetNames(targets, []string{"app"}) {
		t.Fatalf("targets = %#v, want app", targets)
	}
}

func TestParseBuildTargetsDropsOrderOnlyGeneratedOutput(t *testing.T) {
	manifest := strings.Join([]string{
		"build generated.h: future_cmake_custom_command",
		"build app: future_cmake_link_rule main.o",
		"build prepare_app: phony || generated.h",
		"build all: phony app prepare_app",
	}, "\n")

	targets, err := parseBuildTargets(
		context.Background(),
		strings.NewReader(manifest),
	)
	if err != nil {
		t.Fatalf("parseBuildTargets: %v", err)
	}
	want := []string{"all", "app", "prepare_app"}
	if !sameTargetNames(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

// TestParseBuildTargetsKeepsOnlyProjectWideSlashTargets draws the line for the
// slash-bearing names CMake generates: the project-wide ones are worth a task,
// while the per-directory copies of "all", "install", "package", and "test"
// only repeat them once per subdirectory and would swamp the task list.
func TestParseBuildTargetsKeepsOnlyProjectWideSlashTargets(t *testing.T) {
	manifest := strings.Join([]string{
		"build install/local: phony",
		"build install/strip: phony",
		"build install/parallel: phony",
		"build test_prep/smoke: phony",
		"build lib/all: phony",
		"build lib/install: phony",
		"build lib/install/local: phony",
		"build lib/install/strip: phony",
		"build lib/package: phony",
		"build lib/test: phony",
		"build generated/header.h: future_cmake_custom_command",
	}, "\n")

	targets, err := parseBuildTargets(
		context.Background(),
		strings.NewReader(manifest),
	)
	if err != nil {
		t.Fatalf("parseBuildTargets: %v", err)
	}
	want := []string{
		"install/local",
		"install/parallel",
		"install/strip",
		"test_prep/smoke",
	}
	if !sameTargetNames(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestParseBuildTargetsHandlesNinjaEscapes(t *testing.T) {
	manifest := strings.Join([]string{
		"build phantom$ target: phony dependency",
		"build target$:debug: phony dependency",
		"build literal$$cash: phony dependency",
		"build real: phony input$: odd",
	}, "\n")

	targets, err := parseBuildTargets(
		context.Background(),
		strings.NewReader(manifest),
	)
	if err != nil {
		t.Fatalf("parseBuildTargets: %v", err)
	}
	want := []string{"real", "target:debug"}
	if !sameTargetNames(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestParseBuildTargetsAcceptsLongLines(t *testing.T) {
	manifest := "build giant: phony " + strings.Repeat("dependency ", 7000)
	targets, err := parseBuildTargets(
		context.Background(),
		strings.NewReader(manifest),
	)
	if err != nil {
		t.Fatalf("parseBuildTargets: %v", err)
	}
	if !sameTargetNames(targets, []string{"giant"}) {
		t.Fatalf("targets = %#v, want giant", targets)
	}
}

func TestParseBuildTargetsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := parseBuildTargets(ctx, strings.NewReader("build app: phony\n"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parseBuildTargets error = %v, want context.Canceled", err)
	}
}

func TestListBuildTargetsSupportsNinjaMultiConfig(t *testing.T) {
	dir := t.TempDir()
	writeInternalCMakeProject(t, dir)
	writeInternalBuildTree(t, dir, ninjaMultiConfigGenerator)

	targets, err := New("jmw-absent-cmake-fixture").listBuildTargets(
		context.Background(),
		dir,
	)
	if err != nil {
		t.Fatalf("listBuildTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].name != "app" || targets[0].buildDir != "build/Debug" {
		t.Fatalf("targets = %#v", targets)
	}
}

func TestListBuildTargetsWarnsForUnreadableConfirmedBuildTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not deny reads on Windows")
	}
	dir := t.TempDir()
	writeInternalCMakeProject(t, dir)
	writeInternalBuildTree(t, dir, ninjaGenerator)
	blockedDir := filepath.Join(dir, "build", "Blocked")
	if err := os.Rename(filepath.Join(dir, "build", "Debug"), blockedDir); err != nil {
		t.Fatal(err)
	}
	writeInternalBuildTree(t, dir, ninjaGenerator)
	blockedPath := filepath.Join(blockedDir, ninjaBuildFileName)
	if err := os.Chmod(blockedPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(blockedPath, 0o600); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			t.Errorf("restore Ninja build file mode: %v", err)
		}
	})
	if file, openErr := os.Open(blockedPath); openErr == nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		t.Skip("file permissions are not enforced for this process")
	} else if !errors.Is(openErr, fs.ErrPermission) {
		t.Skipf("chmod did not produce a permission error: %v", openErr)
	}

	targets, err := New("jmw-absent-cmake-fixture").listBuildTargets(
		context.Background(),
		dir,
	)
	warning, failure := runner.SplitIssues(err)
	if warning == nil || !errors.Is(warning, fs.ErrPermission) {
		t.Fatalf("listBuildTargets warning = %v, want fs.ErrPermission", warning)
	}
	if !strings.Contains(warning.Error(), blockedPath) {
		t.Fatalf("listBuildTargets warning = %v, want path %q", warning, blockedPath)
	}
	if failure != nil {
		t.Fatalf("listBuildTargets failure = %v", failure)
	}
	if len(targets) != 1 || targets[0].buildDir != "build/Debug" {
		t.Fatalf("targets = %#v, want readable build tree target", targets)
	}
}

func TestListBuildTargetsMatchesSymlinkedProjectPath(t *testing.T) {
	parentDir := t.TempDir()
	projectDir := filepath.Join(parentDir, "project")
	if err := os.Mkdir(projectDir, 0o750); err != nil {
		t.Fatal(err)
	}
	writeInternalCMakeProject(t, projectDir)
	writeInternalBuildTree(t, projectDir, ninjaGenerator)
	linkedDir := filepath.Join(parentDir, "linked-project")
	if err := os.Symlink(projectDir, linkedDir); err != nil {
		t.Skipf("cannot create project symlink: %v", err)
	}

	targets, err := New("jmw-absent-cmake-fixture").listBuildTargets(
		context.Background(),
		linkedDir,
	)
	if err != nil {
		t.Fatalf("listBuildTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].name != "app" {
		t.Fatalf("targets = %#v, want app", targets)
	}
}

func TestParseCMakeCapabilitiesVersion(t *testing.T) {
	version, err := parseCMakeCapabilitiesVersion(
		[]byte(`{"version":{"major":3,"minor":25,"patch":7}}`),
	)
	if err != nil {
		t.Fatalf("parseCMakeCapabilitiesVersion: %v", err)
	}
	if version != (cmakeVersion{major: 3, minor: 25}) {
		t.Fatalf("version = %#v, want CMake 3.25", version)
	}
}

const (
	capabilitiesCancellationHelperEnv    = "JMW_CMAKE_CAPABILITIES_CANCEL_HELPER"
	capabilitiesCancellationBinaryEnv    = "JMW_CMAKE_CAPABILITIES_CANCEL_BINARY"
	capabilitiesCancellationReadyPathEnv = "JMW_CMAKE_CAPABILITIES_CANCEL_READY"
)

func TestListPresetsPreservesCancellationAsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	presets, err := New("cmake").listPresets(ctx, t.TempDir())
	warning, failure := runner.SplitIssues(err)
	if warning != nil {
		t.Fatalf("listPresets warning = %v, want none", warning)
	}
	if !errors.Is(failure, context.Canceled) {
		t.Fatalf("listPresets failure = %v, want context.Canceled", failure)
	}
	if len(presets) != 0 {
		t.Fatalf("listPresets presets = %#v, want none", presets)
	}
}

func TestQueryCMakeVersionPreservesCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the helper uses a POSIX executable wrapper")
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapperPath := filepath.Join(dir, "cmake-cancellation-helper")
	wrapper := "#!/bin/sh\n" +
		"exec \"$" + capabilitiesCancellationBinaryEnv +
		"\" -test.run=^TestQueryCMakeVersionCancellationHelper$\n"
	// #nosec G306 -- the fixed test wrapper must be executable.
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(dir, "ready")
	t.Setenv(capabilitiesCancellationHelperEnv, "1")
	t.Setenv(capabilitiesCancellationBinaryEnv, testBinary)
	t.Setenv(capabilitiesCancellationReadyPathEnv, readyPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, queryErr := queryCMakeVersion(ctx, wrapperPath, dir)
		result <- queryErr
	}()
	waitForCMakeCapabilitiesHelper(t, readyPath, result)
	cancel()

	select {
	case queryErr := <-result:
		if !errors.Is(queryErr, context.Canceled) {
			t.Fatalf("queryCMakeVersion error = %v, want context.Canceled", queryErr)
		}
		if !strings.Contains(queryErr.Error(), "query CMake capabilities") {
			t.Fatalf("queryCMakeVersion error = %v, want query context", queryErr)
		}
		var exitErr *exec.ExitError
		if errors.As(queryErr, &exitErr) {
			t.Fatalf("queryCMakeVersion error = %v, classified as process failure", queryErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the canceled capabilities query")
	}
}

func TestQueryCMakeVersionCancellationHelper(_ *testing.T) {
	if os.Getenv(capabilitiesCancellationHelperEnv) != "1" {
		return
	}
	readyPath := os.Getenv(capabilitiesCancellationReadyPathEnv)
	// #nosec G703 -- the parent test supplies a path inside its temporary directory.
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func waitForCMakeCapabilitiesHelper(
	t *testing.T,
	readyPath string,
	result <-chan error,
) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		if _, err := os.Stat(readyPath); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat capabilities helper readiness: %v", err)
		}
		select {
		case queryErr := <-result:
			t.Fatalf("capabilities query exited before readiness: %v", queryErr)
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("timed out waiting for capabilities helper readiness")
		}
	}
}

func TestParseCMakeCapabilitiesVersionRejectsInvalidVersion(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "malformed JSON", output: `{`, want: "decode capabilities JSON"},
		{name: "missing version", output: `{}`, want: "missing version"},
		{
			name:   "missing major",
			output: `{"version":{"minor":25}}`,
			want:   "missing version.major",
		},
		{
			name:   "missing minor",
			output: `{"version":{"major":3}}`,
			want:   "missing version.minor",
		},
		{
			name:   "non-integer major",
			output: `{"version":{"major":"3","minor":25}}`,
			want:   "cannot unmarshal string",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCMakeCapabilitiesVersion([]byte(test.output))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"parseCMakeCapabilitiesVersion error = %v, want %q",
					err,
					test.want,
				)
			}
		})
	}
}

func TestPresetCommandsForVersion(t *testing.T) {
	tests := []struct {
		name      string
		wantKinds []string
		version   cmakeVersion
	}{
		{name: "older", version: cmakeVersion{major: 3, minor: 18}},
		{name: "3.19", version: cmakeVersion{major: 3, minor: 19}, wantKinds: []string{
			configurePreset,
		}},
		{name: "3.20", version: cmakeVersion{major: 3, minor: 20}, wantKinds: []string{
			configurePreset, buildPreset, testPreset,
		}},
		{name: "3.24", version: cmakeVersion{major: 3, minor: 24}, wantKinds: []string{
			configurePreset, buildPreset, testPreset,
		}},
		{name: "3.25", version: cmakeVersion{major: 3, minor: 25}, wantKinds: []string{
			configurePreset, buildPreset, testPreset, packagePreset, workflowPreset,
		}},
		{name: "higher major", version: cmakeVersion{major: 4}, wantKinds: []string{
			configurePreset, buildPreset, testPreset, packagePreset, workflowPreset,
		}},
	}
	wantCommands := map[string]presetCommand{
		configurePreset: {
			kind:   configurePreset,
			binary: "configured-cmake",
			args:   []string{"--list-presets"},
		},
		buildPreset: {
			kind:   buildPreset,
			binary: "configured-cmake",
			args:   []string{"--list-presets=build"},
		},
		testPreset: {
			kind:   testPreset,
			binary: ctestBinary,
			args:   []string{"--list-presets"},
		},
		packagePreset: {
			kind:   packagePreset,
			binary: cpackBinary,
			args:   []string{"--list-presets"},
		},
		workflowPreset: {
			kind:   workflowPreset,
			binary: "configured-cmake",
			args:   []string{"--list-presets=workflow"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commands := presetCommandsForVersion("configured-cmake", test.version)
			if len(commands) != len(test.wantKinds) {
				t.Fatalf("commands = %#v, want kinds %#v", commands, test.wantKinds)
			}
			for index, kind := range test.wantKinds {
				command := commands[index]
				want := wantCommands[kind]
				if command.kind != want.kind || command.binary != want.binary ||
					command.bestEffort != want.bestEffort ||
					!slices.Equal(command.args, want.args) {
					t.Fatalf("command %d = %#v, want %#v", index, command, want)
				}
			}
		})
	}

	t.Run("best effort", func(t *testing.T) {
		allKinds := []string{
			configurePreset,
			buildPreset,
			testPreset,
			packagePreset,
			workflowPreset,
		}
		commands := bestEffortPresetCommands("configured-cmake")
		if len(commands) != len(allKinds) {
			t.Fatalf("commands = %#v, want kinds %#v", commands, allKinds)
		}
		for index, kind := range allKinds {
			command := commands[index]
			want := wantCommands[kind]
			want.bestEffort = true
			if command.kind != want.kind || command.binary != want.binary ||
				command.bestEffort != want.bestEffort ||
				!slices.Equal(command.args, want.args) {
				t.Fatalf("command %d = %#v, want %#v", index, command, want)
			}
		}
	})
}

func TestCollectPresetsKeepsResultsAcrossCommandFailure(t *testing.T) {
	commands := []presetCommand{
		{kind: configurePreset, binary: configurePreset},
		{kind: buildPreset, binary: buildPreset},
		{kind: testPreset, binary: testPreset},
		{kind: packagePreset, binary: packagePreset},
		{kind: workflowPreset, binary: workflowPreset},
	}
	commandFailure := errors.New("list test presets")
	called := make([]string, 0, len(commands))

	presets, err := collectPresets(
		context.Background(),
		commands,
		func(command presetCommand) ([]preset, error) {
			called = append(called, command.binary)
			if command.binary == testPreset {
				return nil, commandFailure
			}
			return []preset{{
				kind: command.binary,
				name: command.binary,
			}}, nil
		},
	)
	if !errors.Is(err, commandFailure) {
		t.Fatalf("collectPresets error = %v, want %v", err, commandFailure)
	}
	if !strings.Contains(err.Error(), "list CMake presets for test") {
		t.Fatalf("collectPresets error = %v, want test preset context", err)
	}
	if want := []string{
		configurePreset,
		buildPreset,
		testPreset,
		packagePreset,
		workflowPreset,
	}; !slices.Equal(called, want) {
		t.Fatalf("called commands = %#v, want %#v", called, want)
	}
	want := []preset{
		{kind: configurePreset, name: configurePreset},
		{kind: buildPreset, name: buildPreset},
		{kind: packagePreset, name: packagePreset},
		{kind: workflowPreset, name: workflowPreset},
	}
	if !slices.Equal(presets, want) {
		t.Fatalf("presets = %#v, want %#v", presets, want)
	}
}

func TestCollectPresetsTreatsBestEffortFailureAsWarning(t *testing.T) {
	commands := []presetCommand{
		{kind: configurePreset, bestEffort: true},
		{kind: buildPreset, bestEffort: true},
		{kind: testPreset, bestEffort: true},
	}
	commandFailure := errors.New("build preset failure")

	presets, err := collectPresets(
		context.Background(),
		commands,
		func(command presetCommand) ([]preset, error) {
			if command.kind == buildPreset {
				return nil, commandFailure
			}
			return []preset{{kind: command.kind, name: command.kind}}, nil
		},
	)
	warning, failure := runner.SplitIssues(err)
	if warning == nil || !errors.Is(warning, commandFailure) ||
		!strings.Contains(warning.Error(), "list CMake presets for build") {
		t.Fatalf("collectPresets warning = %v, want contextual build warning", warning)
	}
	if failure != nil {
		t.Fatalf("collectPresets failure = %v, want none", failure)
	}
	wantPresets := []preset{
		{kind: configurePreset, name: configurePreset},
		{kind: testPreset, name: testPreset},
	}
	if !slices.Equal(presets, wantPresets) {
		t.Fatalf("presets = %#v, want %#v", presets, wantPresets)
	}
}

func TestCollectPresetsSplitsMissingToolAndFailure(t *testing.T) {
	commands := []presetCommand{
		{kind: configurePreset, binary: configurePreset},
		{kind: buildPreset, binary: buildPreset},
		{kind: testPreset, binary: testPreset},
		{kind: packagePreset, binary: packagePreset},
		{kind: workflowPreset, binary: workflowPreset},
	}
	missing := runner.MarkMissingTool(testPreset, exec.ErrNotFound)
	commandFailure := errors.New("package preset failure")

	presets, err := collectPresets(
		context.Background(),
		commands,
		func(command presetCommand) ([]preset, error) {
			switch command.kind {
			case testPreset:
				return nil, fmt.Errorf("start CMake preset listing: %w", missing)
			case packagePreset:
				return nil, commandFailure
			default:
				return []preset{{
					kind: command.kind,
					name: command.kind,
				}}, nil
			}
		},
	)
	warning, failure := runner.SplitIssues(err)
	wantWarning := "start CMake preset listing: " + missing.Error()
	if warning == nil || warning.Error() != wantWarning ||
		!errors.Is(warning, runner.ErrToolUnavailable) {
		t.Fatalf("collectPresets warning = %v, want %q", warning, wantWarning)
	}
	wantFailure := "list CMake presets for package: " + commandFailure.Error()
	if failure == nil || failure.Error() != wantFailure ||
		!errors.Is(failure, commandFailure) {
		t.Fatalf("collectPresets failure = %v, want %q", failure, wantFailure)
	}
	wantPresets := []preset{
		{kind: configurePreset, name: configurePreset},
		{kind: buildPreset, name: buildPreset},
		{kind: workflowPreset, name: workflowPreset},
	}
	if !slices.Equal(presets, wantPresets) {
		t.Fatalf("presets = %#v, want %#v", presets, wantPresets)
	}
}

func TestCollectPresetsHonorsCancellation(t *testing.T) {
	bestEffortFailure := errors.New("best-effort package preset failure")
	ctx, cancel := context.WithCancel(context.Background())
	commands := []presetCommand{
		{kind: configurePreset, binary: configurePreset},
		{kind: testPreset, binary: testPreset},
		{kind: packagePreset, binary: packagePreset},
		{kind: packagePreset, binary: packagePreset, bestEffort: true},
		{kind: workflowPreset, binary: workflowPreset},
		{kind: buildPreset, binary: buildPreset},
	}
	called := make([]string, 0, len(commands))
	missing := runner.MarkMissingTool(testPreset, exec.ErrNotFound)
	commandFailure := errors.New("package preset failure")

	presets, err := collectPresets(
		ctx,
		commands,
		func(command presetCommand) ([]preset, error) {
			called = append(called, command.kind)
			switch command.kind {
			case testPreset:
				return nil, fmt.Errorf("start CMake preset listing: %w", missing)
			case packagePreset:
				return nil, map[bool]error{
					false: commandFailure,
					true:  bestEffortFailure,
				}[command.bestEffort]
			case workflowPreset:
				cancel()
				return []preset{{kind: workflowPreset, name: "not-appended"}}, nil
			}
			return []preset{{kind: command.kind, name: command.kind}}, nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("collectPresets error = %v, want context.Canceled", err)
	}
	warning, failure := runner.SplitIssues(err)
	if warning == nil || !errors.Is(warning, runner.ErrToolUnavailable) ||
		!errors.Is(warning, exec.ErrNotFound) ||
		!errors.Is(warning, bestEffortFailure) ||
		!strings.Contains(warning.Error(), "list CMake presets for package") {
		t.Fatalf("collectPresets warning = %v, want missing tool and package warning", warning)
	}
	if failure == nil || !errors.Is(failure, commandFailure) ||
		!errors.Is(failure, context.Canceled) {
		t.Fatalf("collectPresets failure = %v, want command failure and cancellation", failure)
	}
	if want := []string{
		configurePreset,
		testPreset,
		packagePreset,
		packagePreset,
		workflowPreset,
	}; !slices.Equal(called, want) {
		t.Fatalf("called commands = %#v, want %#v", called, want)
	}
	wantPresets := []preset{{kind: configurePreset, name: configurePreset}}
	if !slices.Equal(presets, wantPresets) {
		t.Fatalf("presets = %#v, want %#v", presets, wantPresets)
	}
}

type cancelOnErrCheckContext struct {
	context.Context
	checks   int
	cancelAt int
}

func (c *cancelOnErrCheckContext) Err() error {
	c.checks++
	if c.checks >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestCollectPresetsPreCallCancellationKeepsIssues(t *testing.T) {
	bestEffortFailure := errors.New("best-effort package preset failure")
	ctx := &cancelOnErrCheckContext{
		Context:  context.Background(),
		cancelAt: 7,
	}
	commands := []presetCommand{
		{kind: testPreset, binary: testPreset},
		{kind: packagePreset, binary: packagePreset},
		{kind: packagePreset, binary: packagePreset, bestEffort: true},
		{kind: buildPreset, binary: buildPreset},
	}
	called := make([]string, 0, len(commands))
	missing := runner.MarkMissingTool(testPreset, exec.ErrNotFound)
	commandFailure := errors.New("package preset failure")

	presets, err := collectPresets(
		ctx,
		commands,
		func(command presetCommand) ([]preset, error) {
			called = append(called, command.kind)
			switch command.kind {
			case testPreset:
				return nil, fmt.Errorf("start CMake preset listing: %w", missing)
			case packagePreset:
				if command.bestEffort {
					return nil, bestEffortFailure
				}
				return nil, commandFailure
			default:
				return []preset{{kind: command.kind, name: "not-called"}}, nil
			}
		},
	)
	if len(presets) != 0 {
		t.Fatalf("presets = %#v, want none", presets)
	}
	if want := []string{testPreset, packagePreset, packagePreset}; !slices.Equal(called, want) {
		t.Fatalf("called commands = %#v, want %#v", called, want)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("collectPresets error = %v, want context.Canceled", err)
	}
	warning, failure := runner.SplitIssues(err)
	if warning == nil || !errors.Is(warning, runner.ErrToolUnavailable) ||
		!errors.Is(warning, bestEffortFailure) ||
		!strings.Contains(warning.Error(), "list CMake presets for package") {
		t.Fatalf("collectPresets warning = %v, want missing tool and package warning", warning)
	}
	if failure == nil || !errors.Is(failure, commandFailure) ||
		!errors.Is(failure, context.Canceled) {
		t.Fatalf("collectPresets failure = %v, want failure and cancellation", failure)
	}
}

func TestPresetCommandListReportsExitStatusWithoutStderr(t *testing.T) {
	const helperEnv = "JMW_CMAKE_EMPTY_STDERR_HELPER"
	if os.Getenv(helperEnv) == "1" {
		os.Exit(23)
	}
	t.Setenv(helperEnv, "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := presetCommand{
		kind:   configurePreset,
		binary: binary,
		args:   []string{"-test.run=^TestPresetCommandListReportsExitStatusWithoutStderr$"},
	}

	_, err = command.list(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "exit status 23") {
		t.Fatalf("presetCommand.list error = %v, want exit status 23", err)
	}
}

func TestListTasksWarnsForUnsupportedConfiguredGenerator(t *testing.T) {
	dir := t.TempDir()
	writeInternalCMakeProject(t, dir)
	writeInternalBuildTree(t, dir, "Unix Makefiles")

	tasks, err := New("jmw-absent-cmake-fixture").ListTasks(
		context.Background(),
		dir,
	)
	warning, failure := runner.SplitIssues(err)
	if warning == nil || !strings.Contains(
		warning.Error(),
		"supports only Ninja and Ninja Multi-Config",
	) {
		t.Fatalf("ListTasks warning = %v", warning)
	}
	if failure != nil {
		t.Fatalf("ListTasks failure = %v", failure)
	}
	if errors.Is(err, runner.ErrToolUnavailable) {
		t.Fatalf("ListTasks error = %v, unexpectedly matches ErrToolUnavailable", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("tasks = %#v, want none", tasks)
	}
}

func TestListTasksWarnsWhenConfiguredTargetCannotRun(t *testing.T) {
	dir := t.TempDir()
	writeInternalCMakeProject(t, dir)
	writeInternalBuildTree(t, dir, ninjaGenerator)

	tasks, err := New("jmw-absent-cmake-fixture").ListTasks(
		context.Background(),
		dir,
	)
	if !errors.Is(err, runner.ErrToolUnavailable) {
		t.Fatalf("ListTasks error = %v, want ErrToolUnavailable", err)
	}
	if len(tasks) != 1 || tasks[0].Name != "app" {
		t.Fatalf("tasks = %#v", tasks)
	}
}

func TestListTasksKeepsPartialConfiguredTargetsOnTargetFailure(t *testing.T) {
	dir := t.TempDir()
	writeInternalCMakeProject(t, dir)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	targetFailure := errors.New("read second configured build tree")

	tasks, err := New(binary).listTasks(
		context.Background(),
		dir,
		func(context.Context, string) ([]buildTarget, error) {
			return []buildTarget{{
				buildDir: "build/A",
				name:     "app",
			}}, targetFailure
		},
	)
	warning, failure := runner.SplitIssues(err)
	if warning != nil {
		t.Fatalf("ListTasks warning = %v, want none", warning)
	}
	if !errors.Is(failure, targetFailure) {
		t.Fatalf("ListTasks failure = %v, want %v", failure, targetFailure)
	}
	if len(tasks) != 1 || tasks[0].ID != "cmake:target:build%2FA:app" {
		t.Fatalf("tasks = %#v, want partial configured target", tasks)
	}
}

func TestListTasksFailsWhenConfiguredTargetBinaryIsNotExecutable(t *testing.T) {
	dir := t.TempDir()
	writeInternalCMakeProject(t, dir)
	writeInternalBuildTree(t, dir, ninjaGenerator)
	binary := filepath.Join(dir, "cmake-not-executable")
	if err := os.WriteFile(binary, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, lookupErr := exec.LookPath(binary); !errors.Is(lookupErr, fs.ErrPermission) {
		t.Skipf("LookPath does not report permission denied on this host: %v", lookupErr)
	}

	tasks, err := New(binary).ListTasks(context.Background(), dir)
	warning, failure := runner.SplitIssues(err)
	if warning != nil {
		t.Fatalf("ListTasks warning = %v, want none", warning)
	}
	if !errors.Is(failure, fs.ErrPermission) {
		t.Fatalf("ListTasks failure = %v, want fs.ErrPermission", failure)
	}
	if len(tasks) != 1 || tasks[0].Name != "app" {
		t.Fatalf("tasks = %#v", tasks)
	}
}

func TestListTasksFailsWhenConfiguredPresetBinaryIsNotExecutable(t *testing.T) {
	dir := t.TempDir()
	writeInternalCMakeProject(t, dir)
	if err := os.WriteFile(filepath.Join(dir, presetsName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeInternalBuildTree(t, dir, ninjaGenerator)
	binary := filepath.Join(dir, "cmake-not-executable")
	if err := os.WriteFile(binary, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, lookupErr := exec.LookPath(binary); !errors.Is(lookupErr, fs.ErrPermission) {
		t.Skipf("LookPath does not report permission denied on this host: %v", lookupErr)
	}

	tasks, err := New(binary).ListTasks(context.Background(), dir)
	warning, failure := runner.SplitIssues(err)
	if warning != nil {
		t.Fatalf("ListTasks warning = %v, want none", warning)
	}
	if failure == nil || !errors.Is(failure, fs.ErrPermission) ||
		!strings.Contains(failure.Error(), "discover CMake preset support") ||
		!strings.Contains(failure.Error(), "start CMake capabilities query") {
		t.Fatalf("ListTasks failure = %v, want contextual preset discovery permission error", failure)
	}
	if len(tasks) != 1 || tasks[0].Name != "app" {
		t.Fatalf("tasks = %#v", tasks)
	}
}

func TestConfiguredBuildDirsSkipVanishedCandidates(t *testing.T) {
	dir := t.TempDir()
	projectIdentity, err := resolveProjectPath(dir)
	if err != nil {
		t.Fatalf("resolveProjectPath: %v", err)
	}
	trees, err := configuredBuildDirsFromCandidates(
		context.Background(),
		dir,
		projectIdentity,
		[]buildDirCandidate{{
			path:  filepath.Join(dir, "build-vanished"),
			depth: 1,
		}},
	)
	if err != nil {
		t.Fatalf("configuredBuildDirsFromCandidates: %v", err)
	}
	if len(trees) != 0 {
		t.Fatalf("build trees = %#v, want none", trees)
	}
}

func TestConfiguredBuildDirsFindsNamedDirectBuildTree(t *testing.T) {
	dir := t.TempDir()
	buildDir := filepath.Join(dir, "generated-output")
	if err := os.Mkdir(buildDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cache := cacheSourceEntry + dir + "\n" + cacheGeneratorEntry + ninjaGenerator + "\n"
	if err := os.WriteFile(
		filepath.Join(buildDir, cmakeCacheName),
		[]byte(cache),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	trees, err := configuredBuildDirs(context.Background(), dir)
	if err != nil {
		t.Fatalf("configuredBuildDirs: %v", err)
	}
	if len(trees) != 1 || trees[0].path != "generated-output" {
		t.Fatalf("build trees = %#v, want generated-output", trees)
	}
}

func TestIsBuildRootNameRejectsUnrelatedBuildDirectories(t *testing.T) {
	for _, name := range []string{
		"build",
		"build-debug",
		"build_debug",
		"cmake-build-debug",
		"out",
		"_build",
	} {
		if !isBuildRootName(name) {
			t.Errorf("isBuildRootName(%q) = false", name)
		}
	}
	for _, name := range []string{
		"buildscripts",
		"buildtools",
		"building",
	} {
		if isBuildRootName(name) {
			t.Errorf("isBuildRootName(%q) = true", name)
		}
	}
}

func TestValidBuildTargetNameKeepsSupportedTargets(t *testing.T) {
	for _, name := range []string{
		"",
		".",
		"..",
		"../outside",
		"sub/../outside",
		"/absolute",
		"-option",
		"$target",
		"generated/header.h",
		"sub/dir/all",
		"lib/install",
		"test_prep/nested/smoke",
	} {
		if validBuildTargetName(name) {
			t.Errorf("validBuildTargetName(%q) = true", name)
		}
	}
	for _, name := range []string{
		"app",
		"target:Debug",
		"all",
		"install/local",
		"install/strip",
		"install/parallel",
		"test_prep/smoke",
	} {
		if !validBuildTargetName(name) {
			t.Errorf("validBuildTargetName(%q) = false", name)
		}
	}
}

func TestSameProjectPathMatchesExactLexicalPathWithoutFileIdentity(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "missing-project")
	identity, err := resolveProjectPath(projectPath)
	if err != nil {
		t.Fatalf("resolveProjectPath: %v", err)
	}
	if identity.info != nil {
		t.Fatal("missing project path unexpectedly has file identity")
	}
	if !sameProjectPath(identity, projectPath) {
		t.Fatal("exact lexical project path must match without file identity")
	}
}

func TestSameProjectPathMatchesSymlinkToSameDirectory(t *testing.T) {
	parentDir := t.TempDir()
	projectDir := filepath.Join(parentDir, "project")
	if err := os.Mkdir(projectDir, 0o750); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(parentDir, "linked-project")
	if err := os.Symlink(projectDir, linkedDir); err != nil {
		t.Skipf("cannot create project symlink: %v", err)
	}
	projectIdentity, err := resolveProjectPath(projectDir)
	if err != nil {
		t.Fatalf("resolveProjectPath: %v", err)
	}
	if !sameProjectPath(projectIdentity, linkedDir) {
		t.Fatal("symlink to the project directory must match")
	}
}

func TestSameProjectPathRejectsDifferentDirectory(t *testing.T) {
	parentDir := t.TempDir()
	projectDir := filepath.Join(parentDir, "project")
	otherDir := filepath.Join(parentDir, "other")
	for _, dir := range []string{projectDir, otherDir} {
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	projectIdentity, err := resolveProjectPath(projectDir)
	if err != nil {
		t.Fatalf("resolveProjectPath: %v", err)
	}
	if sameProjectPath(projectIdentity, otherDir) {
		t.Fatal("different project directories must not match")
	}
}

func TestSameProjectPathRejectsCaseOnlyDifferentDirectory(t *testing.T) {
	parentDir := t.TempDir()
	upperDir := filepath.Join(parentDir, "Source")
	if err := os.Mkdir(upperDir, 0o750); err != nil {
		t.Fatal(err)
	}
	lowerDir := filepath.Join(parentDir, "source")
	if err := os.Mkdir(lowerDir, 0o750); errors.Is(err, fs.ErrExist) {
		t.Skip("filesystem cannot represent case-only distinct directories")
	} else if err != nil {
		t.Fatal(err)
	}
	projectIdentity, err := resolveProjectPath(upperDir)
	if err != nil {
		t.Fatalf("resolveProjectPath: %v", err)
	}
	lowerInfo, err := os.Stat(lowerDir)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(projectIdentity.info, lowerInfo) {
		t.Skip("filesystem cannot represent case-only distinct directories")
	}
	if sameProjectPath(projectIdentity, lowerDir) {
		t.Fatal("case-only distinct project directories must not match")
	}
}

func TestSameProjectPathRejectsInvalidAlternatePath(t *testing.T) {
	projectDir := t.TempDir()
	projectIdentity, err := resolveProjectPath(projectDir)
	if err != nil {
		t.Fatalf("resolveProjectPath: %v", err)
	}
	if sameProjectPath(projectIdentity, "relative-project") {
		t.Fatal("relative cache source path must not match")
	}
	if sameProjectPath(projectIdentity, filepath.Join(projectDir, "missing")) {
		t.Fatal("missing alternate cache source path must not match")
	}
}

func writeInternalCMakeProject(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(dir, cmakeListsName),
		[]byte("cmake_minimum_required(VERSION 3.20)\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func writeInternalBuildTree(t *testing.T, projectDir string, generator string) {
	t.Helper()
	buildDir := filepath.Join(projectDir, "build", "Debug")
	if err := os.MkdirAll(buildDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cache := cacheSourceEntry + projectDir + "\n" +
		cacheGeneratorEntry + generator + "\n"
	if err := os.WriteFile(
		filepath.Join(buildDir, cmakeCacheName),
		[]byte(cache),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(buildDir, ninjaBuildFileName),
		[]byte("build app: CXX_EXECUTABLE_LINKER__app_\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func sameTargetNames(actual []string, expected []string) bool {
	return slices.Equal(actual, expected)
}
