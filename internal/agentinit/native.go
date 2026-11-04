// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"fmt"
	"strings"

	"github.com/palchukovsky/just-mcp-work/internal/aiprofile"
)

const cursorPermissions = ".cursor/cli.json"

func cursorPermissionFile() permissionFile {
	return permissionFile{
		path: cursorPermissions,
		keys: []string{"allow"},
		// Cursor CLI refuses a project config whose permissions lack deny.
		emptyLists: []string{"deny"},
		owns: func(entry string) bool {
			return strings.HasPrefix(strings.TrimSpace(entry), "Mcp("+serverName+":")
		},
	}
}

func cursorPermissionLists(shell ShellPermission) ([]permissionList, error) {
	managed, err := ClaudeManagedTools(shell)
	if err != nil {
		return nil, err
	}
	tools := make([]string, 0, len(managed.Allow))
	for _, tool := range managedToolNames(managed.Allow) {
		tools = append(tools, "Mcp("+serverName+":"+tool+")")
	}
	return []permissionList{{key: "allow", tools: tools}}, nil
}

func generatedCursorPermissions(shell ShellPermission) (manifestSurface, error) {
	lists, err := cursorPermissionLists(shell)
	if err != nil {
		return manifestSurface{}, err
	}
	fragment, err := marshalManagedPermissionFragment(
		map[string][]string{"allow": lists[0].tools},
		cursorPermissions,
	)
	if err != nil {
		return manifestSurface{}, err
	}
	return newManifestSurface(cursorPermissions, manifestKindCursorPermissions, fragment), nil
}

