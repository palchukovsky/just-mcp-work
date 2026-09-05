// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/palchukovsky/just-mcp-work/internal/policy"
	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

func testRunnerModes(t *testing.T) runner.ValidatedSelections {
	t.Helper()
	catalog, err := runner.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	selections, err := catalog.CanonicalSelections(nil)
	if err != nil {
		t.Fatal(err)
	}
	return selections
}

func testClaudeManagedTools(
	t *testing.T,
	shell ShellPermission,
) ClaudeToolPermissions {
	t.Helper()
	managed, err := ClaudeManagedTools(shell)
	if err != nil {
		t.Fatal(err)
	}
	return managed
}

func validatedTestRunnerModes(
	t *testing.T,
	overrides []runner.Selection,
) runner.ValidatedSelections {
	t.Helper()
	unusedFactory := func(runner.Mode) (runner.Runner, error) {
		return nil, errors.New("test factory must not be called")
	}
	catalog, err := runner.NewCatalog(
		runner.NewRegistration("just", runner.UnreviewedPermissions(), unusedFactory),
		runner.NewRegistration("go", runner.UnreviewedPermissions(), unusedFactory),
	)
	if err != nil {
		t.Fatal(err)
	}
	selections, err := catalog.CanonicalSelections(overrides)
	if err != nil {
		t.Fatal(err)
	}
	return selections
}

func TestApplyIsIdempotentAndPreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# Existing\n\nKeep this.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		RunnerModes:     testRunnerModes(t),
	}
	first, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		path,
		resolvedTestPath(t, filepath.Join(dir, manifestFile)),
		policy.Path(dir),
	}
	if !slices.Equal(first.Paths, wantPaths) {
		t.Fatalf("first result paths = %#v", first.Paths)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	afterFirst, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(afterFirst), "# Existing\n\nKeep this.") ||
		strings.Count(string(afterFirst), beginMarker) != 1 {
		t.Fatalf("unexpected managed file:\n%s", afterFirst)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("idempotent apply changed paths: %#v", second.Paths)
	}
}

// TestApplyCodexConfigRoundTripPreservesTerminatedForeignContent covers the
// managed block init appends to a foreign Codex config and later removes again:
// the document has to come back byte for byte with either line ending.
func TestApplyCodexConfigRoundTripPreservesTerminatedForeignContent(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		lineBreak string
	}{
		{name: "LF", lineBreak: "\n"},
		{name: "CRLF", lineBreak: "\r\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, codexConfig)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			before := []byte("# Existing" + testCase.lineBreak + testCase.lineBreak +
				`title = "keep"` + testCase.lineBreak)
			if err := os.WriteFile(path, before, 0o600); err != nil {
				t.Fatal(err)
			}
			modes := testRunnerModes(t)
			if _, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: true, RunnerModes: modes,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: false, RunnerModes: modes,
			}); err != nil {
				t.Fatal(err)
			}
			// #nosec G304 -- path is created in this test's temporary directory.
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(after, before) {
				t.Fatalf("Codex config round trip = %q, want %q", after, before)
			}
		})
	}
}

func TestApplyBetaTestSelectsTheManagedBlock(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		betaTest  bool
		wantCount int
	}{
		{name: "beta", betaTest: true, wantCount: 1},
		{name: "plain", wantCount: 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir,
				Agents:          []string{"codex"},
				BetaTest:        testCase.betaTest,
				RunnerModes:     testRunnerModes(t),
			}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
			if err != nil {
				t.Fatal(err)
			}
			if count := strings.Count(string(data), betaTestManagedBlockText); count != testCase.wantCount {
				t.Fatalf("beta paragraph count = %d, want %d", count, testCase.wantCount)
			}
		})
	}
}

func TestApplyBetaTestAndPlainModesAreIdempotent(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		betaTest bool
	}{
		{name: "beta", betaTest: true},
		{name: "plain"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			options := Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir,
				Agents:          []string{"claude", "codex"},
				BetaTest:        testCase.betaTest,
				RunnerModes:     testRunnerModes(t),
			}
			first, err := Apply(options)
			if err != nil {
				t.Fatal(err)
			}
			before := make(map[string][]byte, len(first.Paths))
			for _, path := range first.Paths {
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				before[path] = data
			}
			second, err := Apply(options)
			if err != nil {
				t.Fatal(err)
			}
			if len(second.Paths) != 0 {
				t.Fatalf("second apply changed paths: %#v", second.Paths)
			}
			for path, want := range before {
				got, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("second apply changed %s", path)
				}
			}
			if count := strings.Count(
				string(before[filepath.Join(dir, "AGENTS.md")]),
				betaTestManagedBlockText,
			); count > 1 {
				t.Fatalf("beta paragraph count = %d, want at most one", count)
			}
		})
	}
}

func TestApplySwitchesBetaTestBlockCleanly(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		initial    bool
		targetMode bool
	}{
		{name: "plain to beta", targetMode: true},
		{name: "beta to plain", initial: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			fresh := t.TempDir()
			initial := Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir,
				Agents:          []string{"codex"},
				BetaTest:        testCase.initial,
				RunnerModes:     testRunnerModes(t),
			}
			if _, err := Apply(initial); err != nil {
				t.Fatal(err)
			}
			initial.Dir = fresh
			initial.BetaTest = testCase.targetMode
			if _, err := Apply(initial); err != nil {
				t.Fatal(err)
			}
			initial.Dir = dir
			if _, err := Apply(initial); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(fresh, "AGENTS.md"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("switched block differs from fresh target block:\n%s", got)
			}
		})
	}
}

func TestApplyBetaTestPreservesOperatorTextOutsideManagedBlocks(t *testing.T) {
	dir := t.TempDir()
	targets := []struct {
		agent  string
		path   string
		header string
	}{
		{agent: "claude", path: "CLAUDE.md", header: "# Workspace instructions\n\n"},
		{agent: "codex", path: "AGENTS.md", header: "# Workspace instructions\n\n"},
		{
			agent:  "cursor",
			path:   ".cursor/rules/just-mcp-work.mdc",
			header: "---\ndescription: Use workspace tasks through just-mcp-work\n---\n\n",
		},
	}
	want := make(map[string][]byte, len(targets))
	for _, target := range targets {
		path := filepath.Join(dir, target.path)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		prefix := target.header + "Operator text before the managed block for " + target.agent + ".\n\n"
		suffix := "\nOperator text after the managed block for " + target.agent + ".\n"
		if err := os.WriteFile(path, []byte(prefix+canonicalBlock(false)+suffix), 0o600); err != nil {
			t.Fatal(err)
		}
		want[path] = []byte(prefix + canonicalBlock(true) + suffix)
	}
	if _, err := Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude", "codex", "cursor"},
		BetaTest:          true,
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsNo,
	}); err != nil {
		t.Fatal(err)
	}
	for path, expected := range want {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, expected) {
			t.Fatalf("beta apply changed operator text outside managed markers in %s\ngot:  %q\nwant: %q", path, data, expected)
		}
	}
}

func TestApplyBetaTestPreservesForeignConfigurations(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, mcpConfig),
		[]byte(`{"mcpServers":{"foreign":{"command":"keep"}}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexPath, []byte("# foreign Codex configuration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	claudePath := filepath.Join(dir, claudeSettings)
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudePath, []byte(`{"foreign":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude", "codex"},
		BetaTest:          true,
		WriteMCPConfig:    true,
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	}); err != nil {
		t.Fatal(err)
	}
	for path, foreign := range map[string]string{
		filepath.Join(dir, mcpConfig): `"foreign"`,
		codexPath:                     "# foreign Codex configuration",
		claudePath:                    `"foreign"`,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), foreign) {
			t.Fatalf("%s lost foreign content %q", path, foreign)
		}
	}
}

// TestApplyCodexConfigCleanupKeepsLegacyTextValid covers a foreign file that had
// no final line break before init appended its managed block. Both states are
// identical after the append, so cleanup leaves valid text with one final line
// break instead of guessing the missing one back.
func TestApplyCodexConfigCleanupKeepsLegacyTextValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`title = "keep"`), 0o600); err != nil {
		t.Fatal(err)
	}
	modes := testRunnerModes(t)
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: true, RunnerModes: modes,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: false, RunnerModes: modes,
	}); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "title = \"keep\"\n" {
		t.Fatalf("legacy Codex cleanup = %q, want valid terminated text", after)
	}
}

// TestApplyBroadToNarrowSelectionKeepsDeselectedManagedFiles pins the rule that
// an agent missing from the selection is left alone: the block an earlier run
// wrote stays in its instruction file, the Claude settings keep their managed
// entries, and neither surface appears in the result. The opt-out case carries
// its own weight: the selection gate sits in Apply, ahead of the permission
// check inside planClaudeSettings, so a reordering of the two would re-open
// cleanup on a deselected claude while the default case stayed green.
func TestApplyBroadToNarrowSelectionKeepsDeselectedManagedFiles(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		permissions ClaudePermissions
	}{
		{name: "default ask", permissions: ClaudePermissionsAsk},
		{name: "opted out", permissions: ClaudePermissionsNo},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			all := []string{"claude", "codex", "cursor", "copilot", "windsurf"}
			if _, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir, Agents: all, RunnerModes: testRunnerModes(t),
				ClaudePermissions: ClaudePermissionsYes,
			}); err != nil {
				t.Fatal(err)
			}
			settings := claudeSettingsPath(t, dir)
			// #nosec G304 -- settings is created in this test's temporary directory.
			settingsBefore, err := os.ReadFile(settings)
			if err != nil {
				t.Fatal(err)
			}
			result, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
				ClaudePermissions: testCase.permissions,
			})
			if err != nil {
				t.Fatal(err)
			}
			manifestPath := resolvedTestPath(t, filepath.Join(dir, manifestFile))
			if len(result.Paths) != 1 || result.Paths[0] != manifestPath {
				t.Fatalf("narrow selection paths = %#v, want only %s", result.Paths, manifestPath)
			}
			for _, named := range agentTargets() {
				path := filepath.Join(dir, named.target.path)
				// #nosec G304 -- path is created in this test's temporary directory.
				data, readErr := os.ReadFile(path)
				if readErr != nil || !strings.Contains(string(data), canonicalBlock(false)) {
					t.Fatalf("%s instructions = %q, %v", named.name, data, readErr)
				}
			}
			// #nosec G304 -- settings is created in this test's temporary directory.
			settingsAfter, err := os.ReadFile(settings)
			if err != nil || !slices.Equal(settingsAfter, settingsBefore) {
				t.Fatalf("deselected Claude settings changed: %q, %v", settingsAfter, err)
			}
		})
	}
}

func TestApplyModeChangeRejectsDeselectedManagedInstructions(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		initial    bool
		targetMode bool
	}{
		{name: "plain to beta", targetMode: true},
		{name: "beta to plain", initial: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			first, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir,
				Agents:          []string{"claude", "codex", "cursor"},
				BetaTest:        testCase.initial,
				RunnerModes:     testRunnerModes(t),
			})
			if err != nil {
				t.Fatal(err)
			}
			before := make(map[string][]byte, len(first.Paths))
			for _, path := range first.Paths {
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				before[path] = data
			}

			_, err = Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir,
				Agents:          []string{"codex"},
				BetaTest:        testCase.targetMode,
				RunnerModes:     testRunnerModes(t),
			})
			if err == nil {
				t.Fatal("narrow mode-changing Apply() error = nil")
			}
			for _, want := range []string{
				"--agents codex",
				"CLAUDE.md",
				".cursor/rules/just-mcp-work.mdc",
				"--agents claude,codex,cursor",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("mode-change refusal does not contain %q: %v", want, err)
				}
			}
			for path, want := range before {
				got, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("refused mode change modified %s", path)
				}
			}
		})
	}
}

// TestApplyKeepsAliasedInstructionOfDeselectedAgent covers two agent targets
// that resolve to one document, the way a workspace can symlink AGENTS.md and
// CLAUDE.md to a single shared contract. Selecting one of the two agents has to
// leave that document with its managed block: nothing may plan a removal for
// the other agent that would undo the selected agent's own write.
func TestApplyKeepsAliasedInstructionOfDeselectedAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	for _, selected := range []string{"claude", "codex"} {
		t.Run(selected, func(t *testing.T) {
			dir := t.TempDir()
			sharedDirectory := filepath.Join(dir, "tools", "agents")
			if err := os.MkdirAll(sharedDirectory, 0o750); err != nil {
				t.Fatal(err)
			}
			shared := filepath.Join(sharedDirectory, "AGENTS.md")
			foreign := "# Workspace contract\n\nKeep this line.\n"
			if err := os.WriteFile(shared, []byte(foreign), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
				if err := os.Symlink(
					filepath.Join("tools", "agents", "AGENTS.md"),
					filepath.Join(dir, name),
				); err != nil {
					t.Fatal(err)
				}
			}
			modes := testRunnerModes(t)
			if _, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir, Agents: []string{"claude", "codex"}, RunnerModes: modes,
				ClaudePermissions: ClaudePermissionsNo,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir, Agents: []string{selected}, RunnerModes: modes,
				ClaudePermissions: ClaudePermissionsNo,
			}); err != nil {
				t.Fatal(err)
			}
			// #nosec G304 -- shared is created in this test's temporary directory.
			after, err := os.ReadFile(shared)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(after), canonicalBlock(false)) {
				t.Fatalf("narrow selection destroyed the aliased managed block:\n%s", after)
			}
			if strings.Count(string(after), beginMarker) != 1 {
				t.Fatalf("aliased targets duplicated the managed block:\n%s", after)
			}
			if !strings.HasPrefix(string(after), foreign) {
				t.Fatalf("aliased targets lost the foreign contract:\n%s", after)
			}
			for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
				assertFileSymlink(t, filepath.Join(dir, name))
			}
		})
	}
}

