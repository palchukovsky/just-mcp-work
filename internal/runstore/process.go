// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runstore

// ProcessIdentity returns a platform process-start marker for pid.
// An empty value means that the process is unavailable or its marker cannot be read.
func ProcessIdentity(pid int) string {
	identity, alive := processIdentity(pid)
	if !alive {
		return ""
	}
	return identity
}

// ProcessMatches reports whether pid is alive and still has the expected identity.
func ProcessMatches(pid int, expected string) bool { return processMatches(pid, expected) }

func processMatches(pid int, expected string) bool {
	identity, alive := processIdentity(pid)
	if !alive {
		return false
	}
	if expected == "" || identity == "" {
		return true
	}
	return identity == expected
}
