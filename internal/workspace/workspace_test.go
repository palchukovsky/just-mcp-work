// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package workspace

import (
	"context"
	"errors"
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
	projects, err := registry.Discover(context.Background())
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
	projects, err := registry.Discover(context.Background())
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
	projects, err := registry.Discover(context.Background())
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
