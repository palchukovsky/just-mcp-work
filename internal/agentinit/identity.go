// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"bytes"
	"fmt"
)

// antigravityIdentitySchema is the first manifest schema that records the
// Antigravity entry name. Earlier schemas own only the canonical entry.
const antigravityIdentitySchema = 2

// ValidateAntigravityServerName checks the native MCP entry name without changing it.
func ValidateAntigravityServerName(name string) error {
	if len(name) == 0 || len(name) > 128 {
		return fmt.Errorf("antigravity server name must contain 1-128 ASCII characters")
	}
	for index, char := range name {
		letterOrDigit := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9'
		if !letterOrDigit && (index == 0 || char != '-' && char != '_' && char != '.') {
			return fmt.Errorf(
				"invalid Antigravity server name %q: use letters, digits, -, _, or .; "+
					"start with a letter or digit",
				name,
			)
		}
	}
	return nil
}

// recordedAntigravityIdentity returns the Antigravity entry name a manifest of a
// supported schema records. Callers check the schema first.
func recordedAntigravityIdentity(manifest managedManifest) (string, error) {
	if manifest.SchemaVersion < antigravityIdentitySchema {
		if manifest.AntigravityServerName != nil {
			return "", fmt.Errorf(
				"recorded Antigravity server identity requires manifest schema %d; %s",
				antigravityIdentitySchema,
				schemaVersionRecovery,
			)
		}
		return serverName, nil
	}
	if manifest.AntigravityServerName == nil {
		return "", fmt.Errorf(
			"recorded Antigravity server identity is missing; %s",
			antigravityIdentityRecovery,
		)
	}
	name := *manifest.AntigravityServerName
	if err := ValidateAntigravityServerName(name); err != nil {
		return "", fmt.Errorf(
			"recorded Antigravity server identity is invalid: %w; %s",
			err,
			antigravityIdentityRecovery,
		)
	}
	return name, nil
}

const antigravityIdentityRecovery = "while the manifest records no Antigravity MCP surface, " +
	"rerun init with --agents antigravity --antigravity-server-name naming the entry to use; " +
	"otherwise restore antigravity_server_name in .just-mcp-work/managed.json from a " +
	"known-good backup, then rerun init"

// readAntigravityManifest reads the recorded manifest once per init for the
// Antigravity identity. A missing manifest, and one of an unknown schema that
// records no identity, read as no manifest: init replaces the latter after
// asking for its choices again.
func readAntigravityManifest(scope string, planned []plannedEdit) (managedManifest, error) {
	path, err := findScopedConfig(scope, scopedConfig{
		relative: manifestFile, name: "managed manifest", lower: "managed manifest",
	})
	if err != nil {
		return managedManifest{}, err
	}
	if collisionErr := validateManifestPath(planned, path); collisionErr != nil {
		return managedManifest{}, collisionErr
	}
	data, exists, err := readOptionalFile(path)
	if err != nil || !exists {
		return managedManifest{}, err
	}
	manifest, err := decodeManagedManifest(path, data)
	if err != nil {
		return managedManifest{}, err
	}
	if !supportedManifestSchema(manifest.SchemaVersion) {
		if manifest.AntigravityServerName == nil {
			return managedManifest{}, nil
		}
		return managedManifest{}, unsupportedIdentityManifestError(path, manifest.SchemaVersion)
	}
	return manifest, nil
}

// unsupportedIdentityManifestError refuses a manifest of a schema this release
// does not read that records an Antigravity name. No init of this release can
// preserve the name, so serve and init give the same recovery.
func unsupportedIdentityManifestError(path string, schema int) error {
	if schema > manifestSchemaVersion {
		return fmt.Errorf(
			"managed manifest %s is too new (schema version %d) and records an "+
				"Antigravity server identity this release cannot preserve; use the "+
				"just-mcp-work release that wrote it",
			path,
			schema,
		)
	}
	return fmt.Errorf(
		"managed manifest %s has unsupported schema version %d and records an "+
			"Antigravity server identity; %s",
		path,
		schema,
		schemaVersionRecovery,
	)
}

