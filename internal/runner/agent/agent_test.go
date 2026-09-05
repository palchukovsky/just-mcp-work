// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentrunner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

func TestRegistrationDeclaresExactReviewedPermissionPrompt(t *testing.T) {
	catalog, err := runner.NewCatalog(Registration("", ""))
	if err != nil {
		t.Fatal(err)
	}
	requests := catalog.PermissionRequests()
	if len(requests) != 1 {
		t.Fatalf("permission requests = %#v", requests)
	}
	request := requests[0]
	if request.Name != "agent" || !request.Reviewed || request.Default != runner.ModeSafe ||
		request.Question != "Choose coding-agent access." ||
		request.Context != "This runner launches another coding agent, which then acts with your own permissions in this checkout. JMW does not sandbox it." {
		t.Fatalf("agent permission request = %#v", request)
	}
	want := []runner.PermissionChoice{
		{
			Mode:        runner.ModeSafe,
			Label:       "Enabled (safe default)",
			Description: "Expose only the fixed Codex and Claude prompt tasks; no extra CLI flags are accepted.",
			Warning:     "The launched coding agent can act with your permissions in this checkout; JMW does not sandbox it.",
		},
		{
			Mode:        runner.ModeDisabled,
			Label:       "Disabled",
			Description: "Do not expose the coding-agent runner or its tasks.",
		},
	}
	if !reflect.DeepEqual(request.Choices, want) {
		t.Fatalf("agent permission choices = %#v, want %#v", request.Choices, want)
	}
}

