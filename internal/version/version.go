// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package version exposes release and Go build information.
package version

import (
	"runtime/debug"
	"strings"

	"github.com/Masterminds/semver/v3"
)

//nolint:gochecknoglobals // Release builds inject these values through linker flags.
var (
	// Version is replaced by release builds.
	Version = "dev"
	// Commit is replaced by release builds.
	Commit = "none"
)

// Info identifies a stable installed release when one can be determined.
type Info struct {
	Semantic *semver.Version
	Raw      string
	Tag      string
}

// Available reports whether the binary is associated with a stable SemVer release.
func (i Info) Available() bool { return i.Semantic != nil }

// Display returns the stable release tag or the raw development build value.
func (i Info) Display() string {
	if i.Available() {
		return i.Tag
	}
	if i.Raw != "" {
		return i.Raw
	}
	return "dev"
}

// Current resolves the installed version from release linker flags or Go build information.
func Current() Info {
	moduleVersion := ""
	if build, ok := debug.ReadBuildInfo(); ok {
		moduleVersion = build.Main.Version
	}
	return Detect(Version, moduleVersion)
}

// Detect resolves a release version from the linker-injected and Go module values.
func Detect(releaseVersion, moduleVersion string) Info {
	for _, candidate := range []string{releaseVersion, moduleVersion} {
		if info, ok := ParseStable(candidate); ok {
			return info
		}
	}
	if releaseVersion != "" {
		return Info{Raw: releaseVersion}
	}
	return Info{Raw: moduleVersion}
}

// ParseStable accepts a complete stable SemVer version with an optional v prefix.
func ParseStable(value string) (Info, bool) {
	if value == "" {
		return Info{}, false
	}
	parsed, err := semver.StrictNewVersion(strings.TrimPrefix(value, "v"))
	if err != nil || parsed.Prerelease() != "" {
		return Info{}, false
	}
	return Info{Raw: value, Tag: "v" + parsed.String(), Semantic: parsed}, true
}
