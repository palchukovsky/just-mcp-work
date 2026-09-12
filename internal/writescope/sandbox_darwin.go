// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build darwin

package writescope

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const sandboxExecPath = "/usr/bin/sandbox-exec"

// deviceWriteClause is fixed process support, separate from caller-declared roots.
const deviceWriteClause = `(allow file-write-data
  (literal "/dev/null")
  (literal "/dev/zero")
  (literal "/dev/stdout")
  (literal "/dev/stderr")
  (regex #"^/dev/fd/[0-9]+$")
)
`

// Wrap restricts cmd so that writes outside scope fail.
func Wrap(cmd *exec.Cmd, scope Scope) error {
	if cmd == nil {
		return fmt.Errorf("command is required")
	}
	if cmd.Process != nil || cmd.ProcessState != nil {
		return fmt.Errorf("command has already been started")
	}
	if cmd.Path == sandboxExecPath {
		return fmt.Errorf("command is already wrapped with sandbox-exec")
	}
	if len(scope.roots) == 0 {
		return fmt.Errorf("write scope has no resolved roots")
	}
	info, err := os.Stat(sandboxExecPath)
	if err != nil {
		return fmt.Errorf("required sandbox executable %q is unavailable: %w", sandboxExecPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("required sandbox executable %q is not a regular file", sandboxExecPath)
	}

	profile := renderProfile(scope)
	originalPath := cmd.Path
	originalArgs := cmd.Args
	if len(originalArgs) == 0 {
		originalArgs = []string{originalPath}
	}
	wrappedArgs := []string{sandboxExecPath, "-p", profile, originalPath}
	wrappedArgs = append(wrappedArgs, originalArgs[1:]...)
	cmd.Path = sandboxExecPath
	cmd.Args = wrappedArgs
	return nil
}

func renderProfile(scope Scope) string {
	var profile strings.Builder
	profile.WriteString("(version 1)\n")
	profile.WriteString("(allow default)\n")
	profile.WriteString("(deny file-write*)\n")
	profile.WriteString("(allow file-write*\n")
	for _, root := range scope.roots {
		profile.WriteString("  (subpath \"")
		profile.WriteString(escapeSBPLString(root))
		profile.WriteString("\")\n")
	}
	profile.WriteString(")\n")
	profile.WriteString(deviceWriteClause)
	return profile.String()
}

func escapeSBPLString(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"")
	return replacer.Replace(value)
}
