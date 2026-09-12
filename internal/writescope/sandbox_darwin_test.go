// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build darwin

package writescope

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRenderProfileEscapesStringLiterals(t *testing.T) {
	t.Parallel()

	scope := Scope{roots: []string{`/tmp/quote"root`, `/tmp/back\slash`}}
	profile := renderProfile(scope)
	want := "(version 1)\n" +
		"(allow default)\n" +
		"(deny file-write*)\n" +
		"(allow file-write*\n" +
		"  (subpath \"/tmp/quote\\\"root\")\n" +
		"  (subpath \"/tmp/back\\\\slash\")\n" +
		")\n" +
		deviceWriteClause
	if profile != want {
		t.Fatalf("renderProfile() = %q, want escaped profile %q", profile, want)
	}
}

func TestRenderProfileGrantsExactlyScopeRoots(t *testing.T) {
	t.Parallel()

	scope := Scope{roots: []string{"/tmp/first", "/tmp/second"}}
	profile := renderProfile(scope)
	if got := strings.Count(profile, "(subpath \""); got != len(scope.Roots()) {
		t.Fatalf("profile subpath grants = %d, want %d from Roots()", got, len(scope.Roots()))
	}
	for _, root := range scope.Roots() {
		grant := "(subpath \"" + escapeSBPLString(root) + "\")"
		if got := strings.Count(profile, grant); got != 1 {
			t.Fatalf("profile grant %q count = %d, want 1", grant, got)
		}
	}
	for _, device := range []string{"/dev/null", "/dev/zero", "/dev/stdout", "/dev/stderr"} {
		if got := strings.Count(profile, `(literal "`+device+`")`); got != 1 {
			t.Fatalf("profile device grant %q count = %d, want 1", device, got)
		}
		for _, root := range scope.Roots() {
			if root == device {
				t.Fatalf("device literal %q leaked into Roots()", device)
			}
		}
	}
	if got := strings.Count(profile, `(regex #"^/dev/fd/[0-9]+$")`); got != 1 {
		t.Fatalf("profile descriptor regex count = %d, want 1", got)
	}
	if strings.Contains(profile, "/dev/tty") || strings.Contains(profile, `(subpath "/dev`) {
		t.Fatalf("profile grants an unintended device path: %q", profile)
	}
}

func TestWrapPreservesOriginalCommand(t *testing.T) {
	t.Parallel()

	stdin := strings.NewReader("input")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "exit 0")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"WRITESCOPE_TEST=value"}
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	originalPath := cmd.Path
	originalArgs := append([]string(nil), cmd.Args...)
	originalDir := cmd.Dir
	originalEnv := append([]string(nil), cmd.Env...)
	scope := Scope{roots: []string{cmd.Dir}}

	if err := Wrap(cmd, scope); err != nil {
		t.Fatalf("Wrap() error = %v", err)
	}
	wantArgs := []string{sandboxExecPath, "-p", renderProfile(scope), originalPath}
	wantArgs = append(wantArgs, originalArgs[1:]...)
	if cmd.Path != sandboxExecPath || !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("wrapped command = %q %q, want %q %q", cmd.Path, cmd.Args, sandboxExecPath, wantArgs)
	}
	if cmd.Dir != originalDir || !reflect.DeepEqual(cmd.Env, originalEnv) {
		t.Fatalf("command Dir/Env changed to %q/%q", cmd.Dir, cmd.Env)
	}
	if cmd.Stdin != stdin || cmd.Stdout != stdout || cmd.Stderr != stderr {
		t.Fatalf("command stdio changed during Wrap")
	}
}

func TestWrapNeverFallsBackToUnrestrictedProfile(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), "/usr/bin/true")
	if err := Wrap(cmd, Scope{roots: []string{t.TempDir()}}); err != nil {
		t.Fatalf("Wrap() error = %v", err)
	}
	if !strings.Contains(cmd.Args[2], "(deny file-write*)") {
		t.Fatalf("wrapped profile has no file-write denial: %q", cmd.Args[2])
	}
}

func TestWrapRejectsRepeatedOrStartedCommand(t *testing.T) {
	t.Parallel()

	scope := Scope{roots: []string{t.TempDir()}}
	wrapped := exec.CommandContext(t.Context(), "/usr/bin/true")
	if err := Wrap(wrapped, scope); err != nil {
		t.Fatalf("first Wrap() error = %v", err)
	}
	if err := Wrap(wrapped, scope); err == nil || !strings.Contains(err.Error(), "already wrapped") {
		t.Fatalf("second Wrap() error = %v, want already-wrapped error", err)
	}

	started := exec.CommandContext(t.Context(), "/usr/bin/true")
	if err := started.Run(); err != nil {
		t.Fatalf("run fixture command: %v", err)
	}
	err := Wrap(started, scope)
	if err == nil || !strings.Contains(err.Error(), "already been started") {
		t.Fatalf("Wrap(started) error = %v, want already-started error", err)
	}
}

