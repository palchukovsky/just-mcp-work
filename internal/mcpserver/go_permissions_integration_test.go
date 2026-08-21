// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package mcpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
	gorunner "github.com/palchukovsky/just-mcp-work/internal/runner/go"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
	"github.com/palchukovsky/just-mcp-work/internal/workspace"
)

func TestGoPermissionModesAtMCPBoundary(t *testing.T) {
	tests := []struct {
		name    string
		mode    runner.Mode
		wantIDs []string
	}{
		{
			name: "safe",
			mode: runner.ModeSafe,
			wantIDs: []string{
				"go:build",
				"go:test",
				"go:vet",
				"go:mod:download",
			},
		},
		{
			name: "all",
			mode: runner.ModeAll,
			wantIDs: []string{
				"go:build",
				"go:test",
				"go:vet",
				"go:mod:download",
				"go:fmt",
				"go:mod:tidy",
				"go:any",
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assertGoPermissionModeAtMCPBoundary(
				t,
				testCase.mode,
				testCase.wantIDs,
			)
		})
	}
}

func assertGoPermissionModeAtMCPBoundary(
	t *testing.T,
	mode runner.Mode,
	wantIDs []string,
) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "go.mod"),
		[]byte("module example.com/mcp-permissions\n\ngo 1.25.0\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	catalog, err := runner.NewCatalog(gorunner.Registration(""))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := catalog.Resolve(
		[]runner.Selection{{Name: "go", Mode: mode}},
	)
	if err != nil {
		t.Fatal(err)
	}
	server, store := newPermissionBoundaryServer(t, root, registry)
	_, listed, err := server.listTasks(
		context.Background(),
		nil,
		listTasksInput{ProjectPath: "."},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := listedTaskIDs(listed.Tasks); !slices.Equal(got, wantIDs) {
		t.Fatalf("listed Go tasks = %#v, want %#v", got, wantIDs)
	}
	if mode == runner.ModeAll {
		return
	}
	assertUnsafeGoTestArgumentsRejected(t, server, store)
}

func assertUnsafeGoTestArgumentsRejected(
	t *testing.T,
	server *Server,
	store *runstore.Store,
) {
	t.Helper()
	_, rejected, err := server.runTask(
		context.Background(),
		nil,
		runTaskInput{
			ProjectPath: ".",
			TaskID:      "go:test",
			Arguments:   []string{"-exec=/must/not/run", "tool", "compile"},
		},
	)
	if err != nil || rejected.Status != runstore.StatusSpawnError || rejected.RunID == "" {
		t.Fatalf("rejected safe Go task = %#v, %v", rejected, err)
	}
	if rejected.OK {
		t.Fatalf("rejected safe Go task reported success: %#v", rejected)
	}
	meta, err := store.Get(rejected.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != runstore.StatusSpawnError || meta.EndedAt.IsZero() || meta.PID != 0 {
		t.Fatalf("rejected safe Go ledger entry = %#v", meta)
	}
	if meta.RunnerVersion != "" {
		t.Fatalf("argument rejection invoked Go version first: %q", meta.RunnerVersion)
	}
}

type rejectingVersionRunner struct {
	validationCalls int
	buildCalls      int
	versionCalls    int
}

func (*rejectingVersionRunner) Name() string { return "rejecting" }

func (*rejectingVersionRunner) Detect(projectDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(projectDir, "rejecting.task"))
	return err == nil, nil
}

func (*rejectingVersionRunner) ListTasks(context.Context, string) ([]runner.Task, error) {
	return []runner.Task{{ID: "rejecting:fixed", Runner: "rejecting", Name: "fixed"}}, nil
}

func (r *rejectingVersionRunner) ValidateTaskInput(runner.Task, []string) error {
	r.validationCalls++
	return errors.New("arguments rejected by runner")
}

func (r *rejectingVersionRunner) BuildCommand(
	context.Context,
	string,
	runner.Task,
	[]string,
) (*exec.Cmd, error) {
	r.buildCalls++
	return nil, errors.New("unexpected command construction")
}

func (r *rejectingVersionRunner) RunnerVersion(context.Context) (string, error) {
	r.versionCalls++
	return "unexpected", nil
}

func TestTaskInputValidationPrecedesRunnerVersionAndProcessStart(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "rejecting.task"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := &rejectingVersionRunner{}
	registry, err := runner.NewRegistry(
		runner.StaticRegistration(candidate, runner.UnreviewedPermissions()),
	)
	if err != nil {
		t.Fatal(err)
	}
	server, store := newPermissionBoundaryServer(t, root, registry)
	_, rejected, err := server.runTask(
		context.Background(),
		nil,
		runTaskInput{
			ProjectPath: ".",
			TaskID:      "rejecting:fixed",
			Arguments:   []string{"unsafe"},
		},
	)
	if err != nil || rejected.Status != runstore.StatusSpawnError || rejected.RunID == "" {
		t.Fatalf("rejected task = %#v, %v", rejected, err)
	}
	if candidate.validationCalls != 1 || candidate.buildCalls != 0 || candidate.versionCalls != 0 {
		t.Fatalf(
			"validation/build/version calls = %d/%d/%d, want 1/0/0",
			candidate.validationCalls,
			candidate.buildCalls,
			candidate.versionCalls,
		)
	}
	meta, err := store.Get(rejected.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != runstore.StatusSpawnError || meta.EndedAt.IsZero() || meta.PID != 0 {
		t.Fatalf("rejected ledger entry = %#v", meta)
	}
}

func newPermissionBoundaryServer(
	t *testing.T,
	root string,
	registry *runner.Registry,
) (*Server, *runstore.Store) {
	t.Helper()
	workspaceRegistry, err := workspace.NewRegistry(root, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.NewForWorktree(root, root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(
		workspaceRegistry,
		registry,
		store,
		Config{
			Timeout:   5 * time.Second,
			Retention: time.Hour,
			Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return server, store
}
