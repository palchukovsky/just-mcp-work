// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runner_test

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

func TestMarkMissingToolReportsAnAbsentBinary(t *testing.T) {
	_, err := exec.LookPath("jmw-absent-tool-fixture")
	if err == nil {
		t.Skip("the fixture name resolves on this host")
	}
	marked := runner.MarkMissingTool("jmw-absent-tool-fixture", err)
	if !errors.Is(marked, runner.ErrToolUnavailable) {
		t.Fatalf("MarkMissingTool = %v, want ErrToolUnavailable", marked)
	}
	if !errors.Is(marked, exec.ErrNotFound) {
		t.Fatalf("MarkMissingTool = %v, want the original cause kept", marked)
	}
}

// TestMarkMissingToolKeepsOtherFailures pins the boundary of the warning: a
// tool that is installed but fails must stay a project error, or a broken
// build file would silently look like an unconfigured host.
func TestMarkMissingToolKeepsOtherFailures(t *testing.T) {
	failure := errors.New("exit status 2")
	if marked := runner.MarkMissingTool("make", failure); !errors.Is(marked, failure) {
		t.Fatalf("MarkMissingTool = %v, want the original error", marked)
	} else if errors.Is(marked, runner.ErrToolUnavailable) {
		t.Fatalf("MarkMissingTool = %v, want no ErrToolUnavailable", marked)
	}
}

func TestMarkMissingToolPassesSuccessThrough(t *testing.T) {
	if err := runner.MarkMissingTool("make", nil); err != nil {
		t.Fatalf("MarkMissingTool(nil) = %v, want nil", err)
	}
}
