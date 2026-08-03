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
	"github.com/palchukovsky/just-mcp-work/internal/runner"
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
- A task may be absent because the operator withheld it through a runner mode.
  Never recreate or run such a task through run_shell_command,
  start_shell_command, or another shell path.
- Shell tools remain available for genuinely ad-hoc commands outside the
  discovered or withheld task surfaces. Set working_directory to a
  workspace-relative directory (default .).`

// managedBlockText is the instruction block written into the agent files. It
// carries the same contract as promptText in a form an agent can read before
// the server is attached, so the two must not drift apart.
const managedBlockText = `This workspace uses just-mcp-work (JMW) for its runnable tasks; the JMW MCP
server itself carries the full usage rules. Core rule: JMW is there to save
tokens. Run a task through it (list_tasks -> run_task/start_task) whenever you do
not need the full output - build, test, lint, format, check/verify gates - and
trust its receipt instead of re-reading the log of a successful run. Run a
command directly when its full output is the thing you actually need. Pass the
same rule on to sub-agents and other executors. A task may be absent because the
operator withheld it through a runner mode; never recreate or run such a task
through run_shell_command, start_shell_command, or another shell path. Shell
tools remain available for genuinely ad-hoc commands outside the discovered or
withheld task surfaces.`

// ClaudePermissions selects how init treats the Claude tool permission lists.
type ClaudePermissions string

const (
	// ClaudePermissionsAsk confirms the pending change on the console.
	ClaudePermissionsAsk ClaudePermissions = "ask"
	// ClaudePermissionsYes writes the change without a confirmation.
	ClaudePermissionsYes ClaudePermissions = "yes"
	// ClaudePermissionsNo removes this server's entries without adding them back.
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
	// RunnerModes is the complete, catalog-ordered runner selection persisted in
	// every managed MCP server command.
	RunnerModes runner.ValidatedSelections
	// ClaudePermissions selects how the Claude permission lists are treated. The
	// zero value asks through Confirm.
	ClaudePermissions ClaudePermissions
	// Confirm approves a pending Claude permission change. It receives the target
	// path and the planned diff, and is called only in the ask mode. A nil Confirm
	// declines the managed permissions and leaves only the cleanup plan.
	Confirm func(path string, diff string) (bool, error)
}

// Result lists changed or would-change files.
type Result struct {
	Paths []string
	Diffs []string
}

type plannedEdit struct {
	path         string
	before       []byte
	after        []byte
	mode         os.FileMode
	beforeExists bool
	remove       bool
}

// Apply makes the current invocation authoritative for every workspace-local
// surface owned by JMW. All paths and contents are planned before the first
// write, so a malformed later target cannot leave an earlier one updated.
func Apply(options Options) (Result, error) {
	if _, err := options.RunnerModes.Args(); err != nil {
		return Result{}, fmt.Errorf("validate runner mode arguments: %w", err)
	}
	dir, err := filepath.Abs(options.Dir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve workspace directory: %w", err)
	}
	scope, preserveMCPAnchor, err := findScopeRoot(dir)
	if err != nil {
		return Result{}, err
	}
	if len(options.Agents) == 0 {
		options.Agents = []string{"claude", "codex", "cursor"}
	}
	agents := unique(options.Agents)
	selected := make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		if _, ok := agentTarget(agent); !ok {
			return Result{}, fmt.Errorf("unsupported agent %q", agent)
		}
		selected[agent] = struct{}{}
	}
	edits, err := planAgentInstructions(scope, selected)
	if err != nil {
		return Result{}, err
	}
	mcpEdit, err := planMCPConfig(scope, preserveMCPAnchor, options)
	if err != nil {
		return Result{}, err
	}
	edits = appendEdit(edits, mcpEdit)
	codexEdit, err := planCodexConfig(scope, options)
	if err != nil {
		return Result{}, err
	}
	edits = appendEdit(edits, codexEdit)
	// The Claude settings file belongs to the claude agent, so an invocation that
	// does not select claude plans nothing for it, whatever ClaudePermissions says.
	if _, claudeSelected := selected["claude"]; claudeSelected {
		claudeEdit, claudeErr := planClaudeSettings(scope, options)
		if claudeErr != nil {
			return Result{}, claudeErr
		}
		edits = appendEdit(edits, claudeEdit)
	}
	result := resultForEdits(edits)
	if options.DryRun {
		return result, nil
	}
	if err := applyEdits(edits); err != nil {
		return Result{}, err
	}
	return result, nil
}

// planAgentInstructions plans the managed block for the selected agents only. An
// agent the operator did not select is left alone, so init neither rewrites nor
// removes its instruction file. That also keeps two agent targets that resolve
// to one file, such as an AGENTS.md and a CLAUDE.md symlinked to the same
// document, from cancelling each other out.
func planAgentInstructions(
	scope string,
	selected map[string]struct{},
) ([]plannedEdit, error) {
	edits := make([]plannedEdit, 0, len(selected))
	for _, named := range agentTargets() {
		if _, keep := selected[named.name]; !keep {
			continue
		}
		path, err := findAgentInstruction(scope, named.target)
		if err != nil {
			return nil, err
		}
		before, beforeExists, err := readOptionalFile(path)
		if err != nil {
			return nil, err
		}
		after, err := managedContent(before, named.target.header)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		edits = appendEdit(
			edits,
			newEdit(path, before, after, 0o644, beforeExists, false),
		)
	}
	return edits, nil
}

func planMCPConfig(
	scope string,
	preserveAnchor bool,
	options Options,
) (*plannedEdit, error) {
	path, err := findMCPConfig(scope)
	if err != nil {
		return nil, err
	}
	before, beforeExists, err := readOptionalFile(path)
	if err != nil {
		return nil, err
	}
	if options.WriteMCPConfig {
		after, mergeErr := mergeMCPConfig(before, options.RunnerModes)
		if mergeErr != nil {
			return nil, mergeErr
		}
		return newEdit(path, before, after, 0o644, beforeExists, false), nil
	}
	after, remove, err := removeMCPConfig(before)
	if err != nil {
		return nil, err
	}
	if remove && !preserveAnchor {
		preserveAnchor, err = hasHigherMCPConfig(scope)
		if err != nil {
			return nil, err
		}
	}
	if remove && preserveAnchor {
		after = []byte("{}" + documentLineBreak(before))
		remove = false
	}
	return newEdit(path, before, after, 0o644, beforeExists, remove), nil
}

func planCodexConfig(scope string, options Options) (*plannedEdit, error) {
	path, err := findCodexConfig(scope)
	if err != nil {
		return nil, err
	}
	before, beforeExists, err := readOptionalFile(path)
	if err != nil {
		return nil, err
	}
	if options.WriteMCPConfig {
		after, mergeErr := mergeCodexConfig(before, scope, options.RunnerModes)
		if mergeErr != nil {
			return nil, fmt.Errorf("merge %s: %w", path, mergeErr)
		}
		return newEdit(path, before, after, 0o600, beforeExists, false), nil
	}
	after, remove, err := removeCodexConfig(before)
	if err != nil {
		return nil, fmt.Errorf("clean %s: %w", path, err)
	}
	remove, err = preserveScopedFileSymlink(scope, codexConfig, remove)
	if err != nil {
		return nil, err
	}
	return newEdit(path, before, after, 0o600, beforeExists, remove), nil
}

// planClaudeSettings plans the Claude permission lists of a selected claude
// agent. Its caller decides whether the agent is selected at all.
func planClaudeSettings(scope string, options Options) (*plannedEdit, error) {
	path, err := findClaudeSettings(scope)
	if err != nil {
		return nil, err
	}
	before, beforeExists, err := readOptionalFile(path)
	if err != nil {
		return nil, err
	}
	cleaned, remove, err := removeManagedClaudeSettings(before)
	if err != nil {
		return nil, fmt.Errorf("clean %s: %w", path, err)
	}
	if options.ClaudePermissions == ClaudePermissionsNo {
		remove, err = preserveScopedFileSymlink(scope, claudeSettings, remove)
		if err != nil {
			return nil, err
		}
		return newEdit(path, before, cleaned, 0o600, beforeExists, remove), nil
	}
	after, err := mergeClaudeSettings(before)
	if err != nil {
		return nil, fmt.Errorf("merge %s: %w", path, err)
	}
	edit := newEdit(path, before, after, 0o600, beforeExists, false)
	if options.DryRun || options.ClaudePermissions == ClaudePermissionsYes {
		return edit, nil
	}
	confirmed, err := confirmClaudePermissions(
		path,
		simpleDiff(path, before, after, beforeExists),
		options,
	)
	if err != nil {
		return nil, err
	}
	if confirmed {
		return edit, nil
	}
	remove, err = preserveScopedFileSymlink(scope, claudeSettings, remove)
	if err != nil {
		return nil, err
	}
	return newEdit(path, before, cleaned, 0o600, beforeExists, remove), nil
}

func readOptionalFile(path string) ([]byte, bool, error) {
	// #nosec G304 -- callers pass a validated workspace-local managed path.
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("read %s: %w", path, err)
}

func newEdit(
	path string,
	before []byte,
	after []byte,
	mode os.FileMode,
	beforeExists bool,
	remove bool,
) *plannedEdit {
	if bytes.Equal(before, after) && !remove {
		return nil
	}
	return &plannedEdit{
		path:         path,
		before:       before,
		after:        after,
		mode:         mode,
		beforeExists: beforeExists,
		remove:       remove,
	}
}

func appendEdit(edits []plannedEdit, edit *plannedEdit) []plannedEdit {
	if edit == nil {
		return edits
	}
	return append(edits, *edit)
}

func resultForEdits(edits []plannedEdit) Result {
	result := Result{Paths: make([]string, 0, len(edits)), Diffs: make([]string, 0, len(edits))}
	for _, edit := range edits {
		result.Paths = append(result.Paths, edit.path)
		result.Diffs = append(
			result.Diffs,
			simpleDiffWithRemoval(
				edit.path,
				edit.before,
				edit.after,
				edit.beforeExists,
				edit.remove,
			),
		)
	}
	return result
}

func applyEdits(edits []plannedEdit) error {
	for _, edit := range edits {
		if edit.remove {
			if err := os.Remove(edit.path); err != nil {
				return fmt.Errorf("remove %s: %w", edit.path, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(edit.path), 0o750); err != nil {
			return fmt.Errorf("create directory for %s: %w", edit.path, err)
		}
		// #nosec G306,G703 -- every edit path was resolved and validated within
		// the current workspace scope before this write phase began.
		if err := os.WriteFile(edit.path, edit.after, edit.mode); err != nil {
			return fmt.Errorf("write %s: %w", edit.path, err)
		}
	}
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
// The bool reports that an ancestor config anchored a nested invocation, so
// cleanup can preserve that boundary even when the config is otherwise JMW-only.
// Without one, dir is a standalone project and owns all generated files.
func findScopeRoot(dir string) (string, bool, error) {
	for current := dir; ; current = filepath.Dir(current) {
		path := filepath.Join(current, mcpConfig)
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", false, fmt.Errorf("mcp config %s is not a regular file", path)
			}
			return current, current != dir, nil
		}
		if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("inspect %s: %w", path, err)
		}
		if filepath.Dir(current) == current {
			return dir, false, nil
		}
	}
}

// hasHigherMCPConfig reports whether removing the config at scope would expose
// another MCP config as the boundary of the next identical invocation.
func hasHigherMCPConfig(scope string) (bool, error) {
	current := filepath.Dir(scope)
	if current == scope {
		return false, nil
	}
	for {
		path := filepath.Join(current, mcpConfig)
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return false, fmt.Errorf("mcp config %s is not a regular file", path)
			}
			return true, nil
		}
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("inspect %s: %w", path, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, nil
		}
		current = parent
	}
}

// findAgentInstruction resolves one fixed workspace-local instruction target.
func findAgentInstruction(dir string, target target) (string, error) {
	if _, err := findScopedConfig(
		dir,
		scopedConfig{
			relative: target.path,
			name:     "agent instruction",
			lower:    "agent instruction",
		},
	); err != nil {
		return "", err
	}
	return filepath.Join(dir, target.path), nil
}

// findMCPConfig resolves the workspace-local .mcp.json only.
func findMCPConfig(dir string) (string, error) {
	path := filepath.Join(dir, mcpConfig)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("mcp config %s is not a regular file", path)
	}
	return path, nil
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

// preserveScopedFileSymlink converts a planned deletion into a truncation when
// the fixed lexical file itself is a symlink. A symlink in a parent directory
// does not match, so directory-symlink cleanup keeps its existing behavior.
func preserveScopedFileSymlink(
	scope string,
	relative string,
	remove bool,
) (bool, error) {
	if !remove {
		return false, nil
	}
	path := filepath.Join(scope, relative)
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("inspect scoped config %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	return true, nil
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
		resolvedDirectory, resolveErr := resolveWorkspaceScope(directory)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve %s directory: %w", config.name, resolveErr)
		}
		if scopeErr := validateWithinScope(resolvedScope, resolvedDirectory); scopeErr != nil {
			return "", fmt.Errorf("resolve %s directory: %w", config.name, scopeErr)
		}
		return resolvedDirectory, nil
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
	if err := validateWithinScope(scope, resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func validateWithinScope(scope string, resolved string) error {
	relative, err := filepath.Rel(scope, resolved)
	if err != nil {
		return fmt.Errorf("check %s against workspace scope: %w", resolved, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return fmt.Errorf("%s resolves outside workspace scope %s", resolved, scope)
	}
	return nil
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
func MCPConfigSnippet(selections runner.ValidatedSelections) (string, error) {
	data, err := mergeMCPConfig(nil, selections)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type target struct {
	path   string
	header string
}

type namedTarget struct {
	name   string
	target target
}

func agentTargets() []namedTarget {
	return []namedTarget{
		{name: "claude", target: target{path: "CLAUDE.md", header: "# Workspace instructions\n\n"}},
		{name: "codex", target: target{path: "AGENTS.md", header: "# Workspace instructions\n\n"}},
		{
			name: "cursor",
			target: target{
				path:   ".cursor/rules/just-mcp-work.mdc",
				header: "---\ndescription: Use workspace tasks through just-mcp-work\n---\n\n",
			},
		},
		{
			name:   "copilot",
			target: target{path: ".github/copilot-instructions.md", header: "# Copilot instructions\n\n"},
		},
		{
			name:   "windsurf",
			target: target{path: ".windsurfrules", header: "# Workspace instructions\n\n"},
		},
	}
}

func agentTarget(agent string) (target, bool) {
	for _, named := range agentTargets() {
		if named.name == agent {
			return named.target, true
		}
	}
	return target{}, false
}

func canonicalBlock() string {
	return beginMarker + "\n" + managedBlockText + "\n" + endMarker + "\n"
}

func managedContent(before []byte, header string) ([]byte, error) {
	text := string(before)
	// The instruction file keeps the line ending it is written with, so the
	// managed block does not turn a CRLF document into a mixed one.
	lineBreak := documentLineBreak(before)
	start, end, found, err := managedBlockRange(text)
	if err != nil {
		return nil, err
	}
	block := withLineBreak(canonicalBlock(), lineBreak)
	if found {
		prefix := normalizeTrailingLineBreak(text[:start], lineBreak)
		return []byte(prefix + block + text[end:]), nil
	}
	if text == "" {
		// A file created here has no ending of its own to keep, so the header
		// and the block go in with the bare newline they are written with.
		return []byte(header + block), nil
	}
	separator := lineBreak + lineBreak
	if strings.HasSuffix(text, "\n") {
		separator = lineBreak
	}
	return []byte(text + separator + block), nil
}

func managedBlockRange(text string) (int, int, bool, error) {
	beginCount := strings.Count(text, beginMarker)
	endCount := strings.Count(text, endMarker)
	if beginCount == 0 && endCount == 0 {
		return 0, 0, false, nil
	}
	if beginCount != 1 || endCount != 1 {
		return 0, 0, false, fmt.Errorf("managed block markers are malformed")
	}
	start := strings.Index(text, beginMarker)
	end := strings.Index(text, endMarker)
	if end < start {
		return 0, 0, false, fmt.Errorf("managed block markers are malformed")
	}
	end += len(endMarker)
	if suffix, found := trimLeadingLineBreak(text[end:]); found {
		end = len(text) - len(suffix)
	}
	return start, end, true, nil
}

// serverEntry is the managed MCP server definition of this executable. The
// field order defines the order of the generated JSON keys.
type serverEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func managedServerEntry(selections runner.ValidatedSelections) (serverEntry, error) {
	executable, err := os.Executable()
	if err != nil {
		return serverEntry{}, fmt.Errorf("resolve executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return serverEntry{}, fmt.Errorf("make executable path absolute: %w", err)
	}
	args, err := managedServerArgs(".", selections)
	if err != nil {
		return serverEntry{}, err
	}
	return serverEntry{Command: executable, Args: args}, nil
}

// mergeMCPConfig writes the managed server entry into the configuration text.
// Only that entry is rewritten: unrelated servers, key order, and the file's
// own formatting are left exactly as they were.
func mergeMCPConfig(before []byte, selections runner.ValidatedSelections) ([]byte, error) {
	entry, err := managedServerEntry(selections)
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

func removeMCPConfig(before []byte) ([]byte, bool, error) {
	if len(bytes.TrimSpace(before)) == 0 {
		return before, false, nil
	}
	root, err := decodeJSONObject(before, mcpConfig)
	if err != nil {
		return nil, false, err
	}
	members, err := jsonObjectMembers(before, root)
	if err != nil {
		return nil, false, fmt.Errorf("decode existing %s: %w", mcpConfig, err)
	}
	servers, found := jsonFindMember(members, "mcpServers")
	if !found || isJSONNull(before, servers.value) {
		return before, false, nil
	}
	if before[servers.value.start] != '{' {
		return nil, false, fmt.Errorf(
			"mcpServers in %s is not an object; init cannot identify its managed entry",
			mcpConfig,
		)
	}
	serverMembers, err := jsonObjectMembers(before, servers.value)
	if err != nil {
		return nil, false, fmt.Errorf("decode existing %s: %w", mcpConfig, err)
	}
	if _, found := jsonFindMember(serverMembers, serverName); !found {
		return before, false, nil
	}
	if len(members) == 1 && len(serverMembers) == 1 {
		return nil, true, nil
	}
	after, err := jsonRemoveMember(before, servers.value, serverName)
	if err != nil {
		return nil, false, fmt.Errorf("update %s: %w", mcpConfig, err)
	}
	return after, false, nil
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

func removeManagedClaudeSettings(before []byte) ([]byte, bool, error) {
	if len(bytes.TrimSpace(before)) == 0 {
		return before, false, nil
	}
	root, err := decodeJSONObject(before, claudeSettings)
	if err != nil {
		return nil, false, err
	}
	if validateErr := validateClaudePermissions(before); validateErr != nil {
		return nil, false, validateErr
	}
	members, err := jsonObjectMembers(before, root)
	if err != nil {
		return nil, false, fmt.Errorf("decode existing %s: %w", claudeSettings, err)
	}
	permissions, found := jsonFindMember(members, "permissions")
	if !found || isJSONNull(before, permissions.value) {
		return before, false, nil
	}
	if before[permissions.value.start] != '{' {
		return nil, false, fmt.Errorf(
			"permissions in %s is not an object; fix that entry and run init again",
			claudeSettings,
		)
	}
	toolOnly, err := claudeSettingsAreToolOnly(before, members, permissions.value)
	if err != nil {
		return nil, false, err
	}
	cleaned, err := dropManagedClaudeTools(before, nil)
	if err != nil {
		return nil, false, err
	}
	if bytes.Equal(before, cleaned) {
		return before, false, nil
	}
	if toolOnly {
		return nil, true, nil
	}
	return cleaned, false, nil
}

func claudeSettingsAreToolOnly(
	data []byte,
	rootMembers []jsonMember,
	permissions jsonSpan,
) (bool, error) {
	if len(rootMembers) != 1 || rootMembers[0].key != "permissions" {
		return false, nil
	}
	members, err := jsonObjectMembers(data, permissions)
	if err != nil {
		return false, fmt.Errorf("decode existing %s: %w", claudeSettings, err)
	}
	ownedKeys := make(map[string]struct{}, len(claudeOwnedLists()))
	for _, list := range claudeOwnedLists() {
		ownedKeys[list.key] = struct{}{}
	}
	foundManaged := false
	for _, member := range members {
		if _, owned := ownedKeys[member.key]; !owned {
			return false, nil
		}
		if data[member.value.start] != '[' {
			return false, nil
		}
		elements, elementsErr := jsonArrayElements(data, member.value)
		if elementsErr != nil {
			return false, fmt.Errorf("decode existing %s: %w", claudeSettings, elementsErr)
		}
		for _, element := range elements {
			var value any
			if decodeErr := json.Unmarshal(data[element.start:element.end], &value); decodeErr != nil {
				return false, fmt.Errorf("decode existing %s: %w", claudeSettings, decodeErr)
			}
			entry, isString := value.(string)
			if !isString || !isManagedClaudeTool(entry) {
				return false, nil
			}
			foundManaged = true
		}
	}
	return foundManaged, nil
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

func mergeCodexConfig(
	before []byte,
	root string,
	selections runner.ValidatedSelections,
) ([]byte, error) {
	if _, _, _, err := codexBlockRange(string(before)); err != nil {
		return nil, err
	}
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
	args, err := managedServerArgs(root, selections)
	if err != nil {
		return nil, err
	}
	argsValue, err := tomlStringArray(args)
	if err != nil {
		return nil, fmt.Errorf("encode server arguments: %w", err)
	}
	// The config keeps the line ending it is written with, the same way the
	// instruction files and the JSON configs do.
	lineBreak := documentLineBreak(before)
	block := strings.Join(
		[]string{
			codexBegin,
			codexTable,
			"command = " + executableValue,
			"args = " + argsValue,
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

func removeCodexConfig(before []byte) ([]byte, bool, error) {
	text := string(before)
	start, end, found, err := codexBlockRange(text)
	if err != nil {
		return before, false, err
	}
	if !found {
		if rejectErr := rejectUnmanagedCodexServer(text); rejectErr != nil {
			return nil, false, rejectErr
		}
		return before, false, nil
	}
	if rejectErr := rejectUnmanagedCodexServer(text[:start] + text[end:]); rejectErr != nil {
		return nil, false, rejectErr
	}
	if start == 0 && end == len(text) {
		return nil, true, nil
	}
	prefix := trimManagedSeparator(text[:start])
	suffix := text[end:]
	if prefix != "" && suffix != "" && !strings.HasSuffix(prefix, "\n") {
		prefix += documentLineBreak(before)
	}
	return []byte(prefix + suffix), false, nil
}

func codexBlockRange(text string) (int, int, bool, error) {
	beginCount := strings.Count(text, codexBegin)
	endCount := strings.Count(text, codexEnd)
	if beginCount == 0 && endCount == 0 {
		return 0, 0, false, nil
	}
	if beginCount != 1 || endCount != 1 {
		return 0, 0, false, fmt.Errorf("managed Codex MCP block markers are malformed")
	}
	start := strings.Index(text, codexBegin)
	end := strings.Index(text, codexEnd)
	if end < start {
		return 0, 0, false, fmt.Errorf("managed Codex MCP block markers are malformed")
	}
	end += len(codexEnd)
	if suffix, found := trimLeadingLineBreak(text[end:]); found {
		end = len(text) - len(suffix)
	}
	return start, end, true, nil
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

func tomlStringArray(values []string) (string, error) {
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		item, err := tomlString(value)
		if err != nil {
			return "", err
		}
		encoded = append(encoded, item)
	}
	return "[" + strings.Join(encoded, ", ") + "]", nil
}

func managedServerArgs(root string, selections runner.ValidatedSelections) ([]string, error) {
	modeArgs, err := selections.Args()
	if err != nil {
		return nil, fmt.Errorf("build runner mode arguments: %w", err)
	}
	args := []string{"serve", "--root", root}
	return append(args, modeArgs...), nil
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

func simpleDiff(path string, before, after []byte, beforeExists bool) string {
	return simpleDiffWithRemoval(path, before, after, beforeExists, false)
}

func simpleDiffWithRemoval(
	path string,
	before []byte,
	after []byte,
	beforeExists bool,
	remove bool,
) string {
	beforePath := path
	afterPath := path
	if !beforeExists {
		beforePath = "/dev/null"
	}
	if remove {
		afterPath = "/dev/null"
	}
	return "--- " +
		beforePath +
		"\n+++ " + afterPath +
		"\n-" + strings.ReplaceAll(string(before), "\n", "\n-") +
		"\n+" + strings.ReplaceAll(string(after), "\n", "\n+")
}
