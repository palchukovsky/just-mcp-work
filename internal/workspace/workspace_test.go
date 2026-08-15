// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

func TestDiscoverProjectsAndSurfaceInvalidJustfile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "justfile"), "root")
	writeFile(t, filepath.Join(root, "nested", "Justfile"), "nested")
	writeFile(t, filepath.Join(root, "invalid", ".justfile"), "invalid syntax")
	writeFile(t, filepath.Join(root, ".git", "ignored", "justfile"), "ignored")

	runners, err := runner.NewRegistry(testRegistration(fakeJustRunner{}))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: -1, IncludeHidden: true},
	)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	paths := make([]string, 0, len(projects))
	for _, project := range projects {
		paths = append(paths, project.RelPath)
	}
	if want := []string{".", "invalid", "nested"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("project paths = %#v, want %#v", paths, want)
	}

	invalid := projectAt(t, projects, "invalid")
	if invalid.Status != "error" || !strings.Contains(invalid.Errors["just"], "invalid justfile") {
		t.Fatalf("invalid project status = %q, errors = %#v", invalid.Status, invalid.Errors)
	}
	if !reflect.DeepEqual(invalid.Runners, []string{"just"}) {
		t.Fatalf("invalid project runners = %#v", invalid.Runners)
	}
	foundProject := projectAt(t, projects, "nested")
	found, err := registry.Find(context.Background(), foundProject.RelPath)
	if err != nil || found.RelPath != "nested" {
		t.Fatalf("Find = %#v, %v", found, err)
	}
}

// TestDiscoverReportsMissingToolAsWarning keeps a checkout usable on a host
// that lacks one of its build tools: the project stays ready, the runners that
// do work keep their tasks, and the missing tool is still reported.
func TestDiscoverReportsMissingToolAsWarning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "justfile"), "root")
	writeFile(t, filepath.Join(root, "Makefile"), "root")

	runners, err := runner.NewRegistry(
		testRegistration(fakeJustRunner{}),
		testRegistration(unavailableToolRunner{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(context.Background(), Filter{Path: "."})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	project := projectAt(t, projects, ".")
	if project.Status != "ready" {
		t.Fatalf("project status = %q, want ready", project.Status)
	}
	if len(project.Errors) != 0 {
		t.Fatalf("project errors = %#v, want none", project.Errors)
	}
	if !strings.Contains(project.Warnings["make"], "runner tool is unavailable") {
		t.Fatalf("project warnings = %#v", project.Warnings)
	}
	if len(project.Tasks["just"]) != 1 {
		t.Fatalf("just tasks = %#v, want the tasks of the working runner", project.Tasks["just"])
	}
}

func TestProjectAddIssueKeepsMarkedMissingToolAsWarning(t *testing.T) {
	_, lookupErr := exec.LookPath("jmw-absent-tool-fixture")
	if lookupErr == nil {
		t.Skip("the fixture name resolves on this host")
	}

	project := Project{}
	project.addIssue(
		"cmake",
		fmt.Errorf(
			"list CMake tasks: %w",
			runner.MarkMissingTool("jmw-absent-tool-fixture", lookupErr),
		),
	)

	if len(project.Errors) != 0 {
		t.Fatalf("project errors = %#v, want none", project.Errors)
	}
	if !strings.Contains(
		project.Warnings["cmake"],
		"runner tool is unavailable",
	) {
		t.Fatalf("project warnings = %#v", project.Warnings)
	}
}

func TestProjectAddIssueSplitsWarningsAndErrors(t *testing.T) {
	_, lookupErr := exec.LookPath("jmw-absent-tool-fixture")
	if lookupErr == nil {
		t.Skip("the fixture name resolves on this host")
	}

	project := Project{}
	project.addIssue(
		"cmake",
		fmt.Errorf(
			"list CMake tasks: %w",
			errors.Join(
				runner.MarkMissingTool(
					"jmw-absent-tool-fixture",
					lookupErr,
				),
				errors.New("parse configured build tree"),
			),
		),
	)

	if !strings.Contains(
		project.Warnings["cmake"],
		"runner tool is unavailable",
	) {
		t.Fatalf("project warnings = %#v", project.Warnings)
	}
	if !strings.Contains(
		project.Errors["cmake"],
		"parse configured build tree",
	) || strings.Contains(
		project.Errors["cmake"],
		"runner tool is unavailable",
	) {
		t.Fatalf("project errors = %#v", project.Errors)
	}
	if !strings.Contains(
		project.Errors["cmake"],
		"list CMake tasks: parse configured build tree",
	) {
		t.Fatalf("project errors = %#v, want the operation context", project.Errors)
	}
}

func TestProjectAddIssueDoesNotDowngradeJoinedUnavailableSentinel(t *testing.T) {
	project := Project{}
	project.addIssue(
		"cmake",
		errors.Join(
			runner.ErrToolUnavailable,
			errors.New("parse configured build tree"),
		),
	)

	if !strings.Contains(
		project.Warnings["cmake"],
		"runner tool is unavailable",
	) {
		t.Fatalf("project warnings = %#v", project.Warnings)
	}
	if !strings.Contains(
		project.Errors["cmake"],
		"parse configured build tree",
	) {
		t.Fatalf("project errors = %#v", project.Errors)
	}
}

// TestDiscoverKeepsProjectWithOnlyWarnings covers the directory whose single
// signal is a warning: dropping it would hide the diagnosis of why a project
// the operator expects to see reports nothing at all.
func TestDiscoverKeepsProjectWithOnlyWarnings(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Makefile"), "root")

	runners, err := runner.NewRegistry(testRegistration(detectUnavailableToolRunner{}))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(context.Background(), Filter{Path: "."})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	project := projectAt(t, projects, ".")
	if project.Status != "ready" {
		t.Fatalf("project status = %q, want ready", project.Status)
	}
	if len(project.Runners) != 0 {
		t.Fatalf("project runners = %#v, want none", project.Runners)
	}
	if !strings.Contains(project.Warnings["make"], "runner tool is unavailable") {
		t.Fatalf("project warnings = %#v", project.Warnings)
	}
}

// TestDiscoverKeepsPartiallyDiscoveredTasks covers a runner that reports what it
// could discover together with the failure of the rest: the usable tasks must
// survive, and the failure must still mark the project.
func TestDiscoverKeepsPartiallyDiscoveredTasks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Makefile"), "root")

	runners, err := runner.NewRegistry(testRegistration(partialFakeMakeRunner{}))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(context.Background(), Filter{Path: "."})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	project := projectAt(t, projects, ".")
	if project.Status != "error" {
		t.Fatalf("project status = %q, want error", project.Status)
	}
	if !strings.Contains(project.Errors["make"], "half of the targets") {
		t.Fatalf("project errors = %#v", project.Errors)
	}
	if got := project.Tasks["make"]; len(got) != 1 || got[0].ID != "make:task" {
		t.Fatalf("make tasks = %#v, want the discovered part", got)
	}
}

func TestFindRejectsPathsOutsideWorkspace(t *testing.T) {
	registry, err := NewRegistry(t.TempDir(), mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Find(context.Background(), "../outside"); err == nil {
		t.Fatal("Find accepted a path outside the workspace")
	}
}

func TestResolveDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "nested", "fixture"), "fixture")
	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := registry.ResolveDir("nested")
	if err != nil || resolved != filepath.Join(root, "nested") {
		t.Fatalf("ResolveDir(nested) = %q, %v", resolved, err)
	}
	resolved, err = registry.ResolveDir("")
	if err != nil || resolved != root {
		t.Fatalf("ResolveDir(root) = %q, %v", resolved, err)
	}
	for _, relPath := range []string{"../outside", "nested/fixture", "missing"} {
		if _, err := registry.ResolveDir(relPath); err == nil {
			t.Errorf("ResolveDir(%q) succeeded", relPath)
		}
	}

	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, "linked")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if _, err := registry.ResolveDir("linked"); err == nil {
		t.Fatal("ResolveDir followed a directory symlink")
	}
}

