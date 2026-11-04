// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"bytes"
	"fmt"
	"slices"
)

// ValidateAntigravityServerName checks the native MCP entry name without changing it.
func ValidateAntigravityServerName(name string) error {
	if len(name) == 0 || len(name) > 128 {
		return fmt.Errorf("antigravity server name must contain 1-128 ASCII characters")
	}
	for index, char := range name {
		letterOrDigit := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9'
		if !letterOrDigit && (index == 0 || char != '-' && char != '_' && char != '.') {
			return fmt.Errorf("invalid Antigravity server name %q: use letters, digits, -, _, or .; "+
				"start with a letter or digit", name)
		}
	}
	return nil
}

func nativeSurfaceServerName(surface manifestSurface) string {
	if surface.ServerName == "" {
		// Earlier manifests owned only the canonical entry.
		return serverName
	}
	return surface.ServerName
}

func recordedAntigravityIdentity(manifest managedManifest) (string, error) {
	if manifest.SchemaVersion == manifestSchemaVersion && manifest.AntigravityServerName == nil {
		return "", fmt.Errorf("recorded Antigravity server identity is missing; %s",
			antigravityIdentityRecovery)
	}
	if manifest.SchemaVersion != manifestSchemaVersion && manifest.AntigravityServerName != nil {
		return "", fmt.Errorf("recorded Antigravity server identity requires manifest schema %d; %s",
			manifestSchemaVersion, antigravityIdentityRecovery)
	}
	name := serverName
	if manifest.AntigravityServerName != nil {
		name = *manifest.AntigravityServerName
		if err := ValidateAntigravityServerName(name); err != nil {
			return "", fmt.Errorf("recorded Antigravity server identity is invalid: %w; %s",
				err, antigravityIdentityRecovery)
		}
	}
	for _, surface := range manifest.Surfaces {
		if surface.Kind != manifestKindAntigravityConfig {
			if surface.ServerName != "" {
				return "", fmt.Errorf("server_name is only valid for Antigravity surfaces; %s",
					antigravityIdentityRecovery)
			}
			continue
		}
		if manifest.SchemaVersion == manifestSchemaVersion && surface.ServerName == "" ||
			manifest.SchemaVersion != manifestSchemaVersion && surface.ServerName != "" {
			return "", fmt.Errorf("recorded Antigravity surface identity is missing or incompatible; %s",
				antigravityIdentityRecovery)
		}
		if nativeSurfaceServerName(surface) != name {
			return "", fmt.Errorf("recorded Antigravity server identity contradicts its surface; %s",
				antigravityIdentityRecovery)
		}
	}
	return name, nil
}

const antigravityIdentityRecovery = "restore known-good identity metadata in " +
	".just-mcp-work/managed.json from a backup, or repair its schema_version, " +
	"antigravity_server_name and Antigravity surface server_name consistently from known-good " +
	"values; preserve unrelated configuration and recorded answers, then rerun init"

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
		if manifest.AntigravityServerName != nil ||
			slices.ContainsFunc(manifest.Surfaces, func(surface manifestSurface) bool {
				return surface.ServerName != ""
			}) {
			return managedManifest{}, fmt.Errorf("unsupported managed manifest schema %d; %s",
				manifest.SchemaVersion, antigravityIdentityRecovery)
		}
		// Init already replaces an unrecognized pre-identity manifest after asking
		// for its choices again; it cannot supply ownership for a custom identity.
		return managedManifest{}, nil
	}
	return manifest, nil
}

func resolveAntigravityIdentity(
	scope, requested string,
	planned []plannedEdit,
) (string, error) {
	manifest, err := readAntigravityManifest(scope, planned)
	if err != nil {
		return "", err
	}
	name, err := recordedAntigravityIdentity(manifest)
	if err != nil {
		return "", err
	}
	if requested == "" {
		return name, nil
	}
	if err := ValidateAntigravityServerName(requested); err != nil {
		return "", err
	}
	return requested, nil
}

func planAntigravityIdentity(scope string, before []byte, options Options) ([]byte, error) {
	manifest, err := readAntigravityManifest(scope, nil)
	if err != nil {
		return nil, err
	}
	owned, err := recordedAntigravitySurface(manifest)
	if err != nil {
		return nil, err
	}
	name := options.AntigravityServerName
	previous, err := recordedAntigravityIdentity(manifest)
	if err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != 0 && previous != name && !options.WriteMCPConfig {
		return nil, fmt.Errorf("cannot rename Antigravity server while removing MCP configuration")
	}
	if len(bytes.TrimSpace(before)) == 0 {
		if owned != nil && nativeSurfaceServerName(*owned) != name {
			return nil, fmt.Errorf("cannot rename Antigravity server: managed entry is missing; " +
				"repair it with its recorded name first")
		}
		return before, nil
	}
	_, destinationExists, err := namedMCPManagedFragment(before, name)
	if err != nil {
		return nil, err
	}
	if owned == nil {
		if destinationExists && (name != serverName || previous != name) {
			return nil, fmt.Errorf("antigravity server %q already exists and is not managed", name)
		}
		return before, nil
	}
	return renameAntigravityEntry(before, *owned, options, destinationExists)
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

func renameAntigravityEntry(
	before []byte,
	owned manifestSurface,
	options Options,
	destinationExists bool,
) ([]byte, error) {
	previous := nativeSurfaceServerName(owned)
	name := options.AntigravityServerName
	if previous == name {
		return before, nil
	}
	if !options.WriteMCPConfig {
		return nil, fmt.Errorf("cannot rename Antigravity server while removing MCP configuration")
	}
	if destinationExists {
		return nil, fmt.Errorf("antigravity server %q already exists and is not managed", name)
	}
	actual, found, err := manifestSurfaceFromContent(owned, before)
	if err != nil {
		return nil, err
	}
	if !found || actual.SHA256 != owned.SHA256 {
		return nil, fmt.Errorf("cannot rename Antigravity server %q: managed entry is missing "+
			"or edited; repair it with its recorded name first", previous)
	}
	after, remove, err := removeNamedMCPConfig(before, previous)
	if err != nil {
		return nil, err
	}
	if remove {
		after = emptyJSONObject(after)
	}
	return after, nil
}
