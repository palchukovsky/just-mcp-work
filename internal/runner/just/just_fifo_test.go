// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build darwin || linux

package just_test

import (
	"path/filepath"
	"syscall"
	"testing"

	justrunner "github.com/palchukovsky/just-mcp-work/internal/runner/just"
)

func TestDetectRejectsFIFOJustfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "justfile")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	detected, err := justrunner.New("").Detect(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if detected {
		t.Fatal("Detect accepted a FIFO justfile")
	}
}