func TestDiscoverSkipsJustfileIncludedByParentProject(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "justfile"), "root")
	writeFile(t, filepath.Join(root, "nested", "justfile"), "nested")
	writeFile(t, filepath.Join(root, "invalid", "justfile"), "invalid")
	runners, err := runner.NewRegistry(
		testRegistration(includingFakeJustRunner{
			included: filepath.Join(root, "nested"),
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: -1, IncludeHidden: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(projects))
	for _, project := range projects {
		paths = append(paths, project.RelPath)
	}
	if want := []string{".", "invalid"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("project paths = %#v, want %#v", paths, want)
	}
	scoped, _, err := registry.Discover(
		context.Background(),
		Filter{Path: "nested", MaxDepth: 0, IncludeHidden: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(scoped), []string{"nested"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scoped project paths = %#v, want %#v", got, want)
	}
	found, err := registry.Find(context.Background(), "nested")
	if err != nil || found.RelPath != "nested" {
		t.Fatalf("Find scoped project = %#v, %v", found, err)
	}
}

func TestDiscoverSuppressesOnlyIncludedJustRunnerAfterFullScan(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "a", "shared")
	writeFile(t, filepath.Join(shared, "justfile"), "shared")
	writeFile(t, filepath.Join(shared, "Makefile"), "shared")
	writeFile(t, filepath.Join(root, "z", "app", "justfile"), "app")
	runners, err := runner.NewRegistry(
		testRegistration(includingFakeJustRunner{included: shared}),
		testRegistration(fakeMakeRunner{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: -1, IncludeHidden: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(projects))
	for _, project := range projects {
		paths = append(paths, project.RelPath)
	}
	if want := []string{"a/shared", "z/app"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("project paths = %#v, want %#v", paths, want)
	}
	sharedProject := projectAt(t, projects, "a/shared")
	if want := []string{"make"}; !reflect.DeepEqual(sharedProject.Runners, want) {
		t.Fatalf("shared runners = %#v, want %#v", sharedProject.Runners, want)
	}
	if len(sharedProject.Tasks["just"]) != 0 || len(sharedProject.Tasks["make"]) != 1 {
		t.Fatalf("shared tasks = %#v", sharedProject.Tasks)
	}
	found, err := registry.Find(context.Background(), "a/shared")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"just", "make"}; !reflect.DeepEqual(found.Runners, want) {
		t.Fatalf("found shared runners = %#v, want %#v", found.Runners, want)
	}
	if len(found.Tasks["just"]) != 1 || len(found.Tasks["make"]) != 1 {
		t.Fatalf("found shared tasks = %#v", found.Tasks)
	}
}

//nolint:gocyclo // This test pins every independent scan-filter boundary.
func TestDiscoverFilterPrunesBeforeInspection(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		"justfile",
		"top/justfile",
		"top/deeper/justfile",
		".hidden/justfile",
		".just-mcp-work/justfile",
		"target/justfile",
	} {
		writeFile(t, filepath.Join(root, path), "fixture")
	}
	calls := 0
	runners, err := runner.NewRegistry(
		testRegistration(countingFakeJustRunner{calls: &calls}),
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, pruned, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{".", "top"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project paths = %#v, want %#v", got, want)
	}
	if want := (Pruned{Depth: 1, Hidden: 1, Excluded: 2}); pruned != want {
		t.Fatalf("pruned = %#v, want %#v", pruned, want)
	}
	if calls != len(projects) {
		t.Fatalf("ListTasks calls = %d, projects = %d", calls, len(projects))
	}

	projects, pruned, err = registry.Discover(
		context.Background(),
		Filter{Path: "top", MaxDepth: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{"top"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("base-relative project paths = %#v, want %#v", got, want)
	}
	if pruned.Depth != 1 {
		t.Fatalf("base-relative depth pruned = %#v", pruned)
	}

	projects, _, err = registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: -1, IncludeHidden: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{".", ".hidden", "top", "top/deeper"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unlimited project paths = %#v, want %#v", got, want)
	}

	projects, _, err = registry.Discover(
		context.Background(),
		Filter{Path: ".hidden", MaxDepth: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{".hidden"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit hidden base paths = %#v, want %#v", got, want)
	}
	if _, _, discoverErr := registry.Discover(
		context.Background(),
		Filter{Path: "target", MaxDepth: 0},
	); discoverErr == nil {
		t.Fatal("Discover accepted a built-in excluded base")
	}

	writeFile(t, filepath.Join(root, "ignored", "nested", "justfile"), "fixture")
	excludedRegistry, err := NewRegistry(root, runners, []string{"ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := excludedRegistry.Discover(
		context.Background(),
		Filter{Path: "ignored/nested", MaxDepth: 0},
	); err == nil {
		t.Fatal("Discover accepted a base below a user-excluded directory")
	}
}

func TestDiscoverFilterKeepsProjectDetailsAndValidatesInput(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "dual", "justfile"), "fixture")
	writeFile(t, filepath.Join(root, "dual", "Makefile"), "fixture")
	writeFile(t, filepath.Join(root, "just-only", "justfile"), "fixture")
	runners, err := runner.NewRegistry(
		testRegistration(fakeJustRunner{}),
		testRegistration(fakeMakeRunner{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, pruned, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: -1, Runners: []string{"just"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if pruned.RunnerMismatch != 0 {
		t.Fatalf("runner mismatch count = %d", pruned.RunnerMismatch)
	}
	if got, want := projectAt(t, projects, "dual").Runners, []string{"just", "make"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered project runners = %#v, want %#v", got, want)
	}
	projects, pruned, err = registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: -1, Runners: []string{"make"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{"dual"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("make-filtered project paths = %#v, want %#v", got, want)
	}
	if pruned.RunnerMismatch != 1 {
		t.Fatalf("runner mismatch count = %d, want 1", pruned.RunnerMismatch)
	}
	if _, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: -2},
	); err == nil {
		t.Fatal("Discover accepted max_depth below -1")
	}
	if _, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: -1, Runners: []string{"missing"}},
	); err == nil {
		t.Fatal("Discover accepted an unknown runner")
	}
	if _, _, err := registry.Discover(
		context.Background(),
		Filter{Path: "../outside", MaxDepth: -1},
	); err == nil {
		t.Fatal("Discover accepted a path outside the workspace")
	}
}

func TestExcludedGlobUsesSlashSeparatorSemantics(t *testing.T) {
	root := t.TempDir()
	registry, err := NewRegistry(root, mustRunnerRegistry(t), []string{"*/generated"})
	if err != nil {
		t.Fatal(err)
	}
	if !registry.excluded(filepath.Join(root, "a", "generated")) {
		t.Fatal("exclude glob did not match one path segment")
	}
	if registry.excluded(filepath.Join(root, "a", "b", "generated")) {
		t.Fatal("exclude glob crossed multiple slash-separated path segments")
	}
}

func mustRunnerRegistry(t *testing.T) *runner.Registry {
	t.Helper()
	registry, err := runner.NewRegistry(testRegistration(fakeJustRunner{}))
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestDiscoverDoesNotDescendIntoDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	writeFile(t, filepath.Join(external, "justfile"), "external")
	if err := os.Symlink(external, filepath.Join(root, "linked")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	runners, err := runner.NewRegistry(testRegistration(fakeJustRunner{}))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root, runners, nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: -1, IncludeHidden: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("discovered projects through symlink: %#v", projects)
	}
}

type fakeJustRunner struct{}

func (fakeJustRunner) Name() string { return "just" }

func TestDiscoverAdmitsRegisteredWorktreeWithoutWideningScan(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "project")
	worktreeDir := filepath.Join(mainDir, ".wt", "feature")
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeFile(t, filepath.Join(root, ".ordinary-hidden", "justfile"), "hidden")
	writeWorktreeMarkers(t, mainDir, worktreeDir, "feature")

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, pruned, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{
		"project",
		"project/.wt/feature",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project paths = %#v, want %#v", got, want)
	}
	if pruned.Hidden != 2 || pruned.Depth != 0 {
		t.Fatalf("pruned = %#v, want hidden 2 and depth 0", pruned)
	}
	if projectAt(t, projects, "project").Worktree != nil {
		t.Fatal("plain main checkout was annotated as a worktree")
	}
	worktree := projectAt(t, projects, "project/.wt/feature").Worktree
	if worktree == nil || worktree.MainCheckout != "project" {
		t.Fatalf("worktree annotation = %#v, want main checkout project", worktree)
	}
}

func TestActiveWorktreeRootSupportsAbsoluteAndRelativeMarkers(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		relative bool
	}{
		{name: "absolute"},
		{name: "relative", relative: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			base := canonicalTempDir(t)
			mainDir := filepath.Join(base, "main")
			worktreeDir := filepath.Join(base, "linked")
			writeFile(t, filepath.Join(mainDir, "justfile"), "main")
			writeFile(t, filepath.Join(worktreeDir, "nested", "justfile"), "worktree")
			if testCase.relative {
				writeRelativeWorktreeMarkers(t, mainDir, worktreeDir, "feature")
			} else {
				writeWorktreeMarkers(t, mainDir, worktreeDir, "feature")
			}

			root, linked, err := ActiveWorktreeRoot(filepath.Join(worktreeDir, "nested"))
			if err != nil || !linked || root != worktreeDir {
				t.Fatalf("ActiveWorktreeRoot = %q, %t, %v, want %q, true", root, linked, err, worktreeDir)
			}
			registry, err := NewRegistry(
				filepath.Join(worktreeDir, "nested"),
				mustRunnerRegistry(t),
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if registry.Root() != filepath.Join(worktreeDir, "nested") ||
				registry.WorktreeRoot() != worktreeDir {
				t.Fatalf(
					"registry roots = %q, %q, want explicit nested root and %q identity",
					registry.Root(),
					registry.WorktreeRoot(),
					worktreeDir,
				)
			}
		})
	}
}

func TestDiscoverSupportsRelativeWorktreeMarkers(t *testing.T) {
	root := canonicalTempDir(t)
	mainDir := filepath.Join(root, "project")
	worktreeDir := filepath.Join(mainDir, ".wt", "feature")
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeRelativeWorktreeMarkers(t, mainDir, worktreeDir, "feature")

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{
		"project",
		"project/.wt/feature",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project paths = %#v, want %#v", got, want)
	}
}

func TestActiveWorktreeRootRejectsMalformedAndUnsafeMarkers(t *testing.T) {
	t.Run("malformed active marker", func(t *testing.T) {
		worktreeDir := filepath.Join(canonicalTempDir(t), "linked")
		writeFile(t, filepath.Join(worktreeDir, ".git"), "gitdir: first\nsecond\n")
		if _, _, err := ActiveWorktreeRoot(worktreeDir); err == nil ||
			!strings.Contains(err.Error(), "must contain one line") {
			t.Fatalf("ActiveWorktreeRoot error = %v, want malformed marker error", err)
		}
	})

	t.Run("mismatched registry back-reference", func(t *testing.T) {
		base := canonicalTempDir(t)
		mainDir := filepath.Join(base, "main")
		worktreeDir := filepath.Join(base, "linked")
		writeWorktreeMarkers(t, mainDir, worktreeDir, "feature")
		writeFile(
			t,
			filepath.Join(mainDir, ".git", "worktrees", "feature", "gitdir"),
			filepath.Join(base, "other", ".git")+"\n",
		)
		if _, _, err := ActiveWorktreeRoot(worktreeDir); err == nil ||
			!strings.Contains(err.Error(), "back-reference") {
			t.Fatalf("ActiveWorktreeRoot error = %v, want unsafe back-reference error", err)
		}
	})

	t.Run("symlink active marker", func(t *testing.T) {
		base := canonicalTempDir(t)
		worktreeDir := filepath.Join(base, "linked")
		writeFile(t, filepath.Join(base, "marker"), "gitdir: invalid\n")
		if err := os.MkdirAll(worktreeDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			filepath.Join(base, "marker"),
			filepath.Join(worktreeDir, ".git"),
		); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, _, err := ActiveWorktreeRoot(worktreeDir); err == nil ||
			!strings.Contains(err.Error(), "not a regular file or directory") {
			t.Fatalf("ActiveWorktreeRoot error = %v, want symlink rejection", err)
		}
	})
}

func TestDiscoverRejectsRegisteredWorktreeOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "project")
	externalWorktree := t.TempDir()
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	writeFile(t, filepath.Join(externalWorktree, "justfile"), "external")
	writeWorktreeMarkers(t, mainDir, externalWorktree, "external")

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{"project"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project paths = %#v, want %#v", got, want)
	}
}

func TestDiscoverRejectsWorktreeWithMismatchedBackReference(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "project")
	worktreeDir := filepath.Join(mainDir, ".wt", "stale")
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeWorktreeMarkers(t, mainDir, worktreeDir, "stale")
	wrongEntry := filepath.Join(mainDir, ".git", "worktrees", "other")
	writeFile(t, filepath.Join(worktreeDir, ".git"), "gitdir: "+wrongEntry+"\n")

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{"project"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project paths = %#v, want %#v", got, want)
	}
}

func TestDiscoverRejectsWorktreeBackReferenceThroughSymlinkAlias(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "project")
	worktreeDir := filepath.Join(mainDir, ".wt", "feature")
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeWorktreeMarkers(t, mainDir, worktreeDir, "feature")
	mainAlias := filepath.Join(root, "project-alias")
	if err := os.Symlink(mainDir, mainAlias); err != nil {
		t.Skipf("cannot create directory symlink: %v", err)
	}
	writeFile(
		t,
		filepath.Join(worktreeDir, ".git"),
		"gitdir: "+filepath.Join(mainAlias, ".git", "worktrees", "feature")+"\n",
	)

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{"project"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project paths = %#v, want %#v", got, want)
	}
}

func TestDiscoverDeduplicatesWalkedAndAdmittedWorktree(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "project")
	worktreeDir := filepath.Join(mainDir, ".wt", "feature")
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeWorktreeMarkers(t, mainDir, worktreeDir, "feature")

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: MaxDepthUnlimited, IncludeHidden: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{
		"project",
		"project/.wt/feature",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project paths = %#v, want %#v", got, want)
	}
}

