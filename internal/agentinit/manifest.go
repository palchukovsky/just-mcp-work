// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/palchukovsky/just-mcp-work/internal/version"
)

const (
	manifestSchemaVersion = 1
	manifestFile          = ".just-mcp-work/managed.json"

	manifestKindAgentInstructions = "agent-instructions"
	manifestKindCodexConfig       = "codex-config"
	manifestKindMCPConfig         = "mcp-config"
	manifestKindClaudeSettings    = "claude-settings"
)

//nolint:govet // Keep manifest metadata before surfaces in the stable JSON document.
type managedManifest struct {
	SchemaVersion int    `json:"schema_version"`
	Release       string `json:"release"`
	BetaTest      bool   `json:"beta_test,omitempty"`
	// An omitted ShellPermission predates this field and means ask, the only
	// shell permission those manifests could have recorded.
	ShellPermission string            `json:"shell_permission,omitempty"`
	Surfaces        []manifestSurface `json:"surfaces"`
}

type manifestSurface struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

type manifestBlockRange func(string) (int, int, bool, error)

//nolint:gocyclo // Keep the pre-write mode-change refusal beside the manifest it validates.
func planManifest(
	scope string,
	surfaces []manifestSurface,
	betaTest bool,
	shellPermission ShellPermission,
	selected map[string]struct{},
) (*plannedEdit, error) {
	path, err := findScopedConfig(
		scope,
		scopedConfig{relative: manifestFile, name: "managed manifest", lower: "managed manifest"},
	)
	if err != nil {
		return nil, err
	}
	before, beforeExists, err := readOptionalFile(path)
	if err != nil {
		return nil, err
	}
	recordedShellPermission := ""
	for _, surface := range surfaces {
		if surface.Kind == manifestKindClaudeSettings ||
			surface.Kind == manifestKindCodexConfig {
			recordedShellPermission = string(shellPermission)
			break
		}
	}
	if beforeExists {
		var recorded managedManifest
		if decodeErr := json.Unmarshal(before, &recorded); decodeErr == nil &&
			recorded.SchemaVersion == manifestSchemaVersion {
			if recorded.BetaTest != betaTest {
				var missingPaths []string
				for _, surface := range recorded.Surfaces {
					if surface.Kind != manifestKindAgentInstructions {
						continue
					}
					for _, named := range agentTargets() {
						if surface.Path != filepath.ToSlash(named.target.path) {
							continue
						}
						if _, ok := selected[named.name]; !ok &&
							!slices.Contains(missingPaths, surface.Path) {
							missingPaths = append(missingPaths, surface.Path)
						}
						break
					}
				}
				if len(missingPaths) > 0 {
					var selectedNames, requiredNames []string
					for _, named := range agentTargets() {
						_, isSelected := selected[named.name]
						if isSelected {
							selectedNames = append(selectedNames, named.name)
						}
						if isSelected ||
							slices.Contains(missingPaths, filepath.ToSlash(named.target.path)) {
							requiredNames = append(requiredNames, named.name)
						}
					}
					return nil, fmt.Errorf(
						"cannot change beta-test mode with --agents %s: managed agent-instruction "+
							"files outside the selection: %s; re-run with --agents %s",
						strings.Join(selectedNames, ","),
						strings.Join(missingPaths, ", "),
						strings.Join(requiredNames, ","),
					)
				}
			}
			if _, claudeSelected := selected["claude"]; !claudeSelected {
				var carriedClaudeSurface *manifestSurface
				for index := range recorded.Surfaces {
					if recorded.Surfaces[index].Kind == manifestKindClaudeSettings {
						carriedClaudeSurface = &recorded.Surfaces[index]
						break
					}
				}
				if carriedClaudeSurface != nil {
					surfaces = append(surfaces, *carriedClaudeSurface)
					if recordedShellPermission == "" {
						recordedShellPermission = recorded.ShellPermission
						if recordedShellPermission == "" {
							recordedShellPermission = string(ShellPermissionAsk)
						}
					}
				}
			}
			existingShellPermission := ShellPermission(recorded.ShellPermission)
			if existingShellPermission == "" {
				existingShellPermission = ShellPermissionAsk
			}
			_, claudeSelected := selected["claude"]
			if recordedShellPermission != "" &&
				existingShellPermission != ShellPermission(recordedShellPermission) &&
				!claudeSelected {
				var missingPaths []string
				for _, recordedSurface := range recorded.Surfaces {
					if recordedSurface.Kind != manifestKindClaudeSettings ||
						slices.Contains(missingPaths, recordedSurface.Path) {
						continue
					}
					missingPaths = append(missingPaths, recordedSurface.Path)
				}
				if len(missingPaths) > 0 {
					var selectedNames, requiredNames []string
					for _, named := range agentTargets() {
						_, isSelected := selected[named.name]
						if isSelected {
							selectedNames = append(selectedNames, named.name)
						}
						if isSelected || named.name == "claude" {
							requiredNames = append(requiredNames, named.name)
						}
					}
					return nil, fmt.Errorf(
						"cannot change shell permission with --agents %s: managed permission "+
							"files outside the selection: %s; re-run with --agents %s",
						strings.Join(selectedNames, ","),
						strings.Join(missingPaths, ", "),
						strings.Join(requiredNames, ","),
					)
				}
			}
		}
	}
	after, err := json.MarshalIndent(
		managedManifest{
			SchemaVersion:   manifestSchemaVersion,
			Release:         version.Current().Display(),
			BetaTest:        betaTest,
			ShellPermission: recordedShellPermission,
			Surfaces:        surfaces,
		},
		"",
		"  ",
	)
	if err != nil {
		return nil, fmt.Errorf("encode managed manifest: %w", err)
	}
	after = append(after, '\n')
	return newEdit(path, before, after, 0o644, beforeExists, false), nil
}

