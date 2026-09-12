// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package writescope restricts a command to caller-declared writable paths.
package writescope

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Scope is a resolved set of absolute writable roots.
type Scope struct {
	roots []string
}

// Resolve validates declared paths against worktreeRoot and returns the
// effective scope.
func Resolve(worktreeRoot string, declared []string, extra []string) (Scope, error) {
	resolvedRoot, err := resolveWorktreeRoot(worktreeRoot)
	if err != nil {
		return Scope{}, err
	}
	if len(declared) == 0 {
		return Scope{}, fmt.Errorf("declared writable paths: list must not be empty")
	}

	declaredRoots := make([]string, 0, len(declared))
	for _, entry := range declared {
		resolved, resolveErr := resolveDeclared(resolvedRoot, entry)
		if resolveErr != nil {
			return Scope{}, fmt.Errorf("declared writable path %q: %w", entry, resolveErr)
		}
		declaredRoots = append(declaredRoots, resolved)
	}
	extraRoots := make([]string, 0, len(extra))
	for _, entry := range extra {
		resolved, resolveErr := resolveExtra(entry)
		if resolveErr != nil {
			return Scope{}, fmt.Errorf("extra writable path %q: %w", entry, resolveErr)
		}
		extraRoots = append(extraRoots, resolved)
	}
	for _, extraRoot := range extraRoots {
		for _, declaredRoot := range declaredRoots {
			if pathWithin(extraRoot, declaredRoot) {
				return Scope{}, fmt.Errorf(
					"extra writable path %q contains or equals declared writable path %q",
					extraRoot,
					declaredRoot,
				)
			}
		}
	}

	roots := slices.Concat(declaredRoots, extraRoots)
	return Scope{roots: foldRoots(roots)}, nil
}

// Roots returns the effective writable roots, absolute, in a stable order.
func (scope Scope) Roots() []string {
	return append([]string(nil), scope.roots...)
}

func resolveWorktreeRoot(entry string) (string, error) {
	if !filepath.IsAbs(entry) {
		return "", fmt.Errorf("worktree root %q must be absolute", entry)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(entry))
	if err != nil {
		return "", fmt.Errorf("worktree root %q must be an existing directory: %w", entry, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect worktree root %q: %w", entry, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("worktree root %q must be a directory", entry)
	}
	return filepath.Clean(resolved), nil
}

func resolveDeclared(worktreeRoot string, entry string) (string, error) {
	if strings.TrimSpace(entry) == "" {
		return "", fmt.Errorf("must not be blank")
	}
	if filepath.IsAbs(entry) || filepath.VolumeName(entry) != "" {
		return "", fmt.Errorf("must be relative to the worktree root")
	}
	cleaned := filepath.Clean(entry)
	if escapesParent(cleaned) {
		return "", fmt.Errorf("escapes the worktree root via parent traversal")
	}

	candidate := filepath.Join(worktreeRoot, cleaned)
	if !pathWithin(worktreeRoot, candidate) {
		return "", fmt.Errorf("escapes the worktree root")
	}
	if err := rejectFinalSymlink(candidate); err != nil {
		return "", err
	}
	resolved, err := resolveExistingPrefix(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve longest existing prefix: %w", err)
	}
	if !pathWithin(worktreeRoot, resolved) {
		return "", fmt.Errorf("existing prefix resolves outside the worktree root")
	}
	return resolved, nil
}

func resolveExtra(entry string) (string, error) {
	if strings.TrimSpace(entry) == "" {
		return "", fmt.Errorf("must not be blank")
	}
	if !filepath.IsAbs(entry) {
		return "", fmt.Errorf("must be absolute")
	}
	cleaned := filepath.Clean(entry)
	if err := rejectFinalSymlink(cleaned); err != nil {
		return "", err
	}
	resolved, err := resolveExistingPrefix(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve longest existing prefix: %w", err)
	}
	return resolved, nil
}

func rejectFinalSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %q is a symbolic link; declare its target instead", path)
	}
	return nil
}

func resolveExistingPrefix(path string) (string, error) {
	current := filepath.Clean(path)
	missing := make([]string, 0)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return "", fmt.Errorf("resolve %q: %w", current, resolveErr)
			}
			for _, part := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, part)
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("inspect %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("find existing prefix for %q: %w", path, err)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func escapesParent(path string) bool {
	return path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator))
}

func pathWithin(parent string, path string) bool {
	relative, err := filepath.Rel(parent, path)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative == "." || !escapesParent(relative)
}

func foldRoots(roots []string) []string {
	unique := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		unique = append(unique, root)
	}

	folded := make([]string, 0, len(unique))
	for index, root := range unique {
		contained := false
		for otherIndex, other := range unique {
			if index != otherIndex && pathWithin(other, root) {
				contained = true
				break
			}
		}
		if !contained {
			folded = append(folded, root)
		}
	}
	sort.Strings(folded)
	return folded
}