const schemaVersionRecovery = "restore schema_version in .just-mcp-work/managed.json " +
	"from a known-good backup, then rerun init"

// resolveAntigravityIdentity reads the recorded manifest once and returns it
// with the recorded entry name and the one this init uses. An explicit name
// repairs a missing or invalid recorded identity only while no Antigravity
// surface is recorded, so no owned entry can be orphaned; previous is then
// empty and the planner refuses an occupied destination.
func resolveAntigravityIdentity(
	scope string,
	planned []plannedEdit,
	requested string,
) (recorded managedManifest, previous, name string, err error) {
	recorded, err = readAntigravityManifest(scope, planned)
	if err != nil {
		return managedManifest{}, "", "", err
	}
	previous, err = recordedAntigravityIdentity(recorded)
	if err != nil {
		owned, ownedErr := recordedAntigravitySurface(recorded)
		if requested == "" || ownedErr != nil || owned != nil {
			return managedManifest{}, "", "", err
		}
		return recorded, "", requested, nil
	}
	if requested == "" {
		return recorded, previous, previous, nil
	}
	return recorded, previous, requested, nil
}

func planAntigravityIdentity(
	config nativeMCPConfig,
	before []byte,
	options Options,
	recorded managedManifest,
	previous string,
) ([]byte, error) {
	owned, err := recordedAntigravitySurface(recorded)
	if err != nil {
		return nil, err
	}
	name := options.AntigravityServerName
	renaming := previous != "" && previous != name
	if recorded.SchemaVersion != 0 && renaming && !options.WriteMCPConfig {
		return nil, fmt.Errorf(
			"cannot rename Antigravity server while removing MCP configuration",
		)
	}
	if len(bytes.TrimSpace(before)) == 0 {
		if owned != nil && renaming {
			return nil, fmt.Errorf(
				"cannot rename Antigravity server: managed entry is missing; " +
					"repair it with its recorded name first",
			)
		}
		return before, nil
	}
	_, destinationExists, err := namedMCPManagedFragment(before, name)
	if err != nil {
		return nil, err
	}
	if owned == nil {
		if err = checkUnownedAntigravityDestination(name, previous, destinationExists); err != nil {
			return nil, err
		}
		return before, nil
	}
	if !renaming {
		return before, nil
	}
	if destinationExists {
		return nil, fmt.Errorf("antigravity server %q already exists and is not managed", name)
	}
	return removeRenamedAntigravityEntry(config, before, *owned, previous)
}

// checkUnownedAntigravityDestination refuses to take over an existing entry init
// does not own. Only the canonical entry of a workspace that always recorded it
// is written over, as before identity was recorded.
func checkUnownedAntigravityDestination(name, previous string, exists bool) error {
	if exists && (name != serverName || previous != name) {
		return fmt.Errorf("antigravity server %q already exists and is not managed", name)
	}
	return nil
}

// removeRenamedAntigravityEntry removes the entry recorded under previous, but
// only while it still matches its recorded digest.
func removeRenamedAntigravityEntry(
	config nativeMCPConfig,
	before []byte,
	owned manifestSurface,
	previous string,
) ([]byte, error) {
	actual, found, err := manifestSurfaceFromContent(owned, before, previous)
	if err != nil {
		return nil, err
	}
	if !found || actual.SHA256 != owned.SHA256 {
		return nil, fmt.Errorf(
			"cannot rename Antigravity server %q: managed entry is missing "+
				"or edited; repair it with its recorded name first",
			previous,
		)
	}
	config.server = previous
	after, _, err := config.remove(before)
	return after, err
}

func recordedAntigravitySurface(manifest managedManifest) (*manifestSurface, error) {
	var owned *manifestSurface
	for index := range manifest.Surfaces {
		if manifest.Surfaces[index].Kind == manifestKindAntigravityConfig {
			if owned != nil {
				return nil, fmt.Errorf("multiple recorded Antigravity MCP surfaces")
			}
			owned = &manifest.Surfaces[index]
		}
	}
	return owned, nil
}
