// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build !windows

package form

import "os"

// consoleWindow reports a Windows console window; there is none here, so the
// terminal is identified as not a classic console.
func consoleWindow() (bool, bool) {
	return false, true
}

// isPTYPipe reports a Cygwin or MSYS2 pty pipe; those are Windows pipes, so
// there is none here.
func isPTYPipe(*os.File) bool {
	return false
}
