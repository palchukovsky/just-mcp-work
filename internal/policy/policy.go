// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package policy persists workspace runner selections.
package policy

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

const (
	fileName       = ".just-mcp-work.json"
	currentVersion = 1
	policyFileMode = 0o644
)

// Policy is a structurally parsed workspace runner policy.
//
// Load does not validate runner names or modes against a catalog. Callers must route Found false
// to an all-disabled selection. A found policy must pass through
// runner.Catalog.CompleteSelections before its selections are resolved.
type Policy struct {
	// Selections preserves the exact order recorded in the policy file.
	Selections []runner.Selection
	// Found distinguishes an existing policy that selects no runners from no policy file.
	Found bool
}

// policyDocument mirrors the on-disk schema: the version member is written
// first so a reader learns how to interpret the rest before reaching it.
type policyDocument struct {
	Version *int             `json:"version"`
	Runners *[]fileSelection `json:"runners"`
}

type fileSelection struct {
	Name string      `json:"name"`
	Mode runner.Mode `json:"mode"`
}

// Path returns the workspace policy file path for root.
func Path(root string) string {
	return filepath.Join(root, fileName)
}

// Load reads and structurally parses the workspace policy.
//
// A missing file is not an error: Load returns Found false and an empty Selections slice so the
// caller can distinguish absent configuration from an existing policy with no selections.
func Load(root string) (Policy, error) {
	path := Path(root)
	if err := validateRoot(root); err != nil {
		return Policy{}, fmt.Errorf("load policy %s: %w", path, err)
	}
	data, found, err := Read(root)
	if err != nil {
		return Policy{}, fmt.Errorf("load policy %s: %w", path, err)
	}
	if !found {
		return Policy{Found: false, Selections: []runner.Selection{}}, nil
	}
	parsed, err := Parse(data)
	if err != nil {
		return Policy{}, fmt.Errorf("load policy %s: %w", path, err)
	}
	return parsed, nil
}

// Read returns the bytes of a regular workspace policy without following a policy symlink.
// A missing policy returns found false and empty bytes.
func Read(root string) (data []byte, found bool, err error) {
	path := Path(root)
	if root == "" {
		return nil, false, fmt.Errorf("read policy %s: workspace root must not be empty", path)
	}
	existingMode, err := inspectPolicyFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read policy %s: %w", path, err)
	}
	if existingMode == nil {
		return []byte{}, false, nil
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read policy %s: read file: %w", path, err)
	}
	return data, true, nil
}

// Parse structurally parses the bytes of an existing workspace policy.
func Parse(data []byte) (Policy, error) {
	document, err := decodeDocument(data)
	if err != nil {
		return Policy{}, err
	}
	selections, err := structuralSelections(document)
	if err != nil {
		return Policy{}, err
	}
	return Policy{Found: true, Selections: selections}, nil
}

// Save atomically writes catalog-validated selections in their existing catalog order.
func Save(root string, selections runner.ValidatedSelections) error {
	return save(root, selections, os.Rename)
}

// Encode returns the exact policy bytes Save writes without accessing the filesystem.
func Encode(selections runner.ValidatedSelections) ([]byte, error) {
	validated, err := selections.Selections()
	if err != nil {
		return nil, fmt.Errorf("obtain validated selections: %w", err)
	}
	data, err := encodeDocument(validated)
	if err != nil {
		return nil, fmt.Errorf("encode JSON: %w", err)
	}
	return data, nil
}

func save(
	root string,
	selections runner.ValidatedSelections,
	publish func(string, string) error,
) error {
	path := Path(root)
	if err := validateRoot(root); err != nil {
		return fmt.Errorf("save policy %s: %w", path, err)
	}
	existingMode, err := inspectPolicyFile(path)
	if err != nil {
		return fmt.Errorf("save policy %s: %w", path, err)
	}
	data, err := Encode(selections)
	if err != nil {
		return fmt.Errorf("save policy %s: %w", path, err)
	}
	if err := writeAtomically(path, data, existingMode, publish); err != nil {
		return fmt.Errorf("save policy %s: %w", path, err)
	}
	return nil
}

func inspectPolicyFile(path string) (*os.FileMode, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		//nolint:nilnil // A nil mode is the absent-policy signal; a present file always yields one.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf(
			"not a regular file: %s",
			nonRegularFileType(info.Mode()),
		)
	}
	mode := info.Mode()
	return &mode, nil
}

func nonRegularFileType(mode os.FileMode) string {
	switch {
	case mode.IsDir():
		return "directory"
	case mode&os.ModeSymlink != 0:
		return "symbolic link"
	case mode&os.ModeNamedPipe != 0:
		return "named pipe"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeDevice != 0 && mode&os.ModeCharDevice != 0:
		return "character device"
	case mode&os.ModeDevice != 0:
		return "device"
	case mode&os.ModeIrregular != 0:
		return "irregular file"
	default:
		return fmt.Sprintf("non-regular mode %s", mode)
	}
}

func validateRoot(root string) error {
	if root == "" {
		return fmt.Errorf("workspace root must not be empty")
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("inspect workspace root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace root is not a directory")
	}
	return nil
}

