// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build !darwin

package writescope

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Wrap refuses to run where the OS cannot enforce the write boundary.
func Wrap(_ *exec.Cmd, _ Scope) error {
	return fmt.Errorf(
		"write scope is not enforceable on %s; JMW refuses to run rather than promise it",
		runtime.GOOS,
	)
}
