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
	// serverName is the MCP server name owned by this executable. Every marker,
	// table, and permission entry below is built from it, so the name cannot
	// drift apart between the files init manages.
	serverName  = "just-mcp-work"
	beginMarker = "<!-- BEGIN " + serverName + " (managed) -->"
	endMarker   = "<!-- END " + serverName + " (managed) -->"
	mcpConfig   = ".mcp.json"
	codexConfig = ".codex/config.toml"
	codexBegin  = "# >>> " + serverName + " mcp (managed) >>>"
	codexEnd    = "# <<< " + serverName + " mcp <<<"
	codexTable  = "[mcp_servers." + serverName + "]"
	// claudeSettings holds the Claude tool permission lists.
	claudeSettings = ".claude/settings.json"
	// claudeServerRule is the Claude permission entry for this MCP server. Its
	// tool entries extend it with "__" and the tool name.
	claudeServerRule = "mcp__" + serverName
)

// Prompt returns the canonical JMW usage guidance served as the MCP server's
// instructions.
func Prompt() string {
	return promptText
}

const promptText = `This workspace exposes its runnable project tasks through just-mcp-work (JMW).

JMW exists to save tokens, not to wrap every command. Route work through it when
the full output is not what you need, and run the command directly when it is.

USE JMW WHEN
- You only need to know whether something worked - build, test, lint, format,
  check/verify gates. The receipt carries status, exit code, and short output
  tails instead of the whole log.
- You only need a slice of the output: the failing tail, or a byte range of a
  large log.
- The command is long or should not block: start_task returns a run_id at once,
  wait_run and get_run_status follow it, stop_run ends it.
- You delegate. Give sub-agents, workflow stages, and other executors the same
  rule, so a delegated build does not pour a full log into their context either.

RUN IT DIRECTLY WHEN
- The output itself is the answer: git diff, search results, source excerpts,
  generated reports, or anything the user asked to see in full. Sending that
  through JMW pays for the same text twice and buys nothing.

SPEND AS FEW TOKENS AS THE WORK ALLOWS
- On success, trust the receipt. ok: true with exit code 0 is the answer; do not
  fetch logs to double-check a green run unless the current task needs that
  output.
- On failure, start with stdout_tail and stderr_tail. Reach for get_run_logs only
  when the tails do not explain the failure.
- Use tail_bytes: 0 on status tools when even the tails are noise.
- A receipt with status: running and a run_id is normal, not a failure: follow it
  with wait_run or get_run_status, and never launch the same task twice.

HOW TO DRIVE IT
- Discover what exists with list_projects and list_tasks. Prefer an existing task
  over a hand-written command line, and do not edit build files unless asked.
- Ask list_tasks for the tasks you need, not for the catalog: names takes the
  exact names or task IDs you already expect, name_prefix and query search when
  you do not, visibility: public hides the private helpers, and detail: compact
  keeps task identity and parameters, returns at most the first 160 runes of the
  first description line, and drops runner metadata and run statistics. Only
  names, name_prefix, and query are mutually exclusive: use one of those per
  call.
- run_task runs a discovered task and promotes a long run to the background;
  start_task starts it in the background from the beginning.
- run_shell_command runs a command that has no task behind it. Set
  working_directory to a workspace-relative directory (default .).`

// managedBlockText is the instruction block written into the agent files. It
// carries the same contract as promptText in a form an agent can read before
// the server is attached, so the two must not drift apart.
const managedBlockText = `This workspace uses just-mcp-work (JMW) for its runnable tasks; the JMW MCP
server itself carries the full usage rules. Core rule: JMW is there to save
tokens. Run a task through it (list_tasks -> run_task/start_task) whenever you do
not need the full output - build, test, lint, format, check/verify gates - and
trust its receipt instead of re-reading the log of a successful run. Run a
command directly when its full output is the thing you actually need. Pass the
same rule on to sub-agents and other executors.`

// ClaudePermissions selects how init treats the Claude tool permission lists.
type ClaudePermissions string

const (
	// ClaudePermissionsAsk confirms the pending change on the console.
	ClaudePermissionsAsk ClaudePermissions = "ask"
	// ClaudePermissionsYes writes the change without a confirmation.
	ClaudePermissionsYes ClaudePermissions = "yes"
	// ClaudePermissionsNo leaves the Claude permission lists alone.
	ClaudePermissionsNo ClaudePermissions = "no"
)

