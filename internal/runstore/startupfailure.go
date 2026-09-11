// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const startupFailureName = "startup-error.json"

// StartupFailure records why serve refused to start. Root is the directory the
// record was written under.
//
//nolint:govet // Field order follows the stable on-disk record shape.
type StartupFailure struct {
	Time    time.Time         `json:"time"`
	Version string            `json:"version,omitempty"`
	Commit  string            `json:"commit,omitempty"`
	Args    []string          `json:"args"`
	Root    string            `json:"root"`
	Options map[string]string `json:"options,omitempty"`
	Error   string            `json:"error"`
}

// WriteStartupFailure atomically replaces the startup failure for root.
func WriteStartupFailure(root string, failure StartupFailure) error {
	stateRoot := filepath.Join(root, stateDirName)
	if err := makeSafeDir(stateRoot); err != nil {
		return fmt.Errorf("create startup-failure state root: %w", err)
	}
	logRoot := filepath.Join(stateRoot, logDirName)
	if err := makeSafeDir(logRoot); err != nil {
		return fmt.Errorf("create startup-failure log root: %w", err)
	}
	data, err := json.Marshal(failure)
	if err != nil {
		return fmt.Errorf("encode startup failure: %w", err)
	}
	temporary, err := os.CreateTemp(logRoot, ".startup-error-*.json")
	if err != nil {
		return fmt.Errorf("create temporary startup failure: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		//nolint:errcheck // Best-effort cleanup after a failed publish.
		// nosemgrep: discarded-error
		_ = os.Remove(temporaryName)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		//nolint:errcheck // The chmod failure remains the actionable error.
		_ = temporary.Close()
		return fmt.Errorf("set temporary startup failure mode: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		//nolint:errcheck // The write failure remains the actionable error.
		_ = temporary.Close()
		return fmt.Errorf("write temporary startup failure: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary startup failure: %w", err)
	}
	if err := os.Rename(
		temporaryName,
		filepath.Join(logRoot, startupFailureName),
	); err != nil {
		return fmt.Errorf("publish startup failure: %w", err)
	}
	return nil
}

// RemoveStartupFailure removes the startup failure if it exists.
func (s *Store) RemoveStartupFailure() error {
	if err := os.Remove(filepath.Join(s.logRoot, startupFailureName)); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove startup failure: %w", err)
	}
	return nil
}
