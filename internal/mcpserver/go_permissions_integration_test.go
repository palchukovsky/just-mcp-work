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
	"strings"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/aiprofile"
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

//nolint:gocyclo // Each profile exercises one complete task and shell authorization boundary.
func TestAIProfileDoesNotChangeTaskSurfaceOrAuthorization(t *testing.T) {
	wantTaskIDs := []string{
		"go:build",
		"go:test",
		"go:vet",
		"go:mod:download",
	}
	type authorizationOutcome struct {
		rejectedStatus   runstore.Status
		rejectedMessage  string
		shellStatus      runstore.Status
		shellMessage     string
		shellStdoutTail  string
		rejectedExitCode int
		shellExitCode    int
	}
	var wantOutcome *authorizationOutcome
	for _, profile := range testAIProfiles(t) {
		t.Run(string(profile.Family), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(
				filepath.Join(root, "go.mod"),
				[]byte("module example.com/ai-profile-permissions\n\ngo 1.25.0\n"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			registry := newGoPermissionRegistry(t, runner.ModeSafe)
			server, _ := newPermissionBoundaryServer(t, root, registry, profile)

			_, listed, err := server.listTasks(
				context.Background(),
				nil,
				listTasksInput{ProjectPath: "."},
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := listedTaskIDs(listed.Tasks); !slices.Equal(got, wantTaskIDs) {
				t.Fatalf("safe Go tasks = %#v, want %#v", got, wantTaskIDs)
			}

			_, rejected, err := server.runTask(
				context.Background(),
				nil,
				runTaskInput{ProjectPath: ".", TaskID: "go:fmt"},
			)
			if err != nil || rejected.OK || rejected.Status != runstore.StatusSpawnError ||
				rejected.ExitCode != -1 || rejected.RunID == "" || !rejected.LogsReady {
				t.Fatalf("withheld go:fmt receipt = %#v, %v", rejected, err)
			}
			if rejected.AIProfile != profile {
				t.Fatalf(
					"withheld go:fmt profile = %#v, want %#v",
					rejected.AIProfile,
					profile,
				)
			}

			waitUntilComplete := int64(-1)
			tailBytes := int64(64)
			result, shellReceipt, err := server.runShellCommand(
				context.Background(),
				nil,
				runShellCommandInput{
					Command:   shellOutputCommand(),
					MaxWaitMS: &waitUntilComplete,
					TailBytes: &tailBytes,
				},
			)
			if err != nil || result != nil || !shellReceipt.OK ||
				shellReceipt.Status != runstore.StatusOK || shellReceipt.ExitCode != 0 ||
				!shellReceipt.LogsReady || !strings.Contains(shellReceipt.StdoutTail, "shell-output") {
				t.Fatalf("client-permitted shell receipt = %#v, %#v, %v", result, shellReceipt, err)
			}
			if shellReceipt.AIProfile != profile {
				t.Fatalf(
					"client-permitted shell profile = %#v, want %#v",
					shellReceipt.AIProfile,
					profile,
				)
			}

			outcome := authorizationOutcome{
				rejectedStatus:   rejected.Status,
				rejectedExitCode: rejected.ExitCode,
				rejectedMessage:  rejected.Message,
				shellStatus:      shellReceipt.Status,
				shellExitCode:    shellReceipt.ExitCode,
				shellMessage:     shellReceipt.Message,
				shellStdoutTail:  shellReceipt.StdoutTail,
			}
			if wantOutcome == nil {
				wantOutcome = &outcome
			} else if outcome != *wantOutcome {
				t.Fatalf(
					"authorization outcome for %s = %#v, want %#v",
					profile.Family,
					outcome,
					*wantOutcome,
				)
			}
		})
	}
}

func testAIProfiles(t *testing.T) []aiprofile.Profile {
	t.Helper()
	return []aiprofile.Profile{
		aiprofile.Unknown(),
		mustTestAIProfile(t, "codex"),
		mustTestAIProfile(t, "claude"),
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
	registry := newGoPermissionRegistry(t, mode)
	server, store := newPermissionBoundaryServer(t, root, registry, aiprofile.Unknown())
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

func newGoPermissionRegistry(t *testing.T, mode runner.Mode) *runner.Registry {
	t.Helper()
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
	return registry
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
	server, store := newPermissionBoundaryServer(
		t,
		root,
		registry,
		aiprofile.Unknown(),
	)
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
	profile aiprofile.Profile,
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
			AIProfile: profile,
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
