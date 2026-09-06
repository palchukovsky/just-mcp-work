// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build windows

package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func platformShellCommand(dir, command string) *exec.Cmd {
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = "cmd.exe"
	}
	// cmd.exe does not follow CommandLineToArgvW quoting rules. Supplying its
	// command line verbatim preserves quotes inside the requested command.
	// #nosec G702 -- command text is intentionally interpreted by the requested shell tool.
	cmd := exec.CommandContext(context.Background(), shell)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `/D /S /C "` + command + `"`}
	cmd.Dir = dir
	return cmd
}
