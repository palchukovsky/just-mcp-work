// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runner_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

func TestFindRegularFileReturnsFirstMatch(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"second", "third"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path, err := runner.FindRegularFile(dir, "first", "second", "third")
	if err != nil {
		t.Fatalf("FindRegularFile: %v", err)
	}
	if want := filepath.Join(dir, "second"); path != want {
		t.Fatalf("FindRegularFile = %q, want %q", path, want)
	}
}

func TestFindRegularFileSkipsNonRegularCandidates(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "first"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(dir, "second")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path, err := runner.FindRegularFile(dir, "first", "second", "target")
	if err != nil {
		t.Fatalf("FindRegularFile: %v", err)
	}
	if want := filepath.Join(dir, "target"); path != want {
		t.Fatalf("FindRegularFile = %q, want %q", path, want)
	}
}

func TestFindRegularFileReportsMissingFile(t *testing.T) {
	_, err := runner.FindRegularFile(t.TempDir(), "absent")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("FindRegularFile error = %v, want os.ErrNotExist", err)
	}
}