func TestApplyWriteMCPConfigFalseRemovesManagedConfigs(t *testing.T) {
	dir := t.TempDir()
	modes := testRunnerModes(t)
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: true, RunnerModes: modes,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: false, RunnerModes: modes,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{mcpConfig, codexConfig} {
		path := filepath.Join(dir, relative)
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("stale config %s still exists: %v", path, statErr)
		}
		expected := path
		if relative == codexConfig {
			expected = resolvedTestPath(t, path)
		}
		if !containsPath(result.Paths, expected) {
			t.Fatalf("result paths = %#v, missing removed %s", result.Paths, path)
		}
	}
}

func TestApplyNestedCleanupPreservesMCPConfigScopeAnchor(t *testing.T) {
	workspace := t.TempDir()
	project := filepath.Join(workspace, "projects", "service")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, mcpConfig)
	managed, err := mergeMCPConfig(nil, ".")
	if err != nil {
		t.Fatal(err)
	}
	managed = []byte(strings.ReplaceAll(string(managed), "\n", "\r\n"))
	if writeErr := os.WriteFile(path, managed, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             project, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
	}
	first, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(first.Paths, path) {
		t.Fatalf("first result paths = %#v, want preserved anchor %s", first.Paths, path)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "{}\r\n" {
		t.Fatalf("preserved MCP anchor = %q, want empty CRLF object", after)
	}
	if _, statErr := os.Stat(filepath.Join(project, mcpConfig)); !os.IsNotExist(statErr) {
		t.Fatalf("nested MCP config unexpectedly exists: %v", statErr)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("second nested cleanup changed paths: %#v", second.Paths)
	}
}

func TestApplyLocalCleanupPreservesScopeWhenHigherMCPConfigExists(t *testing.T) {
	ancestor := t.TempDir()
	scope := filepath.Join(ancestor, "workspace")
	if err := os.Mkdir(scope, 0o750); err != nil {
		t.Fatal(err)
	}
	ancestorMCPPath := filepath.Join(ancestor, mcpConfig)
	ancestorMCPBefore := []byte(`{"mcpServers":{"other":{"command":"keep"}}}` + "\n")
	if err := os.WriteFile(ancestorMCPPath, ancestorMCPBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	ancestorAgentPath := filepath.Join(ancestor, "AGENTS.md")
	ancestorAgentBefore := []byte("# Ancestor instructions\n")
	if err := os.WriteFile(ancestorAgentPath, ancestorAgentBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	localMCPPath := filepath.Join(scope, mcpConfig)
	localMCPBefore, err := mergeMCPConfig(nil, ".")
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(localMCPPath, localMCPBefore, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             scope, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
	}
	first, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(first.Paths, localMCPPath) {
		t.Fatalf("first result paths = %#v, want local anchor %s", first.Paths, localMCPPath)
	}
	// #nosec G304 -- paths are created in this test's temporary directory.
	localMCPAfter, err := os.ReadFile(localMCPPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(localMCPAfter) != "{}\n" {
		t.Fatalf("local MCP anchor = %q, want empty object", localMCPAfter)
	}
	for path, want := range map[string][]byte{
		ancestorMCPPath:   ancestorMCPBefore,
		ancestorAgentPath: ancestorAgentBefore,
	} {
		// #nosec G304 -- paths are created in this test's temporary directory.
		got, readErr := os.ReadFile(path)
		if readErr != nil || !slices.Equal(got, want) {
			t.Fatalf("ancestor surface %s changed: %q, %v", path, got, readErr)
		}
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("second local cleanup changed paths: %#v", second.Paths)
	}
}

func TestApplyRejectsNonRegularHigherMCPConfigBeforeLocalCleanup(t *testing.T) {
	ancestor := t.TempDir()
	scope := filepath.Join(ancestor, "workspace")
	if err := os.Mkdir(scope, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(ancestor, mcpConfig), 0o750); err != nil {
		t.Fatal(err)
	}
	localMCPPath := filepath.Join(scope, mcpConfig)
	localMCPBefore, err := mergeMCPConfig(nil, ".")
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(localMCPPath, localMCPBefore, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	_, err = Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             scope, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
	})
	if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("Apply error = %v, want non-regular higher MCP config error", err)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	localMCPAfter, readErr := os.ReadFile(localMCPPath)
	if readErr != nil || !slices.Equal(localMCPAfter, localMCPBefore) {
		t.Fatalf("local MCP config changed before rejection: %q, %v", localMCPAfter, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(scope, "AGENTS.md")); !os.IsNotExist(statErr) {
		t.Fatalf("agent instructions changed before rejection: %v", statErr)
	}
}

func TestApplyCleanupPreservesForeignConfigContent(t *testing.T) {
	dir := t.TempDir()
	modes := testRunnerModes(t)
	mcpPath := filepath.Join(dir, mcpConfig)
	mcpBefore := "{\n  \"foreign\": {\"keep\": true},\n  \"mcpServers\": {\n" +
		"    \"other\": {\"command\": \"keep\"}\n  }\n}\n"
	if err := os.WriteFile(mcpPath, []byte(mcpBefore), 0o600); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexPath, []byte("# keep exactly\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: true, RunnerModes: modes,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: false, RunnerModes: modes,
	}); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- paths are created in this test's temporary directory.
	mcpAfter, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []string{`"foreign": {"keep": true}`, `"other": {"command": "keep"}`} {
		if !strings.Contains(string(mcpAfter), foreign) {
			t.Fatalf("MCP cleanup lost %q:\n%s", foreign, mcpAfter)
		}
	}
	// #nosec G304 -- paths are created in this test's temporary directory.
	codexAfter, err := os.ReadFile(codexPath)
	if err != nil || string(codexAfter) != "# keep exactly\n" {
		t.Fatalf("Codex cleanup changed foreign bytes: %q, %v", codexAfter, err)
	}
}

func TestApplyClaudePermissionNoAndDeclineRemoveManagedPermissions(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		confirm     func(ShellPermission, string, string) (bool, error)
		permissions ClaudePermissions
		shell       ShellPermission
	}{
		{
			name:        "no after changing shell choice",
			permissions: ClaudePermissionsNo,
			shell:       ShellPermissionAsk,
		},
		{
			name:        "declined after changing shell choice",
			permissions: ClaudePermissionsAsk,
			shell:       ShellPermissionAsk,
			confirm: func(ShellPermission, string, string) (bool, error) {
				return false, nil
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			modes := testRunnerModes(t)
			if _, err := Apply(Options{
				ShellPermission: ShellPermissionAllow,
				Dir:             dir, Agents: []string{"claude"}, RunnerModes: modes,
				WriteMCPConfig:    true,
				ClaudePermissions: ClaudePermissionsYes,
			}); err != nil {
				t.Fatal(err)
			}
			result, err := Apply(Options{
				ShellPermission:   testCase.shell,
				Dir:               dir,
				Agents:            []string{"claude"},
				WriteMCPConfig:    true,
				RunnerModes:       modes,
				ClaudePermissions: testCase.permissions,
				Confirm:           testCase.confirm,
			})
			if err != nil {
				t.Fatal(err)
			}
			path := claudeSettingsPath(t, dir)
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("managed Claude settings still exist: %v", statErr)
			}
			if !containsPath(result.Paths, path) {
				t.Fatalf("result paths = %#v, missing %s", result.Paths, path)
			}
		})
	}
}

func TestApplyClaudePermissionCleanupSweepsEveryListAndKeepsForeignEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, claudeSettings)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	settings := `{
  "foreign": true,
  "permissions": {
    "allow": ["foreign-allow", "mcp__just-mcp-work"],
    "ask": ["mcp__just-mcp-work__retired"],
    "deny": ["foreign-deny", "mcp__just-mcp-work__old"]
  }
}
`
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude"},
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsNo,
	}); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "mcp__just-mcp-work") {
		t.Fatalf("managed Claude permission survived cleanup:\n%s", text)
	}
	for _, foreign := range []string{"foreign-allow", "foreign-deny", `"foreign": true`} {
		if !strings.Contains(text, foreign) {
			t.Fatalf("foreign Claude setting %q was lost:\n%s", foreign, text)
		}
	}
}

func TestApplyClaudeCleanupKeepsForeignEmptyPermissionKeys(t *testing.T) {
	for _, foreignKey := range []string{"deny", "custom"} {
		t.Run(foreignKey, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, claudeSettings)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			before := `{"permissions":{"allow":["mcp__just-mcp-work"],"` +
				foreignKey + `":[]}}` + "\n"
			if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(Options{
				ShellPermission:   ShellPermissionAsk,
				Dir:               dir,
				Agents:            []string{"claude"},
				RunnerModes:       testRunnerModes(t),
				ClaudePermissions: ClaudePermissionsNo,
			}); err != nil {
				t.Fatal(err)
			}
			// #nosec G304 -- path is created in this test's temporary directory.
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := `{"permissions":{"allow":[],"` + foreignKey + `":[]}}` + "\n"
			if string(after) != want {
				t.Fatalf("Claude cleanup = %q, want %q", after, want)
			}
		})
	}
}

func TestRemoveCodexConfigPreservesMiddleForeignBoundary(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		lineBreak string
	}{
		{name: "LF", lineBreak: "\n"},
		{name: "CRLF", lineBreak: "\r\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			block := strings.Join(
				[]string{
					codexBegin,
					codexTable,
					`command = "just-mcp-work"`,
					codexEnd,
				},
				testCase.lineBreak,
			)
			before := `title = "keep"` + testCase.lineBreak + block +
				testCase.lineBreak + `[other]` + testCase.lineBreak +
				`enabled = true` + testCase.lineBreak
			want := `title = "keep"` + testCase.lineBreak + `[other]` +
				testCase.lineBreak + `enabled = true` + testCase.lineBreak
			after, remove, err := removeCodexConfig([]byte(before))
			if err != nil {
				t.Fatal(err)
			}
			if remove || string(after) != want {
				t.Fatalf("Codex cleanup = %q, remove %t, want %q", after, remove, want)
			}
			var parsed struct {
				Title string
				Other struct {
					Enabled bool
				}
			}
			if _, err := toml.Decode(string(after), &parsed); err != nil {
				t.Fatalf("cleaned Codex config is invalid TOML: %v\n%s", err, after)
			}
			if parsed.Title != "keep" || !parsed.Other.Enabled {
				t.Fatalf("cleaned foreign Codex values = %#v", parsed)
			}
		})
	}
}

func TestApplyDryRunPlansCleanupWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	modes := testRunnerModes(t)
	if _, err := Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude", "codex", "cursor", "copilot", "windsurf"},
		WriteMCPConfig:    true,
		RunnerModes:       modes,
		ClaudePermissions: ClaudePermissionsYes,
	}); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(dir, "CLAUDE.md"),
		filepath.Join(dir, "AGENTS.md"),
		filepath.Join(dir, ".cursor/rules/just-mcp-work.mdc"),
		filepath.Join(dir, ".github/copilot-instructions.md"),
		filepath.Join(dir, ".windsurfrules"),
		filepath.Join(dir, mcpConfig),
		filepath.Join(dir, codexConfig),
		filepath.Join(dir, claudeSettings),
		policy.Path(dir),
		resolvedTestPath(t, filepath.Join(dir, manifestFile)),
	}
	before := make(map[string][]byte, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = data
	}
	confirmed := false
	result, err := Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude", "codex"},
		DryRun:            true,
		WriteMCPConfig:    false,
		RunnerModes:       modes,
		ClaudePermissions: ClaudePermissionsAsk,
		Confirm: func(ShellPermission, string, string) (bool, error) {
			confirmed = true
			return true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed {
		t.Fatal("dry run asked for confirmation")
	}
	if !strings.Contains(strings.Join(result.Diffs, "\n"), "+++ /dev/null") {
		t.Fatalf("dry-run diffs do not report deletions: %#v", result.Diffs)
	}
	for path, want := range before {
		data, readErr := os.ReadFile(path)
		if readErr != nil || !slices.Equal(data, want) {
			t.Fatalf("dry run changed %s: %v", path, readErr)
		}
	}
}

func TestApplyDryRunDistinguishesEmptyExistingFromMissingAgentFile(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		exists bool
	}{
		{name: "existing empty", exists: true},
		{name: "missing"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "AGENTS.md")
			if testCase.exists {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir, Agents: []string{"codex"}, DryRun: true,
				RunnerModes: testRunnerModes(t),
			})
			if err != nil {
				t.Fatal(err)
			}
			diff := resultDiffForPath(t, result, path)
			beforePath := "/dev/null"
			if testCase.exists {
				beforePath = path
			}
			wantPrefix := "--- " + beforePath + "\n+++ " + path + "\n"
			if !strings.HasPrefix(diff, wantPrefix) {
				t.Fatalf("agent diff = %q, want prefix %q", diff, wantPrefix)
			}
			info, statErr := os.Stat(path)
			if testCase.exists {
				if statErr != nil || info.Size() != 0 {
					t.Fatalf("dry run changed existing empty file: %v, %#v", statErr, info)
				}
			} else if !os.IsNotExist(statErr) {
				t.Fatalf("dry run created missing file: %v", statErr)
			}
		})
	}
}

func TestApplyPreflightsEverySurfaceBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "AGENTS.md")
	want := []byte("# Existing\n")
	if err := os.WriteFile(agentPath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(dir, claudeSettings)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"claude", "codex"}, RunnerModes: testRunnerModes(t),
	})
	if err == nil || !strings.Contains(err.Error(), "decode existing .claude/settings.json") {
		t.Fatalf("Apply error = %v, want malformed later target", err)
	}
	data, readErr := os.ReadFile(agentPath)
	if readErr != nil || !slices.Equal(data, want) {
		t.Fatalf("agent file changed before preflight completed: %q, %v", data, readErr)
	}
	if _, statErr := os.Stat(policy.Path(dir)); !os.IsNotExist(statErr) {
		t.Fatalf("policy was written before preflight completed: %v", statErr)
	}
}

func TestApplyReportsPolicyAfterManagedConfigurations(t *testing.T) {
	dir := t.TempDir()
	result, err := Apply(
		Options{
			ShellPermission:   ShellPermissionAsk,
			Dir:               dir,
			Agents:            []string{"claude"},
			WriteMCPConfig:    true,
			RunnerModes:       testRunnerModes(t),
			ClaudePermissions: ClaudePermissionsYes,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(dir, "CLAUDE.md"),
		filepath.Join(dir, mcpConfig),
		resolvedTestPath(t, filepath.Join(dir, codexConfig)),
		resolvedTestPath(t, filepath.Join(dir, claudeSettings)),
		resolvedTestPath(t, filepath.Join(dir, manifestFile)),
		policy.Path(dir),
	}
	if !slices.Equal(result.Paths, want) {
		t.Fatalf("Apply() paths = %#v, want %#v", result.Paths, want)
	}
}

func TestApplyWriteFailureLeavesPreviousPolicyUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file write permissions are not enforced consistently on Windows")
	}
	dir := t.TempDir()
	previous := validatedTestRunnerModes(
		t,
		[]runner.Selection{
			{Name: "just", Mode: runner.ModeAll},
			{Name: "go", Mode: runner.ModeDisabled},
		},
	)
	if err := policy.Save(dir, previous); err != nil {
		t.Fatal(err)
	}
	policyPath := policy.Path(dir)
	before, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(dir, "AGENTS.md")
	if writeErr := os.WriteFile(agentPath, []byte("# Existing\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if chmodErr := os.Chmod(agentPath, 0o444); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	t.Cleanup(func() {
		if chmodErr := os.Chmod(agentPath, 0o600); chmodErr != nil {
			t.Errorf("restore agent instruction permissions: %v", chmodErr)
		}
	})
	widened := validatedTestRunnerModes(
		t,
		[]runner.Selection{
			{Name: "just", Mode: runner.ModeAll},
			{Name: "go", Mode: runner.ModeAll},
		},
	)
	_, err = Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		RunnerModes:     widened,
	})
	if err == nil || !strings.Contains(err.Error(), agentPath) {
		t.Fatalf("Apply() error = %v, want write failure for %s", err, agentPath)
	}
	after, readErr := os.ReadFile(policyPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !slices.Equal(after, before) {
		t.Fatalf("policy changed after an earlier write failed: before %q, after %q", before, after)
	}
}

func TestApplyPlanningFailureLeavesWorkspaceUntouched(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".claude"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(
		Options{
			ShellPermission:   ShellPermissionAsk,
			Dir:               dir,
			Agents:            []string{"claude"},
			WriteMCPConfig:    true,
			RunnerModes:       testRunnerModes(t),
			ClaudePermissions: ClaudePermissionsYes,
		},
	)
	if err == nil {
		t.Fatal("Apply() error = nil, want Claude settings planning failure")
	}
	if _, statErr := os.Stat(policy.Path(dir)); !os.IsNotExist(statErr) {
		t.Fatalf("policy was written before planning completed: %v", statErr)
	}
}

func TestApplyReplacesMalformedPolicy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(policy.Path(dir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(
		Options{
			ShellPermission: ShellPermissionAsk,
			Dir:             dir,
			Agents:          []string{"codex"},
			RunnerModes:     testRunnerModes(t),
		},
	); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	loaded, err := policy.Load(dir)
	if err != nil {
		t.Fatalf("Load() repaired policy error = %v", err)
	}
	if !loaded.Found {
		t.Fatal("Load() repaired policy Found = false, want true")
	}
}

func writeAgentInitWorktreeMarkers(
	t *testing.T,
	mainDir string,
	worktreeDir string,
	name string,
) {
	t.Helper()
	entryDir := filepath.Join(mainDir, ".git", "worktrees", name)
	for path, contents := range map[string]string{
		filepath.Join(entryDir, "gitdir"):  filepath.Join(worktreeDir, ".git") + "\n",
		filepath.Join(worktreeDir, ".git"): "gitdir: " + entryDir + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func resolvedTestPath(t *testing.T, path string) string {
	t.Helper()
	resolvedDirectory, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(resolvedDirectory, filepath.Base(path))
}

// TestPromptDescribesTheTokenSavingContract keeps the two halves of the contract
// in the served instructions: when a compact receipt replaces the output, and
// when output too large for a tail is the reason not to use this server at all.
func TestPromptDescribesTheTokenSavingContract(t *testing.T) {
	const qualifiedRoutingRule = "Route work through it whenever a receipt or a tail answers the " +
		"question, and run directly only when the output you need is too large for a tail."
	const unqualifiedRoutingRule = "Route work through it when the full output is not what you " +
		"need, and run the command directly when it is."
	flat := strings.Join(strings.Fields(Prompt(false)), " ")
	if !strings.Contains(flat, qualifiedRoutingRule) {
		t.Errorf("Prompt does not state the qualified routing rule: %s", flat)
	}
	if strings.Contains(flat, unqualifiedRoutingRule) {
		t.Error("Prompt retains the unqualified routing rule")
	}
	for _, expected := range []string{
		"just-mcp-work (JMW)",
		"save tokens",
		"USE JMW WHEN",
		"RUN IT DIRECTLY WHEN",
		"too large for a tail",
		"delegate",
		"run_shell_command",
		"define_shell_block",
		"block_id",
		"working_directory",
		"run_task",
		"status: running",
		"start_task",
		"wait_run",
		"get_run_status",
		"ok: true",
		"exit code 0",
		"stdout_tail",
		"stderr_tail",
		"get_run_logs",
		"tail_bytes: 0",
		"Omitted tail_bytes means 4096 bytes on status tools",
		"leaves run_task and run_shell_command receipts unchanged",
		"first 160 runes",
		"first description line",
		"names, name_prefix, and query are mutually exclusive",
		"limit defaults to 50 and has a maximum of 200",
		"when truncated is true, continue with next_cursor and unchanged inputs",
		"withheld it through a runner mode",
		"Never recreate or run such a task",
		"genuinely ad-hoc commands",
	} {
		if !strings.Contains(flat, expected) {
			t.Errorf("Prompt does not mention %q", expected)
		}
	}
}

// TestManagedBlockCarriesTheSameContract keeps the block written into AGENTS.md
// and CLAUDE.md on the same rule as the served prompt, without repeating it in
// full: the block is read by agents that may not have the server attached yet.
func TestManagedBlockCarriesTheSameContract(t *testing.T) {
	flat := strings.Join(strings.Fields(managedBlockText), " ")
	for _, expected := range []string{
		"list_tasks -> run_task/start_task",
		"receipt or short tail is enough",
		"tail_bytes on run_task or run_shell_command",
		"too large for a tail",
		"sub-agents",
		"withheld it through a runner mode",
		"another shell path",
	} {
		if !strings.Contains(flat, expected) {
			t.Errorf("managed block does not mention %q: %s", expected, flat)
		}
	}
}

func TestBetaTestManagedBlockCarriesTheFeedbackContract(t *testing.T) {
	flat := strings.Join(strings.Fields(betaTestManagedBlockText), " ")
	for _, expected := range []string{
		"JMW bug, friction, missing capability, or improvement",
		"relevant tool call, command, or error",
		"separate from your findings about the project",
		"Tell the user and stop there",
		"do not open issues and do not send the report anywhere",
	} {
		if !strings.Contains(flat, expected) {
			t.Errorf("beta test managed block does not mention %q: %s", expected, flat)
		}
	}
}

// TestPromptAndManagedBlockShareTheContract holds the served instructions and
// the written block to one list of terms. The two texts are worded for
// different readers, so nothing but a shared check keeps them from drifting
// into two different rules.
func TestPromptAndManagedBlockShareTheContract(t *testing.T) {
	shared := []string{
		serverName + " (JMW)",
		"save tokens",
		"list_tasks",
		"run_task",
		"start_task",
		"tail_bytes",
		"too large for a tail",
	}
	for name, text := range map[string]string{
		"plain prompt":  strings.Join(strings.Fields(Prompt(false)), " "),
		"beta prompt":   strings.Join(strings.Fields(Prompt(true)), " "),
		"managed block": strings.Join(strings.Fields(managedBlockText), " "),
	} {
		for _, expected := range shared {
			if !strings.Contains(text, expected) {
				t.Errorf("%s does not carry the shared term %q", name, expected)
			}
		}
	}
}

func TestPromptSelectsBetaTestContract(t *testing.T) {
	plain := Prompt(false)
	if plain != promptText {
		t.Fatalf("plain prompt changed: got %q, want promptText", plain)
	}

	beta := Prompt(true)
	if wantPrefix := promptText + "\n\nBETA TEST FEEDBACK\n"; !strings.HasPrefix(beta, wantPrefix) {
		t.Fatalf("beta prompt does not start with promptText and its presentation header: %q", beta)
	}
	if servedCount, managedCount := strings.Count(beta, betaTestManagedBlockText),
		strings.Count(canonicalBlock(true), betaTestManagedBlockText); servedCount != 1 || managedCount != 1 {
		t.Fatalf(
			"beta contract counts = served %d, managed %d, want one in both channels",
			servedCount,
			managedCount,
		)
	}
	if strings.Contains(plain, betaTestManagedBlockText) {
		t.Fatal("plain prompt contains the beta-test paragraph")
	}
}

// TestManagedMarkersCarryTheServerName pins the generated markers and the
// permission prefix to serverName, so a rename cannot reach only some of them
// and orphan the blocks a previous init wrote.
func TestManagedMarkersCarryTheServerName(t *testing.T) {
	for name, text := range map[string]string{
		"beginMarker":      beginMarker,
		"endMarker":        endMarker,
		"codexBegin":       codexBegin,
		"codexEnd":         codexEnd,
		"codexTable":       codexTable,
		"claudeServerRule": claudeServerRule,
	} {
		if !strings.Contains(text, serverName) {
			t.Errorf("%s = %q does not carry the server name %q", name, text, serverName)
		}
	}
}

func TestApplyReplacesModifiedManagedBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	modified := "# Existing\n\n" + beginMarker + "\nuser edit\n" + endMarker + "\n"
	if err := os.WriteFile(path, []byte(modified), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"claude"},
		RunnerModes:     testRunnerModes(t),
	}
	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "user edit") ||
		strings.Count(string(data), canonicalBlock(false)) != 1 {
		t.Fatalf("managed block was not replaced:\n%s", data)
	}
}

func TestApplyUpdatesEarlierManagedPrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	previous := beginMarker + "\nold generated wording\n" + endMarker + "\n"
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"claude"}, RunnerModes: testRunnerModes(t),
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Paths) != 3 || !containsPath(result.Paths, policy.Path(dir)) ||
		!containsPath(result.Paths, resolvedTestPath(t, filepath.Join(dir, manifestFile))) {
		t.Fatalf("updated paths = %#v", result.Paths)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != canonicalBlock(false) {
		t.Fatalf("managed prompt was not upgraded:\n%s", data)
	}
}

