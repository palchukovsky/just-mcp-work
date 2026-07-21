// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FindRegularFile returns the path of the first regular file among names inside
// dir. Symlinks are never followed and non-regular candidates are skipped, so a
// directory without a matching regular file reports os.ErrNotExist.
func FindRegularFile(dir string, names ...string) (string, error) {
	for _, name := range names {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err == nil {
			if info.Mode().IsRegular() {
				return path, nil
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect %q: %w", path, err)
		}
	}
	return "", os.ErrNotExist
}