// ParseClaudePermissions resolves the command-line value of the Claude
// permission mode. An empty value selects the console confirmation.
func ParseClaudePermissions(value string) (ClaudePermissions, error) {
	switch mode := ClaudePermissions(strings.ToLower(strings.TrimSpace(value))); mode {
	case "", ClaudePermissionsAsk:
		return ClaudePermissionsAsk, nil
	case ClaudePermissionsYes, ClaudePermissionsNo:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported Claude permission mode %q: use ask, yes, or no", value)
	}
}

// ClaudeToolPrefix is the Claude permission entry prefix of this server's tools.
const ClaudeToolPrefix = claudeServerRule + "__"

// ClaudeToolPermissions holds the managed Claude permission entries of this MCP
// server. Allow lists the tools that may run unattended; Ask lists the tools
// that stay behind a Claude confirmation because they execute a free-form
// command.
type ClaudeToolPermissions struct {
	Allow []string
	Ask   []string
}

// ClaudeManagedTools returns the managed Claude permission entries. The tool
// names must stay in sync with the tools the MCP server registers.
func ClaudeManagedTools() ClaudeToolPermissions {
	return ClaudeToolPermissions{
		Allow: claudeToolRules(
			"run_task",
			"start_task",
			"wait_run",
			"get_run_status",
			"get_run",
			"get_run_logs",
			"list_runs",
			"stop_run",
			"list_projects",
			"list_tasks",
			"version_status",
		),
		Ask: claudeToolRules(
			"run_shell_command",
			"start_shell_command",
		),
	}
}

func claudeToolRules(tools ...string) []string {
	rules := make([]string, 0, len(tools))
	for _, tool := range tools {
		rules = append(rules, ClaudeToolPrefix+tool)
	}
	return rules
}

// Options controls agent instruction injection.
//
//nolint:govet // Field order follows the documented option grouping.
type Options struct {
	Dir            string
	Agents         []string
	DryRun         bool
	WriteMCPConfig bool
	// ClaudePermissions selects how the Claude permission lists are treated. The
	// zero value asks through Confirm.
	ClaudePermissions ClaudePermissions
	// Confirm approves a pending Claude permission change. It receives the target
	// path and the planned diff, and is called only in the ask mode and only when
	// the file would actually change. A nil Confirm declines the change.
	Confirm func(path string, diff string) (bool, error)
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
	agents := unique(options.Agents)
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
	for _, agent := range agents {
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
				// #nosec G306,G703 -- findMCPConfig resolves the nearest regular config
				// at or above Dir, and a local .mcp.json must be readable by its agent.
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
	if err := applyClaudePermissions(scope, agents, options, &result); err != nil {
		return Result{}, err
	}
	return result, nil
}

// applyClaudePermissions installs the managed tool permissions into the Claude
// settings file. It does nothing when Claude is not a selected agent, when the
// operator opted out, or when the confirmation is declined.
func applyClaudePermissions(
	scope string,
	agents []string,
	options Options,
	result *Result,
) error {
	if !slices.Contains(agents, "claude") || options.ClaudePermissions == ClaudePermissionsNo {
		return nil
	}
	path, err := findClaudeSettings(scope)
	if err != nil {
		return err
	}
	// #nosec G304 -- path is the Claude settings file validated not to resolve
	// outside the workspace scope.
	before, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	after, err := mergeClaudeSettings(before)
	if err != nil {
		return fmt.Errorf("merge %s: %w", path, err)
	}
	if bytes.Equal(before, after) {
		return nil
	}
	diff := simpleDiff(path, before, after)
	if !options.DryRun {
		confirmed, confirmErr := confirmClaudePermissions(path, diff, options)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			return nil
		}
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o750); mkdirErr != nil {
			return fmt.Errorf("create directory for %s: %w", path, mkdirErr)
		}
		// #nosec G703 -- findClaudeSettings resolves existing symlinks and rejects
		// targets outside the workspace scope before any file is changed.
		if writeErr := os.WriteFile(path, after, 0o600); writeErr != nil {
			return fmt.Errorf("write %s: %w", path, writeErr)
		}
	}
	result.Paths = append(result.Paths, path)
	result.Diffs = append(result.Diffs, diff)
	return nil
}

