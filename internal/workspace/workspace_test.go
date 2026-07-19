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
}

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
}

func TestDiscoverFilterKeepsProjectDetailsAndValidatesInput(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "dual", "justfile"), "fixture")
	writeFile(t, filepath.Join(root, "dual", "Makefile"), "fixture")
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
