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
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	beginMarker = "<!-- BEGIN just-mcp-work (managed) -->"
	endMarker   = "<!-- END just-mcp-work (managed) -->"
	mcpConfig   = ".mcp.json"
	codexConfig = ".codex/config.toml"
	codexBegin  = "# >>> just-mcp-work mcp (managed) >>>"
	codexEnd    = "# <<< just-mcp-work mcp <<<"
	codexTable  = "[mcp_servers.just-mcp-work]"
)

// Prompt is the canonical agent instruction managed by init.
const Prompt = `This workspace exposes its runnable tasks through the just-mcp-work MCP server
(currently the ` + "`just`" + `, ` + "`CMake`" + `, and ` + "`Make`" + ` runners). When running project tasks:

- Discover tasks with ` + "`list_projects`" + ` and ` + "`list_tasks`" + ` instead of
  reading build files. ` + "`list_projects`" + ` defaults to depth 1 without dot-directories;
  use path to choose a subtree, max_depth or include_hidden to widen directory coverage, and runners
  to restrict projects. ` + "`pruned.depth`" + ` and ` + "`pruned.hidden`" + ` count skipped directory
  subtrees; ` + "`pruned.runner_mismatch`" + ` means relaxing runners may return more projects. Excluded
  paths are configured by the operator and cannot be widened.
- Run short tasks with ` + "`run_task`" + ` (project_path, task_id, arguments). A receipt with
  ` + "`status: running`" + ` and a ` + "`run_id`" + ` is normal: follow it with ` + "`wait_run`" + ` or
  ` + "`get_run_status`" + ` and never start the task again. Prefer ` + "`start_task`" + ` for a task
  whose statistics show a long average duration. ` + "`wait_run`" + ` accepts ` + "`max_wait_ms: 0`" + ` for
  an immediate snapshot; ` + "`get_run_status`" + ` never waits.
- Use ` + "`run_shell_command`" + ` for an arbitrary shell command that is not represented by a
  discovered task. Set ` + "`working_directory`" + ` to a workspace-relative directory (default
  ` + "`.`" + `); do not use it instead of ` + "`run_task`" + ` for an existing task.
- Results are compact. Treat ` + "`ok: true`" + ` and exit code 0 as the primary success
  signal. Do not call ` + "`get_run_logs`" + ` after a successful run just to
  double-check output.
- If more context is needed, first use ` + "`stdout_tail`" + ` and ` + "`stderr_tail`" + ` from the
  receipt. Fetch full logs with ` + "`get_run_logs`" + ` only when explicitly requested
  or when diagnosing a failure and the tail is insufficient. Use ` + "`last_output_age_ms`" + ` as the
  anti-hang signal; ` + "`stats`" + ` reports expected duration from retained runs. Use ` + "`tail_bytes: 0`" + `
  on status tools to suppress output tails.
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
	scope, err := findScopeRoot(dir)
	if err != nil {
		return Result{}, err
	}
	if len(options.Agents) == 0 {
		options.Agents = []string{"claude", "codex", "cursor"}
	}
	var codexPath string
	var codexBefore []byte
	var codexAfter []byte
	if options.WriteMCPConfig {
		codexPath, err = findCodexConfig(scope)
		if err != nil {
			return Result{}, err
		}
		codexBefore, err = os.ReadFile(codexPath)
		if err != nil && !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("read %s: %w", codexPath, err)
		}
		codexAfter, err = mergeCodexConfig(codexBefore, scope)
		if err != nil {
			return Result{}, fmt.Errorf("merge %s: %w", codexPath, err)
		}
	}
	result := Result{}
	for _, agent := range unique(options.Agents) {
		target, ok := agentTarget(agent)
		if !ok {
			return Result{}, fmt.Errorf("unsupported agent %q", agent)
		}
		path, err := findAgentInstruction(scope, target)
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
		path, err := findMCPConfig(scope)
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
		if !bytes.Equal(codexBefore, codexAfter) {
			result.Paths = append(result.Paths, codexPath)
			result.Diffs = append(result.Diffs, simpleDiff(codexPath, codexBefore, codexAfter))
			if !options.DryRun {
				if mkdirErr := os.MkdirAll(filepath.Dir(codexPath), 0o750); mkdirErr != nil {
					return Result{}, fmt.Errorf("create directory for %s: %w", codexPath, mkdirErr)
				}
				// #nosec G703 -- findCodexConfig resolves existing symlinks and rejects
				// targets outside the workspace scope before any file is changed.
				if writeErr := os.WriteFile(codexPath, codexAfter, 0o600); writeErr != nil {
					return Result{}, fmt.Errorf("write %s: %w", codexPath, writeErr)
				}
			}
		}
	}
	return result, nil
}

// findScopeRoot uses the nearest existing MCP config as the workspace boundary.
// Without one, dir is a standalone project and owns all generated files.
func findScopeRoot(dir string) (string, error) {
	for current := dir; ; current = filepath.Dir(current) {
		path := filepath.Join(current, mcpConfig)
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("mcp config %s is not a regular file", path)
			}
			return current, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect %s: %w", path, err)
		}
		if filepath.Dir(current) == current {
			return dir, nil
		}
	}
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

func findCodexConfig(scope string) (string, error) {
	resolvedScope, err := resolveWorkspaceScope(scope)
	if err != nil {
		return "", err
	}
	resolvedDirectory, err := resolveCodexConfigDirectory(scope, resolvedScope)
	if err != nil {
		return "", err
	}
	return resolveCodexConfigFile(scope, resolvedScope, resolvedDirectory)
}

func resolveWorkspaceScope(scope string) (string, error) {
	var missing []string
	for current := scope; ; current = filepath.Dir(current) {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return "", fmt.Errorf("resolve workspace scope %s: %w", scope, resolveErr)
			}
			for _, component := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, component)
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) || filepath.Dir(current) == current {
			return "", fmt.Errorf("resolve workspace scope %s: %w", scope, err)
		}
		missing = append(missing, filepath.Base(current))
	}
}

func resolveCodexConfigDirectory(scope string, resolvedScope string) (string, error) {
	directory := filepath.Join(scope, filepath.Dir(codexConfig))
	info, err := os.Lstat(directory)
	if os.IsNotExist(err) {
		return filepath.Join(resolvedScope, filepath.Dir(codexConfig)), nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect Codex config directory %s: %w", directory, err)
	}
	if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("codex config directory %s is not a directory", directory)
	}
	resolvedDirectory, err := resolveWithinScope(resolvedScope, directory)
	if err != nil {
		return "", fmt.Errorf("resolve Codex config directory: %w", err)
	}
	resolvedInfo, err := os.Stat(resolvedDirectory)
	if err != nil {
		return "", fmt.Errorf("inspect resolved Codex config directory %s: %w", directory, err)
	}
	if !resolvedInfo.IsDir() {
		return "", fmt.Errorf("codex config directory %s is not a directory", directory)
	}
	return resolvedDirectory, nil
}

func resolveCodexConfigFile(
	scope string,
	resolvedScope string,
	resolvedDirectory string,
) (string, error) {
	path := filepath.Join(scope, codexConfig)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return filepath.Join(resolvedDirectory, filepath.Base(codexConfig)), nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect Codex config %s: %w", path, err)
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("codex config %s is not a regular file", path)
	}
	resolvedPath, err := resolveWithinScope(resolvedScope, path)
	if err != nil {
		return "", fmt.Errorf("resolve Codex config: %w", err)
	}
	resolvedInfo, err := os.Stat(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("inspect resolved Codex config %s: %w", path, err)
	}
	if !resolvedInfo.Mode().IsRegular() {
		return "", fmt.Errorf("codex config %s is not a regular file", path)
	}
	return resolvedPath, nil
}

func resolveWithinScope(scope string, path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	relative, err := filepath.Rel(scope, resolved)
	if err != nil {
		return "", fmt.Errorf("check %s against workspace scope: %w", path, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return "", fmt.Errorf("%s resolves outside workspace scope %s", path, scope)
	}
	return resolved, nil
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
	containsServer, parseErr := containsCodexServerTable(text)
	if parseErr != nil {
		return nil, fmt.Errorf("decode Codex config: %w", parseErr)
	}
	if containsServer {
		return nil, fmt.Errorf(
			"unmanaged %s table already exists; remove it before running init",
			codexTable,
		)
	}
	executableValue, err := tomlString(executable)
	if err != nil {
		return nil, fmt.Errorf("encode executable path: %w", err)
	}
	rootValue, err := tomlString(root)
	if err != nil {
		return nil, fmt.Errorf("encode workspace root: %w", err)
	}
	block := strings.Join(
		[]string{
			codexBegin,
			codexTable,
			"command = " + executableValue,
			"args = [\"serve\", \"--root\", " + rootValue + "]",
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

func containsCodexServerTable(text string) (bool, error) {
	config := map[string]any{}
	metadata, err := toml.Decode(text, &config)
	if err != nil {
		return false, fmt.Errorf("decode TOML: %w", err)
	}
	return metadata.IsDefined("mcp_servers", "just-mcp-work"), nil
}

func tomlString(value string) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal TOML string: %w", err)
	}
	return string(encoded), nil
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
