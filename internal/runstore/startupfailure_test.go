// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestWriteStartupFailureCreatesAndReplacesRecord(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".just-mcp-work", "log", "startup-error.json")
	first := StartupFailure{
		Time:    time.Date(2026, time.September, 8, 10, 0, 0, 0, time.UTC),
		Version: "v0.6.0",
		Commit:  "abcdef0",
		Args:    []string{"--root", root},
		Root:    root,
		Options: map[string]string{"timeout": "15m0s"},
		Error:   "first refusal",
	}
	if err := WriteStartupFailure(root, first); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("record mode = %o, want 600", got)
		}
	}
	if got := readStartupFailure(t, path); !reflect.DeepEqual(got, first) {
		t.Fatalf("startup failure = %#v, want %#v", got, first)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Time = first.Time.Add(time.Minute)
	second.Args = []string{"--unknown"}
	second.Error = "second refusal"
	if err := WriteStartupFailure(root, second); err != nil {
		t.Fatal(err)
	}
	if got := readStartupFailure(t, path); !reflect.DeepEqual(got, second) {
		t.Fatalf("replacement startup failure = %#v, want %#v", got, second)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("replacement mode = %o, want 600", got)
		}
	}
}

func TestWriteStartupFailureRefusesDirectorySymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	for _, test := range []struct {
		link func(t *testing.T, root, target string)
		name string
	}{
		{
			name: "state root",
			link: func(t *testing.T, root, target string) {
				t.Helper()
				if err := os.Symlink(target, filepath.Join(root, ".just-mcp-work")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "log root",
			link: func(t *testing.T, root, target string) {
				t.Helper()
				stateRoot := filepath.Join(root, ".just-mcp-work")
				if err := os.Mkdir(stateRoot, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(stateRoot, "log")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			target := t.TempDir()
			test.link(t, root, target)
			if err := WriteStartupFailure(root, StartupFailure{Error: "refusal"}); err == nil {
				t.Fatal("WriteStartupFailure succeeded through a directory symlink")
			}
			throughLink := filepath.Join(target, "startup-error.json")
			if _, err := os.Stat(throughLink); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("startup failure was written through symlink: %v", err)
			}
		})
	}
}

func TestRemoveStartupFailureRemovesOnlyExistingRecord(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".just-mcp-work", "log", "startup-error.json")
	if err := WriteStartupFailure(root, StartupFailure{Error: "refusal"}); err != nil {
		t.Fatal(err)
	}
	store, err := NewForWorktree(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveStartupFailure(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup failure still exists: %v", err)
	}
	if err := store.RemoveStartupFailure(); err != nil {
		t.Fatalf("remove missing startup failure: %v", err)
	}
}

func TestStartupFailureIsInvisibleToRunLedger(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".just-mcp-work", "log", "startup-error.json")
	if err := WriteStartupFailure(root, StartupFailure{Error: "refusal"}); err != nil {
		t.Fatal(err)
	}
	store, err := NewForWorktree(root, root)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListRecentPage(10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 0 || page.Scanned != 0 || page.SkippedMetadata != 0 ||
		page.SkippedIdentity != 0 || page.More {
		t.Fatalf("startup failure appeared in run ledger: %#v", page)
	}
	if err := store.Cleanup(time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cleanup removed startup failure: %v", err)
	}
}

func readStartupFailure(t *testing.T, path string) StartupFailure {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var failure StartupFailure
	if err := json.Unmarshal(data, &failure); err != nil {
		t.Fatal(err)
	}
	return failure
}
