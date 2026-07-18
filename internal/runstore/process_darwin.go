// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build darwin

package runstore

import (
	"fmt"
	"math"

	"golang.org/x/sys/unix"
)

func processIdentity(pid int) (string, bool) {
	if pid <= 0 || uint64(pid) > math.MaxInt32 {
		return "", false
	}
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", unixProcessAlive(pid)
	}
	// #nosec G115 -- the bounds check above makes the conversion lossless.
	processPID := int32(pid)
	if process.Proc.P_pid != processPID {
		return "", false
	}
	started := process.Proc.P_starttime
	return fmt.Sprintf("darwin:%d:%d", started.Sec, started.Usec), true
}