func TestDiscoverDoesNotAnnotateWalkedWorktreeWithMismatchedBackReference(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "project")
	worktreeDir := filepath.Join(mainDir, ".wt", "stale")
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeWorktreeMarkers(t, mainDir, worktreeDir, "stale")
	entryDir := filepath.Join(mainDir, ".git", "worktrees", "stale")
	wrongGitFile := filepath.Join(mainDir, ".wt", "other", ".git")
	writeFile(t, filepath.Join(entryDir, "gitdir"), wrongGitFile+"\n")

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: MaxDepthUnlimited, IncludeHidden: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{
		"project",
		"project/.wt/stale",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project paths = %#v, want %#v", got, want)
	}
	if worktree := projectAt(t, projects, "project/.wt/stale").Worktree; worktree != nil {
		t.Fatalf("stale worktree annotation = %#v, want nil", worktree)
	}
}

func TestDiscoverDoesNotAnnotateCandidateThroughSymlinkAlias(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "project")
	worktreeDir := filepath.Join(mainDir, ".wt", "feature")
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeWorktreeMarkers(t, mainDir, worktreeDir, "feature")
	candidateAlias := filepath.Join(root, "candidate-alias")
	if err := os.Symlink(worktreeDir, candidateAlias); err != nil {
		t.Skipf("cannot create directory symlink: %v", err)
	}
	entryDir := filepath.Join(mainDir, ".git", "worktrees", "feature")
	writeFile(t, filepath.Join(entryDir, "gitdir"), filepath.Join(candidateAlias, ".git")+"\n")

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: MaxDepthUnlimited, IncludeHidden: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{
		"project",
		"project/.wt/feature",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project paths = %#v, want %#v", got, want)
	}
	if worktree := projectAt(t, projects, "project/.wt/feature").Worktree; worktree != nil {
		t.Fatalf("aliased worktree annotation = %#v, want nil", worktree)
	}
}