func confirmClaudePermissions(path string, diff string, options Options) (bool, error) {
	if options.ClaudePermissions == ClaudePermissionsYes {
		return true, nil
	}
	if options.Confirm == nil {
		return false, nil
	}
	confirmed, err := options.Confirm(path, diff)
	if err != nil {
		return false, fmt.Errorf("confirm %s: %w", path, err)
	}
	return confirmed, nil
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

// scopedConfig describes an agent configuration file that must stay inside the
// workspace scope. Go error strings start in lower case, so a message that opens
// with the label needs its own form.
type scopedConfig struct {
	relative string
	name     string
	lower    string
}

func findCodexConfig(scope string) (string, error) {
	return findScopedConfig(
		scope,
		scopedConfig{relative: codexConfig, name: "Codex config", lower: "codex config"},
	)
}

func findClaudeSettings(scope string) (string, error) {
	return findScopedConfig(
		scope,
		scopedConfig{relative: claudeSettings, name: "Claude settings", lower: "claude settings"},
	)
}

func findScopedConfig(scope string, config scopedConfig) (string, error) {
	resolvedScope, err := resolveWorkspaceScope(scope)
	if err != nil {
		return "", err
	}
	resolvedDirectory, err := resolveScopedConfigDirectory(scope, resolvedScope, config)
	if err != nil {
		return "", err
	}
	return resolveScopedConfigFile(scope, resolvedScope, resolvedDirectory, config)
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

func resolveScopedConfigDirectory(
	scope string,
	resolvedScope string,
	config scopedConfig,
) (string, error) {
	directory := filepath.Join(scope, filepath.Dir(config.relative))
	info, err := os.Lstat(directory)
	if os.IsNotExist(err) {
		return filepath.Join(resolvedScope, filepath.Dir(config.relative)), nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect %s directory %s: %w", config.name, directory, err)
	}
	if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		// Windows directory junctions are reported by Lstat as irregular files.
		// Follow the path once before rejecting it so junction-backed workspace
		// configuration, such as the installer fallback, remains usable.
		resolvedInfo, statErr := os.Stat(directory)
		if statErr != nil || !resolvedInfo.IsDir() {
			return "", fmt.Errorf("%s directory %s is not a directory", config.lower, directory)
		}
	}
	resolvedDirectory, err := resolveWithinScope(resolvedScope, directory)
	if err != nil {
		return "", fmt.Errorf("resolve %s directory: %w", config.name, err)
	}
	resolvedInfo, err := os.Stat(resolvedDirectory)
	if err != nil {
		return "", fmt.Errorf("inspect resolved %s directory %s: %w", config.name, directory, err)
	}
	if !resolvedInfo.IsDir() {
		return "", fmt.Errorf("%s directory %s is not a directory", config.lower, directory)
	}
	return resolvedDirectory, nil
}

func resolveScopedConfigFile(
	scope string,
	resolvedScope string,
	resolvedDirectory string,
	config scopedConfig,
) (string, error) {
	path := filepath.Join(scope, config.relative)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return filepath.Join(resolvedDirectory, filepath.Base(config.relative)), nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect %s %s: %w", config.name, path, err)
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("%s %s is not a regular file", config.lower, path)
	}
	// Resolve the directory before appending the file name. On Windows,
	// EvalSymlinks can fail for a file addressed through a directory junction,
	// even though the file is present in the junction target.
	resolvedPath, err := resolveWithinScope(
		resolvedScope,
		filepath.Join(resolvedDirectory, filepath.Base(config.relative)),
	)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", config.name, err)
	}
	resolvedInfo, err := os.Stat(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("inspect resolved %s %s: %w", config.name, path, err)
	}
	if !resolvedInfo.Mode().IsRegular() {
		return "", fmt.Errorf("%s %s is not a regular file", config.lower, path)
	}
	return resolvedPath, nil
}

func resolveWithinScope(scope string, path string) (string, error) {
	resolved, err := resolvePath(path)
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

func resolvePath(path string) (string, error) {
	link, err := os.Readlink(path)
	if err == nil {
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(path), link)
		}
		resolved, resolveErr := filepath.EvalSymlinks(link)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve %s: %w", link, resolveErr)
		}
		return resolved, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
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
	return beginMarker + "\n" + managedBlockText + "\n" + endMarker + "\n"
}

