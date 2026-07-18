// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build linux

package runstore

import (
	"os"
	"testing"
)

func TestLinuxProcessIdentityFallsBackWhenProcIsUnavailable(t *testing.T) {
	const pid = 4242
	identity, alive := linuxProcessIdentity(
		pid,
		func(path string) ([]byte, error) {
			if path != "/proc/4242/stat" {
				t.Fatalf("stat path = %q, want /proc/4242/stat", path)
			}
			return nil, os.ErrNotExist
		},
		func(gotPID int) bool {
			if gotPID != pid {
				t.Fatalf("liveness PID = %d, want %d", gotPID, pid)
			}
			return true
		},
	)
	if identity != "" || !alive {
		t.Fatalf("identity, alive = %q, %t; want empty identity for a live PID", identity, alive)
	}
}