func decodeDocument(data []byte) (policyDocument, error) {
	if err := validateDocumentMembers(data); err != nil {
		return policyDocument{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document policyDocument
	if err := decoder.Decode(&document); err != nil {
		return policyDocument{}, fmt.Errorf("decode JSON: %w", err)
	}
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return document, nil
	}
	if err != nil {
		return policyDocument{}, fmt.Errorf("decode trailing JSON: %w", err)
	}
	return policyDocument{}, fmt.Errorf("decode JSON: multiple top-level values")
}

func validateDocumentMembers(data []byte) error {
	// This token pre-pass rejects duplicate and case-variant members before encoding/json's
	// case-insensitive matching and last-value-wins behavior can obscure the effective policy.
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter || delimiter != '{' {
		return nil
	}
	seen := make(map[string]struct{}, 2)
	for decoder.More() {
		memberToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return fmt.Errorf("decode JSON member: %w", tokenErr)
		}
		member, isString := memberToken.(string)
		if !isString {
			return fmt.Errorf("decode JSON member: name is not a string")
		}
		if _, repeated := seen[member]; repeated {
			return fmt.Errorf("JSON member %q is repeated", member)
		}
		seen[member] = struct{}{}
		switch member {
		case "version":
			var value json.RawMessage
			if err := decoder.Decode(&value); err != nil {
				return fmt.Errorf("decode JSON member %q: %w", member, err)
			}
		case "runners":
			var entries []json.RawMessage
			if err := decoder.Decode(&entries); err != nil {
				return fmt.Errorf("decode JSON member %q: %w", member, err)
			}
			for index, entry := range entries {
				if err := validateSelectionMembers(entry, index); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unknown JSON member %q", member)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode JSON object: %w", err)
	}
	return nil
}

func validateSelectionMembers(data []byte, index int) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode runners[%d]: %w", index, err)
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter || delimiter != '{' {
		return nil
	}
	seen := make(map[string]struct{}, 2)
	for decoder.More() {
		memberToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return fmt.Errorf("decode runners[%d] member: %w", index, tokenErr)
		}
		member, isString := memberToken.(string)
		if !isString {
			return fmt.Errorf("decode runners[%d] member: name is not a string", index)
		}
		if _, repeated := seen[member]; repeated {
			return fmt.Errorf(
				"runners[%d] JSON member %q is repeated",
				index,
				member,
			)
		}
		seen[member] = struct{}{}
		if member != "name" && member != "mode" {
			return fmt.Errorf(
				"runners[%d] has unknown JSON member %q",
				index,
				member,
			)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("decode runners[%d].%s: %w", index, member, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode runners[%d] object: %w", index, err)
	}
	return nil
}

func structuralSelections(document policyDocument) ([]runner.Selection, error) {
	if document.Version == nil {
		return nil, fmt.Errorf("version is missing")
	}
	if *document.Version != currentVersion {
		return nil, fmt.Errorf("unsupported version %d", *document.Version)
	}
	if document.Runners == nil {
		return nil, fmt.Errorf("runners must be an array")
	}
	selections := make([]runner.Selection, 0, len(*document.Runners))
	seen := make(map[string]struct{}, len(*document.Runners))
	for index, item := range *document.Runners {
		if item.Name == "" {
			return nil, fmt.Errorf("runners[%d].name is empty", index)
		}
		if item.Mode == "" {
			return nil, fmt.Errorf("runners[%d].mode is empty", index)
		}
		if _, duplicate := seen[item.Name]; duplicate {
			return nil, fmt.Errorf("runners[%d].name %q is duplicated", index, item.Name)
		}
		seen[item.Name] = struct{}{}
		selections = append(selections, runner.Selection{Name: item.Name, Mode: item.Mode})
	}
	return selections, nil
}

func encodeDocument(selections []runner.Selection) ([]byte, error) {
	runners := make([]fileSelection, 0, len(selections))
	for _, selection := range selections {
		runners = append(runners, fileSelection{Name: selection.Name, Mode: selection.Mode})
	}
	version := currentVersion
	data, err := json.MarshalIndent(
		policyDocument{Version: &version, Runners: &runners},
		"",
		"  ",
	)
	if err != nil {
		return nil, fmt.Errorf("marshal policy document: %w", err)
	}
	return append(data, '\n'), nil
}

func writeAtomically(
	path string,
	data []byte,
	existingMode *os.FileMode,
	publish func(string, string) error,
) error {
	temporary, err := createTemporaryFile(filepath.Dir(path))
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			//nolint:errcheck // Preserve the primary write or publish failure.
			_ = temporary.Close()
		}
		// Temporary cleanup is best effort: after a successful rename the name is
		// already gone, and after a failure the caller is told about that failure,
		// not about the leftover file.
		//nolint:errcheck // Rationale above.
		// nosemgrep: discarded-error
		_ = os.Remove(temporaryName)
	}()
	if existingMode != nil {
		// #nosec G302 -- Existing policy permissions are preserved exactly.
		if err := temporary.Chmod(*existingMode); err != nil {
			return fmt.Errorf("preserve temporary file permissions: %w", err)
		}
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	closed = true
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := publish(temporaryName, path); err != nil {
		return fmt.Errorf("publish temporary file: %w", err)
	}
	return nil
}

func createTemporaryFile(directory string) (*os.File, error) {
	for range 100 {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, fmt.Errorf("create temporary file name: %w", err)
		}
		path := filepath.Join(
			directory,
			fmt.Sprintf(".just-mcp-work-%x.tmp", suffix),
		)
		// #nosec G302,G304 -- 0644 is a creation maximum subject to the process
		// umask, and the random file name is confined to the validated workspace root.
		temporary, err := os.OpenFile(
			path,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			policyFileMode,
		)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("create temporary file: %w", err)
		}
		return temporary, nil
	}
	return nil, fmt.Errorf("create temporary file: no unique name available")
}