func blockManifestSurface(
	relativePath string,
	kind string,
	after []byte,
	blockRange manifestBlockRange,
) (manifestSurface, error) {
	fragment, found, err := managedBlockFragment(after, blockRange)
	if err != nil {
		return manifestSurface{}, err
	}
	if !found {
		return manifestSurface{}, fmt.Errorf("managed fragment missing from %s", relativePath)
	}
	return newManifestSurface(relativePath, kind, fragment), nil
}

func mcpManifestSurface(after []byte) (manifestSurface, error) {
	fragment, found, err := mcpManagedFragment(after)
	if err != nil {
		return manifestSurface{}, err
	}
	if !found {
		return manifestSurface{}, fmt.Errorf("managed server entry missing from %s", mcpConfig)
	}
	return newManifestSurface(mcpConfig, manifestKindMCPConfig, fragment), nil
}

func claudeManifestSurface(after []byte) (manifestSurface, error) {
	fragment, _, err := claudeManagedFragment(after)
	if err != nil {
		return manifestSurface{}, err
	}
	return newManifestSurface(claudeSettings, manifestKindClaudeSettings, fragment), nil
}

func managedClaudeEntries(entries []string) []string {
	managed := make([]string, 0, len(entries))
	for _, entry := range entries {
		if isManagedClaudeTool(entry) {
			managed = append(managed, entry)
		}
	}
	return managed
}

func newManifestSurface(relativePath string, kind string, fragment []byte) manifestSurface {
	normalized := bytes.ReplaceAll(fragment, []byte("\r\n"), []byte("\n"))
	normalized = bytes.TrimSuffix(normalized, []byte("\n"))
	digest := sha256.Sum256(normalized)
	return manifestSurface{
		Path:   filepath.ToSlash(relativePath),
		Kind:   kind,
		SHA256: hex.EncodeToString(digest[:]),
	}
}

func appendManifestSurface(
	surfaces []manifestSurface,
	surface *manifestSurface,
) []manifestSurface {
	if surface == nil {
		return surfaces
	}
	return append(surfaces, *surface)
}

func managedBlockFragment(
	content []byte,
	blockRange manifestBlockRange,
) ([]byte, bool, error) {
	start, end, found, err := blockRange(string(content))
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	return content[start:end], true, nil
}