func planCursorPermissions(scope string, options Options) (*plannedEdit, *manifestSurface, error) {
	path, err := findScopedConfig(scope, scopedConfig{
		relative: cursorPermissions,
		name:     "Cursor permissions",
		lower:    "Cursor permissions",
	})
	if err != nil {
		return nil, nil, err
	}
	before, exists, err := readOptionalFile(path)
	if err != nil {
		return nil, nil, err
	}
	file := cursorPermissionFile()
	data := emptyJSONObject(before)
	if err = file.validate(data); err != nil {
		return nil, nil, err
	}
	if _, _, err = file.managedFragment(data); err != nil {
		return nil, nil, fmt.Errorf("%w; fix that entry and run init again", err)
	}
	var after []byte
	var surface *manifestSurface
	remove := false
	if options.WriteMCPConfig {
		lists, listErr := cursorPermissionLists(options.ShellPermission)
		if listErr != nil {
			return nil, nil, listErr
		}
		after, err = file.merge(before, lists)
		if err == nil {
			var managed manifestSurface
			managed, err = file.manifestSurface(after, manifestKindCursorPermissions)
			surface = &managed
		}
	} else {
		after, remove, err = file.remove(before)
		if remove {
			after = emptyJSONObject(after)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	remove, err = preserveScopedFileSymlink(scope, cursorPermissions, remove)
	if err != nil {
		return nil, nil, err
	}
	return newEdit(cursorPermissions, path, before, after, 0o600, exists, remove), surface, nil
}

type nativeMCPConfig struct {
	agent  string
	name   string
	path   string
	kind   string
	family aiprofile.Family
	server string
}

func nativeMCPConfigs() []nativeMCPConfig {
	return []nativeMCPConfig{
		{
			agent:  "cursor",
			server: serverName,
			name:   "Cursor",
			path:   ".cursor/mcp.json",
			kind:   manifestKindCursorConfig,
			family: aiprofile.FamilyCursor,
		},
		{
			agent:  "antigravity",
			server: serverName,
			name:   "Antigravity CLI",
			path:   ".agents/mcp_config.json",
			kind:   manifestKindAntigravityConfig,
			family: aiprofile.FamilyAntigravity,
		},
		{
			agent:  "grok",
			server: serverName,
			name:   "Grok",
			path:   ".grok/config.toml",
			kind:   manifestKindGrokConfig,
			family: aiprofile.FamilyClaude,
		},
	}
}

func (config nativeMCPConfig) merge(
	before []byte,
	root string,
	profile aiprofile.Profile,
) ([]byte, error) {
	switch config.agent {
	case "grok":
		return mergeTOMLMCPConfig(before, root, profile, nil)
	case "antigravity", "cursor":
		return mergeNamedMCPConfig(before, root, profile, config.server)
	default:
		return nil, fmt.Errorf("unsupported native MCP client %q", config.agent)
	}
}

func (config nativeMCPConfig) remove(before []byte) ([]byte, bool, error) {
	switch config.agent {
	case "grok":
		return removeCodexConfig(before)
	case "antigravity", "cursor":
		after, remove, err := removeNamedMCPConfig(before, config.server)
		if remove {
			after = emptyJSONObject(after)
		}
		return after, remove, err
	default:
		return nil, false, fmt.Errorf("unsupported native MCP client %q", config.agent)
	}
}

func (config nativeMCPConfig) surface(content []byte) (manifestSurface, bool, error) {
	recorded := manifestSurface{Path: config.path, Kind: config.kind}
	if config.agent == "antigravity" {
		recorded.ServerName = config.server
	}
	return manifestSurfaceFromContent(recorded, content)
}

func generatedNativeMCPSurface(
	kind string,
	root string,
	families aiprofile.Selection,
	antigravityServerName string,
) (manifestSurface, error) {
	for _, config := range nativeMCPConfigs() {
		if config.kind != kind {
			continue
		}
		if config.agent == "antigravity" {
			config.server = antigravityServerName
		}
		content, err := config.merge(nil, root, families.ProfileFor(config.family))
		if err != nil {
			return manifestSurface{}, err
		}
		surface, found, err := config.surface(content)
		if err != nil {
			return manifestSurface{}, err
		}
		if !found {
			return manifestSurface{}, fmt.Errorf("generated native MCP entry is missing")
		}
		return surface, nil
	}
	return manifestSurface{}, fmt.Errorf("unsupported native MCP surface %q", kind)
}

func planNativeMCPConfigs(
	scope string,
	selected map[string]struct{},
	options Options,
) ([]plannedEdit, []manifestSurface, error) {
	var edits []plannedEdit
	var surfaces []manifestSurface
	for _, config := range nativeMCPConfigs() {
		if _, ok := selected[config.agent]; !ok {
			continue
		}
		path, err := findScopedConfig(scope, scopedConfig{
			relative: config.path,
			name:     config.agent + " MCP config",
			lower:    config.agent + " MCP config",
		})
		if err != nil {
			return nil, nil, err
		}
		before, exists, err := readOptionalFile(path)
		if err != nil {
			return nil, nil, err
		}
		original := before
		if config.agent == "antigravity" {
			before, err = planAntigravityIdentity(scope, before, options)
			if err != nil {
				return nil, nil, fmt.Errorf("plan %s: %w", config.path, err)
			}
			config.server = options.AntigravityServerName
		}
		var after []byte
		remove := false
		if options.WriteMCPConfig {
			after, err = config.merge(before, scope, options.AIFamilies.ProfileFor(config.family))
			if err == nil {
				var surface manifestSurface
				var found bool
				surface, found, err = config.surface(after)
				if err == nil && !found {
					err = fmt.Errorf("managed server entry is missing")
				}
				if err == nil {
					surfaces = append(surfaces, surface)
				}
			}
		} else {
			after, remove, err = config.remove(before)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("plan %s: %w", config.path, err)
		}
		remove, err = preserveScopedFileSymlink(scope, config.path, remove)
		if err != nil {
			return nil, nil, err
		}
		edits = append(edits, *newEdit(config.path, path, original, after, 0o600, exists, remove))
	}
	return edits, surfaces, nil
}
