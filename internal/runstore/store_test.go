// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

//nolint:gocyclo,govet // This end-to-end test keeps metadata and paging assertions together.
func TestBeginFinishMetadataAndPagedLogs(t *testing.T) {
	worktreeRoot := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(worktreeRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	worktreeRoot, err := filepath.EvalSymlinks(worktreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewForWorktree(t.TempDir(), worktreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(Meta{
		WorktreeRoot: filepath.Join(t.TempDir(), "wrong-worktree"),
		ProjectPath:  "project",
		Runner:       "just",
		TaskID:       "just:test",
		Args:         []string{"one"},
		CWD:          worktreeRoot,
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
		meta.WorktreeRoot != worktreeRoot ||
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

//nolint:gocyclo // The table checks direct and paged reads for each identity state.
func TestStoreRequiresMatchingPersistedWorktreeIdentity(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		mutate    func(map[string]any, string)
		wantError string
	}{
		{
			name: "missing",
			mutate: func(metadata map[string]any, _ string) {
				delete(metadata, "worktree_root")
			},
			wantError: "missing required worktree_root",
		},
		{
			name: "mismatched",
			mutate: func(metadata map[string]any, root string) {
				metadata["worktree_root"] = filepath.Join(root, "other-worktree")
			},
			wantError: "does not match store worktree root",
		},
		{
			name:   "matching",
			mutate: func(map[string]any, string) {},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewForWorktree(root, root)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := store.Begin(Meta{
				WorktreeRoot: filepath.Join(root, "caller-supplied"),
				TaskID:       "just:identity",
			})
			if err != nil {
				t.Fatal(err)
			}
			if handle.Meta.WorktreeRoot != store.WorktreeRoot() {
				t.Fatalf(
					"begin worktree root = %q, want store identity %q",
					handle.Meta.WorktreeRoot,
					store.WorktreeRoot(),
				)
			}
			if _, writeErr := handle.Stdout().Write([]byte("identity-output")); writeErr != nil {
				t.Fatal(writeErr)
			}
			if finishErr := handle.Finish(StatusOK, 0, "", false, false); finishErr != nil {
				t.Fatal(finishErr)
			}
			metaPath := filepath.Join(store.LogRoot(), handle.Meta.RunID, "meta.json")
			data, err := os.ReadFile(metaPath)
			if err != nil {
				t.Fatal(err)
			}
			var metadata map[string]any
			if decodeErr := json.Unmarshal(data, &metadata); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			testCase.mutate(metadata, root)
			data, err = json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			if writeErr := os.WriteFile(metaPath, data, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}

			_, _, existingErr := store.existingRun(handle.Meta.RunID)
			meta, getErr := store.Get(handle.Meta.RunID)
			page, listErr := store.ListRecent(10)
			log, readErr := store.ReadLog(handle.Meta.RunID, "stdout", 0, 64)
			tail, tailErr := store.ReadLogTail(handle.Meta.RunID, "stdout", 64)
			state, stateErr := store.LogState(handle.Meta.RunID)
			zeroTail, zeroTailErr := store.ReadLogTail(handle.Meta.RunID, "stdout", 0)
			if zeroTailErr != nil || zeroTail != nil {
				t.Fatalf("zero-byte tail = %q, %v", zeroTail, zeroTailErr)
			}
			if testCase.wantError == "" {
				if existingErr != nil || getErr != nil || listErr != nil ||
					readErr != nil || tailErr != nil || stateErr != nil ||
					meta.WorktreeRoot != store.WorktreeRoot() || len(page.Runs) != 1 ||
					page.Runs[0].Meta.WorktreeRoot != store.WorktreeRoot() ||
					string(log) != "identity-output" || string(tail) != "identity-output" ||
					state.StdoutBytes != int64(len("identity-output")) {
					t.Fatalf(
						"matching reads = %#v, %#v, %q, %q, %#v, %v, %v, %v, %v, %v, %v",
						meta,
						page,
						log,
						tail,
						state,
						existingErr,
						getErr,
						listErr,
						readErr,
						tailErr,
						stateErr,
					)
				}
				return
			}
			if listErr != nil || len(page.Runs) != 0 ||
				page.Scanned != 1 || page.SkippedIdentity != 1 {
				t.Fatalf("ListRecent = %#v, %v, want one identity skip", page, listErr)
			}
			for method, methodErr := range map[string]error{
				"existingRun": existingErr,
				"Get":         getErr,
				"ReadLog":     readErr,
				"ReadLogTail": tailErr,
				"LogState":    stateErr,
			} {
				if methodErr == nil || !strings.Contains(methodErr.Error(), testCase.wantError) {
					t.Errorf("%s error = %v, want %q", method, methodErr, testCase.wantError)
				}
			}
		})
	}
}

func TestNewKeepsMainAndWorktreeStateRootsSeparate(t *testing.T) {
	base := t.TempDir()
	mainRoot := filepath.Join(base, "main")
	worktreeRoot := filepath.Join(mainRoot, ".wt", "feature")
	if err := os.MkdirAll(worktreeRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	mainStore, err := NewForWorktree(mainRoot, mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	worktreeStore, err := NewForWorktree(worktreeRoot, worktreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if mainStore.StateRoot() == worktreeStore.StateRoot() ||
		mainStore.LogRoot() == worktreeStore.LogRoot() {
		t.Fatalf(
			"state roots overlap: main=%q/%q worktree=%q/%q",
			mainStore.StateRoot(),
			mainStore.LogRoot(),
			worktreeStore.StateRoot(),
			worktreeStore.LogRoot(),
		)
	}
	mainRun, err := mainStore.Begin(Meta{WorktreeRoot: mainRoot, TaskID: "just:test"})
	if err != nil {
		t.Fatal(err)
	}
	worktreeRun, err := worktreeStore.Begin(
		Meta{WorktreeRoot: worktreeRoot, TaskID: "just:test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktreeStore.Get(mainRun.Meta.RunID); err == nil {
		t.Fatal("worktree store read a main-checkout run")
	}
	if _, err := mainStore.Get(worktreeRun.Meta.RunID); err == nil {
		t.Fatal("main-checkout store read a worktree run")
	}
	if err := mainRun.Finish(StatusOK, 0, "", false, false); err != nil {
		t.Fatal(err)
	}
	if err := worktreeRun.Finish(StatusOK, 0, "", false, false); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupSkipsRunningRunOwnedByAnotherLiveStore(t *testing.T) {
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
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

	otherStore, err := NewForWorktree(root, root)
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
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
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
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
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
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
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
	page, err := store.ListRecent(1)
	if err != nil || len(page.Runs) != 1 || page.Runs[0].Meta.RunID != valid.Meta.RunID ||
		page.Scanned != 3 || page.SkippedIdentity != 0 {
		t.Fatalf("ListRecent = %#v, %v, want the older valid run", page, err)
	}
}

//nolint:gocyclo // The two pages and identity skip form one pagination scenario.
func TestListRecentPageUsesExclusiveCursor(t *testing.T) {
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 3 {
		handle, beginErr := store.Begin(Meta{TaskID: "just:page"})
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if finishErr := handle.Finish(StatusOK, 0, "", false, false); finishErr != nil {
			t.Fatal(finishErr)
		}
		ids = append(ids, handle.Meta.RunID)
	}
	mutatePersistedMetadata(
		t,
		filepath.Join(store.LogRoot(), ids[1], "meta.json"),
		func(metadata map[string]any) {
			metadata["worktree_root"] = filepath.Join(root, "foreign-worktree")
		},
	)

	first, err := store.ListRecentPage(1, "")
	if err != nil || !first.More || len(first.Runs) != 1 ||
		first.Runs[0].Meta.RunID != ids[2] || first.Scanned != 1 ||
		first.SkippedIdentity != 0 {
		t.Fatalf("first page = %#v, err=%v", first, err)
	}
	second, err := store.ListRecentPage(1, first.Runs[0].Meta.RunID)
	if err != nil || second.More || len(second.Runs) != 1 ||
		second.Runs[0].Meta.RunID != ids[0] || second.Scanned != 2 ||
		second.SkippedIdentity != 1 {
		t.Fatalf("second page = %#v, err=%v", second, err)
	}
	if _, err := store.ListRecentPage(1, "not-a-run"); err == nil {
		t.Fatal("invalid cursor was accepted")
	}
}

func TestListRecentPageIncludesTrailingIdentitySkipsWithoutMore(t *testing.T) {
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		handle, beginErr := store.Begin(Meta{TaskID: "just:foreign"})
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if finishErr := handle.Finish(StatusOK, 0, "", false, false); finishErr != nil {
			t.Fatal(finishErr)
		}
		mutatePersistedMetadata(
			t,
			filepath.Join(store.LogRoot(), handle.Meta.RunID, "meta.json"),
			func(metadata map[string]any) {
				metadata["worktree_root"] = filepath.Join(root, "foreign-worktree")
			},
		)
	}
	valid, err := store.Begin(Meta{TaskID: "just:valid"})
	if err != nil {
		t.Fatal(err)
	}
	if finishErr := valid.Finish(StatusOK, 0, "", false, false); finishErr != nil {
		t.Fatal(finishErr)
	}

	page, err := store.ListRecentPage(1, "")
	if err != nil || len(page.Runs) != 1 || page.Runs[0].Meta.RunID != valid.Meta.RunID ||
		page.More || page.Scanned != 3 || page.SkippedIdentity != 2 {
		t.Fatalf("ListRecentPage = %#v, %v, want one run and two trailing identity skips", page, err)
	}
}

func TestCleanupSkipsActiveAndDeletesFinishedStaleRun(t *testing.T) {
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
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
	store, err := NewForWorktree(root, root)
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

	restarted, err := NewForWorktree(root, root)
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
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
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
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
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

func TestCleanupAppliesRetentionToPersistedWorktreeIdentityMismatch(t *testing.T) {
	for _, testCase := range []struct {
		mutate  func(map[string]any, string)
		name    string
		expired bool
	}{
		{
			name: "missing/expired",
			mutate: func(metadata map[string]any, _ string) {
				delete(metadata, "worktree_root")
			},
			expired: true,
		},
		{
			name: "missing/unexpired",
			mutate: func(metadata map[string]any, _ string) {
				delete(metadata, "worktree_root")
			},
		},
		{
			name: "foreign/expired",
			mutate: func(metadata map[string]any, root string) {
				metadata["worktree_root"] = filepath.Join(root, "other-worktree")
			},
			expired: true,
		},
		{
			name: "foreign/unexpired",
			mutate: func(metadata map[string]any, root string) {
				metadata["worktree_root"] = filepath.Join(root, "other-worktree")
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewForWorktree(root, root)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := store.Begin(Meta{TaskID: "just:cleanup-identity"})
			if err != nil {
				t.Fatal(err)
			}
			if finishErr := handle.Finish(StatusOK, 0, "", false, false); finishErr != nil {
				t.Fatal(finishErr)
			}
			if testCase.expired {
				handle.Meta.EndedAt = time.Now().UTC().Add(-2 * time.Hour)
			} else {
				handle.Meta.EndedAt = time.Now().UTC().Add(-30 * time.Minute)
			}
			if writeErr := store.writeMeta(handle.dir, handle.Meta); writeErr != nil {
				t.Fatal(writeErr)
			}
			metaPath := filepath.Join(handle.dir, "meta.json")
			mutatePersistedMetadata(t, metaPath, func(metadata map[string]any) {
				testCase.mutate(metadata, root)
			})

			if cleanupErr := store.Cleanup(time.Hour); cleanupErr != nil {
				t.Fatalf("Cleanup error = %v", cleanupErr)
			}
			_, statErr := os.Stat(handle.dir)
			if testCase.expired {
				if !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("expired identity-invalid run remains: %v", statErr)
				}
			} else if statErr != nil {
				t.Fatalf("unexpired identity-invalid run was removed: %v", statErr)
			}
		})
	}
}

//nolint:gocyclo // Each public handle write path must assert the same durable identity invariant.
func TestHandleRejectsWorktreeIdentityMutation(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		root := t.TempDir()
		store, err := NewForWorktree(root, root)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := store.Begin(Meta{TaskID: "just:running-identity"})
		if err != nil {
			t.Fatal(err)
		}
		handle.Meta.WorktreeRoot = filepath.Join(store.Root(), "spoofed")
		persistErr := handle.PersistRunning()
		if persistErr == nil || !strings.Contains(persistErr.Error(), "does not match") {
			t.Fatalf("PersistRunning error = %v", persistErr)
		}
		persisted, err := readMeta(filepath.Join(handle.dir, "meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if persisted.WorktreeRoot != store.WorktreeRoot() {
			t.Fatalf("persisted worktree root = %q", persisted.WorktreeRoot)
		}
		handle.Meta.WorktreeRoot = store.WorktreeRoot()
		if finishErr := handle.Finish(StatusOK, 0, "", false, false); finishErr != nil {
			t.Fatal(finishErr)
		}
	})

	t.Run("terminal-republish", func(t *testing.T) {
		root := t.TempDir()
		store, err := NewForWorktree(root, root)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := store.Begin(Meta{TaskID: "just:terminal-identity"})
		if err != nil {
			t.Fatal(err)
		}
		if finishErr := handle.Finish(StatusOK, 0, "", false, false); finishErr != nil {
			t.Fatal(finishErr)
		}
		handle.Meta.WorktreeRoot = filepath.Join(store.Root(), "spoofed")
		handle.Meta.Error = "must not persist"
		persistErr := handle.PersistFinal()
		if persistErr == nil || !strings.Contains(persistErr.Error(), "does not match") {
			t.Fatalf("PersistFinal error = %v", persistErr)
		}
		persisted, err := readMeta(filepath.Join(handle.dir, "meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if persisted.WorktreeRoot != store.WorktreeRoot() || persisted.Error != "" {
			t.Fatalf("persisted metadata = %#v", persisted)
		}
	})

	t.Run("finish", func(t *testing.T) {
		root := t.TempDir()
		store, err := NewForWorktree(root, root)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := store.Begin(Meta{TaskID: "just:finish-identity"})
		if err != nil {
			t.Fatal(err)
		}
		handle.Meta.WorktreeRoot = filepath.Join(store.Root(), "spoofed")
		finishErr := handle.Finish(StatusOK, 0, "", false, false)
		if finishErr == nil || !errors.Is(finishErr, ErrFinalMetadataPersistence) ||
			!strings.Contains(finishErr.Error(), "does not match") {
			t.Fatalf("Finish error = %v", finishErr)
		}
		persisted, err := readMeta(filepath.Join(handle.dir, "meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if persisted.WorktreeRoot != store.WorktreeRoot() || persisted.Status != StatusRunning {
			t.Fatalf("persisted metadata = %#v", persisted)
		}
	})
}

func TestNewForWorktreeValidatesIdentityBeforeCreatingState(t *testing.T) {
	identityRoot := t.TempDir()
	fileIdentity := filepath.Join(identityRoot, "identity-file")
	if err := os.WriteFile(fileIdentity, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkIdentity := filepath.Join(identityRoot, "identity-link")
	if err := os.Symlink(identityRoot, symlinkIdentity); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, testCase := range []struct {
		name     string
		identity string
	}{
		{name: "missing", identity: filepath.Join(identityRoot, "missing")},
		{name: "regular-file", identity: fileIdentity},
		{name: "symlink", identity: symlinkIdentity},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stateOwner := t.TempDir()
			if _, err := NewForWorktree(stateOwner, testCase.identity); err == nil {
				t.Fatal("invalid worktree identity was accepted")
			}
			if _, err := os.Lstat(filepath.Join(stateOwner, stateDirName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("state was created before identity validation: %v", err)
			}
		})
	}

	stateOwner := t.TempDir()
	store, err := NewForWorktree(stateOwner, identityRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonicalIdentity, err := filepath.EvalSymlinks(identityRoot)
	if err != nil {
		t.Fatal(err)
	}
	if store.WorktreeRoot() != canonicalIdentity {
		t.Fatalf("worktree root = %q, want %q", store.WorktreeRoot(), canonicalIdentity)
	}
	if _, err := os.Stat(store.LogRoot()); err != nil {
		t.Fatalf("valid identity did not create state: %v", err)
	}
}

func TestReadMetaRejectsFileAboveSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, int64(maxMetadataBytes)+1); err != nil {
		t.Fatal(err)
	}
	if _, err := readMeta(path); err == nil ||
		!strings.Contains(err.Error(), strconv.Quote(path)) ||
		!strings.Contains(err.Error(), "65536-byte limit") {
		t.Fatalf("readMeta error = %v, want path and %d-byte limit", err, maxMetadataBytes)
	}
}

func TestBeginRejectsMetadataAboveSizeLimitBeforePublishingRun(t *testing.T) {
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(Meta{Args: []string{strings.Repeat("x", maxMetadataBytes)}})
	if handle != nil || err == nil ||
		!strings.Contains(err.Error(), "run metadata size ") ||
		!strings.Contains(err.Error(), "exceeds 65536-byte limit") {
		t.Fatalf("Begin = %#v, %v, want explicit metadata size error", handle, err)
	}
	entries, readErr := os.ReadDir(store.LogRoot())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("oversized Begin published run entries: %#v", entries)
	}
}

func mutatePersistedMetadata(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if decodeErr := json.Unmarshal(data, &metadata); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	mutate(metadata)
	data, err = json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
