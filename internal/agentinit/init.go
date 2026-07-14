// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package agentinit installs a small managed task-server instruction block.
package agentinit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	beginMarker = "<!-- BEGIN just-mcp-work (managed) -->"
	endMarker   = "<!-- END just-mcp-work (managed) -->"
	mcpConfig   = ".mcp.json"
	codexConfig = ".codex/config.toml"
	codexBegin  = "# >>> just-mcp-work mcp (managed) >>>"
	codexEnd    = "# <<< just-mcp-work mcp <<<"
)

// Prompt is the canonical agent instruction managed by init.
const Prompt = `This workspace exposes its runnable tasks through the just-mcp-work MCP server
(currently the ` + "`just`" + ` runner). When running project tasks:

- Discover tasks with ` + "`list_projects`" + ` and ` + "`list_tasks`" + ` instead of
  reading build files.
- Run tasks with ` + "`run_task`" + ` (project_path, task_id, arguments) — do not shell out
  to the underlying tool or bash directly.
- Results are compact: success returns a run_id, exit code, and duration, not
  full output. Fetch output with ` + "`get_run_logs`" + ` using the run_id (stdout/
  stderr, offset/limit paging).
- Prefer existing tasks; do not edit build files unless asked.`

// Options controls agent instruction injection.
type Options struct {
	Dir            string
	Agents         []string
	DryRun         bool
	WriteMCPConfig bool
}

// Result lists changed or would-change files.
type Result struct {
	Paths []string
	Diffs []string
}

// Apply creates or updates selected agent instruction files.
//
//nolint:gocyclo // Agent-file updates have independent validation and write paths.
func Apply(options Options) (Result, error) {
	dir, err := filepath.Abs(options.Dir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve workspace directory: %w", err)
	}
	if len(options.Agents) == 0 {
		options.Agents = []string{"claude", "codex", "cursor"}
	}
	result := Result{}
	for _, agent := range unique(options.Agents) {
		target, ok := agentTarget(agent)
		if !ok {
			return Result{}, fmt.Errorf("unsupported agent %q", agent)
		}
		path, err := findAgentInstruction(dir, target)
		if err != nil {
			return Result{}, err
		}
		// #nosec G304 -- path is a selected agent target below Dir or the closest
		// ancestor target validated not to escape its containing directory.
		before, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("read %s: %w", path, err)
		}
		after, err := managedContent(before, target.header)
		if err != nil {
			return Result{}, fmt.Errorf("%s: %w", path, err)
		}
		if bytes.Equal(before, after) {
			continue
		}
		result.Paths = append(result.Paths, path)
		result.Diffs = append(result.Diffs, simpleDiff(path, before, after))
		if options.DryRun {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return Result{}, fmt.Errorf("create directory for %s: %w", path, err)
		}
		// #nosec G306,G703 -- path is selected from the static target map. Instruction
		// files are intentionally readable by local coding agents.
		if err := os.WriteFile(path, after, 0o644); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", path, err)
		}
	}
	if options.WriteMCPConfig {
		path, err := findMCPConfig(dir)
		if err != nil {
			return Result{}, err
		}
		// #nosec G304 -- path is either .mcp.json below Dir or the nearest regular
		// ancestor config discovered from Dir.
		before, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("read %s: %w", path, err)
		}
		after, err := mergeMCPConfig(before)
		if err != nil {
			return Result{}, err
		}
		if !bytes.Equal(before, after) {
			result.Paths = append(result.Paths, path)
			result.Diffs = append(result.Diffs, simpleDiff(path, before, after))
			if !options.DryRun {
				// #nosec G306 -- a local .mcp.json must be readable by its host agent.
				if err := os.WriteFile(path, after, 0o644); err != nil {
					return Result{}, fmt.Errorf("write %s: %w", path, err)
				}
			}
		}
		codexPath := filepath.Join(dir, codexConfig)
		codexBefore, readErr := os.ReadFile(codexPath)
		if readErr != nil && !os.IsNotExist(readErr) {
			return Result{}, fmt.Errorf("read %s: %w", codexPath, readErr)
		}
		codexAfter, mergeErr := mergeCodexConfig(codexBefore, dir)
		if mergeErr != nil {
			return Result{}, mergeErr
		}
		if !bytes.Equal(codexBefore, codexAfter) {
			result.Paths = append(result.Paths, codexPath)
			result.Diffs = append(result.Diffs, simpleDiff(codexPath, codexBefore, codexAfter))
			if !options.DryRun {
				if mkdirErr := os.MkdirAll(filepath.Dir(codexPath), 0o750); mkdirErr != nil {
					return Result{}, fmt.Errorf("create directory for %s: %w", codexPath, mkdirErr)
				}
				if writeErr := os.WriteFile(codexPath, codexAfter, 0o644); writeErr != nil {
					return Result{}, fmt.Errorf("write %s: %w", codexPath, writeErr)
				}
			}
		}
	}
	return result, nil
}

