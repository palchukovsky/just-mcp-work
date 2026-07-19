// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runstore

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
)

//nolint:gocyclo,govet // This end-to-end test keeps metadata and paging assertions together.
func TestBeginFinishMetadataAndPagedLogs(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(Meta{
		ProjectPath: "project",
		Runner:      "just",
		TaskID:      "just:test",
		Args:        []string{"one"},
		CWD:         "/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := uuid.Parse(handle.Meta.RunID)
	if err != nil || parsed.Version() != 7 {
		t.Fatalf("run ID %q is not UUIDv7: %v", handle.Meta.RunID, err)
	}
	if handle.Meta.Status != StatusRunning || handle.Meta.StartedAt.IsZero() {
		t.Fatalf("initial metadata = %#v", handle.Meta)
	}
	if handle.Meta.OwnerPID != os.Getpid() {
		t.Fatalf("owner PID = %d, want %d", handle.Meta.OwnerPID, os.Getpid())
	}
	if (runtime.GOOS == "darwin" || runtime.GOOS == "linux" || runtime.GOOS == "windows") &&
		handle.Meta.OwnerIdentity == "" {
		t.Fatalf("owner process identity is empty: %#v", handle.Meta)
	}
	if !processMatches(handle.Meta.OwnerPID, handle.Meta.OwnerIdentity) {
		t.Fatalf("owner process identity does not match: %#v", handle.Meta)
	}
	if _, err := handle.Stdout().Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Stderr().Write([]byte("problem")); err != nil {
		t.Fatal(err)
	}
	page, err := store.ReadLog(handle.Meta.RunID, "stdout", 2, 3)
	if err != nil || string(page) != "cde" {
		t.Fatalf("ReadLog page = %q, %v", page, err)
	}
	if err := handle.Finish(StatusNonzero, 7, "exit status 7", false, true); err != nil {
		t.Fatal(err)
	}
	meta, err := store.Get(handle.Meta.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != StatusNonzero ||
		meta.ExitCode != 7 ||
		meta.StdoutBytes != 6 ||
		meta.StderrBytes != 7 ||
		!meta.StderrTruncated {
		t.Fatalf("final metadata = %#v", meta)
	}
	if meta.EndedAt.IsZero() || meta.EndedAt.Before(meta.StartedAt) {
		t.Fatalf("invalid run timestamps: %#v", meta)
	}
}

func TestCleanupSkipsRunningRunOwnedByAnotherLiveStore(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(Meta{TaskID: "just:active"})
	if err != nil {
		t.Fatal(err)
	}
	handle.Meta.StartedAt = time.Now().UTC().Add(-2 * time.Hour)
	if err = store.writeMeta(handle.dir, handle.Meta); err != nil {
		t.Fatal(err)
	}

	otherStore, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = otherStore.Cleanup(time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(handle.dir); err != nil {
		t.Fatalf("live run was removed by another store: %v", err)
	}
	if err = handle.Finish(StatusOK, 0, "", false, false); err != nil {
		t.Fatal(err)
	}
}

func TestReadLogValidatesPagingAndPath(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadLog("../escape", "stdout", 0, 1); err == nil {
		t.Fatal("path traversal run ID was accepted")
	}
	if _, err := store.ReadLog("missing", "combined", 0, 1); err == nil {
		t.Fatal("invalid stream was accepted")
	}
	if _, err := store.ReadLog("missing", "stdout", -1, 1); err == nil {
		t.Fatal("negative offset was accepted")
	}
	if _, err := store.ReadLog("missing", "stdout", 0, (1<<20)+1); err == nil {
		t.Fatal("oversized page was accepted")
	}
}

func TestReadLogTailAndLogState(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(Meta{TaskID: "just:tail"})
	if err != nil {
		t.Fatal(err)
	}
	if _, writeErr := handle.Stdout().Write([]byte("abcdef😀Z")); writeErr != nil {
		t.Fatal(writeErr)
	}
	tail, err := store.ReadLogTail(handle.Meta.RunID, "stdout", 3)
	if err != nil || string(tail) != "Z" {
		t.Fatalf("ReadLogTail = %q, %v", tail, err)
	}
	state, err := store.LogState(handle.Meta.RunID)
	if err != nil || state.StdoutBytes == 0 || state.NoOutputYet || state.LastOutputAt.IsZero() {
		t.Fatalf("LogState = %#v, %v", state, err)
	}
	if _, err := store.ReadLogTail(handle.Meta.RunID, "stdout", -1); err == nil {
		t.Fatal("negative tail size was accepted")
	}
	if err := handle.Finish(StatusOK, 0, "", false, false); err != nil {
		t.Fatal(err)
	}
}

func TestListRecentSkipsInvalidNewerEntries(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	valid, err := store.Begin(Meta{TaskID: "just:valid"})
	if err != nil {
		t.Fatal(err)
	}
	if finishErr := valid.Finish(StatusOK, 0, "", false, false); finishErr != nil {
		t.Fatal(finishErr)
	}
	for range 2 {
		id := uuid.Must(uuid.NewV7()).String()
		if id <= valid.Meta.RunID {
			t.Fatal("new UUIDv7 is not newer than the valid run")
		}
		if mkdirErr := os.Mkdir(filepath.Join(store.logRoot, id), 0o750); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
	}
	runs, err := store.ListRecent(1)
	if err != nil || len(runs) != 1 || runs[0].RunID != valid.Meta.RunID {
		t.Fatalf("ListRecent = %#v, %v, want the older valid run", runs, err)
	}
}

func TestListRecentPageUsesExclusiveCursor(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 2 {
		handle, beginErr := store.Begin(Meta{TaskID: "just:page"})
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if finishErr := handle.Finish(StatusOK, 0, "", false, false); finishErr != nil {
			t.Fatal(finishErr)
		}
		ids = append(ids, handle.Meta.RunID)
	}
	first, more, err := store.ListRecentPage(1, "")
	if err != nil || !more || len(first) != 1 || first[0].RunID != ids[1] {
		t.Fatalf("first page = %#v, more=%t, err=%v", first, more, err)
	}
	second, more, err := store.ListRecentPage(1, first[0].RunID)
	if err != nil || more || len(second) != 1 || second[0].RunID != ids[0] {
		t.Fatalf("second page = %#v, more=%t, err=%v", second, more, err)
	}
	if _, _, err := store.ListRecentPage(1, "not-a-run"); err == nil {
		t.Fatal("invalid cursor was accepted")
	}
}

func TestCleanupSkipsActiveAndDeletesFinishedStaleRun(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(Meta{TaskID: "just:test"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * time.Hour)
	handle.Meta.StartedAt = old
	if err := store.writeMeta(handle.dir, handle.Meta); err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup(time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handle.dir); err != nil {
		t.Fatalf("active run was removed: %v", err)
	}

	if err := handle.Finish(StatusOK, 0, "", false, false); err != nil {
		t.Fatal(err)
	}
	handle.Meta.EndedAt = old
	if err := store.writeMeta(handle.dir, handle.Meta); err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup(time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handle.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finished stale run still exists: %v", err)
	}
}

func TestCleanupDeletesStaleRunningRunAfterRestart(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(Meta{TaskID: "just:interrupted"})
	if err != nil {
		t.Fatal(err)
	}
	handle.Meta.StartedAt = time.Now().UTC().Add(-2 * time.Hour)
	identity := ProcessIdentity(os.Getpid())
	if identity == "" {
		t.Skip("process identity is unavailable on this platform")
	}
	handle.Meta.OwnerPID = os.Getpid()
	handle.Meta.OwnerIdentity = identity + ":reused"
	if err = store.writeMeta(handle.dir, handle.Meta); err != nil {
		t.Fatal(err)
	}
	if err = handle.stdout.Close(); err != nil {
		t.Fatal(err)
	}
	if err = handle.stderr.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Cleanup(time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(handle.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale interrupted run still exists: %v", err)
	}
}

//nolint:govet // This test keeps cleanup setup and assertions together.
func TestCleanupDoesNotFollowSymlinks(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(Meta{TaskID: "just:test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Finish(StatusOK, 0, "", false, false); err != nil {
		t.Fatal(err)
	}
	handle.Meta.EndedAt = time.Now().UTC().Add(-2 * time.Hour)
	if err := store.writeMeta(handle.dir, handle.Meta); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(external, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(handle.dir, "stdout.log")
	if err := os.Remove(stdoutPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, stdoutPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := store.Cleanup(time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handle.dir); err != nil {
		t.Fatalf("run containing symlink was removed: %v", err)
	}
	// #nosec G304 -- external path is created in this test's temporary directory.
	data, err := os.ReadFile(external)
	if err != nil || string(data) != "keep" {
		t.Fatalf("external symlink target changed: %q, %v", data, err)
	}
}

func TestReadLogRefusesSymlinkedRunDirectory(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.Must(uuid.NewV7()).String()
	external := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(external, "stdout.log"),
		[]byte("outside"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(store.logRoot, runID)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.ReadLog(runID, "stdout", 0, 64); err == nil {
		t.Fatal("ReadLog followed a symlinked run directory")
	}
	if _, err := store.Get(runID); err == nil {
		t.Fatal("Get followed a symlinked run directory")
	}
}