func TestDiscoverReturnsWorktreeDirectoryInspectionError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are required for this error path")
	}
	root := t.TempDir()
	mainDir := filepath.Join(root, "project")
	gitDir := filepath.Join(mainDir, ".git")
	worktreesDir := filepath.Join(gitDir, "worktrees")
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	writeFile(t, filepath.Join(worktreesDir, "placeholder"), "metadata")
	if err := os.Chmod(gitDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(gitDir, 0o750); err != nil {
			t.Errorf("restore git directory permissions: %v", err)
		}
	})
	if _, err := os.Lstat(worktreesDir); err == nil {
		t.Skip("filesystem permits traversal through a mode-zero directory")
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("probe worktree registry: %v", err)
	}

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = registry.Discover(context.Background(), Filter{Path: ".", MaxDepth: 1})
	if err == nil {
		t.Fatal("Discover succeeded with an unreadable worktree registry")
	}
	if !errors.Is(err, os.ErrPermission) ||
		!strings.Contains(err.Error(), "git worktree registry") {
		t.Fatalf("Discover error = %v, want contextual permission error", err)
	}
}

func TestDiscoverReturnsWorktreeMarkerReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are required for this error path")
	}
	root := t.TempDir()
	mainDir := filepath.Join(root, "project")
	worktreeDir := filepath.Join(mainDir, ".wt", "feature")
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeWorktreeMarkers(t, mainDir, worktreeDir, "feature")
	marker := filepath.Join(mainDir, ".git", "worktrees", "feature", "gitdir")
	if err := os.Chmod(marker, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(marker, 0o600); err != nil {
			t.Errorf("restore registry marker permissions: %v", err)
		}
	})
	if _, err := os.ReadFile(marker); err == nil {
		t.Skip("filesystem permits reading a mode-zero file")
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("probe registry marker: %v", err)
	}

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = registry.Discover(context.Background(), Filter{Path: ".", MaxDepth: 1})
	if err == nil {
		t.Fatal("Discover succeeded with an unreadable registry marker")
	}
	if !errors.Is(err, os.ErrPermission) ||
		!strings.Contains(err.Error(), "git worktree entry") {
		t.Fatalf("Discover error = %v, want contextual permission error", err)
	}
}

func TestFindReturnsWorktreeMarkerReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are required for this error path")
	}
	root := t.TempDir()
	worktreeDir := filepath.Join(root, "linked")
	externalMain := t.TempDir()
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeWorktreeMarkers(t, externalMain, worktreeDir, "linked")
	marker := filepath.Join(worktreeDir, ".git")
	if err := os.Chmod(marker, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(marker, 0o600); err != nil {
			t.Errorf("restore worktree marker permissions: %v", err)
		}
	})
	if _, err := os.ReadFile(marker); err == nil {
		t.Skip("filesystem permits reading a mode-zero file")
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("probe worktree marker: %v", err)
	}

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Find(context.Background(), "linked")
	if err == nil {
		t.Fatal("Find succeeded with an unreadable worktree marker")
	}
	if !errors.Is(err, os.ErrPermission) ||
		!strings.Contains(err.Error(), "inspect worktree metadata") {
		t.Fatalf("Find error = %v, want contextual permission error", err)
	}
}

func TestFindTreatsSymlinkedMainCheckoutAsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	externalMain := t.TempDir()
	mainLink := filepath.Join(root, "main-link")
	if err := os.Symlink(externalMain, mainLink); err != nil {
		t.Skipf("cannot create directory symlink: %v", err)
	}
	worktreeDir := filepath.Join(root, "linked")
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeFile(
		t,
		filepath.Join(worktreeDir, ".git"),
		"gitdir: "+filepath.Join(mainLink, ".git", "worktrees", "linked")+"\n",
	)

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := registry.Find(context.Background(), "linked")
	if err != nil {
		t.Fatal(err)
	}
	if project.Worktree == nil ||
		project.Worktree.MainCheckout != outsideWorkspaceMainCheckout {
		t.Fatalf("worktree annotation = %#v, want outside workspace", project.Worktree)
	}
}