func TestWrapRejectsEmptyScope(t *testing.T) {
	t.Parallel()

	err := Wrap(exec.CommandContext(t.Context(), "/usr/bin/true"), Scope{})
	if err == nil || !strings.Contains(err.Error(), "no resolved roots") {
		t.Fatalf("Wrap(empty scope) error = %v, want empty-scope error", err)
	}
}

func TestWrapEnforcesWriteBoundary(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	allowedRoot := filepath.Join(worktreeRoot, "allowed")
	if err := os.Mkdir(allowedRoot, 0o700); err != nil {
		t.Fatalf("create allowed root: %v", err)
	}
	scope, err := Resolve(worktreeRoot, []string{"allowed"}, nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	inside := filepath.Join(allowedRoot, "inside")
	insideCmd := exec.CommandContext(t.Context(), "/usr/bin/touch", inside)
	if err := Wrap(insideCmd, scope); err != nil {
		t.Fatalf("Wrap(inside) error = %v", err)
	}
	if output, runErr := insideCmd.CombinedOutput(); runErr != nil {
		t.Fatalf("inside command error = %v; output = %q", runErr, output)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Fatalf("inside file was not created: %v", err)
	}

	outside := filepath.Join(worktreeRoot, "outside")
	outsideCmd := exec.CommandContext(t.Context(), "/usr/bin/touch", outside)
	if err := Wrap(outsideCmd, scope); err != nil {
		t.Fatalf("Wrap(outside) error = %v", err)
	}
	if output, runErr := outsideCmd.CombinedOutput(); runErr == nil {
		t.Fatalf("outside command succeeded; output = %q", output)
	}
	if _, err := os.Lstat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside path exists after rejected write; Lstat error = %v", err)
	}
}

func TestWrapAllowsStandardDeviceAndRejectsOutsideWrite(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	allowedRoot := filepath.Join(worktreeRoot, "allowed")
	if err := os.Mkdir(allowedRoot, 0o700); err != nil {
		t.Fatalf("create allowed root: %v", err)
	}
	scope, err := Resolve(worktreeRoot, []string{"allowed"}, nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	inside := filepath.Join(allowedRoot, "inside")
	outside := filepath.Join(worktreeRoot, "outside")
	cmd := exec.CommandContext(t.Context(),
		"/bin/sh",
		"-c",
		`printf inside > "$1" && printf devnull > /dev/null && printf outside > "$2"`,
		"writescope-test",
		inside,
		outside,
	)
	if err := Wrap(cmd, scope); err != nil {
		t.Fatalf("Wrap() error = %v", err)
	}
	output, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("outside write succeeded; output = %q", output)
	}
	if bytes.Contains(output, []byte("/dev/null")) {
		t.Fatalf("write to /dev/null failed: %v; output = %q", runErr, output)
	}
	if !bytes.Contains(output, []byte(outside)) {
		t.Fatalf("command failed before outside write: %v; output = %q", runErr, output)
	}
	if content, err := os.ReadFile(inside); err != nil || string(content) != "inside" {
		t.Fatalf("inside file content = %q, error = %v; want %q", content, err, "inside")
	}
	if _, err := os.Lstat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside path exists after rejected write; Lstat error = %v", err)
	}
}

func TestWrapAllowsInheritedDescriptorDevicePaths(t *testing.T) {
	t.Parallel()

	worktreeRoot := t.TempDir()
	scope, err := Resolve(worktreeRoot, []string{"allowed"}, nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	for _, test := range []struct {
		name    string
		command string
		want    string
	}{
		{name: "stdout", command: `printf stdout > /dev/stdout`, want: "stdout"},
		{name: "stderr", command: `printf stderr > /dev/stderr`, want: "stderr"},
		{name: "numbered descriptor", command: `printf fd > /dev/fd/1`, want: "fd"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// #nosec G204 -- the commands are fixed test-only strings.
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", test.command)
			if wrapErr := Wrap(cmd, scope); wrapErr != nil {
				t.Fatalf("Wrap() error = %v", wrapErr)
			}
			output, runErr := cmd.CombinedOutput()
			if runErr != nil || string(output) != test.want {
				t.Fatalf("device write output = %q, error = %v; want %q", output, runErr, test.want)
			}
		})
	}
}