// findAgentInstruction finds the closest selected instruction file at or above dir.
// When none exists, it returns the workspace-local path that should be created.
func findAgentInstruction(dir string, target target) (string, error) {
	for current := dir; ; current = filepath.Dir(current) {
		path := filepath.Join(current, target.path)
		found, err := validateAgentInstruction(path)
		if err != nil {
			return "", err
		}
		if found {
			return path, nil
		}
		if filepath.Dir(current) == current {
			return filepath.Join(dir, target.path), nil
		}
	}
}

func validateAgentInstruction(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return false, fmt.Errorf("agent instruction %s is not a regular file", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, fmt.Errorf("resolve %s: %w", path, err)
	}
	base, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return false, fmt.Errorf("resolve agent instruction directory %s: %w", path, err)
	}
	relative, err := filepath.Rel(base, resolved)
	if err != nil {
		return false, fmt.Errorf("check agent instruction %s: %w", path, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return false, fmt.Errorf("agent instruction %s resolves outside its directory", path)
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		return false, fmt.Errorf("inspect resolved agent instruction %s: %w", path, err)
	}
	if !resolvedInfo.Mode().IsRegular() {
		return false, fmt.Errorf("agent instruction %s is not a regular file", path)
	}
	return true, nil
}

// findMCPConfig finds the closest regular .mcp.json at or above dir. When none
// exists, it returns the path where a workspace-local configuration should be created.
func findMCPConfig(dir string) (string, error) {
	for current := dir; ; current = filepath.Dir(current) {
		path := filepath.Join(current, mcpConfig)
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("mcp config %s is not a regular file", path)
			}
			return path, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect %s: %w", path, err)
		}
		if filepath.Dir(current) == current {
			return filepath.Join(dir, mcpConfig), nil
		}
	}
}

// MCPConfigSnippet is a ready-to-paste local MCP configuration.
func MCPConfigSnippet() (string, error) {
	data, err := mergeMCPConfig(nil)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type target struct {
	path   string
	header string
}

func agentTarget(agent string) (target, bool) {
	switch agent {
	case "claude":
		return target{path: "CLAUDE.md", header: "# Workspace instructions\n\n"}, true
	case "codex":
		return target{path: "AGENTS.md", header: "# Workspace instructions\n\n"}, true
	case "cursor":
		return target{
			path:   ".cursor/rules/just-mcp-work.mdc",
			header: "---\ndescription: Use workspace tasks through just-mcp-work\n---\n\n",
		}, true
	case "copilot":
		return target{path: ".github/copilot-instructions.md", header: "# Copilot instructions\n\n"}, true
	case "windsurf":
		return target{path: ".windsurfrules", header: "# Workspace instructions\n\n"}, true
	default:
		return target{}, false
	}
}

func canonicalBlock() string {
	return beginMarker + "\n" + Prompt + "\n" + endMarker + "\n"
}

func managedContent(before []byte, header string) ([]byte, error) {
	text := string(before)
	start := strings.Index(text, beginMarker)
	end := strings.Index(text, endMarker)
	block := canonicalBlock()
	if start >= 0 || end >= 0 {
		if start < 0 || end < start {
			return nil, fmt.Errorf("managed block markers are malformed")
		}
		end += len(endMarker)
		if end < len(text) && text[end] == '\n' {
			end++
		}
		return []byte(text[:start] + block + text[end:]), nil
	}
	if text == "" {
		return []byte(header + block), nil
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text + "\n" + block), nil
}

func mergeMCPConfig(before []byte) ([]byte, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, fmt.Errorf("make executable path absolute: %w", err)
	}
	config := map[string]any{}
	if len(bytes.TrimSpace(before)) > 0 {
		if decodeErr := json.Unmarshal(before, &config); decodeErr != nil {
			return nil, fmt.Errorf("decode existing .mcp.json: %w", decodeErr)
		}
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		servers = map[string]any{}
		config["mcpServers"] = servers
	}
	servers["just-mcp-work"] = map[string]any{
		"command": executable,
		"args":    []string{"serve", "--root", "."},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode .mcp.json: %w", err)
	}
	return append(data, '\n'), nil
}

func mergeCodexConfig(before []byte, root string) ([]byte, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, fmt.Errorf("make executable path absolute: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	text := string(before)
	if start := strings.Index(text, codexBegin); start >= 0 {
		end := strings.Index(text[start:], codexEnd)
		if end < 0 {
			return nil, fmt.Errorf("managed Codex MCP block markers are malformed")
		}
		end += start + len(codexEnd)
		if end < len(text) && text[end] == '\n' {
			end++
		}
		text = text[:start] + text[end:]
	}
	block := strings.Join(
		[]string{
			codexBegin,
			"[mcp_servers.just-mcp-work]",
			"command = " + tomlString(executable),
			"args = [\"serve\", \"--root\", " + tomlString(root) + "]",
			"startup_timeout_sec = 120",
			codexEnd,
		},
		"\n",
	)
	text = strings.TrimSpace(text)
	if text != "" {
		text += "\n\n"
	}
	return []byte(text + block + "\n"), nil
}

func tomlString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func unique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func simpleDiff(path string, before, after []byte) string {
	return "--- " +
		path +
		"\n+++ " + path +
		"\n-" + strings.ReplaceAll(string(before), "\n", "\n-") +
		"\n+" + strings.ReplaceAll(string(after), "\n", "\n+")
}