func TestRegistrationRejectsAllMode(t *testing.T) {
	catalog, err := runner.NewCatalog(Registration("", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Resolve([]runner.Selection{{Name: runnerName, Mode: runner.ModeAll}}); err == nil {
		t.Fatal("agent runner accepted all mode")
	}
}

func TestRunnerDoesNotImplementVersionProvider(t *testing.T) {
	if _, implemented := any(newRunner(taskSpecs("", ""))).(runner.VersionProvider); implemented {
		t.Fatal("agent runner must not implement VersionProvider")
	}
}

func TestDetectAcceptsGitDirectoryAndRegularFileOnly(t *testing.T) {
	r := newRunner(taskSpecs("", ""))

	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, gitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	assertDetected(t, r, directory, true)

	file := t.TempDir()
	writeGitFile(t, file)
	assertDetected(t, r, file, true)

	link := t.TempDir()
	if err := os.Symlink(filepath.Join(file, gitPath), filepath.Join(link, gitPath)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	assertDetected(t, r, link, false)

	assertDetected(t, r, t.TempDir(), false)
}

func TestListTasksIncludesAvailableBinariesAndWarnsForMissingOnesWithoutRedetecting(t *testing.T) {
	dir := t.TempDir()

	t.Run("both present", func(t *testing.T) {
		tasks, err := resolvedAgentRunner(t, "go", "go").ListTasks(context.Background(), dir)
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if got, want := taskIDs(tasks), []string{"agent:codex", "agent:claude"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("task IDs = %#v, want %#v", got, want)
		}
		empty := ""
		wantCodex := []runner.Param{
			{Name: "prompt", Kind: runner.ParamSingular, Doc: "task prompt"},
			{Name: "model", Kind: runner.ParamSingular, Default: &empty, Doc: "optional model"},
			{Name: "effort", Kind: runner.ParamSingular, Default: &empty, Doc: "optional model reasoning effort"},
		}
		wantClaude := []runner.Param{
			{Name: "prompt", Kind: runner.ParamSingular, Doc: "task prompt"},
			{Name: "model", Kind: runner.ParamSingular, Default: &empty, Doc: "optional model"},
			{Name: "effort", Kind: runner.ParamSingular, Default: &empty, Doc: "optional effort"},
		}
		if got, want := tasks[0].Params, wantCodex; !reflect.DeepEqual(got, want) {
			t.Fatalf("codex parameters = %#v, want %#v", got, want)
		}
		if got, want := tasks[1].Params, wantClaude; !reflect.DeepEqual(got, want) {
			t.Fatalf("claude parameters = %#v, want %#v", got, want)
		}
		if got, want := tasks[0].Meta["command"], "exec [-m <model>] [-c model_reasoning_effort=<effort>] -- <prompt>"; got != want {
			t.Fatalf("codex command metadata = %#v, want %#v", got, want)
		}
		if got, want := tasks[1].Meta["command"], "-p [--model <model>] [--effort <effort>] -- <prompt>"; got != want {
			t.Fatalf("claude command metadata = %#v, want %#v", got, want)
		}
	})

	t.Run("one missing", func(t *testing.T) {
		tasks, err := resolvedAgentRunner(t, "go", "jmw-absent-claude-fixture").ListTasks(context.Background(), dir)
		if got, want := taskIDs(tasks), []string{"agent:codex"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("task IDs = %#v, want %#v", got, want)
		}
		assertMissingToolWarning(t, err)
	})

	t.Run("both missing", func(t *testing.T) {
		tasks, err := resolvedAgentRunner(t, "jmw-absent-codex-fixture", "jmw-absent-claude-fixture").ListTasks(context.Background(), dir)
		if len(tasks) != 0 {
			t.Fatalf("tasks = %#v, want none", tasks)
		}
		assertMissingToolWarning(t, err)
		if !strings.Contains(err.Error(), "jmw-absent-codex-fixture") || !strings.Contains(err.Error(), "jmw-absent-claude-fixture") {
			t.Fatalf("joined missing tool warning = %v", err)
		}
	})
}

func TestListTasksLooksUpEachBinaryWithoutExecutingIt(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "agent-fixture")
	r := newRunner(taskSpecs(binary, binary))

	tasks, err := r.ListTasks(context.Background(), dir)
	if len(tasks) != 0 {
		t.Fatalf("tasks before install = %#v, want none", tasks)
	}
	assertMissingToolWarning(t, err)

	// #nosec G306 -- the fixture must be executable for exec.LookPath to find it.
	if err = os.WriteFile(binary, []byte("#!/bin/sh\ntouch \"$0.ran\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tasks, err = r.ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks after install: %v", err)
	}
	if got, want := taskIDs(tasks), []string{"agent:codex", "agent:claude"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("task IDs after install = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(binary + ".ran"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discovery executed agent binary: %v", err)
	}
}

func TestBuildCommandUsesExactArgv(t *testing.T) {
	dir := t.TempDir()
	writeGitFile(t, dir)
	r := newRunner(taskSpecs("", ""))

	for _, test := range []struct {
		name string
		id   string
		args []string
		want []string
	}{
		{"codex prompt", "agent:codex", []string{"fix the test"}, []string{"codex", "exec", "--", "fix the test"}},
		{"codex flag-shaped prompt", "agent:codex", []string{"--cd=/tmp"}, []string{"codex", "exec", "--", "--cd=/tmp"}},
		{"codex model", "agent:codex", []string{"fix the test", "gpt-5"}, []string{"codex", "exec", "-m", "gpt-5", "--", "fix the test"}},
		{"codex model effort", "agent:codex", []string{"fix the test", "gpt-5", "high"}, []string{"codex", "exec", "-m", "gpt-5", "-c", "model_reasoning_effort=high", "--", "fix the test"}},
		{"codex default model effort", "agent:codex", []string{"fix the test", "", "high"}, []string{"codex", "exec", "-c", "model_reasoning_effort=high", "--", "fix the test"}},
		{"claude prompt", "agent:claude", []string{"fix the test"}, []string{"claude", "-p", "--", "fix the test"}},
		{"claude flag-shaped prompt", "agent:claude", []string{"--cd=/tmp"}, []string{"claude", "-p", "--", "--cd=/tmp"}},
		{"claude model", "agent:claude", []string{"fix the test", "sonnet"}, []string{"claude", "-p", "--model", "sonnet", "--", "fix the test"}},
		{"claude model effort", "agent:claude", []string{"fix the test", "sonnet", "high"}, []string{"claude", "-p", "--model", "sonnet", "--effort", "high", "--", "fix the test"}},
		{"claude default model effort", "agent:claude", []string{"fix the test", "", "max"}, []string{"claude", "-p", "--effort", "max", "--", "fix the test"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, err := r.BuildCommand(context.Background(), dir, taskByID(t, r, test.id), test.args)
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			if !reflect.DeepEqual(cmd.Args, test.want) {
				t.Fatalf("command args = %#v, want %#v", cmd.Args, test.want)
			}
			if cmd.Dir != dir {
				t.Fatalf("command dir = %q, want %q", cmd.Dir, dir)
			}
		})
	}
}

func TestCommandRenderingComesFromTaskTable(t *testing.T) {
	empty := ""
	spec := taskSpec{
		id:          "fixture",
		binary:      "fixture-agent",
		name:        "fixture",
		description: "Run the fixture agent.",
		argv:        []string{"run"},
		params: []taskParam{
			{Param: runner.Param{Name: "prompt", Kind: runner.ParamSingular, Doc: "task prompt"}},
			{Param: runner.Param{Name: "model", Kind: runner.ParamSingular, Default: &empty, Doc: "optional model"}, argv: []string{"--fixture-model", "<model>"}},
		},
	}
	if err := validateTaskSpecs([]taskSpec{spec}); err != nil {
		t.Fatalf("validateTaskSpecs: %v", err)
	}
	r := newRunner([]taskSpec{spec})
	task := taskByID(t, r, "agent:fixture")
	if got, want := task.Meta["command"], "run [--fixture-model <model>] -- <prompt>"; got != want {
		t.Fatalf("command metadata = %#v, want %#v", got, want)
	}
	_, argv, err := r.commandArgs(task, []string{"review", "fixture-model"})
	if err != nil {
		t.Fatalf("commandArgs: %v", err)
	}
	if want := []string{"run", "--fixture-model", "fixture-model", "--", "review"}; !reflect.DeepEqual(argv, want) {
		t.Fatalf("command args = %#v, want %#v", argv, want)
	}
}

func TestValidateTaskInputRejectsInvalidInputs(t *testing.T) {
	r := newRunner(taskSpecs("", ""))
	codex := taskByID(t, r, "agent:codex")
	claude := taskByID(t, r, "agent:claude")

	//nolint:govet // Field order follows the reading order of the table rows below.
	for _, test := range []struct {
		name string
		task runner.Task
		args []string
		want string
	}{
		{"empty prompt", codex, []string{" \t "}, `task "agent:codex" requires a non-empty prompt`},
		{"zero arguments", codex, nil, `task "agent:codex" requires a prompt argument`},
		{"too many arguments", claude, []string{"prompt", "model", "effort", "extra"}, `task "agent:claude" accepts at most 3 arguments`},
		{"model flag", codex, []string{"prompt", "--danger"}, `task "agent:codex" parameter "model" must be a value, not a flag`},
		{"effort flag", codex, []string{"prompt", "model", "-c"}, `task "agent:codex" parameter "effort" must be a value, not a flag`},
		{"model surrounding whitespace", codex, []string{"prompt", " gpt-5"}, `task "agent:codex" parameter "model" must not have surrounding whitespace`},
		{"blank optional slot", codex, []string{"prompt", " "}, `task "agent:codex" parameter "model" must not have surrounding whitespace`},
		{"invalid codex effort", codex, []string{"prompt", "", "maximum"}, `task "agent:codex" parameter "effort" must be one of low, medium, high, xhigh, max, ultra`},
		{"invalid claude effort", claude, []string{"prompt", "", "ultra"}, `task "agent:claude" parameter "effort" must be one of low, medium, high, xhigh, max`},
		{"foreign runner", runner.Task{ID: "go:test", Runner: "go"}, []string{"prompt"}, `task "go:test" does not belong to the agent runner`},
		{"unknown command", runner.Task{ID: "agent:any", Runner: "agent"}, []string{"prompt"}, `task "agent:any" has an unsupported agent command`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := r.ValidateTaskInput(test.task, test.args)
			if err == nil {
				t.Fatal("ValidateTaskInput accepted invalid input")
			}
			if err.Error() != test.want {
				t.Fatalf("ValidateTaskInput error = %q, want %q", err, test.want)
			}
		})
	}

	t.Run("tampered metadata", func(t *testing.T) {
		tampered := taskByID(t, r, "agent:codex")
		tampered.Meta["command"] = "exec --danger <prompt>"
		if err := r.ValidateTaskInput(tampered, []string{"prompt"}); err == nil {
			t.Fatal("ValidateTaskInput accepted tampered task metadata")
		}
	})
	t.Run("tampered parameters", func(t *testing.T) {
		tampered := taskByID(t, r, "agent:codex")
		tampered.Params[1].Name = "extra"
		if err := r.ValidateTaskInput(tampered, []string{"prompt"}); err == nil {
			t.Fatal("ValidateTaskInput accepted tampered task parameters")
		}
	})
}

func TestInvalidTaskTablesAreRejectedAtResolve(t *testing.T) {
	valid := taskSpecs("codex", "claude")
	missingRenderer := cloneTaskSpecs(valid)
	missingRenderer[0].params[1].argv = nil
	for _, test := range []struct {
		name  string
		specs []taskSpec
	}{
		{"empty", nil},
		{"duplicate id", append(valid, valid[0])},
		{"missing binary", []taskSpec{{id: "codex", name: "codex", description: "run", argv: []string{"exec"}, params: valid[0].params}}},
		{"missing prompt", []taskSpec{{id: "codex", binary: "codex", name: "codex", description: "run", argv: []string{"exec"}, params: []taskParam{{Param: runner.Param{Name: "model", Kind: runner.ParamSingular}}}}}},
		{"missing parameter renderer", missingRenderer[:1]},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog, err := runner.NewCatalog(registration(test.specs))
			if err != nil {
				t.Fatalf("NewCatalog: %v", err)
			}
			if _, err := catalog.Resolve([]runner.Selection{{Name: runnerName, Mode: runner.ModeSafe}}); err == nil {
				t.Fatal("Resolve accepted invalid task table")
			}
		})
	}
}