func TestApplyMergesMCPConfigWithoutClobberingOtherServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	before := `{"project":"value","mcpServers":{"other":{"command":"other","args":["serve"]}}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if decodeErr := json.Unmarshal(data, &config); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if config["project"] != "value" {
		t.Fatalf("top-level config was clobbered: %#v", config)
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok || servers["other"] == nil || servers["just-mcp-work"] == nil {
		t.Fatalf("merged servers = %#v", config["mcpServers"])
	}
	assertServerCommand(t, servers)
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("idempotent MCP merge changed paths: %#v", second.Paths)
	}
}

func TestApplyPersistsRunnerPolicyAndKeepsServerArgsMinimal(t *testing.T) {
	dir := t.TempDir()
	selections := []runner.Selection{
		{Name: "just", Mode: runner.ModeAll},
		{Name: "go", Mode: runner.ModeDisabled},
	}
	validated := validatedTestRunnerModes(t, selections)
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     validated,
	}
	result, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, policy.Path(dir)) {
		t.Fatalf("apply paths = %#v, want %s", result.Paths, policy.Path(dir))
	}
	loaded, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantSelections, err := validated.Selections()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Found || !slices.Equal(loaded.Selections, wantSelections) {
		t.Fatalf("saved policy = %+v, want selections %#v", loaded, wantSelections)
	}
	assertManagedServerArgs(
		t,
		readJSONServerArgs(t, filepath.Join(dir, mcpConfig)),
		dir,
	)
	assertManagedServerArgs(
		t,
		readCodexServerArgs(t, filepath.Join(dir, codexConfig)),
		dir,
	)

	snippet, err := MCPConfigSnippet(dir)
	if err != nil {
		t.Fatal(err)
	}
	var snippetConfig struct {
		MCPServers map[string]serverEntry `json:"mcpServers"`
	}
	if decodeErr := json.Unmarshal([]byte(snippet), &snippetConfig); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	assertManagedServerArgs(t, snippetConfig.MCPServers[serverName].Args, dir)

	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("idempotent runner policy apply changed paths: %#v", second.Paths)
	}
}

func TestApplyRejectsUnvalidatedRunnerModesBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	_, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: true,
	})
	if err == nil || !strings.Contains(err.Error(), "not validated by a catalog") {
		t.Fatalf("Apply error = %v, want unvalidated runner selection rejection", err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("unvalidated runner modes wrote files: %#v", entries)
	}
}

// TestApplyKeepsForeignMCPConfigFormatting pins the promise that init only
// rewrites its own entry: key order, indentation width, and the exact text of
// every other value stay as the operator wrote them.
func TestApplyKeepsForeignMCPConfigFormatting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, mcpConfig)
	before := strings.Join(
		[]string{
			"{",
			`    "zeta": 1,`,
			`    "mcpServers": {`,
			`        "other": { "command": "other", "args": ["serve"] }`,
			"    },",
			`    "alpha": [1, 2]`,
			"}",
			"",
		},
		"\n",
	)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"{\n    \"zeta\": 1,\n",
		`        "other": { "command": "other", "args": ["serve"] },`,
		"\n    \"alpha\": [1, 2]\n}\n",
		"\n        \"just-mcp-work\": {\n            \"command\": ",
	} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("merged config lost %q:\n%s", expected, data)
		}
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if containsPath(second.Paths, path) {
		t.Fatalf("second apply rewrote the config:\n%s", data)
	}
}

// TestApplyKeepsForeignClaudeSettingsFormatting checks the same promise for the
// Claude settings, where init also has to delete its retired entries from lists
// that belong to somebody else.
func TestApplyKeepsForeignClaudeSettingsFormatting(t *testing.T) {
	dir := t.TempDir()
	path := claudeSettingsPath(t, dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	before := strings.Join(
		[]string{
			"{",
			`    "model": "opus",`,
			`    "permissions": {`,
			`        "allow": [`,
			`            "mcp__just-mcp-work__retired_tool",`,
			`            "Bash(git status:*)"`,
			"        ],",
			`        "deny": ["Bash(rm:*)"]`,
			"    },",
			`    "env": { "JMW": "1" }`,
			"}",
			"",
		},
		"\n",
	)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"claude"}, RunnerModes: testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"{\n    \"model\": \"opus\",\n",
		"\n            \"Bash(git status:*)\",\n            \"mcp__just-mcp-work__run_task\",",
		`        "deny": ["Bash(rm:*)"],`,
		"\n    \"env\": { \"JMW\": \"1\" }\n}\n",
	} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("merged settings lost %q:\n%s", expected, data)
		}
	}
	if strings.Contains(string(data), "retired_tool") {
		t.Fatalf("retired entry survived:\n%s", data)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if containsPath(second.Paths, path) {
		t.Fatalf("second apply rewrote the settings:\n%s", data)
	}
}

// TestApplyKeepsCRLFLineEndings checks that every file init edits keeps the
// line ending it is written with, so a workspace checked out with CRLF does not
// end up with two endings mixed in one file.
func TestApplyKeepsCRLFLineEndings(t *testing.T) {
	dir := t.TempDir()
	settings := claudeSettingsPath(t, dir)
	files := map[string]string{
		filepath.Join(dir, "CLAUDE.md"): "# Notes\r\n",
		filepath.Join(dir, "AGENTS.md"): "# Notes\r\n",
		filepath.Join(dir, codexConfig): "# notes\r\n",
		filepath.Join(dir, mcpConfig):   " \r\n",
		settings:                        "{\r\n  \"permissions\": {\r\n    \"allow\": [\r\n    ]\r\n  }\r\n}\r\n",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	options := Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude", "codex"},
		WriteMCPConfig:    true,
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	}
	first, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Paths) != len(files)+2 || !containsPath(first.Paths, policy.Path(dir)) ||
		!containsPath(first.Paths, resolvedTestPath(t, filepath.Join(dir, manifestFile))) {
		t.Fatalf("apply changed %v, want all files, policy, and manifest", first.Paths)
	}
	for path := range files {
		// #nosec G304 -- path is created in this test's temporary directory.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		text := string(data)
		if strings.Count(text, "\n") != strings.Count(text, "\r\n") {
			t.Fatalf("%s mixes line endings:\n%q", path, text)
		}
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("second apply rewrote %v", second.Paths)
	}
}

// TestApplyRepairsLegacyLFBlocksInCRLFDocuments checks the upgrade path from
// init versions that always wrote their managed blocks with LF. The JSON
// config merged in the same run keeps the ending of its existing content.
func TestApplyRepairsLegacyLFBlocksInCRLFDocuments(t *testing.T) {
	dir := t.TempDir()
	agentsPath := filepath.Join(dir, "AGENTS.md")
	codexPath := filepath.Join(dir, codexConfig)
	mcpPath := filepath.Join(dir, mcpConfig)
	legacyCodexBlock := strings.Join(
		[]string{
			codexBegin,
			codexTable,
			`command = "stale"`,
			codexEnd,
		},
		"\n",
	)
	files := map[string]string{
		agentsPath: "# Notes\r\n\n" + canonicalBlock(false),
		codexPath:  "# notes\r\n\n" + legacyCodexBlock + "\n",
		mcpPath:    "{\r\n  \"mcpServers\": {}\r\n}\r\n",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	wantPrefixes := map[string]string{
		agentsPath: "# Notes\r\n\r\n" + beginMarker + "\r\n",
		codexPath:  "# notes\r\n\r\n" + codexBegin + "\r\n",
	}
	for path := range files {
		// #nosec G304 -- path is created in this test's temporary directory.
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Count(text, "\n") != strings.Count(text, "\r\n") {
			t.Fatalf("%s still mixes line endings:\n%q", path, text)
		}
		wantPrefix, checked := wantPrefixes[path]
		if checked && !strings.HasPrefix(text, wantPrefix) {
			t.Fatalf("%s kept the legacy block boundary:\n%q", path, text)
		}
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("second apply rewrote %v", second.Paths)
	}
}

// TestApplyKeepsCodexBlockInPlace checks that a managed block an operator moved
// is refreshed where it stands instead of being appended again at the end.
func TestApplyKeepsCodexBlockInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	before := strings.Join(
		[]string{
			codexBegin,
			codexTable,
			`command = "stale"`,
			codexEnd,
			"",
			"[mcp_servers.other]",
			`command = "other"`,
			"",
		},
		"\n",
	)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	assertCodexMCPConfig(t, path, dir)
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), codexBegin) {
		t.Fatalf("managed block moved away from the top:\n%s", data)
	}
	if !strings.HasSuffix(string(data), "[mcp_servers.other]\ncommand = \"other\"\n") {
		t.Fatalf("unmanaged tail was rewritten:\n%s", data)
	}
	if strings.Contains(string(data), "stale") {
		t.Fatalf("stale managed entry survived:\n%s", data)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if containsPath(second.Paths, path) {
		t.Fatalf("second apply rewrote the Codex config:\n%s", data)
	}
}

func TestApplyRejectsManagedCodexReplacementThatDuplicatesOperatorKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	before := []byte(strings.Join(
		[]string{
			codexBegin,
			codexTable,
			`command = "old"`,
			`args = []`,
			`startup_timeout_sec = 120`,
			codexEnd,
			`default_tools_approval_mode = "approve"`,
			"",
		},
		"\n",
	))
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	})
	if err == nil || !strings.Contains(err.Error(), path) ||
		!strings.Contains(err.Error(), "default_tools_approval_mode") {
		t.Fatalf(
			"Apply() error = %v, want duplicate default_tools_approval_mode and %s",
			err,
			path,
		)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("Codex config changed after rejected merge:\nbefore: %s\nafter: %s", before, after)
	}
}

func TestApplyWritesCodexShellApprovalModes(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		shell ShellPermission
	}{
		{name: "ask", shell: ShellPermissionAsk},
		{name: "allow", shell: ShellPermissionAllow},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assertApplyWritesCodexShellApprovalModes(t, testCase.shell)
		})
	}
}

func assertApplyWritesCodexShellApprovalModes(t *testing.T, shell ShellPermission) {
	t.Helper()
	dir := t.TempDir()
	options := Options{
		ShellPermission: shell,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, codexConfig)
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertCodexShellApprovalModes(t, data, shell)
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("second apply changed paths: %#v", second.Paths)
	}
}

func assertCodexShellApprovalModes(t *testing.T, data []byte, shell ShellPermission) {
	t.Helper()
	text := string(data)
	if strings.Contains(text, "[mcp_servers.just-mcp-work.tools.") {
		t.Fatalf("Codex tool approval uses a subtable header:\n%s", data)
	}
	startup := strings.Index(text, "startup_timeout_sec = 120")
	defaultApproval := strings.Index(text, `default_tools_approval_mode = "prompt"`)
	if startup < 0 || defaultApproval <= startup {
		t.Fatalf("Codex approval key order is wrong:\n%s", data)
	}
	var config struct {
		MCPServers map[string]struct {
			Tools map[string]struct {
				ApprovalMode string `toml:"approval_mode"`
			} `toml:"tools"`
			DefaultToolsApprovalMode string `toml:"default_tools_approval_mode"`
		} `toml:"mcp_servers"`
	}
	if _, decodeErr := toml.Decode(text, &config); decodeErr != nil {
		t.Fatalf("decode workspace Codex config: %v\n%s", decodeErr, data)
	}
	server := config.MCPServers[serverName]
	if server.DefaultToolsApprovalMode != "prompt" {
		t.Fatalf("Codex default approval = %q, want prompt", server.DefaultToolsApprovalMode)
	}
	managed := testClaudeManagedTools(t, shell)
	wantModes := codexApprovalModes(managed)
	if len(server.Tools) != len(wantModes) {
		t.Fatalf("Codex tools = %#v, want %d managed tools", server.Tools, len(wantModes))
	}
	toolNames := make([]string, 0, len(wantModes))
	for tool, want := range wantModes {
		if got := server.Tools[tool].ApprovalMode; got != want {
			t.Fatalf("Codex approval for %s = %q, want %q", tool, got, want)
		}
		toolNames = append(toolNames, tool)
	}
	slices.Sort(toolNames)
	previous := defaultApproval
	for _, tool := range toolNames {
		line := `tools.` + tool + `.approval_mode = "` + wantModes[tool] + `"`
		index := strings.Index(text, line)
		if index <= previous {
			t.Fatalf("Codex tool approval order is not stable at %q:\n%s", line, data)
		}
		previous = index
	}
}

func codexApprovalModes(managed ClaudeToolPermissions) map[string]string {
	wantModes := make(map[string]string, len(managed.Allow)+len(managed.Ask))
	for _, rule := range managed.Allow {
		wantModes[strings.TrimPrefix(rule, ClaudeToolPrefix)] = "approve"
	}
	for _, rule := range managed.Ask {
		wantModes[strings.TrimPrefix(rule, ClaudeToolPrefix)] = "prompt"
	}
	return wantModes
}

func TestApplySwitchesCodexShellApprovalInsideManagedBlockOnly(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		lineBreak string
	}{
		{name: "LF", lineBreak: "\n"},
		{name: "CRLF", lineBreak: "\r\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assertApplySwitchesCodexShellApproval(t, testCase.lineBreak)
		})
	}
}

func assertApplySwitchesCodexShellApproval(t *testing.T, lineBreak string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	prefix := strings.Join(
		[]string{"# operator prefix", "[operator.before]", `value = "before"`, "", ""},
		lineBreak,
	)
	// #nosec G703 -- path is created in this test's temporary directory.
	if err := os.WriteFile(path, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	assertCodexShellApprovalSwitch(t, path, lineBreak, &options)
}

func assertCodexShellApprovalSwitch(
	t *testing.T,
	path string,
	lineBreak string,
	options *Options,
) {
	t.Helper()
	// Add an operator key immediately after the marker. It belongs to the server
	// table because the managed block deliberately ends in that table.
	// #nosec G304 -- path is created in this test's temporary directory.
	managed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.Join(
		[]string{
			"# operator server extension",
			`operator_key = "keep"`,
			"",
			"[operator.after]",
			`value = "after"`,
			"",
		},
		lineBreak,
	)
	withForeign := strings.TrimSuffix(string(managed), lineBreak) + lineBreak + suffix
	// #nosec G703 -- path is created in this test's temporary directory.
	if writeErr := os.WriteFile(path, []byte(withForeign), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	same, err := Apply(*options)
	if err != nil {
		t.Fatal(err)
	}
	if len(same.Paths) != 0 {
		t.Fatalf("same-choice apply changed paths: %#v", same.Paths)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	beforeSwitch, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	options.ShellPermission = ShellPermissionAllow
	switched, err := Apply(*options)
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath := resolvedTestPath(t, path)
	if !containsPath(switched.Paths, resolvedPath) {
		t.Fatalf("switched paths = %#v, want %s", switched.Paths, resolvedPath)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	afterSwitch, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertCodexShellApprovalSwitchResult(t, beforeSwitch, afterSwitch)
}

func assertCodexShellApprovalSwitchResult(t *testing.T, beforeSwitch, afterSwitch []byte) {
	t.Helper()
	if before, after := codexTextOutsideManagedBlock(t, beforeSwitch),
		codexTextOutsideManagedBlock(t, afterSwitch); before != after {
		t.Fatalf("foreign Codex text changed:\nbefore: %q\nafter:  %q", before, after)
	}
	for _, tool := range []string{"run_shell_command", "start_shell_command"} {
		line := `tools.` + tool + `.approval_mode = "approve"`
		if !bytes.Contains(afterSwitch, []byte(line)) {
			t.Fatalf("allow switch lacks %q:\n%s", line, afterSwitch)
		}
	}
	var config struct {
		MCPServers map[string]struct {
			OperatorKey string `toml:"operator_key"`
		} `toml:"mcp_servers"`
		Operator struct {
			Before struct{ Value string }
			After  struct{ Value string }
		}
	}
	if _, decodeErr := toml.Decode(string(afterSwitch), &config); decodeErr != nil {
		t.Fatalf("decode switched Codex config: %v\n%s", decodeErr, afterSwitch)
	}
	if config.MCPServers[serverName].OperatorKey != "keep" ||
		config.Operator.Before.Value != "before" || config.Operator.After.Value != "after" {
		t.Fatalf("foreign Codex values moved: %#v", config)
	}
}

func codexTextOutsideManagedBlock(t *testing.T, data []byte) string {
	t.Helper()
	text := string(data)
	start := strings.Index(text, codexBegin)
	end := strings.Index(text, codexEnd)
	if start < 0 || end < start {
		t.Fatalf("managed block missing:\n%s", data)
	}
	end += len(codexEnd)
	return text[:start] + "<managed>" + text[end:]
}

func TestApplyMergesNearestMCPConfig(t *testing.T) {
	workspace := t.TempDir()
	project := filepath.Join(workspace, "projects", "service")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(workspace, mcpConfig)
	before := `{"mcpServers":{"other":{"command":"other"}}}`
	if err := os.WriteFile(configPath, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(
		Options{
			ShellPermission: ShellPermissionAsk,
			Dir:             project,
			Agents:          []string{"codex"},
			WriteMCPConfig:  true,
			RunnerModes:     testRunnerModes(t),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, configPath) {
		t.Fatalf("updated paths = %#v, want %s", result.Paths, configPath)
	}
	if _, statErr := os.Stat(filepath.Join(project, mcpConfig)); !os.IsNotExist(statErr) {
		t.Fatalf("child MCP config unexpectedly exists: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(project, "AGENTS.md")); !os.IsNotExist(statErr) {
		t.Fatalf("child agent instructions unexpectedly exist: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, "AGENTS.md")); statErr != nil {
		t.Fatalf("workspace agent instructions were not created: %v", statErr)
	}
	assertCodexMCPConfig(t, filepath.Join(workspace, codexConfig), workspace)
	if _, statErr := os.Stat(filepath.Join(project, codexConfig)); !os.IsNotExist(statErr) {
		t.Fatalf("child Codex config unexpectedly exists: %v", statErr)
	}

	// #nosec G304 -- configPath is created in this test's temporary directory.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok || servers["other"] == nil || servers["just-mcp-work"] == nil {
		t.Fatalf("merged servers = %#v", servers)
	}
	assertServerCommand(t, servers)
}

//nolint:gocyclo // The end-to-end assertions cover one worktree configuration transaction.
func TestApplyKeepsManagedConfigurationInsideActiveWorktree(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mainDir := filepath.Join(base, "main")
	worktreeDir := filepath.Join(mainDir, ".wt", "feature")
	nested := filepath.Join(worktreeDir, "nested")
	entryDir := filepath.Join(mainDir, ".git", "worktrees", "feature")
	backReference, err := filepath.Rel(entryDir, filepath.Join(worktreeDir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := filepath.Rel(worktreeDir, entryDir)
	if err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(entryDir, "gitdir"):  backReference + "\n",
		filepath.Join(worktreeDir, ".git"): "gitdir: " + gitDir + "\n",
	} {
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o750); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(path, []byte(contents), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if mkdirErr := os.MkdirAll(nested, 0o750); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	mainConfigPath := filepath.Join(mainDir, mcpConfig)
	mainConfig := []byte(`{"mcpServers":{"main":{"command":"main"}}}` + "\n")
	if writeErr := os.WriteFile(mainConfigPath, mainConfig, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	result, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             nested,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(worktreeDir, "AGENTS.md"),
		filepath.Join(worktreeDir, mcpConfig),
		filepath.Join(worktreeDir, codexConfig),
		policy.Path(worktreeDir),
	} {
		if !containsPath(result.Paths, path) {
			t.Fatalf("updated paths = %#v, want %s", result.Paths, path)
		}
	}
	mainAfter, err := os.ReadFile(mainConfigPath)
	if err != nil || !slices.Equal(mainAfter, mainConfig) {
		t.Fatalf("main checkout config changed: %q, %v", mainAfter, err)
	}
	wantArgs := []string{"serve", "--root", worktreeDir}
	if args := readJSONServerArgs(t, filepath.Join(worktreeDir, mcpConfig)); !slices.Equal(args, wantArgs) {
		t.Fatalf("worktree MCP args = %#v, want %#v", args, wantArgs)
	}
	assertCodexMCPConfig(t, filepath.Join(worktreeDir, codexConfig), worktreeDir)
	if _, statErr := os.Stat(filepath.Join(nested, mcpConfig)); !os.IsNotExist(statErr) {
		t.Fatalf("nested config unexpectedly exists: %v", statErr)
	}
}

func TestApplyDirectLinkedWorktreeCleanupPreservesHigherLocalAnchor(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mainDir := filepath.Join(base, "main")
	worktreeDir := filepath.Join(base, "linked")
	scope := filepath.Join(worktreeDir, "nested")
	writeAgentInitWorktreeMarkers(t, mainDir, worktreeDir, "feature")
	if mkdirErr := os.MkdirAll(scope, 0o750); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	higherPath := filepath.Join(worktreeDir, mcpConfig)
	higherBefore := []byte(`{"mcpServers":{"outer":{"command":"outer"}}}` + "\n")
	if writeErr := os.WriteFile(higherPath, higherBefore, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	localPath := filepath.Join(scope, mcpConfig)
	managed, err := mergeMCPConfig(nil, scope)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(localPath, managed, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             scope, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
	}
	first, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Scope != scope || !containsPath(first.Paths, localPath) {
		t.Fatalf("first result = %#v, want direct scope %q and local anchor", first, scope)
	}
	localAfter, err := os.ReadFile(localPath)
	if err != nil || string(localAfter) != "{}\n" {
		t.Fatalf("local anchor = %q, %v, want empty object", localAfter, err)
	}
	higherAfter, err := os.ReadFile(higherPath)
	if err != nil || !slices.Equal(higherAfter, higherBefore) {
		t.Fatalf("higher worktree config changed: %q, %v", higherAfter, err)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("second cleanup changed paths: %#v", second.Paths)
	}
}

func TestApplyNestedRepositoryMarkersStopAtRepositoryBoundary(t *testing.T) {
	for _, markerKind := range []string{"directory", "file"} {
		t.Run(markerKind, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			mainDir := filepath.Join(base, "main")
			worktreeDir := filepath.Join(base, "linked")
			nestedRepo := filepath.Join(worktreeDir, "nested-repository")
			selectedDir := filepath.Join(nestedRepo, "service")
			writeAgentInitWorktreeMarkers(t, mainDir, worktreeDir, "feature")
			if mkdirErr := os.MkdirAll(selectedDir, 0o750); mkdirErr != nil {
				t.Fatal(mkdirErr)
			}
			switch markerKind {
			case "directory":
				if mkdirErr := os.Mkdir(filepath.Join(nestedRepo, ".git"), 0o750); mkdirErr != nil {
					t.Fatal(mkdirErr)
				}
			case "file":
				submoduleGitDir := filepath.Join(mainDir, ".git", "modules", "nested-repository")
				if writeErr := os.WriteFile(
					filepath.Join(nestedRepo, ".git"),
					[]byte("gitdir: "+submoduleGitDir+"\n"),
					0o600,
				); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			mainConfigPath := filepath.Join(mainDir, mcpConfig)
			mainBefore := []byte(`{"mcpServers":{"main":{"command":"main"}}}` + "\n")
			if writeErr := os.WriteFile(mainConfigPath, mainBefore, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}

			result, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             selectedDir,
				Agents:          []string{"codex"},
				WriteMCPConfig:  true,
				RunnerModes:     testRunnerModes(t),
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Scope != selectedDir ||
				!containsPath(result.Paths, filepath.Join(selectedDir, mcpConfig)) {
				t.Fatalf("result = %#v, want selected repository scope %q", result, selectedDir)
			}
			mainAfter, err := os.ReadFile(mainConfigPath)
			if err != nil || !slices.Equal(mainAfter, mainBefore) {
				t.Fatalf("main checkout config changed: %q, %v", mainAfter, err)
			}
		})
	}
}

func TestApplyCreatesMCPConfigInWorkspaceWhenNoneExists(t *testing.T) {
	dir := t.TempDir()
	result, err := Apply(
		Options{
			ShellPermission: ShellPermissionAsk,
			Dir:             dir,
			Agents:          []string{"codex"},
			WriteMCPConfig:  true,
			RunnerModes:     testRunnerModes(t),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, mcpConfig)
	if !containsPath(result.Paths, path) {
		t.Fatalf("updated paths = %#v, want %s", result.Paths, path)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("MCP servers = %#v", config["mcpServers"])
	}
	assertServerCommand(t, servers)
	codexPath := filepath.Join(dir, codexConfig)
	assertCodexMCPConfig(t, codexPath, dir)
	assertFileMode(t, codexPath, 0o600)
}

func TestApplySupportsMissingStandaloneWorkspace(t *testing.T) {
	tests := []struct {
		name   string
		dryRun bool
	}{
		{name: "write"},
		{name: "dry run", dryRun: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			resolvedBase, err := filepath.EvalSymlinks(base)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(base, "missing", "workspace")
			result, err := Apply(
				Options{
					ShellPermission: ShellPermissionAsk,
					Dir:             dir,
					Agents:          []string{"codex"},
					DryRun:          test.dryRun,
					WriteMCPConfig:  true,
					RunnerModes:     testRunnerModes(t),
				},
			)
			if err != nil {
				t.Fatal(err)
			}

			expectedPaths := []string{
				filepath.Join(dir, "AGENTS.md"),
				filepath.Join(dir, mcpConfig),
				filepath.Join(resolvedBase, "missing", "workspace", codexConfig),
				policy.Path(dir),
			}
			for _, path := range expectedPaths {
				if !containsPath(result.Paths, path) {
					t.Fatalf("updated paths = %#v, want %s", result.Paths, path)
				}
			}
			if test.dryRun {
				if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
					t.Fatalf("dry-run workspace stat error = %v, want not exist", statErr)
				}
				return
			}
			assertCodexMCPConfig(t, filepath.Join(dir, codexConfig), dir)
		})
	}
}

func TestApplyMergesWorkspaceCodexMCPConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	before := "[mcp_servers.other]\ncommand = \"other\"\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	}); err != nil {
		t.Fatal(err)
	}
	assertCodexMCPConfig(t, path, dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[mcp_servers.other]") {
		t.Fatalf("unmanaged Codex server was removed:\n%s", data)
	}
}

func TestApplyRejectsUnmanagedCodexServerWithoutChangingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	before := strings.Join(
		[]string{
			"[mcp_servers.other]",
			"command = \"other\"",
			"",
			"[mcp_servers . \"just-mcp-work\"] # configured manually",
			"command = \"just-mcp-work\"",
			"",
		},
		"\n",
	)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	})
	if err == nil || !strings.Contains(err.Error(), "unmanaged "+codexTable) {
		t.Fatalf("Apply error = %v, want an unmanaged Codex server error", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != before {
		t.Fatalf("Codex config changed after rejected merge:\n%s", data)
	}
	for _, unexpected := range []string{"AGENTS.md", mcpConfig} {
		if _, statErr := os.Stat(filepath.Join(dir, unexpected)); !os.IsNotExist(statErr) {
			t.Fatalf("%s was changed before the Codex config rejection: %v", unexpected, statErr)
		}
	}
}

func TestApplyDisableRejectsUnmanagedCodexServerWithoutPartialChanges(t *testing.T) {
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "AGENTS.md")
	agentBefore := []byte("# Existing\n")
	if err := os.WriteFile(agentPath, agentBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(dir, mcpConfig)
	mcpBefore, err := mergeMCPConfig(nil, ".")
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(mcpPath, mcpBefore, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	codexPath := filepath.Join(dir, codexConfig)
	if mkdirErr := os.MkdirAll(filepath.Dir(codexPath), 0o750); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	codexBefore := []byte(
		"[mcp_servers.just-mcp-work]\ncommand = \"just-mcp-work\"\n",
	)
	if writeErr := os.WriteFile(codexPath, codexBefore, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	_, err = Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: false,
		RunnerModes: testRunnerModes(t),
	})
	if err == nil || !strings.Contains(err.Error(), "unmanaged "+codexTable) {
		t.Fatalf("Apply error = %v, want an unmanaged Codex server error", err)
	}
	for path, want := range map[string][]byte{
		agentPath: agentBefore,
		mcpPath:   mcpBefore,
		codexPath: codexBefore,
	} {
		// #nosec G304 -- paths are created in this test's temporary directory.
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s changed before the Codex rejection:\n%s", path, got)
		}
	}
}

func TestApplyRejectsInlineCodexServerWithoutChangingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	before := strings.Join(
		[]string{
			"[mcp_servers]",
			"just-mcp-work = { command = \"just-mcp-work\", args = [] }",
			"",
		},
		"\n",
	)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	})
	if err == nil || !strings.Contains(err.Error(), "unmanaged "+codexTable) {
		t.Fatalf("Apply error = %v, want an unmanaged Codex server error", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != before {
		t.Fatalf("Codex config changed after rejected merge:\n%s", data)
	}
	for _, unexpected := range []string{"AGENTS.md", mcpConfig} {
		if _, statErr := os.Stat(filepath.Join(dir, unexpected)); !os.IsNotExist(statErr) {
			t.Fatalf("%s was changed before the Codex config rejection: %v", unexpected, statErr)
		}
	}
}

func TestApplyUpdatesSafeSymlinkedCodexConfigDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir := t.TempDir()
	targetDirectory := filepath.Join(dir, "shared", "codex")
	if err := os.MkdirAll(targetDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDirectory, "config.toml")
	before := "[mcp_servers.other]\ncommand = \"other\"\n"
	if err := os.WriteFile(target, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("shared", "codex"),
		filepath.Join(dir, filepath.Dir(codexConfig)),
	); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, resolvedTarget) {
		t.Fatalf("updated paths = %#v, want resolved target %s", result.Paths, resolvedTarget)
	}
	assertCodexMCPConfig(t, target, dir)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[mcp_servers.other]") {
		t.Fatalf("unmanaged Codex server was removed:\n%s", data)
	}
}

func TestApplyUpdatesSafeSymlinkedCodexConfigFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "shared-config.toml")
	before := []byte("[mcp_servers.other]\ncommand = \"other\"\n")
	if err := os.WriteFile(target, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if linkErr := os.Symlink(filepath.Join("..", filepath.Base(target)), path); linkErr != nil {
		t.Fatal(linkErr)
	}

	result, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, resolvedTarget) {
		t.Fatalf("updated paths = %#v, want resolved target %s", result.Paths, resolvedTarget)
	}
	assertCodexMCPConfig(t, target, dir)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Codex config link was replaced: mode = %s", info.Mode())
	}
}

func TestApplyDisablePreservesSafeSymlinkedCodexConfigFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, codexConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "shared-codex-config.toml")
	managed, err := mergeCodexConfig(nil, dir, ShellPermissionAsk)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(target, managed, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if linkErr := os.Symlink(filepath.Join("..", filepath.Base(target)), path); linkErr != nil {
		t.Fatal(linkErr)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	modes := testRunnerModes(t)
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, RunnerModes: modes,
	}
	dryRunOptions := options
	dryRunOptions.DryRun = true
	dryRun, err := Apply(dryRunOptions)
	if err != nil {
		t.Fatal(err)
	}
	assertTruncationDiff(t, dryRun, resolvedTarget)
	result, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	assertTruncationDiff(t, result, resolvedTarget)
	assertFileSymlink(t, path)
	// #nosec G304 -- target is created in this test's temporary directory.
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("Codex symlink target kept managed content:\n%s", after)
	}
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: true, RunnerModes: modes,
	}); err != nil {
		t.Fatal(err)
	}
	assertFileSymlink(t, path)
	assertCodexMCPConfig(t, target, dir)
}

// TestApplyClaudePermissionNoPreservesSafeSymlinkedClaudeSettingsFile checks the
// opt-out cleanup of a settings file the workspace exposes as a symlink: the
// managed entries go through the link and the link itself stays in place.
func TestApplyClaudePermissionNoPreservesSafeSymlinkedClaudeSettingsFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, claudeSettings)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "shared-claude-settings.json")
	managed, err := mergeClaudeSettings(nil, ShellPermissionAsk)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(target, managed, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if linkErr := os.Symlink(filepath.Join("..", filepath.Base(target)), path); linkErr != nil {
		t.Fatal(linkErr)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	modes := testRunnerModes(t)
	options := Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude"},
		RunnerModes:       modes,
		ClaudePermissions: ClaudePermissionsNo,
	}
	dryRunOptions := options
	dryRunOptions.DryRun = true
	dryRun, err := Apply(dryRunOptions)
	if err != nil {
		t.Fatal(err)
	}
	assertTruncationDiff(t, dryRun, resolvedTarget)
	result, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	assertTruncationDiff(t, result, resolvedTarget)
	assertFileSymlink(t, path)
	// #nosec G304 -- target is created in this test's temporary directory.
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("Claude symlink target kept managed content:\n%s", after)
	}
	if _, err := Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude"},
		RunnerModes:       modes,
		ClaudePermissions: ClaudePermissionsYes,
	}); err != nil {
		t.Fatal(err)
	}
	assertFileSymlink(t, path)
	allow, ask := readClaudePermissions(t, target)
	managedTools := testClaudeManagedTools(t, ShellPermissionAsk)
	if !slices.Equal(allow, managedTools.Allow) || !slices.Equal(ask, managedTools.Ask) {
		t.Fatalf("restored Claude permissions allow = %#v, ask = %#v", allow, ask)
	}
}

func TestApplyRejectsEscapingCodexConfigSymlinkWithoutChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, filepath.Dir(codexConfig))); err != nil {
		t.Fatal(err)
	}

	_, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	})
	if err == nil || !strings.Contains(err.Error(), "resolves outside workspace scope") {
		t.Fatalf("Apply error = %v, want an escaping Codex directory error", err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "config.toml")); !os.IsNotExist(statErr) {
		t.Fatalf("escaping symlink target was unexpectedly changed: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(statErr) {
		t.Fatalf("agent instructions changed before the Codex path rejection: %v", statErr)
	}
}

func TestApplyRejectsInvalidCodexConfigSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	tests := []struct {
		name      string
		link      string
		prepare   func(t *testing.T, dir string)
		wantError string
	}{
		{
			name:      "broken",
			link:      "missing-config.toml",
			wantError: "resolve Codex config",
		},
		{
			name:      "loop",
			link:      "config.toml",
			wantError: "resolve Codex config",
		},
		{
			name: "non-regular target",
			link: "config-directory",
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(dir, "config-directory"), 0o750); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "not a regular file",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			directory := filepath.Join(dir, filepath.Dir(codexConfig))
			if err := os.Mkdir(directory, 0o750); err != nil {
				t.Fatal(err)
			}
			if test.prepare != nil {
				test.prepare(t, directory)
			}
			if err := os.Symlink(test.link, filepath.Join(dir, codexConfig)); err != nil {
				t.Fatal(err)
			}

			_, err := Apply(
				Options{
					ShellPermission: ShellPermissionAsk,
					Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: true,
					RunnerModes: testRunnerModes(t),
				},
			)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Apply error = %v, want error containing %q", err, test.wantError)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(statErr) {
				t.Fatalf("agent instructions changed before the Codex path rejection: %v", statErr)
			}
		})
	}
}

func TestApplyRejectsNonRegularNearestMCPConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, mcpConfig)
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(
		Options{
			ShellPermission: ShellPermissionAsk,
			Dir:             dir,
			Agents:          []string{"codex"},
			WriteMCPConfig:  true,
			RunnerModes:     testRunnerModes(t),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Apply error = %v, want a non-regular file error", err)
	}
}

func TestApplyKeepsAgentInstructionsWithinResolvedScope(t *testing.T) {
	workspace := t.TempDir()
	project := filepath.Join(workspace, "projects", "service")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# Existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             project, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(project, "AGENTS.md")
	if !containsPath(result.Paths, localPath) {
		t.Fatalf("updated paths = %#v, want %s", result.Paths, localPath)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	ancestor, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(ancestor) != "# Existing\n" {
		t.Fatalf("ancestor agent instructions changed:\n%s", ancestor)
	}
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), canonicalBlock(false)) {
		t.Fatalf("agent instructions do not contain the managed block:\n%s", data)
	}
}

func TestApplyDoesNotSearchAboveResolvedWorkspaceScope(t *testing.T) {
	ancestor := t.TempDir()
	workspace := filepath.Join(ancestor, "workspace")
	project := filepath.Join(workspace, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	ancestorPath := filepath.Join(ancestor, "AGENTS.md")
	ancestorBefore := []byte("# Shared ancestor\n")
	if err := os.WriteFile(ancestorPath, ancestorBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, mcpConfig), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             project, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
	}); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- paths are created in this test's temporary directory.
	ancestorAfter, err := os.ReadFile(ancestorPath)
	if err != nil || !slices.Equal(ancestorAfter, ancestorBefore) {
		t.Fatalf("ancestor target changed: %q, %v", ancestorAfter, err)
	}
	workspaceData, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil || !strings.Contains(string(workspaceData), canonicalBlock(false)) {
		t.Fatalf("workspace target = %q, %v", workspaceData, err)
	}
}

func TestApplyUpdatesSafeSymlinkedAgentInstruction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	workspace := t.TempDir()
	instructionsDir := filepath.Join(workspace, "instructions")
	if err := os.Mkdir(instructionsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(instructionsDir, "AGENTS.md")
	if err := os.WriteFile(target, []byte("# Existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "AGENTS.md")
	if err := os.Symlink(filepath.Join("instructions", "AGENTS.md"), path); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(workspace, "projects", "service")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, mcpConfig), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             project, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, path) {
		t.Fatalf("updated paths = %#v, want %s", result.Paths, path)
	}
	// #nosec G304 -- target is created in this test's temporary directory.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), canonicalBlock(false)) {
		t.Fatalf("symlink target does not contain the managed block:\n%s", data)
	}
}

func assertTruncationDiff(t *testing.T, result Result, path string) {
	t.Helper()
	diff := resultDiffForPath(t, result, path)
	if strings.Contains(diff, "\n+++ /dev/null\n") {
		t.Fatalf("result falsely reports symlink deletion: %s", diff)
	}
}

func assertFileSymlink(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("file symlink was replaced: mode = %s", info.Mode())
	}
}

// TestApplyDisableKeepsSafeCodexConfigDirectorySymlink covers a JMW-only file
// that lives below a directory symlink: the file goes, the operator's symlink
// stays. preserveScopedFileSymlink only rescues the file itself, so a parent
// directory symlink must not turn the deletion into a truncation.
func TestApplyDisableKeepsSafeCodexConfigDirectorySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	workspace := t.TempDir()
	targetDirectory := filepath.Join(workspace, "shared", "codex")
	if err := os.MkdirAll(targetDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDirectory, filepath.Base(codexConfig))
	managed, err := mergeCodexConfig(nil, workspace, ShellPermissionAsk)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(target, managed, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	link := filepath.Join(workspace, filepath.Dir(codexConfig))
	if linkErr := os.Symlink(filepath.Join("shared", "codex"), link); linkErr != nil {
		t.Fatal(linkErr)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             workspace, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, resolvedTarget) {
		t.Fatalf("updated paths = %#v, want removed %s", result.Paths, resolvedTarget)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Codex config directory symlink was replaced: mode = %s", info.Mode())
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("JMW-only target below directory symlink still exists: %v", statErr)
	}
}

func TestMCPConfigSnippetUsesAbsoluteExecutablePath(t *testing.T) {
	if _, err := MCPConfigSnippet(""); err == nil ||
		!strings.Contains(err.Error(), "scope root is required") {
		t.Fatalf("empty MCP scope error = %v", err)
	}
	snippet, err := MCPConfigSnippet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(snippet), &config); err != nil {
		t.Fatal(err)
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("MCP servers = %#v", config["mcpServers"])
	}
	assertServerCommand(t, servers)
}

func assertServerCommand(t *testing.T, servers map[string]any) {
	t.Helper()
	server, ok := servers["just-mcp-work"].(map[string]any)
	if !ok {
		t.Fatalf("just-mcp-work server = %#v", servers["just-mcp-work"])
	}
	command, ok := server["command"].(string)
	if !ok || !filepath.IsAbs(command) {
		t.Fatalf("server command = %#v, want an absolute path", server["command"])
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	if command != executable {
		t.Fatalf("server command = %q, want %q", command, executable)
	}
}

func readJSONServerArgs(t *testing.T, path string) []string {
	t.Helper()
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCPServers map[string]serverEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	return config.MCPServers[serverName].Args
}

func readCodexServerArgs(t *testing.T, path string) []string {
	t.Helper()
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCPServers map[string]struct {
			Args []string
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(data), &config); err != nil {
		t.Fatal(err)
	}
	return config.MCPServers[serverName].Args
}

func assertManagedServerArgs(t *testing.T, args []string, root string) {
	t.Helper()
	want := []string{"serve", "--root", root}
	if !slices.Equal(args, want) {
		t.Fatalf("server args = %#v, want %#v", args, want)
	}
}

func TestTOMLStringEscapesWindowsPath(t *testing.T) {
	value := `C:\Users\runneradmin\just-mcp-work.exe`
	encoded, err := tomlString(value)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Command string
	}
	if _, err := toml.Decode("command = "+encoded, &config); err != nil {
		t.Fatalf("decode encoded Windows path: %v", err)
	}
	if config.Command != value {
		t.Fatalf("command = %q, want %q", config.Command, value)
	}
}

func assertCodexMCPConfig(t *testing.T, path, root string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCPServers map[string]struct {
			Command           string
			Args              []string
			StartupTimeoutSec int `toml:"startup_timeout_sec"`
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(data), &config); err != nil {
		t.Fatalf("decode workspace Codex config: %v", err)
	}
	server, found := config.MCPServers["just-mcp-work"]
	if !found || server.Command == "" ||
		!slices.Equal(server.Args, []string{"serve", "--root", root}) ||
		server.StartupTimeoutSec != 120 {
		t.Fatalf("invalid workspace Codex config:\n%s", data)
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func containsPath(paths []string, want string) bool {
	return slices.Contains(paths, want)
}

func resultDiffForPath(t *testing.T, result Result, path string) string {
	t.Helper()
	if len(result.Paths) != len(result.Diffs) {
		t.Fatalf("result paths and diffs differ in length: %#v, %#v", result.Paths, result.Diffs)
	}
	for index, candidate := range result.Paths {
		if candidate == path {
			return result.Diffs[index]
		}
	}
	t.Fatalf("result paths = %#v, want %s", result.Paths, path)
	return ""
}

func TestApplyWritesClaudePermissionsWhenAccepted(t *testing.T) {
	dir := t.TempDir()
	path := claudeSettingsPath(t, dir)
	options := Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude"},
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	}
	result, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, path) {
		t.Fatalf("result paths = %#v", result.Paths)
	}
	assertFileMode(t, path, 0o600)
	allow, ask := readClaudePermissions(t, path)
	managed := testClaudeManagedTools(t, ShellPermissionAsk)
	if !slices.Equal(allow, managed.Allow) || !slices.Equal(ask, managed.Ask) {
		t.Fatalf("permissions allow = %#v, ask = %#v", allow, ask)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if containsPath(second.Paths, path) {
		t.Fatalf("idempotent apply changed the Claude settings: %#v", second.Paths)
	}
}

func TestApplyPlacesShellToolsInSelectedClaudeList(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		shell ShellPermission
	}{
		{name: "ask", shell: ShellPermissionAsk},
		{name: "allow", shell: ShellPermissionAllow},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := claudeSettingsPath(t, dir)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			before := "{\n" +
				"  \"model\": \"opus\",\n" +
				"  \"permissions\": {\n" +
				"    \"allow\": [\"foreign-allow\", \"mcp__just-mcp-work__retired\"],\n" +
				"    \"ask\": [\"foreign-ask\", \"mcp__just-mcp-work\"],\n" +
				"    \"deny\": [\"foreign-deny\", \"mcp__just-mcp-work__old\"]\n" +
				"  },\n" +
				"  \"env\": { \"KEEP\": \"exact\" }\n" +
				"}\n"
			if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(Options{
				ShellPermission:   testCase.shell,
				Dir:               dir,
				Agents:            []string{"claude"},
				RunnerModes:       testRunnerModes(t),
				ClaudePermissions: ClaudePermissionsYes,
			}); err != nil {
				t.Fatal(err)
			}
			allow, ask := readClaudePermissions(t, path)
			managed := testClaudeManagedTools(t, testCase.shell)
			if want := append([]string{"foreign-allow"}, managed.Allow...); !slices.Equal(allow, want) {
				t.Fatalf("shell permission %s wrote allow = %#v, want %#v", testCase.shell, allow, want)
			}
			if want := append([]string{"foreign-ask"}, managed.Ask...); !slices.Equal(ask, want) {
				t.Fatalf("shell permission %s wrote ask = %#v, want %#v", testCase.shell, ask, want)
			}
			// #nosec G304 -- path is created in this test's temporary directory.
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, foreign := range []string{
				`"model": "opus"`,
				`"env": { "KEEP": "exact" }`,
				`"deny": ["foreign-deny"]`,
			} {
				if !strings.Contains(string(data), foreign) {
					t.Fatalf("shell permission %s lost foreign bytes %q:\n%s", testCase.shell, foreign, data)
				}
			}
		})
	}
}

func TestApplyOmitsEmptyOwnedListAndMovesShellToolsOnRerun(t *testing.T) {
	dir := t.TempDir()
	path := claudeSettingsPath(t, dir)
	options := Options{
		ShellPermission:   ShellPermissionAllow,
		Dir:               dir,
		Agents:            []string{"claude"},
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	allow, ask := readClaudePermissions(t, path)
	allowManaged := testClaudeManagedTools(t, ShellPermissionAllow)
	if !slices.Equal(allow, allowManaged.Allow) || len(ask) != 0 {
		t.Fatalf("allow choice wrote allow = %#v, ask = %#v", allow, ask)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if containsPath(second.Paths, path) {
		t.Fatalf("idempotent allow apply changed the Claude settings: %#v", second.Paths)
	}
	var settings map[string]any
	readJSONFile(t, path, &settings)
	permissions, ok := settings["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions = %#v, want object", settings["permissions"])
	}
	if _, exists := permissions["ask"]; exists {
		t.Fatalf("allow choice created an ask list: %#v", permissions["ask"])
	}

	options.ShellPermission = ShellPermissionAsk
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	allow, ask = readClaudePermissions(t, path)
	askManaged := testClaudeManagedTools(t, ShellPermissionAsk)
	if !slices.Equal(allow, askManaged.Allow) || !slices.Equal(ask, askManaged.Ask) {
		t.Fatalf("rerun with ask wrote allow = %#v, ask = %#v", allow, ask)
	}
	for _, shellRule := range allowManaged.Allow[len(askManaged.Allow):] {
		if slices.Contains(allow, shellRule) || !slices.Contains(ask, shellRule) {
			t.Fatalf("shell rule %q was not moved cleanly: allow = %#v, ask = %#v", shellRule, allow, ask)
		}
	}
}

func TestApplyReplacesEveryManagedClaudeEntry(t *testing.T) {
	dir := t.TempDir()
	settings := claudeSettingsPath(t, dir)
	if err := os.MkdirAll(filepath.Dir(settings), 0o750); err != nil {
		t.Fatal(err)
	}
	before := `{
	  "model": "opus",
	  "permissions": {
	    "allow": ["Bash(git status:*)", "mcp__just-mcp-work__retired_tool"],
	    "ask": ["mcp__just-mcp-work", "Bash(git add:*)"],
	    "deny": ["mcp__just-mcp-work__run_shell_command", "mcp__just-mcp-work-other__keep"]
	  }
	}`
	if err := os.WriteFile(settings, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Apply(
		Options{
			ShellPermission:   ShellPermissionAsk,
			Dir:               dir,
			Agents:            []string{"claude"},
			RunnerModes:       testRunnerModes(t),
			ClaudePermissions: ClaudePermissionsYes,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(result.Paths, settings) {
		t.Fatalf("result paths = %#v", result.Paths)
	}
	// #nosec G304 -- settings is created in this test's temporary directory.
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if decodeErr := json.Unmarshal(data, &config); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if config["model"] != "opus" {
		t.Fatalf("unrelated settings were clobbered: %#v", config)
	}
	permissions, ok := config["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions = %#v", config["permissions"])
	}
	allow := stringList(t, permissions["allow"])
	ask := stringList(t, permissions["ask"])
	deny := stringList(t, permissions["deny"])
	managed := testClaudeManagedTools(t, ShellPermissionAsk)
	if !slices.Equal(allow, append([]string{"Bash(git status:*)"}, managed.Allow...)) {
		t.Fatalf("allow = %#v", allow)
	}
	if !slices.Equal(ask, append([]string{"Bash(git add:*)"}, managed.Ask...)) {
		t.Fatalf("ask = %#v", ask)
	}
	if !slices.Equal(deny, []string{"mcp__just-mcp-work-other__keep"}) {
		t.Fatalf("deny = %#v", deny)
	}
	if strings.Contains(string(data), "retired_tool") {
		t.Fatalf("unknown managed entry survived:\n%s", data)
	}
}

func TestApplySkipsClaudePermissionsWithoutApproval(t *testing.T) {
	for _, testCase := range []struct {
		options Options
		name    string
	}{
		{name: "declined", options: Options{
			ShellPermission:   ShellPermissionAsk,
			ClaudePermissions: ClaudePermissionsAsk,
			RunnerModes:       testRunnerModes(t),
			Confirm: func(ShellPermission, string, string) (bool, error) {
				return false, nil
			},
		}},
		{name: "no confirmation available", options: Options{
			ShellPermission:   ShellPermissionAsk,
			ClaudePermissions: ClaudePermissionsAsk,
			RunnerModes:       testRunnerModes(t),
		}},
		{name: "opted out", options: Options{
			ShellPermission:   ShellPermissionAsk,
			ClaudePermissions: ClaudePermissionsNo,
			RunnerModes:       testRunnerModes(t),
		}},
		{name: "claude not selected", options: Options{
			ShellPermission:   ShellPermissionAsk,
			Agents:            []string{"codex"},
			RunnerModes:       testRunnerModes(t),
			ClaudePermissions: ClaudePermissionsYes,
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			options := testCase.options
			options.Dir = dir
			if options.Agents == nil {
				options.Agents = []string{"claude"}
			}
			result, err := Apply(options)
			if err != nil {
				t.Fatal(err)
			}
			path := claudeSettingsPath(t, dir)
			if containsPath(result.Paths, path) {
				t.Fatalf("result reports an unwritten path: %#v", result.Paths)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("Claude settings were written: %v", statErr)
			}
		})
	}
}

func TestApplyReportsClaudePermissionsDiffWithoutAskingOnDryRun(t *testing.T) {
	dir := t.TempDir()
	confirmed := false
	result, err := Apply(
		Options{
			ShellPermission: ShellPermissionAsk,
			Dir:             dir,
			Agents:          []string{"claude"},
			DryRun:          true,
			RunnerModes:     testRunnerModes(t),
			Confirm: func(ShellPermission, string, string) (bool, error) {
				confirmed = true
				return true, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed {
		t.Fatal("dry run asked for a confirmation")
	}
	path := claudeSettingsPath(t, dir)
	if !containsPath(result.Paths, path) {
		t.Fatalf("result paths = %#v", result.Paths)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("dry run wrote the Claude settings: %v", statErr)
	}
	if !strings.Contains(strings.Join(result.Diffs, "\n"), ClaudeToolPrefix+"run_task") {
		t.Fatalf("diffs do not describe the managed tools: %#v", result.Diffs)
	}
}

func TestApplyRejectsInvalidClaudePermissionLists(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		settings  string
		wantError string
	}{
		{
			name:      "permissions is not an object",
			settings:  `{"permissions": []}`,
			wantError: "is not an object",
		},
		{
			name:      "allow is not a list",
			settings:  `{"permissions": {"allow": "all"}}`,
			wantError: "permissions.allow",
		},
		{
			name:      "invalid JSON",
			settings:  "{",
			wantError: "decode existing",
		},
		{
			name:      "duplicate key",
			settings:  `{"permissions": {"allow": []}, "permissions": {"ask": []}}`,
			wantError: "occurs more than once",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := claudeSettingsPath(t, dir)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(testCase.settings), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Apply(
				Options{
					ShellPermission:   ShellPermissionAsk,
					Dir:               dir,
					Agents:            []string{"claude"},
					RunnerModes:       testRunnerModes(t),
					ClaudePermissions: ClaudePermissionsYes,
				},
			)
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("Apply error = %v, want %q", err, testCase.wantError)
			}
			// #nosec G304 -- path is created in this test's temporary directory.
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(data) != testCase.settings {
				t.Fatalf("rejected settings were changed:\n%s", data)
			}
		})
	}
}

// TestApplyWritesClaudePermissionsOverNullLists covers the settings file that
// spells an unset permission list as null. validateClaudePermissions accepts
// that spelling, so the edit has to write the list rather than reject it.
func TestApplyWritesClaudePermissionsOverNullLists(t *testing.T) {
	dir := t.TempDir()
	path := claudeSettingsPath(t, dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	settings := "{\n  \"permissions\": {\n    \"allow\": null,\n    \"ask\": null\n  }\n}\n"
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"claude"}, RunnerModes: testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	allow, ask := readClaudePermissions(t, path)
	managed := testClaudeManagedTools(t, ShellPermissionAsk)
	if !slices.Equal(allow, managed.Allow) || !slices.Equal(ask, managed.Ask) {
		t.Fatalf("allow = %#v, ask = %#v", allow, ask)
	}
	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if containsPath(second.Paths, path) {
		t.Fatal("second apply rewrote the settings")
	}
}

// TestApplyRejectsUnusableMCPConfig keeps init from editing a configuration it
// cannot merge into, and from touching the file it refused.
func TestApplyRejectsUnusableMCPConfig(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		config    string
		wantError string
	}{
		{
			name:      "mcpServers is not an object",
			config:    `{"mcpServers": "everything"}`,
			wantError: "mcpServers in .mcp.json is not an object",
		},
		{
			name:      "duplicate key",
			config:    `{"mcpServers": {}, "mcpServers": {}}`,
			wantError: `object key "mcpServers" occurs more than once`,
		},
		{
			name:      "duplicate server key",
			config:    `{"mcpServers": {"other": {}, "other": {}}}`,
			wantError: `object key "other" occurs more than once`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, mcpConfig)
			if err := os.WriteFile(path, []byte(testCase.config), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: true,
				RunnerModes: testRunnerModes(t),
			})
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("Apply error = %v, want %q", err, testCase.wantError)
			}
			// #nosec G304 -- path is created in this test's temporary directory.
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(data) != testCase.config {
				t.Fatalf("rejected config was changed:\n%s", data)
			}
		})
	}
}

func TestApplyReportsClaudePermissionConfirmationFailure(t *testing.T) {
	dir := t.TempDir()
	_, err := Apply(
		Options{
			ShellPermission: ShellPermissionAsk,
			Dir:             dir,
			Agents:          []string{"claude"},
			RunnerModes:     testRunnerModes(t),
			Confirm: func(ShellPermission, string, string) (bool, error) {
				return false, os.ErrClosed
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "confirm") {
		t.Fatalf("Apply error = %v", err)
	}
}

func TestParseClaudePermissions(t *testing.T) {
	for _, testCase := range []struct {
		value string
		want  ClaudePermissions
	}{
		{value: "", want: ClaudePermissionsAsk},
		{value: "ask", want: ClaudePermissionsAsk},
		{value: " YES ", want: ClaudePermissionsYes},
		{value: "No", want: ClaudePermissionsNo},
	} {
		got, err := ParseClaudePermissions(testCase.value)
		if err != nil || got != testCase.want {
			t.Fatalf("ParseClaudePermissions(%q) = %q, %v", testCase.value, got, err)
		}
	}
	if _, err := ParseClaudePermissions("maybe"); err == nil {
		t.Fatal("ParseClaudePermissions accepted an unsupported mode")
	}
}

func TestParseShellPermission(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		value     string
		want      ShellPermission
		wantError bool
	}{
		{name: "allow", value: "allow", want: ShellPermissionAllow},
		{name: "ask", value: "ask", want: ShellPermissionAsk},
		{name: "case and whitespace", value: "  AlLoW\t", want: ShellPermissionAllow},
		{name: "empty", wantError: true},
		{name: "unknown", value: "maybe", wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ParseShellPermission(testCase.value)
			if (err != nil) != testCase.wantError || got != testCase.want {
				t.Fatalf(
					"ParseShellPermission(%q) = %q, %v; want %q, error %t",
					testCase.value,
					got,
					err,
					testCase.want,
					testCase.wantError,
				)
			}
		})
	}
}

func TestClaudeManagedToolsPlacesShellRulesWithoutChangingTheUnion(t *testing.T) {
	ask := testClaudeManagedTools(t, ShellPermissionAsk)
	allow := testClaudeManagedTools(t, ShellPermissionAllow)
	shellRules := claudeToolRules(
		"define_shell_block",
		"run_shell_command",
		"start_shell_command",
	)
	if !slices.Equal(ask.Ask, shellRules) {
		t.Fatalf("ask choice shell rules = %#v, want %#v", ask.Ask, shellRules)
	}
	if len(allow.Ask) != 0 {
		t.Fatalf("allow choice ask rules = %#v, want empty", allow.Ask)
	}
	if !slices.Equal(allow.Allow[len(allow.Allow)-len(shellRules):], shellRules) {
		t.Fatalf("allow choice does not end with shell rules: %#v", allow.Allow)
	}
	if !slices.Equal(
		slices.Concat(ask.Allow, ask.Ask),
		slices.Concat(allow.Allow, allow.Ask),
	) {
		t.Fatalf("managed rule union differs: ask = %#v, allow = %#v", ask, allow)
	}
	if _, err := ClaudeManagedTools(""); err == nil {
		t.Fatal("ClaudeManagedTools accepted an empty shell permission")
	}
}

func TestApplyRejectsInvalidShellPermission(t *testing.T) {
	for _, shellPermission := range []ShellPermission{"sometimes"} {
		_, err := Apply(Options{
			ShellPermission: shellPermission,
			RunnerModes:     testRunnerModes(t),
		})
		if err == nil || !strings.Contains(err.Error(), "Options.ShellPermission") {
			t.Fatalf(
				"Apply(%q) error = %v, want Options.ShellPermission validation",
				shellPermission,
				err,
			)
		}
	}
}

func TestApplyAllowsUnsetShellPermissionWithoutClaudeSettingsSurface(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		permissions ClaudePermissions
		agents      []string
	}{
		{name: "codex only", agents: []string{"codex"}},
		{
			name:        "Claude permissions removed",
			agents:      []string{"claude"},
			permissions: ClaudePermissionsNo,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Apply(Options{
				Dir:               t.TempDir(),
				Agents:            testCase.agents,
				RunnerModes:       testRunnerModes(t),
				ClaudePermissions: testCase.permissions,
			})
			if err != nil {
				t.Fatalf("Apply() error = %v, want nil", err)
			}
		})
	}
}

func TestApplyRequiresShellPermissionCallbackWhenChoiceIsUnset(t *testing.T) {
	dir := t.TempDir()
	_, err := Apply(Options{
		Dir:            dir,
		Agents:         []string{"codex"},
		WriteMCPConfig: true,
		RunnerModes:    testRunnerModes(t),
	})
	if err == nil || !strings.Contains(err.Error(), "Options.AskShellPermission") {
		t.Fatalf("Apply() error = %v, want missing Options.AskShellPermission", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(statErr) {
		t.Fatalf("missing shell permission callback wrote AGENTS.md: %v", statErr)
	}
}

func TestApplyResolvesShellPermissionAfterNormalizingAgents(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		wantOffer   ShellPermission
		agents      []string
		current     bool
		wantCurrent bool
	}{
		{
			name:      "mixed-case Claude uses default",
			agents:    []string{" Claude "},
			wantOffer: ShellPermissionAsk,
		},
		{
			name:        "empty selection uses current Claude placement",
			current:     true,
			wantCurrent: true,
			wantOffer:   ShellPermissionAllow,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			if testCase.current {
				path := filepath.Join(dir, claudeSettings)
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				managed := testClaudeManagedTools(t, ShellPermissionAllow)
				settings, err := json.Marshal(map[string]any{
					"permissions": map[string]any{"allow": managed.Allow},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, settings, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			asked := 0
			_, err := Apply(Options{
				Dir:               dir,
				Agents:            testCase.agents,
				RunnerModes:       testRunnerModes(t),
				ClaudePermissions: ClaudePermissionsYes,
				AskShellPermission: func(
					offer ShellPermission,
					current bool,
				) (ShellPermission, error) {
					asked++
					if offer != testCase.wantOffer || current != testCase.wantCurrent {
						t.Fatalf(
							"shell offer = %q, %t; want %q, %t",
							offer,
							current,
							testCase.wantOffer,
							testCase.wantCurrent,
						)
					}
					return offer, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if asked != 1 {
				t.Fatalf("shell permission callback calls = %d, want 1", asked)
			}
		})
	}
}

func TestValidateMergedCodexConfigRejectsMissingServerTable(t *testing.T) {
	_, err := validateMergedCodexConfig("approval_policy = \"on-request\"\n")
	if err == nil || !strings.Contains(err.Error(), codexTable) {
		t.Fatalf("validateMergedCodexConfig() error = %v, want missing %s", err, codexTable)
	}
}

func TestCurrentShellPermission(t *testing.T) {
	defineRule := ClaudeToolPrefix + "define_shell_block"
	runRule := ClaudeToolPrefix + "run_shell_command"
	startRule := ClaudeToolPrefix + "start_shell_command"
	for _, testCase := range []struct {
		name     string
		settings string
		want     ShellPermission
		exists   bool
		found    bool
	}{
		{name: "no file", want: ShellPermissionAsk},
		{
			name: "allow",
			settings: `{"permissions":{"allow":["` + defineRule + `","` + runRule +
				`","` + startRule + `"],"ask":[]}}`,
			exists: true,
			want:   ShellPermissionAllow,
			found:  true,
		},
		{
			name: "ask",
			settings: `{"permissions":{"allow":[],"ask":["` + defineRule + `","` +
				runRule + `","` + startRule + `"]}}`,
			exists: true,
			want:   ShellPermissionAsk,
			found:  true,
		},
		{
			name:     "legacy allow",
			settings: `{"permissions":{"allow":["` + runRule + `","` + startRule + `"],"ask":[]}}`,
			exists:   true,
			want:     ShellPermissionAllow,
			found:    true,
		},
		{
			name: "define rule split from legacy rules",
			settings: `{"permissions":{"allow":["` + runRule + `","` + startRule +
				`"],"ask":["` + defineRule + `"]}}`,
			exists: true,
			want:   ShellPermissionAsk,
		},
		{
			name: "contradictory",
			settings: `{"permissions":{"allow":["` + runRule + `","` + startRule +
				`"],"ask":["` + runRule + `","` + startRule + `"]}}`,
			exists: true,
			want:   ShellPermissionAsk,
		},
		{
			name:     "partial",
			settings: `{"permissions":{"allow":[],"ask":["` + runRule + `"]}}`,
			exists:   true,
			want:     ShellPermissionAsk,
		},
		{
			name: "split",
			settings: `{"permissions":{"allow":["` + runRule + `"],"ask":["` +
				startRule + `"]}}`,
			exists: true,
			want:   ShellPermissionAsk,
		},
		{
			name:     "absent from both",
			settings: `{"permissions":{"allow":["foreign"],"ask":[]}}`,
			exists:   true,
			want:     ShellPermissionAsk,
		},
		{name: "empty file", exists: true, want: ShellPermissionAsk},
		{name: "whitespace file", settings: " \n\t", exists: true, want: ShellPermissionAsk},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			if testCase.exists {
				path := filepath.Join(dir, claudeSettings)
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(testCase.settings), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, found, err := CurrentShellPermission(dir)
			if err != nil || got != testCase.want || found != testCase.found {
				t.Fatalf(
					"CurrentShellPermission() = (%q, %t, %v), want (%q, %t, nil)",
					got,
					found,
					err,
					testCase.want,
					testCase.found,
				)
			}
		})
	}
}

func TestCurrentShellPermissionReportsMalformedSettings(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		settings string
	}{
		{name: "invalid JSON", settings: "{not json"},
		{
			name: "duplicate permissions key",
			settings: `{"permissions":{"allow":[]},` +
				`"permissions":{"ask":[]}}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, claudeSettings)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(testCase.settings), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := CurrentShellPermission(dir); err == nil ||
				!strings.Contains(err.Error(), path) {
				t.Fatalf("CurrentShellPermission error = %v, want malformed settings path", err)
			}
		})
	}
}

func TestCurrentShellPermissionReportsUnreadableSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, claudeSettings)
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CurrentShellPermission(dir); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("CurrentShellPermission error = %v, want unreadable settings path", err)
	}
}

func TestClaudeManagedToolsUseTheServerPrefix(t *testing.T) {
	managed := testClaudeManagedTools(t, ShellPermissionAsk)
	seen := map[string]struct{}{}
	for _, rule := range slices.Concat(managed.Allow, managed.Ask) {
		if !strings.HasPrefix(rule, ClaudeToolPrefix) || !isManagedClaudeTool(rule) {
			t.Fatalf("managed rule %q is not addressed to this server", rule)
		}
		if _, exists := seen[rule]; exists {
			t.Fatalf("managed rule %q is listed twice", rule)
		}
		seen[rule] = struct{}{}
	}
	if isManagedClaudeTool("mcp__just-mcp-work-other__run_task") {
		t.Fatal("another server's entry is treated as managed")
	}
}

// claudeSettingsPath resolves the settings path the way Apply does, so a
// temporary directory behind a symlink, such as /var on macOS, still matches.
func claudeSettingsPath(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(resolved, ".claude", "settings.json")
}

func readClaudePermissions(t *testing.T, path string) ([]string, []string) {
	t.Helper()
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Ask   []string `json:"ask"`
		} `json:"permissions"`
	}
	if decodeErr := json.Unmarshal(data, &config); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	return config.Permissions.Allow, config.Permissions.Ask
}

func stringList(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value %#v is not a list", value)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, isText := item.(string)
		if !isText {
			t.Fatalf("list item %#v is not a string", item)
		}
		result = append(result, text)
	}
	return result
}
