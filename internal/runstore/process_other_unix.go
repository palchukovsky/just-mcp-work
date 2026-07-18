// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build !windows && !linux && !darwin

package runstore

func processIdentity(pid int) (string, bool) {
	return "", unixProcessAlive(pid)
}