func resolvedAgentRunner(t *testing.T, codexBinary, claudeBinary string) *Runner {
	t.Helper()
	catalog, err := runner.NewCatalog(Registration(codexBinary, claudeBinary))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := catalog.Resolve([]runner.Selection{{Name: runnerName, Mode: runner.ModeSafe}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, found := registry.Get(runnerName)
	if !found {
		t.Fatal("agent runner is absent")
	}
	r, ok := resolved.(*Runner)
	if !ok {
		t.Fatalf("resolved runner = %T, want *Runner", resolved)
	}
	return r
}

func assertDetected(t *testing.T, r *Runner, dir string, want bool) {
	t.Helper()
	detected, err := r.Detect(dir)
	if err != nil {
		t.Fatalf("Detect(%q): %v", dir, err)
	}
	if detected != want {
		t.Fatalf("Detect(%q) = %t, want %t", dir, detected, want)
	}
}

func writeGitFile(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, gitPath), []byte("gitdir: /tmp/elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func taskByID(t *testing.T, r *Runner, id string) runner.Task {
	t.Helper()
	for _, spec := range r.specs {
		if runnerName+":"+spec.id == id {
			return canonicalTask(spec)
		}
	}
	t.Fatalf("task %q not found", id)
	return runner.Task{}
}

func taskIDs(tasks []runner.Task) []string {
	ids := make([]string, len(tasks))
	for index, task := range tasks {
		ids[index] = task.ID
	}
	return ids
}

func assertMissingToolWarning(t *testing.T, err error) {
	t.Helper()
	warning, failure := runner.SplitIssues(err)
	if warning == nil || !errors.Is(warning, runner.ErrToolUnavailable) || failure != nil {
		t.Fatalf("ListTasks issue = warning %v, failure %v", warning, failure)
	}
}