func managedContent(before []byte, header string) ([]byte, error) {
	text := string(before)
	// The instruction file keeps the line ending it is written with, so the
	// managed block does not turn a CRLF document into a mixed one.
	lineBreak := documentLineBreak(before)
	start := strings.Index(text, beginMarker)
	end := strings.Index(text, endMarker)
	block := withLineBreak(canonicalBlock(), lineBreak)
	if start >= 0 || end >= 0 {
		if start < 0 || end < start {
			return nil, fmt.Errorf("managed block markers are malformed")
		}
		prefix := normalizeTrailingLineBreak(text[:start], lineBreak)
		end += len(endMarker)
		suffix, _ := trimLeadingLineBreak(text[end:])
		return []byte(prefix + block + suffix), nil
	}
	if text == "" {
		// A file created here has no ending of its own to keep, so the header
		// and the block go in with the bare newline they are written with.
		return []byte(header + block), nil
	}
	text = normalizeTrailingLineBreak(text, lineBreak)
	if !strings.HasSuffix(text, lineBreak) {
		text += lineBreak
	}
	return []byte(text + lineBreak + block), nil
}

// serverEntry is the managed MCP server definition of this executable. The
// field order defines the order of the generated JSON keys.
type serverEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func managedServerEntry() (serverEntry, error) {
	executable, err := os.Executable()
	if err != nil {
		return serverEntry{}, fmt.Errorf("resolve executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return serverEntry{}, fmt.Errorf("make executable path absolute: %w", err)
	}
	return serverEntry{Command: executable, Args: []string{"serve", "--root", "."}}, nil
}

// mergeMCPConfig writes the managed server entry into the configuration text.
// Only that entry is rewritten: unrelated servers, key order, and the file's
// own formatting are left exactly as they were.
func mergeMCPConfig(before []byte) ([]byte, error) {
	entry, err := managedServerEntry()
	if err != nil {
		return nil, err
	}
	data := emptyJSONObject(before)
	root, err := decodeJSONObject(data, mcpConfig)
	if err != nil {
		return nil, err
	}
	members, err := jsonObjectMembers(data, root)
	if err != nil {
		return nil, fmt.Errorf("decode existing %s: %w", mcpConfig, err)
	}
	servers, found := jsonFindMember(members, "mcpServers")
	if !found || isJSONNull(data, servers.value) {
		merged, setErr := jsonSetMember(data, root, "mcpServers", map[string]any{serverName: entry})
		if setErr != nil {
			return nil, fmt.Errorf("update %s: %w", mcpConfig, setErr)
		}
		return merged, nil
	}
	if data[servers.value.start] != '{' {
		return nil, fmt.Errorf(
			"mcpServers in %s is not an object; init refuses to replace it, "+
				"fix or remove that entry and run init again",
			mcpConfig,
		)
	}
	merged, err := jsonSetMember(data, servers.value, serverName, entry)
	if err != nil {
		return nil, fmt.Errorf("update %s: %w", mcpConfig, err)
	}
	return merged, nil
}

// emptyJSONObject substitutes an empty object for a missing or blank file, so
// a created file and an edited one take the same code path.
func emptyJSONObject(before []byte) []byte {
	if len(bytes.TrimSpace(before)) == 0 {
		return []byte("{}" + documentLineBreak(before))
	}
	return before
}

// isJSONNull reports whether the span holds the null literal, which init treats
// as an unset value it may take over.
func isJSONNull(data []byte, span jsonSpan) bool {
	return string(data[span.start:span.end]) == "null"
}

// decodeJSONObject validates that data holds a JSON object and returns its span.
func decodeJSONObject(data []byte, name string) (jsonSpan, error) {
	decoded := map[string]any{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return jsonSpan{}, fmt.Errorf("decode existing %s: %w", name, err)
	}
	span, err := jsonDocumentSpan(data)
	if err != nil {
		return jsonSpan{}, fmt.Errorf("decode existing %s: %w", name, err)
	}
	return span, nil
}

// mergeClaudeSettings replaces the managed permission entries. Every entry of
// this MCP server is dropped from every permission list first, including tools
// and wildcards this version does not know, and the current entries are then
// appended to the allow and ask lists. Foreign entries, unrelated settings, and
// the file's own formatting are preserved byte for byte.
func mergeClaudeSettings(before []byte) ([]byte, error) {
	data := emptyJSONObject(before)
	root, err := decodeJSONObject(data, claudeSettings)
	if err != nil {
		return nil, err
	}
	if validateErr := validateClaudePermissions(data); validateErr != nil {
		return nil, validateErr
	}
	members, err := jsonObjectMembers(data, root)
	if err != nil {
		return nil, fmt.Errorf("decode existing %s: %w", claudeSettings, err)
	}
	owned := claudeOwnedLists()
	permissions, found := jsonFindMember(members, "permissions")
	if !found || isJSONNull(data, permissions.value) {
		lists := make(map[string]any, len(owned))
		for _, list := range owned {
			lists[list.key] = list.tools
		}
		settings, setErr := jsonSetMember(data, root, "permissions", lists)
		if setErr != nil {
			return nil, fmt.Errorf("update %s: %w", claudeSettings, setErr)
		}
		return settings, nil
	}
	// The owning lists lose the retired entries and gain the current ones in one
	// rewrite each. Emptying them first and refilling them afterwards would
	// destroy the layout in between and make a repeated init rewrite the file.
	owning := make([]string, 0, len(owned))
	for _, list := range owned {
		owning = append(owning, list.key)
		data, err = appendClaudeTools(data, list.key, list.tools)
		if err != nil {
			return nil, err
		}
	}
	return dropManagedClaudeTools(data, owning)
}

// claudeOwnedList is one permission list this server maintains.
type claudeOwnedList struct {
	key   string
	tools []string
}

// claudeOwnedLists reports the permission lists this server owns. Validation,
// the refill, and the exclusion from the sweep all read the keys from here, so
// a list added to it cannot be validated by halves or stripped again.
func claudeOwnedLists() []claudeOwnedList {
	managed := ClaudeManagedTools()
	return []claudeOwnedList{
		{key: "allow", tools: managed.Allow},
		{key: "ask", tools: managed.Ask},
	}
}

// validateClaudePermissions rejects a settings file whose permission lists this
// server cannot edit, before any byte of it is changed.
func validateClaudePermissions(data []byte) error {
	var settings struct {
		Permissions any `json:"permissions"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("decode existing %s: %w", claudeSettings, err)
	}
	if settings.Permissions == nil {
		return nil
	}
	permissions, isObject := settings.Permissions.(map[string]any)
	if !isObject {
		return fmt.Errorf(
			"permissions in %s is not an object; fix that entry and run init again",
			claudeSettings,
		)
	}
	for _, list := range claudeOwnedLists() {
		key := list.key
		value, exists := permissions[key]
		if !exists || value == nil {
			continue
		}
		if _, isList := value.([]any); !isList {
			return fmt.Errorf(
				"permissions.%s in %s is not a list; fix that entry and run init again",
				key,
				claudeSettings,
			)
		}
	}
	return nil
}

// claudePermissionsSpan locates the permissions object of the settings file.
func claudePermissionsSpan(data []byte) (jsonSpan, error) {
	root, err := jsonDocumentSpan(data)
	if err != nil {
		return jsonSpan{}, fmt.Errorf("read %s: %w", claudeSettings, err)
	}
	members, err := jsonObjectMembers(data, root)
	if err != nil {
		return jsonSpan{}, fmt.Errorf("read %s: %w", claudeSettings, err)
	}
	permissions, found := jsonFindMember(members, "permissions")
	if !found {
		return jsonSpan{}, fmt.Errorf("permissions in %s is missing", claudeSettings)
	}
	return permissions.value, nil
}

// dropManagedClaudeTools removes every entry of this server from every
// permission list except the ones already rewritten, keeping the foreign
// entries of those lists untouched.
func dropManagedClaudeTools(data []byte, rewritten []string) ([]byte, error) {
	permissions, err := claudePermissionsSpan(data)
	if err != nil {
		return nil, err
	}
	members, err := jsonObjectMembers(data, permissions)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", claudeSettings, err)
	}
	keys := make([]string, 0, len(members))
	for _, member := range members {
		if data[member.value.start] == '[' && !slices.Contains(rewritten, member.key) {
			keys = append(keys, member.key)
		}
	}
	// Every edit shifts the spans that follow it, so each list is located again.
	for _, key := range keys {
		data, err = editClaudeList(data, key, nil)
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

// appendClaudeTools appends the managed tools to a permission list, writing the
// list anew when the settings file has no list to append to. A null value is an
// unset list, the same way validateClaudePermissions accepts it.
func appendClaudeTools(data []byte, key string, tools []string) ([]byte, error) {
	permissions, err := claudePermissionsSpan(data)
	if err != nil {
		return nil, err
	}
	members, err := jsonObjectMembers(data, permissions)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", claudeSettings, err)
	}
	if member, found := jsonFindMember(members, key); !found || isJSONNull(data, member.value) {
		settings, setErr := jsonSetMember(data, permissions, key, tools)
		if setErr != nil {
			return nil, fmt.Errorf("update %s: %w", claudeSettings, setErr)
		}
		return settings, nil
	}
	return editClaudeList(data, key, tools)
}

// editClaudeList rewrites one permission list in place.
func editClaudeList(data []byte, key string, add []string) ([]byte, error) {
	permissions, err := claudePermissionsSpan(data)
	if err != nil {
		return nil, err
	}
	members, err := jsonObjectMembers(data, permissions)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", claudeSettings, err)
	}
	member, found := jsonFindMember(members, key)
	if !found {
		return nil, fmt.Errorf("permissions.%s in %s is missing", key, claudeSettings)
	}
	if data[member.value.start] != '[' {
		return nil, fmt.Errorf(
			"permissions.%s in %s is not a list; fix that entry and run init again",
			key,
			claudeSettings,
		)
	}
	edited, err := jsonRewriteStringList(data, member.value, isManagedClaudeTool, add)
	if err != nil {
		return nil, fmt.Errorf("update permissions.%s in %s: %w", key, claudeSettings, err)
	}
	return edited, nil
}

// isManagedClaudeTool reports whether the permission entry addresses this MCP
// server. Another server whose name only starts with the same text is kept.
func isManagedClaudeTool(entry string) bool {
	entry = strings.TrimSpace(entry)
	return entry == claudeServerRule || strings.HasPrefix(entry, claudeServerRule+"__")
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
	executableValue, err := tomlString(executable)
	if err != nil {
		return nil, fmt.Errorf("encode executable path: %w", err)
	}
	rootValue, err := tomlString(root)
	if err != nil {
		return nil, fmt.Errorf("encode workspace root: %w", err)
	}
	// The config keeps the line ending it is written with, the same way the
	// instruction files and the JSON configs do.
	lineBreak := documentLineBreak(before)
	block := strings.Join(
		[]string{
			codexBegin,
			codexTable,
			"command = " + executableValue,
			"args = [\"serve\", \"--root\", " + rootValue + "]",
			"startup_timeout_sec = 120",
			codexEnd,
		},
		lineBreak,
	)
	text := string(before)
	// An existing managed block is replaced where it stands, so the operator's
	// own ordering and spacing around it survive.
	if start := strings.Index(text, codexBegin); start >= 0 {
		end := strings.Index(text[start:], codexEnd)
		if end < 0 {
			return nil, fmt.Errorf("managed Codex MCP block markers are malformed")
		}
		end += start + len(codexEnd)
		if err := rejectUnmanagedCodexServer(text[:start] + text[end:]); err != nil {
			return nil, err
		}
		prefix := normalizeTrailingLineBreak(text[:start], lineBreak)
		suffix := text[end:]
		if rest, found := trimLeadingLineBreak(suffix); found {
			suffix = lineBreak + rest
		}
		return []byte(prefix + block + suffix), nil
	}
	if err := rejectUnmanagedCodexServer(text); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		text = ""
	} else {
		text = strings.TrimRight(text, "\r\n") + lineBreak + lineBreak
	}
	return []byte(text + block + lineBreak), nil
}

// rejectUnmanagedCodexServer refuses to take over a manually configured server
// entry instead of silently replacing it.
func rejectUnmanagedCodexServer(text string) error {
	containsServer, err := containsCodexServerTable(text)
	if err != nil {
		return fmt.Errorf("decode Codex config: %w", err)
	}
	if containsServer {
		return fmt.Errorf(
			"unmanaged %s table already exists; remove it before running init",
			codexTable,
		)
	}
	return nil
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
