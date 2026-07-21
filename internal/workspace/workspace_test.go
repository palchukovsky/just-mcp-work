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

	runners, err := runner.NewRegistry(fakeJustRunner{})
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

	runners, err := runner.NewRegistry(fakeJustRunner{}, unavailableToolRunner{})
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

// TestDiscoverKeepsProjectWithOnlyWarnings covers the directory whose single
// signal is a warning: dropping it would hide the diagnosis of why a project
// the operator expects to see reports nothing at all.
func TestDiscoverKeepsProjectWithOnlyWarnings(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Makefile"), "root")

	runners, err := runner.NewRegistry(detectUnavailableToolRunner{})
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

	runners, err := runner.NewRegistry(partialFakeMakeRunner{})
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
	runners, err := runner.NewRegistry(includingFakeJustRunner{
		included: filepath.Join(root, "nested"),
	})
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
		includingFakeJustRunner{included: shared},
		fakeMakeRunner{},
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
	runners, err := runner.NewRegistry(countingFakeJustRunner{calls: &calls})
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
	runners, err := runner.NewRegistry(fakeJustRunner{}, fakeMakeRunner{})
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
	registry, err := runner.NewRegistry(fakeJustRunner{})
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
	runners, err := runner.NewRegistry(fakeJustRunner{})
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
