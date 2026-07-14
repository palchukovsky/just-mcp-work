// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package version exposes build information injected with ldflags.
package version

//nolint:gochecknoglobals // Release builds inject these values through linker flags.
var (
	// Version is replaced by release builds.
	Version = "dev"
	// Commit is replaced by release builds.
	Commit = "none"
)
