// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"fmt"

	"github.com/palchukovsky/just-mcp-work/internal/aiprofile"
)

type nativeMCPConfig struct {
	agent string
	path  string
	kind  string
}

func nativeMCPConfigs() []nativeMCPConfig {
	return []nativeMCPConfig{
		{agent: "grok", path: ".grok/config.toml", kind: manifestKindGrokConfig},
		{agent: "antigravity", path: ".agents/mcp_config.json", kind: manifestKindAntigravityConfig},
	}
}

func (config nativeMCPConfig) merge(before []byte, root string) ([]byte, error) {
	switch config.agent {
	case "grok":
		return mergeTOMLMCPConfig(before, root, aiprofile.Profile{}, nil)
	case "antigravity":
		return mergeMCPConfig(before, root, aiprofile.Profile{})
	default:
		return nil, fmt.Errorf("unsupported native MCP client %q", config.agent)
	}
}

func (config nativeMCPConfig) remove(before []byte) ([]byte, bool, error) {
	switch config.agent {
	case "grok":
		return removeCodexConfig(before)
	case "antigravity":
		after, remove, err := removeMCPConfig(before)
		if remove {
			after = emptyJSONObject(after)
		}
		return after, remove, err
	default:
		return nil, false, fmt.Errorf("unsupported native MCP client %q", config.agent)
	}
}

func (config nativeMCPConfig) surface(content []byte) (manifestSurface, bool, error) {
	return manifestSurfaceFromContent(manifestSurface{Path: config.path, Kind: config.kind}, content)
}

func generatedNativeMCPSurface(kind, root string) (manifestSurface, error) {
	for _, config := range nativeMCPConfigs() {
		if config.kind != kind {
			continue
		}
		content, err := config.merge(nil, root)
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
		var after []byte
		remove := false
		if options.WriteMCPConfig {
			after, err = config.merge(before, scope)
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
		edits = append(edits, *newEdit(config.path, path, before, after, 0o600, exists, remove))
	}
	return edits, surfaces, nil
}