func TestFindRejectsInternalRegistryThroughSymlink(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	worktreeDir := filepath.Join(root, "linked")
	externalGitDir := t.TempDir()
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	if err := os.Symlink(externalGitDir, filepath.Join(mainDir, ".git")); err != nil {
		t.Skipf("cannot create directory symlink: %v", err)
	}
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeFile(
		t,
		filepath.Join(externalGitDir, "worktrees", "linked", "gitdir"),
		filepath.Join(worktreeDir, ".git")+"\n",
	)
	writeFile(
		t,
		filepath.Join(worktreeDir, ".git"),
		"gitdir: "+filepath.Join(mainDir, ".git", "worktrees", "linked")+"\n",
	)

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := registry.Find(context.Background(), "linked")
	if err != nil {
		t.Fatal(err)
	}
	if project.Worktree != nil {
		t.Fatalf("worktree annotation = %#v, want nil", project.Worktree)
	}
}

func TestDiscoverUsesWindowsFilesystemIdentityForWorktreePaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows case-insensitive path identity is required")
	}
	root := t.TempDir()
	mainDir := filepath.Join(root, "ProjectCase")
	worktreeDir := filepath.Join(mainDir, ".wt", "FeatureCase")
	entryDir := filepath.Join(mainDir, ".git", "worktrees", "FeatureCase")
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeFile(
		t,
		filepath.Join(entryDir, "gitdir"),
		strings.ToUpper(filepath.Join(worktreeDir, ".git"))+"\n",
	)
	writeFile(
		t,
		filepath.Join(worktreeDir, ".git"),
		"gitdir: "+strings.ToUpper(entryDir)+"\n",
	)

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: ".", MaxDepth: MaxDepthUnlimited, IncludeHidden: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{
		"ProjectCase",
		"ProjectCase/.wt/FeatureCase",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project paths = %#v, want %#v", got, want)
	}
	worktree := projectAt(t, projects, "ProjectCase/.wt/FeatureCase").Worktree
	if worktree == nil || worktree.MainCheckout != "ProjectCase" {
		t.Fatalf("worktree annotation = %#v, want main checkout ProjectCase", worktree)
	}
}

