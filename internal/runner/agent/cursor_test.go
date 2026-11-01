// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentrunner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestCursorBuildCommandUsesExactArgv(t *testing.T) {
	r := newRunner(taskSpecs("", "", "", "", ""))
	task := taskByID(t, r, "agent:cursor")
	dir := t.TempDir()
	writeGitFile(t, dir)
	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "prompt",
			args: []string{"fix the test"},
			want: []string{
				"cursor-agent", "--print", "--output-format", "text", "--trust", "--force",
				"--", "fix the test",
			},
		},
		{
			name: "flag-shaped prompt",
			args: []string{"--force"},
			want: []string{
				"cursor-agent", "--print", "--output-format", "text", "--trust", "--force",
				"--", "--force",
			},
		},
		{
			name: "model",
			args: []string{"fix the test", "auto"},
			want: []string{
				"cursor-agent", "--print", "--output-format", "text", "--trust", "--force",
				"--model", "auto", "--", "fix the test",
			},
		},
		{
			name: "empty model",
			args: []string{"fix the test", ""},
			want: []string{
				"cursor-agent", "--print", "--output-format", "text", "--trust", "--force",
				"--", "fix the test",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, err := r.BuildCommand(context.Background(), dir, task, test.args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cmd.Args, test.want) || cmd.Dir != dir {
				t.Fatalf("Cursor command = %q in %q, want %q in %q", cmd.Args, cmd.Dir, test.want, dir)
			}
		})
	}
}

func TestCursorCommandScopeAndParameters(t *testing.T) {
	r := newRunner(taskSpecs("", "", "", "", ""))
	task := taskByID(t, r, "agent:cursor")
	if len(task.Params) != 2 || task.Params[1].Name != "model" {
		t.Fatalf("Cursor parameters = %#v", task.Params)
	}
	dir := t.TempDir()
	writeGitFile(t, dir)
	args := []string{"--force", "model with spaces"}
	cmd, err := r.BuildScopedCommand(context.Background(), dir, task, args)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cursor-agent", "--print", "--output-format", "text", "--trust", "--force",
		"--sandbox", "disabled", "--model", "model with spaces", "--", "--force",
	}
	if !reflect.DeepEqual(cmd.Args, want) || cmd.Dir != dir {
		t.Fatalf("scoped Cursor command = %q in %q, want %q in %q", cmd.Args, cmd.Dir, want, dir)
	}
	for _, invalid := range [][]string{
		nil,
		{" "},
		{"prompt", "--force"},
		{"prompt", " model"},
		{"prompt", "model", "high"},
	} {
		if err = r.ValidateTaskInput(task, invalid); err == nil {
			t.Fatalf("Cursor accepted invalid arguments %q", invalid)
		}
		if _, err = r.BuildCommand(context.Background(), dir, task, invalid); err == nil {
			t.Fatalf("Cursor built invalid arguments %q", invalid)
		}
	}
	task.Description = "Run Cursor with arbitrary flags."
	if err = r.ValidateTaskInput(task, []string{"prompt"}); err == nil {
		t.Fatal("Cursor accepted a tampered task description")
	}
}

func TestCursorMissingExecutableDoesNotUseAgent(t *testing.T) {
	dir := t.TempDir()
	name := "agent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	// #nosec G306 -- the fixture must be executable for discovery.
	if err := os.WriteFile(filepath.Join(dir, name), []byte("not Cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	r := newRunner(taskSpecs("", "", "", "", ""))
	tasks, err := r.ListTasks(context.Background(), dir)
	assertMissingToolWarning(t, err)
	if !strings.Contains(err.Error(), "cursor-agent") {
		t.Fatalf("missing Cursor warning = %v", err)
	}
	for _, task := range tasks {
		if task.ID == "agent:cursor" {
			t.Fatal("Cursor task used another vendor's agent executable")
		}
	}
}

func TestCursorStateDirectoryResolution(t *testing.T) {
	r := newRunner(taskSpecs("", "", "", "", ""))
	task := taskByID(t, r, "agent:cursor")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CURSOR_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	assertScope := func(want string) {
		t.Helper()
		got, scopeErr := r.TaskWriteScope(task)
		if scopeErr != nil || !reflect.DeepEqual(got, []string{want}) {
			t.Fatalf("Cursor state directory = %q, %v; want only %q", got, scopeErr, want)
		}
	}
	assertScope(filepath.Join(home, ".cursor"))
	t.Setenv("XDG_CONFIG_HOME", "custom-config-home")
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly":
		assertScope(filepath.Join("custom-config-home", "cursor"))
	default:
		assertScope(filepath.Join(home, ".cursor"))
	}
	t.Setenv("CURSOR_CONFIG_DIR", "custom-cursor-home")
	assertScope("custom-cursor-home")
}
