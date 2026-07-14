// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build !windows

package executor

import (
	"os/signal"
	"syscall"
)

func configureHelperChild() {
	signal.Ignore(syscall.SIGTERM)
}