func TestWindowsPathResolutionRejectsAmbiguousCaseFold(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows case-fold resolution is required")
	}
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	upperVariant := filepath.Join(parent, "Feature")
	lowerVariant := filepath.Join(parent, "feature")
	if err := os.MkdirAll(upperVariant, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lowerVariant, 0o750); err != nil {
		t.Fatal(err)
	}
	upperInfo, err := os.Lstat(upperVariant)
	if err != nil {
		t.Fatal(err)
	}
	lowerInfo, err := os.Lstat(lowerVariant)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(upperInfo, lowerInfo) {
		t.Skip("temporary directory is not case-sensitive")
	}

	resolver := newPathResolver(context.Background())
	resolved, state, err := resolver.resolveWithin(
		parent,
		upperVariant,
		resolvedDirectory,
	)
	if err != nil {
		t.Fatal(err)
	}
	if state != pathResolved || resolved != upperVariant {
		t.Fatalf("exact path resolution = (%q, %d), want %q", resolved, state, upperVariant)
	}
	ambiguous := filepath.Join(parent, "FEATURE")
	resolved, state, err = resolver.resolveWithin(parent, ambiguous, resolvedDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if state != pathAmbiguous || resolved != "" {
		t.Fatalf("folded path resolution = (%q, %d), want ambiguous", resolved, state)
	}
}

func TestFindRejectsAlternateCaseGitRegistryComponentsOnCaseSensitiveWindows(
	t *testing.T,
) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows case-sensitive path identity is required")
	}
	testCases := []struct {
		canonicalParent func(string) string
		alternateParent func(string) string
		entryDir        func(string) string
		name            string
	}{
		{
			name: "git directory",
			canonicalParent: func(mainDir string) string {
				return filepath.Join(mainDir, ".git")
			},
			alternateParent: func(mainDir string) string {
				return filepath.Join(mainDir, ".GIT")
			},
			entryDir: func(mainDir string) string {
				return filepath.Join(mainDir, ".GIT", "worktrees", "linked")
			},
		},
		{
			name: "worktrees directory",
			canonicalParent: func(mainDir string) string {
				return filepath.Join(mainDir, ".git", "worktrees")
			},
			alternateParent: func(mainDir string) string {
				return filepath.Join(mainDir, ".git", "WORKTREES")
			},
			entryDir: func(mainDir string) string {
				return filepath.Join(mainDir, ".git", "WORKTREES", "linked")
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			mainDir := filepath.Join(root, "main")
			worktreeDir := filepath.Join(root, "linked")
			canonicalParent := testCase.canonicalParent(mainDir)
			alternateParent := testCase.alternateParent(mainDir)
			canonicalRegistry := filepath.Join(mainDir, ".git", "worktrees")
			if err := os.MkdirAll(canonicalRegistry, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(testCase.entryDir(mainDir), 0o750); err != nil {
				t.Fatal(err)
			}
			canonicalInfo, err := os.Lstat(canonicalParent)
			if err != nil {
				t.Fatal(err)
			}
			alternateInfo, err := os.Lstat(alternateParent)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(canonicalInfo, alternateInfo) {
				t.Skip("temporary directory is not case-sensitive")
			}
			writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
			writeFile(
				t,
				filepath.Join(testCase.entryDir(mainDir), "gitdir"),
				filepath.Join(worktreeDir, ".git")+"\n",
			)
			writeFile(
				t,
				filepath.Join(worktreeDir, ".git"),
				"gitdir: "+testCase.entryDir(mainDir)+"\n",
			)

			registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			project, err := registry.Find(context.Background(), "linked")
			if err != nil {
				t.Fatal(err)
			}
			if project.Worktree != nil {
				t.Fatalf("worktree annotation = %#v, want nil", project.Worktree)
			}
		})
	}
}

