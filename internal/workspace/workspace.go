// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package workspace discovers runner-backed projects below one root directory.
package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

// Project is a discovered directory with one or more task runners.
//
//nolint:govet // Field order follows the stable MCP project response shape.
type Project struct {
	RelPath string            `json:"rel_path"`
	Runners []string          `json:"runners"`
	Status  string            `json:"status"`
	Errors  map[string]string `json:"errors,omitempty"`
	Tasks   map[string][]runner.Task
	Dir     string
}

// Registry scans a workspace on demand. It does not retain stale project data.
type Registry struct {
	root     string
	runners  *runner.Registry
	excludes []string
}

// NewRegistry creates a workspace registry rooted at root.
func NewRegistry(root string, runners *runner.Registry, excludes []string) (*Registry, error) {
	if runners == nil {
		return nil, fmt.Errorf("runner registry must not be nil")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	return &Registry{
		root:     filepath.Clean(absRoot),
		runners:  runners,
		excludes: append([]string(nil), excludes...),
	}, nil
}

// Root returns the resolved workspace root.
func (r *Registry) Root() string { return r.root }

// Discover scans the root and returns projects sorted by relative path.
func (r *Registry) Discover(ctx context.Context) ([]Project, error) {
	projects := make([]Project, 0)
	err := filepath.WalkDir(r.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !entry.IsDir() {
			return nil
		}
		if path != r.root && entry.Type()&fs.ModeSymlink != 0 {
			return filepath.SkipDir
		}
		if path != r.root && r.excluded(path) {
			return filepath.SkipDir
		}

		project, found, err := r.inspect(ctx, path)
		if err != nil {
			return err
		}
		if found {
			projects = append(projects, project)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan workspace: %w", err)
	}
	included := make(map[includedProject]struct{})
	for _, project := range projects {
		if err := r.markIncluded(ctx, project, included); err != nil {
			return nil, err
		}
	}
	filtered := projects[:0]
	for _, project := range projects {
		if suppressIncluded(&project, included) {
			filtered = append(filtered, project)
		}
	}
	projects = filtered
	sort.Slice(projects, func(i, j int) bool { return projects[i].RelPath < projects[j].RelPath })
	return projects, nil
}

type includedProject struct {
	dir    string
	runner string
}

func (r *Registry) markIncluded(
	ctx context.Context,
	project Project,
	included map[includedProject]struct{},
) error {
	for _, name := range project.Runners {
		if _, failed := project.Errors[name]; failed {
			continue
		}
		candidate, ok := r.runners.Get(name)
		if !ok {
			continue
		}
		provider, ok := candidate.(runner.IncludedProjectProvider)
		if !ok {
			continue
		}
		dirs, err := provider.IncludedProjectDirs(ctx, project.Dir)
		if err != nil {
			return fmt.Errorf("find included projects for %s: %w", project.RelPath, err)
		}
		for _, dir := range dirs {
			clean := filepath.Clean(dir)
			rel, relErr := filepath.Rel(r.root, clean)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			if clean != project.Dir {
				included[includedProject{dir: clean, runner: name}] = struct{}{}
			}
		}
	}
	return nil
}

func suppressIncluded(project *Project, included map[includedProject]struct{}) bool {
	runners := project.Runners[:0]
	for _, name := range project.Runners {
		key := includedProject{dir: filepath.Clean(project.Dir), runner: name}
		if _, ok := included[key]; ok {
			delete(project.Tasks, name)
			delete(project.Errors, name)
			continue
		}
		runners = append(runners, name)
	}
	project.Runners = runners
	for key := range included {
		if key.dir == filepath.Clean(project.Dir) {
			delete(project.Errors, key.runner)
		}
	}
	if len(project.Runners) == 0 && len(project.Errors) == 0 {
		return false
	}
	project.Status = "ready"
	if len(project.Errors) > 0 {
		project.Status = "error"
	}
	return true
}

// Find discovers projects and looks up one relative project path.
func (r *Registry) Find(ctx context.Context, relPath string) (Project, error) {
	if !validRelPath(relPath) {
		return Project{}, fmt.Errorf("invalid project path %q", relPath)
	}
	projects, err := r.Discover(ctx)
	if err != nil {
		return Project{}, err
	}
	for _, project := range projects {
		if project.RelPath == relPath {
			return project, nil
		}
	}
	return Project{}, fmt.Errorf("unknown project_path %q", relPath)
}

// ResolveDir resolves a workspace-relative directory without following symlinks.
// An empty path resolves to the workspace root.
func (r *Registry) ResolveDir(relPath string) (string, error) {
	if relPath == "" {
		relPath = "."
	}
	if !validRelPath(relPath) {
		return "", fmt.Errorf("invalid working directory %q", relPath)
	}
	path := r.root
	if relPath == "." {
		return path, nil
	}
	for component := range strings.SplitSeq(filepath.Clean(relPath), string(filepath.Separator)) {
		path = filepath.Join(path, component)
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("inspect working directory %q: %w", relPath, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf("working directory %q contains a symlink", relPath)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("working directory %q is not a directory", relPath)
		}
	}
	return path, nil
}

func (r *Registry) inspect(ctx context.Context, dir string) (Project, bool, error) {
	rel, err := filepath.Rel(r.root, dir)
	if err != nil {
		return Project{}, false, fmt.Errorf("resolve project path: %w", err)
	}
	if rel == "." {
		rel = "."
	}
	project := Project{
		RelPath: filepath.ToSlash(rel),
		Status:  "ready",
		Tasks:   make(map[string][]runner.Task),
	}
	for _, candidate := range r.runners.All() {
		detected, err := candidate.Detect(dir)
		if err != nil {
			project.Errors = addError(project.Errors, candidate.Name(), err)
			continue
		}
		if !detected {
			continue
		}
		project.Runners = append(project.Runners, candidate.Name())
		tasks, err := candidate.ListTasks(ctx, dir)
		if err != nil {
			project.Errors = addError(project.Errors, candidate.Name(), err)
			continue
		}
		project.Tasks[candidate.Name()] = tasks
	}
	if len(project.Runners) == 0 && len(project.Errors) == 0 {
		return Project{}, false, nil
	}
	if len(project.Errors) > 0 {
		project.Status = "error"
	}
	project.Dir = dir
	return project, true, nil
}

func validRelPath(path string) bool {
	if path == "." {
		return true
	}
	if path == "" || filepath.IsAbs(path) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	return clean != ".." && !strings.HasPrefix(clean, "../")
}

func (r *Registry) excluded(path string) bool {
	rel, err := filepath.Rel(r.root, path)
	if err != nil {
		return true
	}
	name := filepath.Base(path)
	for _, pattern := range append(
		[]string{".git", "node_modules", "target", ".just-mcp-work"},
		r.excludes...,
	) {
		if name == pattern {
			return true
		}
		matched, matchErr := pathpkg.Match(pattern, filepath.ToSlash(rel))
		if matchErr == nil && matched {
			return true
		}
	}
	return false
}

func addError(errors map[string]string, name string, err error) map[string]string {
	if errors == nil {
		errors = make(map[string]string)
	}
	errors[name] = err.Error()
	return errors
}