func mcpManagedFragment(content []byte) ([]byte, bool, error) {
	var document struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		return nil, false, fmt.Errorf("decode managed %s entry: %w", mcpConfig, err)
	}
	raw, found := document.MCPServers[serverName]
	if !found {
		return nil, false, nil
	}
	var entry any
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, true, fmt.Errorf("decode managed server entry in %s: %w", mcpConfig, err)
	}
	fragment, err := json.Marshal(entry)
	if err != nil {
		return nil, true, fmt.Errorf("encode managed server entry in %s: %w", mcpConfig, err)
	}
	return fragment, true, nil
}

func claudeManagedFragment(content []byte) ([]byte, bool, error) {
	var document struct {
		Permissions map[string]json.RawMessage `json:"permissions"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		return nil, false, fmt.Errorf("decode managed permissions in %s: %w", claudeSettings, err)
	}
	managed := make(map[string][]string)
	count := 0
	for name, raw := range document.Permissions {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, false, fmt.Errorf(
				"decode permissions.%s in %s: %w",
				name,
				claudeSettings,
				err,
			)
		}
		values, isList := value.([]any)
		if !isList {
			if value != nil && (name == "allow" || name == "ask") {
				return nil, false, fmt.Errorf(
					"permissions.%s in %s is not a list",
					name,
					claudeSettings,
				)
			}
			continue
		}
		entries := make([]string, 0, len(values))
		for _, value := range values {
			entry, isString := value.(string)
			if !isString {
				return nil, false, fmt.Errorf(
					"permissions.%s in %s contains a non-string entry",
					name,
					claudeSettings,
				)
			}
			entries = append(entries, entry)
		}
		entries = managedClaudeEntries(entries)
		if len(entries) == 0 {
			continue
		}
		managed[name] = entries
		count += len(entries)
	}
	fragment, err := marshalClaudeManagedFragment(managed)
	if err != nil {
		return nil, count > 0,
			fmt.Errorf("encode managed permissions in %s: %w", claudeSettings, err)
	}
	return fragment, count > 0, nil
}

func marshalClaudeManagedFragment(permissions map[string][]string) ([]byte, error) {
	fragment, err := json.Marshal(permissions)
	if err != nil {
		return nil, fmt.Errorf("marshal managed Claude entries: %w", err)
	}
	return fragment, nil
}

// ReadRecordedBetaTest reports the beta-test mode recorded in the workspace manifest.
// A missing manifest reports plain with known true. A present malformed or
// schema-incompatible manifest reports known false; filesystem errors are returned.
func ReadRecordedBetaTest(root string) (bool, bool, error) {
	manifestPath := filepath.Join(root, manifestFile)
	data, exists, err := readOptionalFile(manifestPath)
	if err != nil {
		return false, false, err
	}
	if !exists {
		return false, true, nil
	}

	var manifest managedManifest
	if decodeErr := json.Unmarshal(data, &manifest); decodeErr != nil ||
		manifest.SchemaVersion != manifestSchemaVersion {
		//nolint:nilerr // Decode/schema failures mean unknown mode; init remains the repair path.
		return false, false, nil
	}
	return manifest.BetaTest, true, nil
}

// ReadRecordedShellPermission reports the shell permission recorded in the
// workspace manifest. A missing, legacy, malformed, or schema-incompatible
// manifest has no recorded choice; filesystem and invalid-choice errors are
// returned.
func ReadRecordedShellPermission(root string) (ShellPermission, bool, error) {
	manifestPath := filepath.Join(root, manifestFile)
	data, exists, err := readOptionalFile(manifestPath)
	if err != nil {
		return "", false, err
	}
	if !exists {
		return "", false, nil
	}

	var manifest managedManifest
	if decodeErr := json.Unmarshal(data, &manifest); decodeErr != nil ||
		manifest.SchemaVersion != manifestSchemaVersion || manifest.ShellPermission == "" {
		//nolint:nilerr // Decode/schema failures mean no recorded shell permission; init repairs it.
		return "", false, nil
	}
	permission, err := ParseShellPermission(manifest.ShellPermission)
	if err != nil {
		return "", false, fmt.Errorf(
			"read shell permission from managed manifest %s: %w",
			manifestPath,
			err,
		)
	}
	return permission, true, nil
}

// VerifyManagedSurfaces checks that the generated workspace configuration
// recorded by the last init still matches this binary and the files on disk.
// It returns the recorded beta-test mode after successful verification; a missing
// manifest is accepted as plain, and every error returns false.
//
//nolint:gocyclo // Each refusal stage stays explicit so its recovery message remains specific.
func VerifyManagedSurfaces(root string) (bool, error) {
	manifestPath := filepath.Join(root, manifestFile)
	data, exists, err := readOptionalFile(manifestPath)
	if err != nil {
		return false, fmt.Errorf(
			"managed manifest %s is unreadable: %w; %s",
			manifestPath,
			err,
			managedManifestRecovery(root),
		)
	}
	if !exists {
		return false, nil
	}

	var manifest managedManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false, fmt.Errorf(
			"managed manifest %s is unreadable: %w; %s",
			manifestPath,
			err,
			managedManifestRecovery(root),
		)
	}
	if manifest.SchemaVersion != manifestSchemaVersion {
		description := fmt.Sprintf("has unsupported schema version %d", manifest.SchemaVersion)
		if manifest.SchemaVersion > manifestSchemaVersion {
			description = fmt.Sprintf("is too new (schema version %d)", manifest.SchemaVersion)
		}
		return false, fmt.Errorf(
			"managed manifest %s %s; %s",
			manifestPath,
			description,
			managedManifestRecovery(root),
		)
	}
	shellPermission := ShellPermissionAsk
	if manifest.ShellPermission != "" {
		parsed, parseErr := ParseShellPermission(manifest.ShellPermission)
		if parseErr != nil {
			return false, fmt.Errorf(
				"managed manifest %s is unusable: %w; %s",
				manifestPath,
				parseErr,
				managedManifestRecovery(root),
			)
		}
		shellPermission = parsed
	}

	resolvedPaths := make([]string, len(manifest.Surfaces))
	for _, recorded := range manifest.Surfaces {
		path := filepath.FromSlash(recorded.Path)
		if filepath.IsAbs(path) {
			return false, fmt.Errorf(
				"managed manifest %s is unusable: managed surface path %q is absolute; %s",
				manifestPath,
				recorded.Path,
				managedManifestRecovery(root),
			)
		}
		elements := strings.FieldsFunc(
			recorded.Path,
			func(char rune) bool { return char == '/' || char == '\\' },
		)
		if slices.Contains(elements, "..") {
			return false, fmt.Errorf(
				"managed manifest %s is unusable: managed surface path %q contains a .. element; %s",
				manifestPath,
				recorded.Path,
				managedManifestRecovery(root),
			)
		}
	}
	for index, recorded := range manifest.Surfaces {
		path, err := findScopedConfig(
			root,
			scopedConfig{
				relative: filepath.FromSlash(recorded.Path),
				name:     "managed surface",
				lower:    "managed surface",
			},
		)
		if err != nil {
			return false, fmt.Errorf(
				"managed manifest %s is unusable: path %q: %w; %s",
				manifestPath,
				recorded.Path,
				err,
				managedManifestRecovery(root),
			)
		}
		resolvedPaths[index] = path
	}

	for index, recorded := range manifest.Surfaces {
		generated, err := generatedManifestSurface(
			root,
			manifest.BetaTest,
			shellPermission,
			recorded,
		)
		if err != nil {
			return false, fmt.Errorf(
				"managed manifest %s is unreadable: %w; %s",
				manifestPath,
				err,
				managedManifestRecovery(root),
			)
		}
		if generated.SHA256 != recorded.SHA256 {
			return false, fmt.Errorf(
				"generated configuration changed since it was written for %s "+
					"(recorded by just-mcp-work %s); %s",
				resolvedPaths[index],
				manifest.Release,
				managedManifestRecovery(root),
			)
		}
	}

	for index, recorded := range manifest.Surfaces {
		path := resolvedPaths[index]
		content, exists, err := readOptionalFile(path)
		if err != nil {
			return false, fmt.Errorf(
				"managed configuration in %s is unreadable: %w; %s",
				path,
				err,
				managedManifestRecovery(root),
			)
		}
		if !exists {
			return false, fmt.Errorf(
				"managed configuration in %s is missing; %s",
				path,
				managedManifestRecovery(root),
			)
		}
		actual, found, err := manifestSurfaceFromContent(recorded, content)
		if err != nil {
			return false, fmt.Errorf(
				"managed configuration in %s is malformed: %w; %s",
				path,
				err,
				managedManifestRecovery(root),
			)
		}
		if !found {
			return false, fmt.Errorf(
				"managed configuration in %s is missing; %s",
				path,
				managedManifestRecovery(root),
			)
		}
		if actual.SHA256 != recorded.SHA256 {
			ownership := "just-mcp-work owns the entries it generates in that file, " +
				"so keep your own entries separate"
			if recorded.Kind == manifestKindAgentInstructions ||
				recorded.Kind == manifestKindCodexConfig {
				ownership = "just-mcp-work owns the text between its markers, " +
					"so keep your own text outside them"
			}
			return false, fmt.Errorf(
				"managed configuration in %s was edited; %s; %s",
				path,
				ownership,
				managedManifestRecovery(root),
			)
		}
	}
	return manifest.BetaTest, nil
}

func generatedManifestSurface(
	root string,
	betaTest bool,
	shellPermission ShellPermission,
	recorded manifestSurface,
) (manifestSurface, error) {
	switch recorded.Kind {
	case manifestKindAgentInstructions:
		return newManifestSurface(
			recorded.Path,
			recorded.Kind,
			[]byte(canonicalBlock(betaTest)),
		), nil
	case manifestKindCodexConfig:
		content, err := mergeCodexConfig(nil, root, shellPermission)
		if err != nil {
			return manifestSurface{}, err
		}
		return blockManifestSurface(
			recorded.Path,
			recorded.Kind,
			content,
			codexBlockRange,
		)
	case manifestKindMCPConfig:
		content, err := mergeMCPConfig(nil, root)
		if err != nil {
			return manifestSurface{}, err
		}
		fragment, found, err := mcpManagedFragment(content)
		if err != nil {
			return manifestSurface{}, err
		}
		if !found {
			return manifestSurface{}, fmt.Errorf("generated managed server entry is missing")
		}
		return newManifestSurface(recorded.Path, recorded.Kind, fragment), nil
	case manifestKindClaudeSettings:
		permissions, err := ClaudeManagedTools(shellPermission)
		if err != nil {
			return manifestSurface{}, err
		}
		managed := map[string][]string{"allow": permissions.Allow}
		if len(permissions.Ask) > 0 {
			managed["ask"] = permissions.Ask
		}
		fragment, err := marshalClaudeManagedFragment(managed)
		if err != nil {
			return manifestSurface{}, fmt.Errorf("encode managed permissions: %w", err)
		}
		return newManifestSurface(recorded.Path, recorded.Kind, fragment), nil
	default:
		return manifestSurface{}, fmt.Errorf(
			"unknown managed surface kind %q for %s",
			recorded.Kind,
			recorded.Path,
		)
	}
}

func manifestSurfaceFromContent(
	recorded manifestSurface,
	content []byte,
) (manifestSurface, bool, error) {
	var (
		fragment []byte
		found    bool
		err      error
	)
	switch recorded.Kind {
	case manifestKindAgentInstructions:
		fragment, found, err = managedBlockFragment(content, managedBlockRange)
	case manifestKindCodexConfig:
		fragment, found, err = managedBlockFragment(content, codexBlockRange)
	case manifestKindMCPConfig:
		fragment, found, err = mcpManagedFragment(content)
	case manifestKindClaudeSettings:
		fragment, found, err = claudeManagedFragment(content)
	default:
		return manifestSurface{}, false, fmt.Errorf(
			"unknown managed surface kind %q",
			recorded.Kind,
		)
	}
	if err != nil || !found {
		return manifestSurface{}, found, err
	}
	return newManifestSurface(recorded.Path, recorded.Kind, fragment), true, nil
}

func managedManifestRecovery(root string) string {
	return fmt.Sprintf("run just-mcp-work init --dir %q", root)
}
