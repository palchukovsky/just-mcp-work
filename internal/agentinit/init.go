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
	// claudeSettings holds the Claude tool permission lists.
	claudeSettings = ".claude/settings.json"
	// claudeServerRule is the Claude permission entry for this MCP server. Its
	// tool entries extend it with "__" and the tool name.
	claudeServerRule = "mcp__just-mcp-work"
)

// TripWirePrograms returns the programs whose direct shell invocation must be
// replaced by a discovered task. Prompt, the managed instruction block, and the
// MCP tool descriptions all render this one list, so a new runner cannot land
// in only one of them.
func TripWirePrograms() []string {
	return []string{"just", "cargo", "go", "make", "cmake", "docker", "npm", "ruff", "black"}
}

// tripWireList renders the programs as a plain enumeration.
func tripWireList() string {
	return strings.Join(TripWirePrograms(), ", ")
}

// tripWireChoice renders the programs as an enumeration that reads as a closed
// choice, for the sentences that end on the list instead of continuing it.
func tripWireChoice() string {
	programs := TripWirePrograms()
	if len(programs) < 2 {
		return tripWireList()
	}
	return strings.Join(programs[:len(programs)-1], ", ") + ", or " + programs[len(programs)-1]
}

// Prompt returns the canonical JMW usage guidance served as the MCP server's
// instructions. It renders TripWirePrograms instead of repeating the list.
func Prompt() string {
	return fmt.Sprintf(promptTemplate, tripWireChoice(), tripWireList())
}

const promptTemplate = `This workspace exposes runnable project tasks through just-mcp-work (JMW).

SCOPE - including delegated work. These rules bind you AND every sub-agent,
workflow stage, worktree, or external executor you spawn. When you delegate any
work that runs a project task (build, test, lint, format, a check/verify gate, or
a run), your delegated prompt MUST tell the executor to run it through JMW
(list_tasks -> run_task/start_task) and MUST NOT contain a hardcoded %s shell
line. A raw build/test/lint shell command embedded in a sub-agent prompt is a
rule violation, not a convenience.

TRIP-WIRE - stop before these tokens. Before you (or a delegate) run a shell
command whose program is %s, or the name of any discovered task or gate (check,
verify, lint, test, build, or a check-*/lint-* variant): STOP. That command is a
discovered task - run it via run_task/start_task, never Bash. Using Bash for a
task JMW already exposes is a violation even when it "works".

GATES ARE LONG TASKS. Any check/verify/CI-style gate has a long average duration:
launch it with start_task + wait_run, never a blocking Bash and never a bare
run_task you might mistake for hung. While a run_id is active, never launch the
task again.

WHERE BASH IS STILL FINE. Direct Bash is acceptable only for ad-hoc, read-only
inspection with no task representation - git status/diff/log, grep, sed -n, ls.
Anything runnable-as-a-task, and anything that mutates the tree or build, goes
through JMW.

- Discover existing tasks with list_projects and list_tasks.
- Use JMW to save tokens when a compact execution receipt is enough: success
  status, exit code, and short stdout/stderr tails, especially for successful
  checks where the full log is not needed.
- Run an existing task with run_task when that compact receipt is enough. A
  receipt with status: running and a run_id is normal: follow it with wait_run or
  get_run_status and do not start the task again. Prefer start_task for a task
  whose statistics show a long average duration.
- JMW is an execution-and-receipt tool, not a universal shell wrapper. If stdout
  itself is the data that must be inspected in full or quoted - for example git
  diff, large search results, source excerpts, generated reports, or command
  output explicitly requested by the user - use the normal shell or a specialized
  read/navigation tool instead of JMW.
- Use run_shell_command only for commands without an existing task and only when a
  compact receipt is sufficient; never for a command that maps to a discovered
  task (see TRIP-WIRE). Set working_directory to a workspace-relative directory
  (default .).
- On success, trust ok: true and exit code 0. Do not fetch logs merely to
  double-check the output.
- On failure, inspect stdout_tail and stderr_tail from the receipt first. Use
  get_run_logs only when the tails are missing or insufficient for diagnosis. Use
  tail_bytes: 0 on status tools to suppress output tails.
- Prefer existing tasks; do not edit build files unless asked.`

func managedBlockBody() string {
	return fmt.Sprintf(managedBlockTemplate, tripWireList())
}

const managedBlockTemplate = `This workspace uses just-mcp-work (JMW) for its runnable tasks. Full usage rules
are provided by the JMW MCP server (its instructions and tool descriptions). Core
rule: run project tasks - build, test, lint, format, check/verify gates -
through JMW (list_tasks -> run_task/start_task), never a raw shell line whose
program is %s, including in prompts you hand to sub-agents, workflows, or other
executors. Use direct Bash only for read-only inspection (git status/diff, grep,
ls).`

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
	return beginMarker + "\n" + managedBlockBody() + "\n" + endMarker + "\n"
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

// mergeClaudeSettings replaces the managed permission entries. Every entry of
// this MCP server is dropped from every permission list first, including tools
// and wildcards this version does not know, and the current entries are then
// appended to the allow and ask lists.
func mergeClaudeSettings(before []byte) ([]byte, error) {
	settings := map[string]any{}
	if len(bytes.TrimSpace(before)) > 0 {
		if err := json.Unmarshal(before, &settings); err != nil {
			return nil, fmt.Errorf("decode existing %s: %w", claudeSettings, err)
		}
	}
	permissions, err := claudePermissionLists(settings)
	if err != nil {
		return nil, err
	}
	for key, value := range permissions {
		list, isList := value.([]any)
		if !isList {
			continue
		}
		permissions[key] = withoutManagedClaudeTools(list)
	}
	managed := ClaudeManagedTools()
	if appendErr := appendClaudeTools(permissions, "allow", managed.Allow); appendErr != nil {
		return nil, appendErr
	}
	if appendErr := appendClaudeTools(permissions, "ask", managed.Ask); appendErr != nil {
		return nil, appendErr
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", claudeSettings, err)
	}
	return append(data, '\n'), nil
}

func claudePermissionLists(settings map[string]any) (map[string]any, error) {
	value, exists := settings["permissions"]
	if !exists || value == nil {
		permissions := map[string]any{}
		settings["permissions"] = permissions
		return permissions, nil
	}
	permissions, isObject := value.(map[string]any)
	if !isObject {
		return nil, fmt.Errorf("permissions in %s is not an object", claudeSettings)
	}
	return permissions, nil
}

func appendClaudeTools(permissions map[string]any, key string, tools []string) error {
	list, isList := permissions[key].([]any)
	if value, exists := permissions[key]; exists && value != nil && !isList {
		return fmt.Errorf("permissions.%s in %s is not a list", key, claudeSettings)
	}
	if list == nil {
		list = make([]any, 0, len(tools))
	}
	for _, tool := range tools {
		list = append(list, tool)
	}
	permissions[key] = list
	return nil
}

func withoutManagedClaudeTools(list []any) []any {
	kept := make([]any, 0, len(list))
	for _, item := range list {
		if text, isText := item.(string); isText && isManagedClaudeTool(text) {
			continue
		}
		kept = append(kept, item)
	}
	return kept
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