func TestDiscoverRejectsAlternateCaseWorktreeMarkerOnCaseSensitiveWindows(
	t *testing.T,
) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows case-sensitive path identity is required")
	}
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	worktreeDir := filepath.Join(mainDir, ".wt", "linked")
	entryDir := filepath.Join(mainDir, ".git", "worktrees", "linked")
	canonicalMarker := filepath.Join(worktreeDir, ".git")
	alternateMarker := filepath.Join(worktreeDir, ".GIT")
	writeFile(t, filepath.Join(mainDir, "justfile"), "main")
	writeFile(t, canonicalMarker, "gitdir: "+entryDir+"\n")
	writeFile(t, alternateMarker, "gitdir: "+entryDir+"\n")
	canonicalInfo, err := os.Lstat(canonicalMarker)
	if err != nil {
		t.Fatal(err)
	}
	alternateInfo, err := os.Lstat(alternateMarker)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(canonicalInfo, alternateInfo) {
		t.Skip("temporary directory is not case-sensitive")
	}
	writeFile(t, filepath.Join(entryDir, "gitdir"), alternateMarker+"\n")

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, _, err := registry.Discover(
		context.Background(),
		Filter{Path: "main", MaxDepth: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectPaths(projects), []string{"main"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project paths = %#v, want %#v", got, want)
	}
}

func TestFindAnnotatesWorktreeWhoseMainCheckoutIsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	worktreeDir := filepath.Join(root, "linked")
	externalMain := t.TempDir()
	writeFile(t, filepath.Join(worktreeDir, "justfile"), "worktree")
	writeFile(
		t,
		filepath.Join(worktreeDir, ".git"),
		"gitdir: "+filepath.Join(externalMain, ".git", "worktrees", "linked")+"\n",
	)

	registry, err := NewRegistry(root, mustRunnerRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := registry.Find(context.Background(), "linked")
	if err != nil {
		t.Fatal(err)
	}
	if project.Worktree == nil ||
		project.Worktree.MainCheckout != outsideWorkspaceMainCheckout {
		t.Fatalf("worktree annotation = %#v", project.Worktree)
	}
}

func writeWorktreeMarkers(t *testing.T, mainDir, worktreeDir, name string) {
	t.Helper()
	entryDir := filepath.Join(mainDir, ".git", "worktrees", name)
	writeFile(t, filepath.Join(entryDir, "gitdir"), filepath.Join(worktreeDir, ".git")+"\n")
	writeFile(t, filepath.Join(worktreeDir, ".git"), "gitdir: "+entryDir+"\n")
}

func writeRelativeWorktreeMarkers(t *testing.T, mainDir, worktreeDir, name string) {
	t.Helper()
	entryDir := filepath.Join(mainDir, ".git", "worktrees", name)
	backReference, err := filepath.Rel(entryDir, filepath.Join(worktreeDir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := filepath.Rel(worktreeDir, entryDir)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(entryDir, "gitdir"), backReference+"\n")
	writeFile(t, filepath.Join(worktreeDir, ".git"), "gitdir: "+gitDir+"\n")
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func testRegistration(candidate runner.Runner) runner.Registration {
	return runner.StaticRegistration(candidate, runner.UnreviewedPermissions())
}

func (fakeJustRunner) Detect(projectDir string) (bool, error) {
	for _, name := range []string{"justfile", "Justfile", ".justfile"} {
		if info, err := os.Stat(filepath.Join(projectDir, name)); err == nil && !info.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

func (fakeJustRunner) ListTasks(_ context.Context, projectDir string) ([]runner.Task, error) {
	for _, name := range []string{"justfile", "Justfile", ".justfile"} {
		// #nosec G304 -- test runner reads one of its fixed justfile names below the test project.
		data, err := os.ReadFile(filepath.Join(projectDir, name))
		if err == nil {
			if strings.Contains(string(data), "invalid") {
				return nil, errors.New("invalid justfile")
			}
			return []runner.Task{{ID: "just:task", Runner: "just", Name: "task"}}, nil
		}
	}
	return nil, errors.New("justfile disappeared")
}

func (fakeJustRunner) BuildCommand(
	context.Context,
	string,
	runner.Task,
	[]string,
) (*exec.Cmd, error) {
	return nil, errors.New("not used")
}

//nolint:govet // Embedded runner keeps the test double compact.
type countingFakeJustRunner struct {
	calls *int
	fakeJustRunner
}

func (r countingFakeJustRunner) ListTasks(ctx context.Context, projectDir string) ([]runner.Task, error) {
	*r.calls++
	return r.fakeJustRunner.ListTasks(ctx, projectDir)
}

type fakeMakeRunner struct{}

func (fakeMakeRunner) Name() string { return "make" }

func (fakeMakeRunner) Detect(projectDir string) (bool, error) {
	info, err := os.Lstat(filepath.Join(projectDir, "Makefile"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect Makefile: %w", err)
	}
	return info.Mode().IsRegular(), nil
}

func (fakeMakeRunner) ListTasks(context.Context, string) ([]runner.Task, error) {
	return []runner.Task{{ID: "make:task", Runner: "make", Name: "task"}}, nil
}

func (fakeMakeRunner) BuildCommand(
	context.Context,
	string,
	runner.Task,
	[]string,
) (*exec.Cmd, error) {
	return nil, errors.New("not used")
}

// unavailableToolRunner stands for a runner whose build tool is not installed
// on this host.
type unavailableToolRunner struct{ fakeMakeRunner }

func (unavailableToolRunner) ListTasks(context.Context, string) ([]runner.Task, error) {
	return nil, fmt.Errorf("find the Make binary: %w", runner.ErrToolUnavailable)
}

// detectUnavailableToolRunner stands for a runner that cannot even detect a
// project because its build tool is missing, so the warning is the only thing
// the directory contributes.
type detectUnavailableToolRunner struct{ fakeMakeRunner }

func (detectUnavailableToolRunner) Detect(string) (bool, error) {
	return false, fmt.Errorf("find the Make binary: %w", runner.ErrToolUnavailable)
}

// partialFakeMakeRunner stands for a runner that discovers part of a project and
// reports why the rest is missing.
type partialFakeMakeRunner struct{ fakeMakeRunner }

func (r partialFakeMakeRunner) ListTasks(
	ctx context.Context,
	projectDir string,
) ([]runner.Task, error) {
	tasks, err := r.fakeMakeRunner.ListTasks(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	return tasks, errors.New("half of the targets are unreadable")
}

type includingFakeJustRunner struct {
	fakeJustRunner
	included string
}

func (r includingFakeJustRunner) IncludedProjectDirs(context.Context, string) ([]string, error) {
	return []string{r.included}, nil
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func projectAt(t *testing.T, projects []Project, relPath string) Project {
	t.Helper()
	for _, project := range projects {
		if project.RelPath == relPath {
			return project
		}
	}
	t.Fatalf("project %q not found in %#v", relPath, projects)
	return Project{}
}

func projectPaths(projects []Project) []string {
	paths := make([]string, 0, len(projects))
	for _, project := range projects {
		paths = append(paths, project.RelPath)
	}
	return paths
}
