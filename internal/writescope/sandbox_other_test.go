// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build !darwin

package writescope

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestWrapRefusesUnsupportedPlatform(t *testing.T) {
	t.Parallel()

	err := Wrap(exec.CommandContext(t.Context(), "unimportant"), Scope{})
	if err == nil {
		t.Fatalf("Wrap() error = nil, want fail-closed refusal")
	}
	for _, text := range []string{runtime.GOOS, "not enforceable", "refuses to run"} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("Wrap() error = %q, want text %q", err, text)
		}
	}
}
