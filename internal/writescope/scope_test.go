// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package writescope

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestResolveRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write root fixture: %v", err)
	}

	tests := []struct {
		name     string
		root     string
		want     string
		declared []string
	}{
		{
			name:     "empty list",
			root:     worktreeRoot,
			declared: nil,
			want:     "list must not be empty",
		},
		{
			name:     "relative worktree root",
			root:     "relative-root",
			declared: []string{"output"},
			want:     `worktree root "relative-root" must be absolute`,
		},
		{
			name:     "missing worktree root",
			root:     filepath.Join(worktreeRoot, "missing-root"),
			declared: []string{"output"},
			want:     "must be an existing directory",
		},
		{
			name:     "non-directory worktree root",
			root:     fileRoot,
			declared: []string{"output"},
			want:     "must be a directory",
		},
		{
			name:     "blank entry",
			root:     worktreeRoot,
			declared: []string{" \t "},
			want:     `declared writable path " \t ": must not be blank`,
		},
		{
			name:     "absolute entry",
			root:     worktreeRoot,
			declared: []string{filepath.Join(worktreeRoot, "output")},
			want:     "must be relative to the worktree root",
		},
		{
			name:     "parent escape",
			root:     worktreeRoot,
			declared: []string{filepath.Join("..", "outside")},
			want:     "escapes the worktree root via parent traversal",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Resolve(test.root, test.declared, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestResolveRejectsInvalidExtraPaths(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	for _, test := range []struct {
		name  string
		extra string
		want  string
	}{
		{name: "blank", extra: "  ", want: `extra writable path "  ": must not be blank`},
		{name: "relative", extra: "state", want: "must be absolute"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Resolve(worktreeRoot, []string{"output"}, []string{test.extra})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestResolveRejectsSymlinkLeavingWorktree(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(worktreeRoot, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("create symlink fixture: %v", err)
	}

	entry := filepath.Join("outside-link", "not-created-yet")
	_, err := Resolve(worktreeRoot, []string{entry}, nil)
	if err == nil || !strings.Contains(err.Error(), "existing prefix resolves outside") {
		t.Fatalf("Resolve(%q) error = %v, want resolved-prefix escape rejection", entry, err)
	}
}

func TestResolveRejectsDeclaredFinalSymlink(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	target := filepath.Join(worktreeRoot, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(worktreeRoot, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("create symlink fixture: %v", err)
	}

	_, err := Resolve(worktreeRoot, []string{"link"}, nil)
	if err == nil || !strings.Contains(err.Error(), link) ||
		!strings.Contains(err.Error(), "is a symbolic link; declare its target instead") {
		t.Fatalf("Resolve() error = %v, want declared final-symlink refusal naming %q", err, link)
	}
}

func TestResolveRejectsExtraFinalSymlink(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("create symlink fixture: %v", err)
	}

	_, err := Resolve(worktreeRoot, []string{"output"}, []string{link})
	if err == nil || !strings.Contains(err.Error(), link) ||
		!strings.Contains(err.Error(), "is a symbolic link; declare its target instead") {
		t.Fatalf("Resolve() error = %v, want extra final-symlink refusal naming %q", err, link)
	}
}

func TestResolveAllowsMissingTailAfterResolvingSymlinks(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	realRoot := filepath.Join(worktreeRoot, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatalf("create real directory: %v", err)
	}
	link := filepath.Join(worktreeRoot, "link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("create symlink fixture: %v", err)
	}

	scope, err := Resolve(
		worktreeRoot,
		[]string{filepath.Join("link", "new", "output")},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	resolvedRealRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatalf("resolve real directory: %v", err)
	}
	want := []string{filepath.Join(resolvedRealRoot, "new", "output")}
	if got := scope.Roots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %q, want %q", got, want)
	}
}

func TestResolveAllowsResolvedExtraOutsideWorktree(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	extraTarget := t.TempDir()
	extraParent := t.TempDir()
	extraLink := filepath.Join(extraParent, "state-link")
	if err := os.Symlink(extraTarget, extraLink); err != nil {
		t.Skipf("create extra symlink fixture: %v", err)
	}

	scope, err := Resolve(
		worktreeRoot,
		[]string{"output"},
		[]string{filepath.Join(extraLink, "new-state")},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	resolvedWorktreeRoot, err := filepath.EvalSymlinks(worktreeRoot)
	if err != nil {
		t.Fatalf("resolve worktree root: %v", err)
	}
	resolvedExtraTarget, err := filepath.EvalSymlinks(extraTarget)
	if err != nil {
		t.Fatalf("resolve extra target: %v", err)
	}
	want := []string{
		filepath.Join(resolvedExtraTarget, "new-state"),
		filepath.Join(resolvedWorktreeRoot, "output"),
	}
	sort.Strings(want)
	if got := scope.Roots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %q, want resolved declared and extra roots %q", got, want)
	}
}

func TestResolveDeduplicatesAndFoldsContainedRoots(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	scope, err := Resolve(
		worktreeRoot,
		[]string{"b/child", "a", "a", "b", "a/child"},
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	resolvedWorktreeRoot, err := filepath.EvalSymlinks(worktreeRoot)
	if err != nil {
		t.Fatalf("resolve worktree root: %v", err)
	}
	want := []string{
		filepath.Join(resolvedWorktreeRoot, "a"),
		filepath.Join(resolvedWorktreeRoot, "b"),
	}
	if got := scope.Roots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %q, want deduplicated roots %q", got, want)
	}
}

func TestResolveRejectsExtraRootContainingDeclaredRoot(t *testing.T) {
	t.Parallel()

	temporaryRoot := t.TempDir()
	worktreeRoot := filepath.Join(temporaryRoot, "wt")
	if err := os.Mkdir(worktreeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	declaredRoot := filepath.Join(worktreeRoot, "src")
	resolvedTemporaryRoot, err := filepath.EvalSymlinks(temporaryRoot)
	if err != nil {
		t.Fatal(err)
	}
	resolvedWorktreeRoot, err := filepath.EvalSymlinks(worktreeRoot)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Resolve(worktreeRoot, []string{"src"}, []string{temporaryRoot})
	if err == nil || !strings.Contains(err.Error(), resolvedTemporaryRoot) ||
		!strings.Contains(err.Error(), filepath.Join(resolvedWorktreeRoot, "src")) ||
		!strings.Contains(err.Error(), "contains or equals") {
		t.Fatalf(
			"Resolve() error = %v, want containing extra %q and declared %q",
			err,
			resolvedTemporaryRoot,
			declaredRoot,
		)
	}
}

func TestResolveUsesPathComponentsForContainment(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	scope, err := Resolve(worktreeRoot, []string{"a/bc", "a/b"}, nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	resolvedWorktreeRoot, err := filepath.EvalSymlinks(worktreeRoot)
	if err != nil {
		t.Fatalf("resolve worktree root: %v", err)
	}
	want := []string{
		filepath.Join(resolvedWorktreeRoot, "a", "b"),
		filepath.Join(resolvedWorktreeRoot, "a", "bc"),
	}
	if got := scope.Roots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %q, want component-distinct roots %q", got, want)
	}
}

func TestRootsReturnsCopy(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	scope, err := Resolve(worktreeRoot, []string{"output"}, nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	resolvedWorktreeRoot, err := filepath.EvalSymlinks(worktreeRoot)
	if err != nil {
		t.Fatalf("resolve worktree root: %v", err)
	}
	roots := scope.Roots()
	roots[0] = filepath.Join(worktreeRoot, "replacement")
	want := []string{filepath.Join(resolvedWorktreeRoot, "output")}
	if got := scope.Roots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() after caller mutation = %q, want %q", got, want)
	}
}
