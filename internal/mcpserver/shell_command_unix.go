// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build !windows

package mcpserver

import (
	"context"
	"os"
	"os/exec"
)

func platformShellCommand(dir, command string) *exec.Cmd {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	// #nosec G702 -- command text is intentionally interpreted by the requested shell tool.
	cmd := exec.CommandContext(context.Background(), shell, "-c", command)
	cmd.Dir = dir
	return cmd
}
