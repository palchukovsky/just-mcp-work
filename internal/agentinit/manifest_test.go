// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/aiprofile"
	"github.com/palchukovsky/just-mcp-work/internal/policy"
	"github.com/palchukovsky/just-mcp-work/internal/version"
)

func wantManagedManifestRecovery(root string) string {
	return "run just-mcp-work init --dir " + strconv.Quote(root)
}

//nolint:gocyclo // This test pins the manifest document, every surface, and idempotency together.
func TestApplyWritesManifestForEveryManagedSurfaceAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	options := Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude", "codex", "cursor", "copilot", "windsurf"},
		WriteMCPConfig:    true,
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	}
	first, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	resolvedDirectory, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(resolvedDirectory, manifestFile)
	if len(first.Paths) < 2 || first.Paths[len(first.Paths)-2] != manifestPath ||
		first.Paths[len(first.Paths)-1] != policy.Path(dir) {
		t.Fatalf("manifest and policy were not reported last in publication order: %#v", first.Paths)
	}
	manifest, manifestBytes := readManagedManifest(t, dir)
	if bytes.Contains(manifestBytes, []byte("\"beta_test\"")) {
		t.Fatalf("plain manifest contains beta_test key:\n%s", manifestBytes)
	}
	if manifest.BetaTest {
		t.Fatal("plain manifest beta test = true, want false")
	}
	if manifest.AIFamily != aiprofile.FamilyUnknown ||
		!bytes.Contains(manifestBytes, []byte(`"ai_family": "unknown"`)) {
		t.Fatalf("plain manifest AI family = %q:\n%s", manifest.AIFamily, manifestBytes)
	}
	if manifest.ShellPermission != string(ShellPermissionAsk) {
		t.Fatalf(
			"manifest shell permission = %q, want %q",
			manifest.ShellPermission,
			ShellPermissionAsk,
		)
	}
	if manifest.SchemaVersion != manifestSchemaVersion {
		t.Fatalf("schema version = %d, want %d", manifest.SchemaVersion, manifestSchemaVersion)
	}
	if manifest.Release != version.Current().Display() {
		t.Fatalf("release = %q, want %q", manifest.Release, version.Current().Display())
	}
	want := []struct {
		path string
		kind string
	}{
		{path: "CLAUDE.md", kind: manifestKindAgentInstructions},
		{path: "AGENTS.md", kind: manifestKindAgentInstructions},
		{path: ".cursor/rules/just-mcp-work.mdc", kind: manifestKindAgentInstructions},
		{path: ".github/copilot-instructions.md", kind: manifestKindAgentInstructions},
		{path: ".windsurfrules", kind: manifestKindAgentInstructions},
		{path: mcpConfig, kind: manifestKindMCPConfig},
		{path: codexConfig, kind: manifestKindCodexConfig},
		{path: claudeSettings, kind: manifestKindClaudeSettings},
		{path: guideFile, kind: manifestKindAgentGuide},
	}
	if len(manifest.Surfaces) != len(want) {
		t.Fatalf("manifest surfaces = %#v, want %d entries", manifest.Surfaces, len(want))
	}
	for index, expected := range want {
		surface := manifest.Surfaces[index]
		if surface.Path != expected.path || surface.Kind != expected.kind {
			t.Fatalf("surface %d = %#v, want path %q kind %q", index, surface, expected.path, expected.kind)
		}
		if strings.Contains(surface.Path, "\\") {
			t.Fatalf("surface path is not slash-normalized: %q", surface.Path)
		}
		if wantHash := expectedSurfaceHash(t, dir, surface); surface.SHA256 != wantHash {
			t.Fatalf("surface %s hash = %q, want %q", surface.Path, surface.SHA256, wantHash)
		}
	}

	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("second apply changed paths: %#v", second.Paths)
	}
	_, afterSecond := readManagedManifest(t, dir)
	if !bytes.Equal(afterSecond, manifestBytes) {
		t.Fatalf("second apply changed manifest bytes:\nfirst: %s\nsecond: %s", manifestBytes, afterSecond)
	}
}

func TestApplyDryRunPlansManifestWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	result, err := Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude", "codex"},
		DryRun:            true,
		WriteMCPConfig:    true,
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolvedDirectory, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(resolvedDirectory, manifestFile)
	if len(result.Paths) < 2 || result.Paths[len(result.Paths)-2] != manifestPath ||
		result.Paths[len(result.Paths)-1] != policy.Path(dir) {
		t.Fatalf("dry run paths = %#v, want manifest then policy last", result.Paths)
	}
	guidePath := filepath.Join(resolvedDirectory, guideFile)
	if !containsPath(result.Paths, guidePath) {
		t.Fatalf("dry run paths = %#v, want agent guide %s", result.Paths, guidePath)
	}
	for _, path := range result.Paths {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("dry run wrote %s: %v", path, statErr)
		}
	}
}

func TestApplyNarrowedSelectionReplacesManifestSurfaceSet(t *testing.T) {
	dir := t.TempDir()
	allAgents := []string{"claude", "codex", "cursor", "copilot", "windsurf"}
	if _, err := Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            allAgents,
		WriteMCPConfig:    true,
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	}); err != nil {
		t.Fatal(err)
	}
	narrowOptions := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     testRunnerModes(t),
	}
	result, err := Apply(narrowOptions)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := resolvedTestPath(t, filepath.Join(dir, manifestFile))
	if len(result.Paths) != 1 || result.Paths[0] != manifestPath {
		t.Fatalf("narrowed apply paths = %#v, want only manifest", result.Paths)
	}
	manifest, manifestBeforeRepeat := readManagedManifest(t, dir)
	if manifest.ShellPermission != string(ShellPermissionAsk) {
		t.Fatalf(
			"narrowed manifest shell permission = %q, want %q",
			manifest.ShellPermission,
			ShellPermissionAsk,
		)
	}
	want := []struct {
		path string
		kind string
	}{
		{path: "AGENTS.md", kind: manifestKindAgentInstructions},
		{path: mcpConfig, kind: manifestKindMCPConfig},
		{path: codexConfig, kind: manifestKindCodexConfig},
		{path: guideFile, kind: manifestKindAgentGuide},
		{path: claudeSettings, kind: manifestKindClaudeSettings},
	}
	assertManifestSurfaces(t, manifest.Surfaces, want)
	if _, verifyErr := VerifyManagedSurfaces(dir); verifyErr != nil {
		t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", verifyErr)
	}

	repeated, err := Apply(narrowOptions)
	if err != nil {
		t.Fatalf("repeated narrowed Apply() error = %v, want nil", err)
	}
	if len(repeated.Paths) != 0 {
		t.Fatalf("repeated narrowed Apply() paths = %#v, want none", repeated.Paths)
	}
	_, manifestAfterRepeat := readManagedManifest(t, dir)
	if !bytes.Equal(manifestAfterRepeat, manifestBeforeRepeat) {
		t.Fatalf(
			"repeated narrowed Apply() changed manifest bytes:\n%s\n%s",
			manifestBeforeRepeat,
			manifestAfterRepeat,
		)
	}
	for _, agent := range allAgents {
		named, _ := agentTarget(agent)
		if _, statErr := os.Stat(filepath.Join(dir, named.path)); statErr != nil {
			t.Fatalf("deselected surface %s was removed: %v", named.path, statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, claudeSettings)); statErr != nil {
		t.Fatalf("deselected Claude settings were removed: %v", statErr)
	}
}

func TestApplyDoesNotResolveCarriedClaudeSettingsForDeselectedAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	modes := testRunnerModes(t)
	if _, err = Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude", "codex"},
		RunnerModes:       modes,
		ClaudePermissions: ClaudePermissionsYes,
	}); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(dir, filepath.FromSlash(claudeSettings))
	if err = os.Remove(settingsPath); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("missing-settings.json", settingsPath); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		RunnerModes:     modes,
	})
	if err != nil {
		t.Fatalf("Apply() resolved carried Claude settings for a deselected agent: %v", err)
	}
	if containsPath(result.Paths, settingsPath) {
		t.Fatalf("Apply() planned a carried Claude settings edit: %#v", result.Paths)
	}
	info, err := os.Lstat(settingsPath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("carried Claude settings symlink changed: %#v, %v", info, err)
	}
	manifest, _ := readManagedManifest(t, dir)
	for _, surface := range manifest.Surfaces {
		if surface.Kind == manifestKindClaudeSettings && surface.Path == claudeSettings {
			return
		}
	}
	t.Fatalf("manifest dropped carried Claude settings: %#v", manifest.Surfaces)
}

func TestApplyRejectsShellPermissionChangeWithExcludedRecordedSurface(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		target     ShellPermission
		wantReject bool
	}{
		{name: "changed choice", target: ShellPermissionAsk, wantReject: true},
		{name: "unchanged choice", target: ShellPermissionAllow},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			modes := testRunnerModes(t)
			if _, err := Apply(Options{
				ShellPermission:   ShellPermissionAllow,
				Dir:               dir,
				Agents:            []string{"claude"},
				WriteMCPConfig:    true,
				RunnerModes:       modes,
				ClaudePermissions: ClaudePermissionsYes,
			}); err != nil {
				t.Fatal(err)
			}
			manifestPath := filepath.Join(dir, manifestFile)
			_, manifestBefore := readManagedManifest(t, dir)
			settingsPath := filepath.Join(dir, claudeSettings)
			settingsBefore, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatal(err)
			}

			_, err = Apply(Options{
				ShellPermission: testCase.target,
				Dir:             dir,
				Agents:          []string{"codex"},
				WriteMCPConfig:  true,
				RunnerModes:     modes,
			})
			if !testCase.wantReject {
				if err != nil {
					t.Fatalf("unchanged narrow Apply() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("shell-permission-changing narrow Apply() error = nil")
			}
			for _, want := range []string{
				"--agents codex",
				claudeSettings,
				"--agents claude,codex",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("shell-permission refusal does not contain %q: %v", want, err)
				}
			}
			if strings.Contains(err.Error(), "--write-mcp-config") {
				t.Fatalf("shell-permission refusal has obsolete MCP-config suffix: %v", err)
			}
			manifestAfter, readErr := os.ReadFile(manifestPath)
			if readErr != nil || !bytes.Equal(manifestAfter, manifestBefore) {
				t.Fatalf("refused shell permission changed manifest: %v", readErr)
			}
			settingsAfter, readErr := os.ReadFile(settingsPath)
			if readErr != nil || !bytes.Equal(settingsAfter, settingsBefore) {
				t.Fatalf("refused shell permission changed Claude settings: %v", readErr)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(statErr) {
				t.Fatalf("refused shell permission wrote AGENTS.md: %v", statErr)
			}
		})
	}
}

func TestApplyRejectsShellPermissionChangeAfterNarrowCarryForward(t *testing.T) {
	dir := t.TempDir()
	modes := testRunnerModes(t)
	confirmed := false
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAllow,
		Dir:             dir,
		WriteMCPConfig:  true,
		RunnerModes:     modes,
		Confirm: func(
			permission ShellPermission,
			_ string,
			_ string,
		) (bool, error) {
			confirmed = true
			if permission != ShellPermissionAllow {
				t.Fatalf(
					"confirmation shell permission = %q, want %q",
					permission,
					ShellPermissionAllow,
				)
			}
			return true, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if !confirmed {
		t.Fatal("initial Apply() did not confirm default Claude permissions")
	}

	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAllow,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     modes,
	}); err != nil {
		t.Fatalf("unchanged narrow Apply() error = %v, want nil", err)
	}
	_, manifestBeforeRefusal := readManagedManifest(t, dir)
	settingsPath := filepath.Join(dir, claudeSettings)
	settingsBeforeRefusal, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(dir, codexConfig)
	codexBeforeRefusal, err := os.ReadFile(codexPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     modes,
	})
	wantError := "cannot change shell permission with --agents codex: managed permission " +
		"files outside the selection: " + claudeSettings +
		"; re-run with --agents claude,codex"
	if err == nil || err.Error() != wantError {
		t.Fatalf("third Apply() error = %v, want %q", err, wantError)
	}

	manifestAfterRefusal, readErr := os.ReadFile(filepath.Join(dir, manifestFile))
	if readErr != nil || !bytes.Equal(manifestAfterRefusal, manifestBeforeRefusal) {
		t.Fatalf("refused shell permission changed manifest: %v", readErr)
	}
	settingsAfterRefusal, readErr := os.ReadFile(settingsPath)
	if readErr != nil || !bytes.Equal(settingsAfterRefusal, settingsBeforeRefusal) {
		t.Fatalf("refused shell permission changed Claude settings: %v", readErr)
	}
	codexAfterRefusal, readErr := os.ReadFile(codexPath)
	if readErr != nil || !bytes.Equal(codexAfterRefusal, codexBeforeRefusal) {
		t.Fatalf("refused shell permission changed Codex config: %v", readErr)
	}
}

func TestApplyShellPermissionGuardTreatsMissingRecordedValueAsAsk(t *testing.T) {
	dir := t.TempDir()
	modes := testRunnerModes(t)
	if _, err := Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude"},
		RunnerModes:       modes,
		ClaudePermissions: ClaudePermissionsYes,
	}); err != nil {
		t.Fatal(err)
	}
	manifest, _ := readManagedManifest(t, dir)
	manifest.ShellPermission = ""
	writeJSONFile(t, filepath.Join(dir, manifestFile), manifest)

	_, err := Apply(Options{
		ShellPermission: ShellPermissionAllow,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     modes,
	})
	if err == nil || !strings.Contains(err.Error(), claudeSettings) {
		t.Fatalf("legacy shell-permission change error = %v, want excluded %s", err, claudeSettings)
	}
}

func TestApplyChangesShellPermissionWhileRemovingCodexConfig(t *testing.T) {
	dir := t.TempDir()
	modes := testRunnerModes(t)
	if _, err := Apply(Options{
		ShellPermission:   ShellPermissionAllow,
		Dir:               dir,
		Agents:            []string{"claude", "codex"},
		WriteMCPConfig:    true,
		RunnerModes:       modes,
		ClaudePermissions: ClaudePermissionsYes,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude"},
		WriteMCPConfig:    false,
		RunnerModes:       modes,
		ClaudePermissions: ClaudePermissionsYes,
	})
	if err != nil {
		t.Fatalf("shell-permission-changing cleanup Apply() error = %v, want nil", err)
	}
	manifest, _ := readManagedManifest(t, dir)
	if manifest.ShellPermission != string(ShellPermissionAsk) {
		t.Fatalf(
			"cleanup manifest shell permission = %q, want %q",
			manifest.ShellPermission,
			ShellPermissionAsk,
		)
	}
	codexPath := filepath.Join(dir, codexConfig)
	if _, statErr := os.Stat(codexPath); !os.IsNotExist(statErr) {
		t.Fatalf("removed Codex config still exists: %v", statErr)
	}
	if !containsPath(result.Paths, resolvedTestPath(t, codexPath)) {
		t.Fatalf("cleanup paths = %#v, missing %s", result.Paths, codexPath)
	}
}

func TestApplyCarriesShellPermissionAcrossSurfaceFreeRun(t *testing.T) {
	dir := t.TempDir()
	modes := testRunnerModes(t)
	if _, err := Apply(Options{
		ShellPermission:   ShellPermissionAllow,
		Dir:               dir,
		Agents:            []string{"claude"},
		RunnerModes:       modes,
		ClaudePermissions: ClaudePermissionsYes,
	}); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(dir, claudeSettings)
	settingsBefore, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	surfaceFreeOptions := Options{
		Dir:               dir,
		Agents:            []string{"codex"},
		WriteMCPConfig:    false,
		RunnerModes:       modes,
		ClaudePermissions: ClaudePermissionsNo,
	}
	if _, applyErr := Apply(surfaceFreeOptions); applyErr != nil {
		t.Fatalf("permission-surface-free Apply() error = %v, want nil", applyErr)
	}
	manifest, manifestBeforeRepeat := readManagedManifest(t, dir)
	if manifest.ShellPermission != string(ShellPermissionAllow) {
		t.Fatalf(
			"carried shell permission = %q, want %q",
			manifest.ShellPermission,
			ShellPermissionAllow,
		)
	}
	assertManifestSurfaces(t, manifest.Surfaces, []struct {
		path string
		kind string
	}{
		{path: "AGENTS.md", kind: manifestKindAgentInstructions},
		{path: guideFile, kind: manifestKindAgentGuide},
		{path: claudeSettings, kind: manifestKindClaudeSettings},
	})

	repeated, err := Apply(surfaceFreeOptions)
	if err != nil {
		t.Fatalf("repeated permission-surface-free Apply() error = %v, want nil", err)
	}
	if len(repeated.Paths) != 0 {
		t.Fatalf("repeated permission-surface-free Apply() paths = %#v, want none", repeated.Paths)
	}
	_, manifestAfterRepeat := readManagedManifest(t, dir)
	if !bytes.Equal(manifestAfterRepeat, manifestBeforeRepeat) {
		t.Fatalf(
			"repeated permission-surface-free Apply() changed manifest bytes:\n%s\n%s",
			manifestBeforeRepeat,
			manifestAfterRepeat,
		)
	}

	_, err = Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		RunnerModes:     modes,
	})
	if err == nil || !strings.Contains(err.Error(), claudeSettings) {
		t.Fatalf("later shell-permission change error = %v, want excluded %s", err, claudeSettings)
	}
	manifestAfterRefusal, readErr := os.ReadFile(filepath.Join(dir, manifestFile))
	if readErr != nil || !bytes.Equal(manifestAfterRefusal, manifestBeforeRepeat) {
		t.Fatalf("refused shell permission changed carried manifest: %v", readErr)
	}
	settingsAfterRefusal, readErr := os.ReadFile(settingsPath)
	if readErr != nil || !bytes.Equal(settingsAfterRefusal, settingsBefore) {
		t.Fatalf("refused shell permission changed Claude settings: %v", readErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, codexConfig)); !os.IsNotExist(statErr) {
		t.Fatalf("refused shell permission wrote Codex config: %v", statErr)
	}
}

func TestApplyWriteMCPConfigFalseOmitsConfigSurfaces(t *testing.T) {
	dir := t.TempDir()
	modes := testRunnerModes(t)
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, WriteMCPConfig: true, RunnerModes: modes,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Options{
		Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: false, RunnerModes: modes,
	}); err != nil {
		t.Fatal(err)
	}
	manifest, _ := readManagedManifest(t, dir)
	if manifest.ShellPermission != "" {
		t.Fatalf("manifest shell permission = %q, want omitted", manifest.ShellPermission)
	}
	assertManifestSurfaces(t, manifest.Surfaces, []struct {
		path string
		kind string
	}{
		{path: "AGENTS.md", kind: manifestKindAgentInstructions},
		{path: guideFile, kind: manifestKindAgentGuide},
	})
	for _, relative := range []string{mcpConfig, codexConfig} {
		if _, statErr := os.Stat(filepath.Join(dir, relative)); !os.IsNotExist(statErr) {
			t.Fatalf("removed config %s still exists: %v", relative, statErr)
		}
	}
}

func TestManifestHashesIgnoreForeignJSONContent(t *testing.T) {
	dir := t.TempDir()
	options := Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               dir,
		Agents:            []string{"claude", "codex"},
		WriteMCPConfig:    true,
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	before, _ := readManagedManifest(t, dir)

	mcpPath := filepath.Join(dir, mcpConfig)
	var mcpDocument map[string]any
	readJSONFile(t, mcpPath, &mcpDocument)
	servers, ok := mcpDocument["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers = %#v, want object", mcpDocument["mcpServers"])
	}
	servers["foreign"] = map[string]any{"command": "foreign", "args": []string{"serve"}}
	mcpDocument["foreignTopLevel"] = []string{"kept", "local"}
	writeJSONFile(t, mcpPath, mcpDocument)

	settingsPath := filepath.Join(dir, claudeSettings)
	var settingsDocument map[string]any
	readJSONFile(t, settingsPath, &settingsDocument)
	permissions, ok := settingsDocument["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions = %#v, want object", settingsDocument["permissions"])
	}
	allow, ok := permissions["allow"].([]any)
	if !ok {
		t.Fatalf("allow = %#v, want list", permissions["allow"])
	}
	permissions["allow"] = append([]any{"Bash(git status:*)"}, allow...)
	permissions["deny"] = []string{"Bash(rm:*)"}
	settingsDocument["model"] = "foreign-model"
	writeJSONFile(t, settingsPath, settingsDocument)

	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	after, _ := readManagedManifest(t, dir)
	for _, relative := range []string{mcpConfig, claudeSettings} {
		beforeHash := manifestSurfaceByPath(t, before.Surfaces, relative).SHA256
		afterHash := manifestSurfaceByPath(t, after.Surfaces, relative).SHA256
		if afterHash != beforeHash {
			t.Fatalf("foreign content changed %s hash: before %q, after %q", relative, beforeHash, afterHash)
		}
	}
}

func TestManifestHashNormalizesCRLFManagedBlock(t *testing.T) {
	dirs := []string{t.TempDir(), t.TempDir()}
	lineBreaks := []string{"\n", "\r\n"}
	hashes := make([]string, len(dirs))
	for index, dir := range dirs {
		if err := os.WriteFile(
			filepath.Join(dir, "AGENTS.md"),
			[]byte("# Existing"+lineBreaks[index]),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(Options{
			ShellPermission: ShellPermissionAsk,
			Dir:             dir, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
		}); err != nil {
			t.Fatal(err)
		}
		manifest, _ := readManagedManifest(t, dir)
		hashes[index] = manifestSurfaceByPath(t, manifest.Surfaces, "AGENTS.md").SHA256
	}
	if hashes[0] != hashes[1] {
		t.Fatalf("LF hash %q differs from CRLF hash %q", hashes[0], hashes[1])
	}
}

func TestManifestOmitsRemovedOrDeclinedClaudePermissions(t *testing.T) {
	for _, testCase := range []struct {
		confirm     func(ShellPermission, string, string) (bool, error)
		name        string
		permissions ClaudePermissions
	}{
		{name: "no", permissions: ClaudePermissionsNo},
		{
			name:        "declined",
			permissions: ClaudePermissionsAsk,
			confirm: func(ShellPermission, string, string) (bool, error) {
				return false, nil
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			modes := testRunnerModes(t)
			if _, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir, Agents: []string{"claude"}, RunnerModes: modes,
				ClaudePermissions: ClaudePermissionsYes,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             dir, Agents: []string{"claude"}, RunnerModes: modes,
				ClaudePermissions: testCase.permissions, Confirm: testCase.confirm,
			}); err != nil {
				t.Fatal(err)
			}
			manifest, _ := readManagedManifest(t, dir)
			if manifest.ShellPermission != "" {
				t.Fatalf(
					"manifest shell permission = %q without Claude settings",
					manifest.ShellPermission,
				)
			}
			assertManifestSurfaces(t, manifest.Surfaces, []struct {
				path string
				kind string
			}{
				{path: "CLAUDE.md", kind: manifestKindAgentInstructions},
				{path: guideFile, kind: manifestKindAgentGuide},
			})
		})
	}
}

func TestManifestReleaseIsContextOnly(t *testing.T) {
	previous := version.Version
	version.Version = "v9.8.7"
	t.Cleanup(func() { version.Version = previous })

	dir := t.TempDir()
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
	}); err != nil {
		t.Fatalf("Apply rejected release context: %v", err)
	}
	manifest, _ := readManagedManifest(t, dir)
	if manifest.Release != version.Current().Display() {
		t.Fatalf("release = %q, want context %q", manifest.Release, version.Current().Display())
	}
}

func readManagedManifest(t *testing.T, dir string) (managedManifest, []byte) {
	t.Helper()
	path := filepath.Join(dir, manifestFile)
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest managedManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode manifest: %v\n%s", err, data)
	}
	return manifest, data
}

func assertManifestSurfaces(
	t *testing.T,
	got []manifestSurface,
	want []struct {
		path string
		kind string
	},
) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("manifest surfaces = %#v, want %#v", got, want)
	}
	for index, expected := range want {
		if got[index].Path != expected.path || got[index].Kind != expected.kind {
			t.Fatalf("surface %d = %#v, want path %q kind %q", index, got[index], expected.path, expected.kind)
		}
	}
}

func manifestSurfaceByPath(
	t *testing.T,
	surfaces []manifestSurface,
	relativePath string,
) manifestSurface {
	t.Helper()
	for _, surface := range surfaces {
		if surface.Path == relativePath {
			return surface
		}
	}
	t.Fatalf("manifest has no surface %q: %#v", relativePath, surfaces)
	return manifestSurface{}
}

func expectedSurfaceHash(t *testing.T, dir string, surface manifestSurface) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(surface.Path))
	// #nosec G304 -- path is selected from this test's temporary manifest.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fragment []byte
	switch surface.Kind {
	case manifestKindAgentInstructions:
		fragment = blockFragmentForTest(t, data, managedBlockRange)
	case manifestKindAgentGuide:
		fragment = data
	case manifestKindCodexConfig:
		fragment = blockFragmentForTest(t, data, codexBlockRange)
	case manifestKindMCPConfig:
		var document struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if decodeErr := json.Unmarshal(data, &document); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		var entry any
		if decodeErr := json.Unmarshal(document.MCPServers[serverName], &entry); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		fragment, err = json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
	case manifestKindClaudeSettings:
		var document struct {
			Permissions map[string][]string `json:"permissions"`
		}
		if decodeErr := json.Unmarshal(data, &document); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		managed := make(map[string][]string)
		for name, entries := range document.Permissions {
			entries = managedClaudeEntries(entries)
			if len(entries) > 0 {
				managed[name] = entries
			}
		}
		fragment, err = json.Marshal(managed)
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown manifest surface kind %q", surface.Kind)
	}
	normalized := bytes.ReplaceAll(fragment, []byte("\r\n"), []byte("\n"))
	normalized = bytes.TrimSuffix(normalized, []byte("\n"))
	digest := sha256.Sum256(normalized)
	return hex.EncodeToString(digest[:])
}

func blockFragmentForTest(t *testing.T, data []byte, find manifestBlockRange) []byte {
	t.Helper()
	start, end, found, err := find(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("managed block not found")
	}
	return data[start:end]
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	// #nosec G304 -- path is created in this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "    ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestManifestPrecedesPolicyInResultOrder(t *testing.T) {
	dir := t.TempDir()
	result, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantTail := []string{
		resolvedTestPath(t, filepath.Join(dir, manifestFile)),
		policy.Path(dir),
	}
	if len(result.Paths) < len(wantTail) ||
		result.Paths[len(result.Paths)-2] != wantTail[0] ||
		result.Paths[len(result.Paths)-1] != wantTail[1] {
		t.Fatalf("result paths = %#v, want manifest then policy tail %#v", result.Paths, wantTail)
	}
}

func TestApplyUsesResolvedManagedManifestPath(t *testing.T) {
	dir := t.TempDir()
	stateDirectory := filepath.Join(dir, "state")
	if err := os.Mkdir(stateDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	manifestDirectory := filepath.Join(dir, filepath.Dir(manifestFile))
	if err := os.Symlink(stateDirectory, manifestDirectory); err != nil {
		t.Skipf("create managed state directory symlink: %v", err)
	}

	options := Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             dir, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
	}
	result, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	resolvedManifest := resolvedTestPath(
		t,
		filepath.Join(stateDirectory, filepath.Base(manifestFile)),
	)
	if !containsPath(result.Paths, resolvedManifest) {
		t.Fatalf("Apply() paths = %#v, want resolved manifest %s", result.Paths, resolvedManifest)
	}
	if _, readErr := os.ReadFile(resolvedManifest); readErr != nil {
		t.Fatalf("read resolved manifest: %v", readErr)
	}
	resolvedGuide := resolvedTestPath(
		t,
		filepath.Join(stateDirectory, filepath.Base(guideFile)),
	)
	if !containsPath(result.Paths, resolvedGuide) {
		t.Fatalf("Apply() paths = %#v, want resolved guide %s", result.Paths, resolvedGuide)
	}
	guide, readErr := os.ReadFile(resolvedGuide)
	if readErr != nil || string(guide) != promptText+"\n" {
		t.Fatalf("resolved guide = %q, %v, want promptText plus newline", guide, readErr)
	}

	second, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Paths) != 0 {
		t.Fatalf("second Apply() paths = %#v, want none", second.Paths)
	}
}

func TestReadRecordedBetaTestTreatsNoManifestAsPlain(t *testing.T) {
	got, known, err := ReadRecordedBetaTest(t.TempDir())
	if err != nil {
		t.Fatalf("ReadRecordedBetaTest() error = %v, want nil", err)
	}
	if got || !known {
		t.Fatalf("ReadRecordedBetaTest() = (%t, %t), want (false, true)", got, known)
	}
}

func TestReadRecordedBetaTestReportsMalformedManifestAsUnknown(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, manifestFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, known, err := ReadRecordedBetaTest(root)
	if err != nil {
		t.Fatalf("ReadRecordedBetaTest() error = %v, want nil", err)
	}
	if got || known {
		t.Fatalf("ReadRecordedBetaTest() = (%t, %t), want (false, false)", got, known)
	}
}

func TestReadRecordedBetaTestReportsUnsupportedSchemaAsUnknown(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, manifestFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(
		t,
		path,
		managedManifest{
			SchemaVersion: manifestSchemaVersion + 1,
			BetaTest:      true,
		},
	)

	got, known, err := ReadRecordedBetaTest(root)
	if err != nil {
		t.Fatalf("ReadRecordedBetaTest() error = %v, want nil", err)
	}
	if got || known {
		t.Fatalf("ReadRecordedBetaTest() = (%t, %t), want (false, false)", got, known)
	}
}

func TestReadRecordedBetaTestReturnsHealthyBetaMode(t *testing.T) {
	root := t.TempDir()
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             root,
		Agents:          []string{"codex"},
		BetaTest:        true,
		RunnerModes:     testRunnerModes(t),
	}); err != nil {
		t.Fatal(err)
	}

	got, known, err := ReadRecordedBetaTest(root)
	if err != nil {
		t.Fatalf("ReadRecordedBetaTest() error = %v, want nil", err)
	}
	if !got || !known {
		t.Fatalf("ReadRecordedBetaTest() = (%t, %t), want (true, true)", got, known)
	}
}

func TestReadRecordedBetaTestReturnsHealthyPlainMode(t *testing.T) {
	root := applyVerificationWorkspace(t)

	got, known, err := ReadRecordedBetaTest(root)
	if err != nil {
		t.Fatalf("ReadRecordedBetaTest() error = %v, want nil", err)
	}
	if got || !known {
		t.Fatalf("ReadRecordedBetaTest() = (%t, %t), want (false, true)", got, known)
	}
}

func TestReadRecordedBetaTestReturnsManifestIOError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, manifestFile)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := ReadRecordedBetaTest(root); err == nil {
		t.Fatal("ReadRecordedBetaTest() error = nil, want manifest I/O error")
	}
}

func TestReadRecordedAIProfileUsesUnknownForLegacyManifest(t *testing.T) {
	root := applyVerificationWorkspace(t)
	manifest, _ := readManagedManifest(t, root)
	legacyDocument := map[string]any{
		"schema_version":   manifest.SchemaVersion,
		"release":          manifest.Release,
		"shell_permission": manifest.ShellPermission,
		"surfaces":         manifest.Surfaces,
	}
	writeJSONFile(t, filepath.Join(root, manifestFile), legacyDocument)

	profile, found, err := ReadRecordedAIProfile(root)
	if err != nil {
		t.Fatalf("ReadRecordedAIProfile() error = %v, want nil", err)
	}
	if !found || profile != aiprofile.Unknown() {
		t.Fatalf("ReadRecordedAIProfile() = (%#v, %t), want unknown current", profile, found)
	}
	if _, err = VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("VerifyManagedSurfaces() legacy AI family error = %v, want nil", err)
	}
}

func TestReadRecordedAIProfileRejectsUnusableManifest(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		want    string
		content []byte
	}{
		{name: "malformed", content: []byte("{not json"), want: "decode managed manifest"},
		{
			name: "empty family",
			content: []byte(
				`{"schema_version": 1, "ai_family": ""}`,
			),
			want: `unsupported AI family ""`,
		},
		{
			name:    "null family",
			content: []byte(`{"schema_version": 1, "ai_family": null}`),
			want:    "unsupported AI family null",
		},
		{
			name:    "unsupported schema",
			content: []byte(`{"schema_version": 2, "ai_family": "codex"}`),
			want:    "unsupported schema version 2",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, manifestFile)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, testCase.content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ReadRecordedAIProfile(root); err == nil ||
				!strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("ReadRecordedAIProfile() error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestApplyRecordsAIProfileInManagedServerArguments(t *testing.T) {
	root := t.TempDir()
	codex := mustParseAIProfile(t, string(aiprofile.FamilyCodex))
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             root,
		Agents:          []string{"codex"},
		WriteMCPConfig:  true,
		AIProfile:       codex,
		RunnerModes:     testRunnerModes(t),
	}); err != nil {
		t.Fatal(err)
	}

	manifest, _ := readManagedManifest(t, root)
	if manifest.AIFamily != aiprofile.FamilyCodex {
		t.Fatalf("manifest AI family = %q, want codex", manifest.AIFamily)
	}
	wantArgs := []string{"serve", "--root", root, "--ai", "codex"}
	for path, got := range map[string][]string{
		mcpConfig:   readJSONServerArgs(t, filepath.Join(root, mcpConfig)),
		codexConfig: readCodexServerArgs(t, filepath.Join(root, codexConfig)),
	} {
		if !slices.Equal(got, wantArgs) {
			t.Fatalf("%s server args = %#v, want %#v", path, got, wantArgs)
		}
	}
	recorded, found, err := ReadRecordedAIProfile(root)
	if err != nil || !found || recorded != codex {
		t.Fatalf("ReadRecordedAIProfile() = (%#v, %t, %v), want codex current", recorded, found, err)
	}
	if _, err = VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
	}
}

func TestVerifyManagedSurfacesRejectsUnknownAIFamily(t *testing.T) {
	root := applyVerificationWorkspace(t)
	manifest, _ := readManagedManifest(t, root)
	manifest.AIFamily = "gemini"
	writeJSONFile(t, filepath.Join(root, manifestFile), manifest)

	_, err := VerifyManagedSurfaces(root)
	if err == nil || !strings.Contains(err.Error(), `unsupported AI family "gemini"`) ||
		!strings.Contains(err.Error(), wantManagedManifestRecovery(root)) {
		t.Fatalf(
			"VerifyManagedSurfaces() error = %v, want unknown AI family and recovery",
			err,
		)
	}
	if _, _, err = ReadRecordedAIProfile(root); err == nil ||
		!strings.Contains(err.Error(), `unsupported AI family "gemini"`) {
		t.Fatalf("ReadRecordedAIProfile() error = %v, want unknown AI family", err)
	}
}

func TestVerifyManagedSurfacesAcceptsAndRejectsBetaBlock(t *testing.T) {
	root := t.TempDir()
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             root,
		Agents:          []string{"codex"},
		BetaTest:        true,
		RunnerModes:     testRunnerModes(t),
	}); err != nil {
		t.Fatal(err)
	}
	managedSurfaces, err := VerifyManagedSurfaces(root)
	if err != nil {
		t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
	}
	if !managedSurfaces.BetaTest {
		t.Fatal("VerifyManagedSurfaces() beta test = false, want true")
	}
	path := filepath.Join(root, "AGENTS.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(
		string(data),
		betaTestManagedBlockText,
		betaTestManagedBlockText+"\nhand edit",
		1,
	)
	//nolint:gosec // The test path is a fixed filename below t.TempDir().
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyManagedSurfaces(root); err == nil ||
		!strings.Contains(err.Error(), "was edited") {
		t.Fatalf("VerifyManagedSurfaces() error = %v, want edited beta block error", err)
	}
}

func TestVerifyManagedSurfacesRejectsSwitchedCanonicalBlocks(t *testing.T) {
	tests := []struct {
		name     string
		betaTest bool
	}{
		{
			name:     "beta workspace with plain block",
			betaTest: true,
		},
		{
			name:     "plain workspace with beta block",
			betaTest: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if _, err := Apply(Options{
				ShellPermission: ShellPermissionAsk,
				Dir:             root,
				Agents:          []string{"codex"},
				BetaTest:        test.betaTest,
				RunnerModes:     testRunnerModes(t),
			}); err != nil {
				t.Fatal(err)
			}
			managedSurfaces, err := VerifyManagedSurfaces(root)
			if err != nil {
				t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
			}
			if managedSurfaces.BetaTest != test.betaTest {
				t.Fatalf(
					"VerifyManagedSurfaces() beta test = %t, want %t",
					managedSurfaces.BetaTest,
					test.betaTest,
				)
			}
			path := filepath.Join(root, "AGENTS.md")
			if err := os.WriteFile(path, []byte(canonicalBlock(!test.betaTest)), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyManagedSurfaces(root); err == nil ||
				!strings.Contains(err.Error(), "was edited") {
				t.Fatalf("VerifyManagedSurfaces() error = %v, want edited block error", err)
			}
		})
	}
}

func TestVerifyManagedSurfacesAcceptsFreshWorkspaceAndNoManifest(t *testing.T) {
	t.Run("fresh workspace", func(t *testing.T) {
		root := applyVerificationWorkspace(t)
		managedSurfaces, err := VerifyManagedSurfaces(root)
		if err != nil {
			t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
		}
		wantGuidePath := resolvedTestPath(t, filepath.Join(root, guideFile))
		if managedSurfaces.BetaTest || managedSurfaces.AgentGuidePath != wantGuidePath {
			t.Fatalf("VerifyManagedSurfaces() = %#v, want plain mode with a guide", managedSurfaces)
		}
	})
	t.Run("managed block without manifest", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(
			filepath.Join(root, "AGENTS.md"),
			[]byte("# Local\n\n"+canonicalBlock(false)),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		managedSurfaces, err := VerifyManagedSurfaces(root)
		if err != nil {
			t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
		}
		if managedSurfaces.BetaTest || managedSurfaces.AgentGuidePath != "" {
			t.Fatalf("VerifyManagedSurfaces() = %#v, want no modes without a manifest", managedSurfaces)
		}
	})
}

func TestVerifyManagedSurfacesTreatsManifestWithoutBetaTestAsPlain(t *testing.T) {
	root := applyVerificationWorkspace(t)
	manifest, _ := readManagedManifest(t, root)
	legacyDocument := map[string]any{
		"schema_version": manifest.SchemaVersion,
		"release":        manifest.Release,
		"surfaces":       manifest.Surfaces,
	}
	writeJSONFile(t, filepath.Join(root, manifestFile), legacyDocument)

	managedSurfaces, err := VerifyManagedSurfaces(root)
	if err != nil {
		t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
	}
	if managedSurfaces.BetaTest {
		t.Fatal("VerifyManagedSurfaces() beta test = true without beta_test field, want false")
	}
}

func TestVerifyManagedSurfacesUsesRecordedShellPermission(t *testing.T) {
	for _, surface := range []string{"claude", "codex"} {
		for _, shellPermission := range []ShellPermission{
			ShellPermissionAsk,
			ShellPermissionAllow,
		} {
			t.Run(surface+"/"+string(shellPermission), func(t *testing.T) {
				root := t.TempDir()
				options := Options{
					ShellPermission: shellPermission,
					Dir:             root,
					Agents:          []string{surface},
					RunnerModes:     testRunnerModes(t),
				}
				if surface == "claude" {
					options.ClaudePermissions = ClaudePermissionsYes
				} else {
					options.WriteMCPConfig = true
				}
				if _, err := Apply(options); err != nil {
					t.Fatal(err)
				}
				manifest, _ := readManagedManifest(t, root)
				if manifest.ShellPermission != string(shellPermission) {
					t.Fatalf(
						"manifest shell permission = %q, want %q",
						manifest.ShellPermission,
						shellPermission,
					)
				}
				if _, err := VerifyManagedSurfaces(root); err != nil {
					t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
				}
			})
		}
	}
}

func TestVerifyManagedSurfacesTreatsManifestWithoutShellPermissionAsAsk(t *testing.T) {
	root := applyVerificationWorkspace(t)
	manifest, _ := readManagedManifest(t, root)
	legacyDocument := map[string]any{
		"schema_version": manifest.SchemaVersion,
		"release":        manifest.Release,
		"beta_test":      manifest.BetaTest,
		"surfaces":       manifest.Surfaces,
	}
	writeJSONFile(t, filepath.Join(root, manifestFile), legacyDocument)

	if _, err := VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("VerifyManagedSurfaces() legacy ask error = %v, want nil", err)
	}
}

func TestVerifyManagedSurfacesRejectsShellPermissionDisagreement(t *testing.T) {
	for _, surface := range []string{"claude", "codex"} {
		for _, shellPermission := range []ShellPermission{
			ShellPermissionAsk,
			ShellPermissionAllow,
		} {
			t.Run(surface+"/"+string(shellPermission), func(t *testing.T) {
				root := t.TempDir()
				options := Options{
					ShellPermission: shellPermission,
					Dir:             root,
					Agents:          []string{surface},
					RunnerModes:     testRunnerModes(t),
				}
				wantRelativePath := codexConfig
				if surface == "claude" {
					options.ClaudePermissions = ClaudePermissionsYes
					wantRelativePath = claudeSettings
				} else {
					options.WriteMCPConfig = true
				}
				if _, err := Apply(options); err != nil {
					t.Fatal(err)
				}
				manifest, _ := readManagedManifest(t, root)
				if shellPermission == ShellPermissionAsk {
					manifest.ShellPermission = string(ShellPermissionAllow)
				} else {
					manifest.ShellPermission = string(ShellPermissionAsk)
				}
				writeJSONFile(t, filepath.Join(root, manifestFile), manifest)

				_, err := VerifyManagedSurfaces(root)
				wantPath := resolvedTestPath(t, filepath.Join(root, wantRelativePath))
				if err == nil ||
					!strings.Contains(
						err.Error(),
						"generated configuration changed since it was written",
					) ||
					!strings.Contains(err.Error(), wantPath) {
					t.Fatalf(
						"VerifyManagedSurfaces() error = %v, want shell-choice refusal for %s",
						err,
						wantPath,
					)
				}
			})
		}
	}
}

func TestVerifyManagedSurfacesRejectsUnknownShellPermission(t *testing.T) {
	root := applyVerificationWorkspace(t)
	manifest, _ := readManagedManifest(t, root)
	manifest.ShellPermission = "sometimes"
	writeJSONFile(t, filepath.Join(root, manifestFile), manifest)

	_, err := VerifyManagedSurfaces(root)
	if err == nil ||
		!strings.Contains(err.Error(), "unsupported shell permission") ||
		!strings.Contains(err.Error(), wantManagedManifestRecovery(root)) {
		t.Fatalf(
			"VerifyManagedSurfaces() error = %v, want unknown shell permission and recovery",
			err,
		)
	}
}

func TestVerifyManagedSurfacesAcceptsManifestWithoutAgentGuide(t *testing.T) {
	root := applyVerificationWorkspace(t)
	manifest, _ := readManagedManifest(t, root)
	surfaces := make([]manifestSurface, 0, len(manifest.Surfaces)-1)
	for _, surface := range manifest.Surfaces {
		if surface.Kind != manifestKindAgentGuide {
			surfaces = append(surfaces, surface)
		}
	}
	if len(surfaces) != len(manifest.Surfaces)-1 {
		t.Fatalf("removed %d guide surfaces, want one", len(manifest.Surfaces)-len(surfaces))
	}
	manifest.Surfaces = surfaces
	writeJSONFile(t, filepath.Join(root, manifestFile), manifest)
	if err := os.Remove(filepath.Join(root, guideFile)); err != nil {
		t.Fatal(err)
	}

	managedSurfaces, err := VerifyManagedSurfaces(root)
	if err != nil {
		t.Fatalf("VerifyManagedSurfaces() rejected pre-guide manifest: %v", err)
	}
	if managedSurfaces.AgentGuidePath != "" {
		t.Fatalf("VerifyManagedSurfaces() = %#v, want no guide for a pre-guide manifest", managedSurfaces)
	}
}

func TestVerifyManagedSurfacesRequiresAgentGuideAtCanonicalPath(t *testing.T) {
	root := applyVerificationWorkspace(t)
	manifest, _ := readManagedManifest(t, root)
	for index := range manifest.Surfaces {
		if manifest.Surfaces[index].Kind != manifestKindAgentGuide {
			continue
		}
		manifest.Surfaces[index].Path = ".just-mcp-work/other-guide.md"
		if err := os.WriteFile(
			filepath.Join(root, filepath.FromSlash(manifest.Surfaces[index].Path)),
			[]byte(promptText+"\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		break
	}
	writeJSONFile(t, filepath.Join(root, manifestFile), manifest)

	managedSurfaces, err := VerifyManagedSurfaces(root)
	if err != nil {
		t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
	}
	if managedSurfaces.AgentGuidePath != "" {
		t.Fatalf("VerifyManagedSurfaces() = %#v, want no guide", managedSurfaces)
	}
}

func TestVerifyManagedSurfacesRejectsRecordedBetaModeWithPlainBlock(t *testing.T) {
	root := t.TempDir()
	if _, err := Apply(Options{
		ShellPermission: ShellPermissionAsk,
		Dir:             root,
		Agents:          []string{"codex"},
		BetaTest:        true,
		RunnerModes:     testRunnerModes(t),
	}); err != nil {
		t.Fatal(err)
	}
	manifest, _ := readManagedManifest(t, root)
	if !manifest.BetaTest {
		t.Fatal("manifest beta test = false, want true")
	}
	plainBlock := []byte(canonicalBlock(false))
	path := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(path, plainBlock, 0o600); err != nil {
		t.Fatal(err)
	}
	for index := range manifest.Surfaces {
		surface := &manifest.Surfaces[index]
		if surface.Path == "AGENTS.md" {
			surface.SHA256 = newManifestSurface(
				surface.Path,
				surface.Kind,
				plainBlock,
			).SHA256
		}
	}
	writeJSONFile(t, filepath.Join(root, manifestFile), manifest)

	_, err := VerifyManagedSurfaces(root)
	wantPath := resolvedTestPath(t, path)
	if err == nil ||
		!strings.Contains(err.Error(), "generated configuration changed since it was written") ||
		!strings.Contains(err.Error(), wantPath) {
		t.Fatalf(
			"VerifyManagedSurfaces() error = %v, want generated-change refusal for %s",
			err,
			wantPath,
		)
	}
}

func TestVerifyManagedSurfacesRejectsUntrustedManifestPaths(t *testing.T) {
	tests := []struct {
		path func(*testing.T, string, string) string
		name string
	}{
		{
			name: "absolute",
			path: func(_ *testing.T, _ string, outside string) string {
				return filepath.ToSlash(outside)
			},
		},
		{
			name: "parent element escapes",
			path: func(t *testing.T, root string, outside string) string {
				t.Helper()
				relative, err := filepath.Rel(root, outside)
				if err != nil {
					t.Fatal(err)
				}
				return filepath.ToSlash(relative)
			},
		},
		{
			name: "parent element stays inside",
			path: func(_ *testing.T, _ string, _ string) string {
				return "nested/../AGENTS.md"
			},
		},
		{
			name: "symlink resolves outside",
			path: func(t *testing.T, root string, outside string) string {
				t.Helper()
				if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
					t.Skipf("create escaping managed surface symlink: %v", err)
				}
				return "escape"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := applyVerificationWorkspace(t)
			outside := filepath.Join(t.TempDir(), "outside.md")
			if err := os.WriteFile(outside, []byte(canonicalBlock(false)), 0o600); err != nil {
				t.Fatal(err)
			}
			manifest, _ := readManagedManifest(t, root)
			surface := manifestSurfaceByPath(t, manifest.Surfaces, "AGENTS.md")
			surface.Path = test.path(t, root, outside)
			manifest.Surfaces = []manifestSurface{surface}
			writeJSONFile(t, filepath.Join(root, manifestFile), manifest)

			_, err := VerifyManagedSurfaces(root)
			manifestPath := filepath.Join(root, manifestFile)
			wantRecovery := wantManagedManifestRecovery(root)
			if err == nil ||
				!strings.Contains(err.Error(), "managed manifest "+manifestPath+" is unusable") ||
				!strings.Contains(err.Error(), wantRecovery) ||
				strings.Contains(err.Error(), "managed configuration in") {
				t.Fatalf(
					"VerifyManagedSurfaces() error = %v, want unusable manifest and recovery",
					err,
				)
			}
		})
	}
}

//nolint:gocyclo // The table deliberately exercises missing and edited states for every kind.
func TestVerifyManagedSurfacesRejectsEditedMissingAndDeletedContent(t *testing.T) {
	tests := []struct {
		name         string
		relativePath string
		change       func(*testing.T, string)
		wantText     string
		wantGuidance string
	}{
		{
			name:         "edited agent instructions",
			relativePath: "AGENTS.md",
			change: func(t *testing.T, root string) {
				t.Helper()
				path := filepath.Join(root, "AGENTS.md")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				edited := strings.Replace(
					string(data),
					managedBlockText,
					managedBlockText+"\nlocal edit inside managed block",
					1,
				)
				// #nosec G703 -- path is created in this test's temporary directory.
				if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantText:     "was edited",
			wantGuidance: "keep your own text outside them",
		},
		{
			name:         "edited agent guide",
			relativePath: guideFile,
			change: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(root, guideFile),
					[]byte("truncated\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
			wantText:     "was edited",
			wantGuidance: "owns that whole file",
		},
		{
			name:         "edited Codex block",
			relativePath: codexConfig,
			change: func(t *testing.T, root string) {
				t.Helper()
				path := filepath.Join(root, codexConfig)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				edited := bytes.Replace(
					data,
					[]byte("startup_timeout_sec = 120"),
					[]byte("startup_timeout_sec = 121"),
					1,
				)
				if bytes.Equal(edited, data) {
					t.Fatal("Codex managed block did not contain startup timeout")
				}
				// #nosec G703 -- path is a fixed config path under the test's temporary root.
				if err := os.WriteFile(path, edited, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantText:     "was edited",
			wantGuidance: "keep your own text outside them",
		},
		{
			name:         "edited MCP server entry",
			relativePath: mcpConfig,
			change: func(t *testing.T, root string) {
				t.Helper()
				path := filepath.Join(root, mcpConfig)
				var document map[string]any
				readJSONFile(t, path, &document)
				servers, ok := document["mcpServers"].(map[string]any)
				if !ok {
					t.Fatalf("mcpServers = %#v, want object", document["mcpServers"])
				}
				entry, ok := servers[serverName].(map[string]any)
				if !ok {
					t.Fatalf("managed server = %#v, want object", servers[serverName])
				}
				entry["args"] = []string{"serve", "--root", root, "edited"}
				writeJSONFile(t, path, document)
			},
			wantText:     "was edited",
			wantGuidance: "keep your own entries separate",
		},
		{
			name:         "edited Claude permission entry",
			relativePath: claudeSettings,
			change: func(t *testing.T, root string) {
				t.Helper()
				path := filepath.Join(root, claudeSettings)
				var document map[string]any
				readJSONFile(t, path, &document)
				permissions, ok := document["permissions"].(map[string]any)
				if !ok {
					t.Fatalf("permissions = %#v, want object", document["permissions"])
				}
				allow, ok := permissions["allow"].([]any)
				if !ok {
					t.Fatalf("permissions.allow = %#v, want list", permissions["allow"])
				}
				for index, entry := range allow {
					value, isString := entry.(string)
					if !isString || !isManagedClaudeTool(value) {
						continue
					}
					permissions["allow"] = append(allow[:index:index], allow[index+1:]...)
					writeJSONFile(t, path, document)
					return
				}
				t.Fatal("permissions.allow has no managed entry")
			},
			wantText:     "was edited",
			wantGuidance: "keep your own entries separate",
		},
		{
			name:         "missing managed block",
			relativePath: "AGENTS.md",
			change: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(root, "AGENTS.md"),
					[]byte("# Local content only\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
			wantText: "is missing",
		},
		{
			name:         "deleted managed file",
			relativePath: "AGENTS.md",
			change: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, "AGENTS.md")); err != nil {
					t.Fatal(err)
				}
			},
			wantText: "is missing",
		},
		{
			name:         "deleted agent guide",
			relativePath: guideFile,
			change: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, guideFile)); err != nil {
					t.Fatal(err)
				}
			},
			wantText: "is missing",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := applyVerificationWorkspace(t)
			test.change(t, root)
			_, err := VerifyManagedSurfaces(root)
			wantPath := resolvedTestPath(
				t,
				filepath.Join(root, filepath.FromSlash(test.relativePath)),
			)
			wantRecovery := wantManagedManifestRecovery(root)
			if err == nil ||
				!strings.Contains(err.Error(), "managed configuration in "+wantPath) ||
				!strings.Contains(err.Error(), test.wantText) ||
				!strings.Contains(err.Error(), wantRecovery) {
				t.Fatalf(
					"VerifyManagedSurfaces() error = %v, want %q, %q, and %q",
					err,
					wantPath,
					test.wantText,
					wantRecovery,
				)
			}
			if test.wantGuidance != "" && !strings.Contains(err.Error(), test.wantGuidance) {
				t.Fatalf("edited-surface error has no %q guidance: %v", test.wantGuidance, err)
			}
		})
	}
}

func TestVerifyManagedSurfacesAcceptsManagedBlockWithoutFinalNewline(t *testing.T) {
	root := applyVerificationWorkspace(t)
	path := filepath.Join(root, codexConfig)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatalf("generated %s has no final newline", path)
	}
	withoutFinalNewline := bytes.TrimSuffix(data, []byte("\n"))
	// #nosec G703 -- path is a fixed config path under the test's temporary root.
	if writeErr := os.WriteFile(path, withoutFinalNewline, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, verifyErr := VerifyManagedSurfaces(root); verifyErr != nil {
		t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", verifyErr)
	}

	result, err := Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               root,
		Agents:            []string{"claude", "codex"},
		WriteMCPConfig:    true,
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Paths) != 0 {
		t.Fatalf("Apply() paths = %#v after boundary-only change, want none", result.Paths)
	}
}

func TestVerifyManagedSurfacesRejectsChangesInsideOwnedJSONEntries(t *testing.T) {
	tests := []struct {
		change       func(*testing.T, string, string)
		name         string
		relativePath string
	}{
		{
			name:         "extra MCP server member",
			relativePath: mcpConfig,
			change: func(t *testing.T, _ string, path string) {
				t.Helper()
				var document map[string]any
				readJSONFile(t, path, &document)
				servers, ok := document["mcpServers"].(map[string]any)
				if !ok {
					t.Fatalf("mcpServers = %#v, want object", document["mcpServers"])
				}
				entry, ok := servers[serverName].(map[string]any)
				if !ok {
					t.Fatalf("managed server = %#v, want object", servers[serverName])
				}
				entry["env"] = map[string]string{"LOCAL": "edited"}
				writeJSONFile(t, path, document)
			},
		},
		{
			name:         "managed Claude entry in deny",
			relativePath: claudeSettings,
			change: func(t *testing.T, _ string, path string) {
				t.Helper()
				var document map[string]any
				readJSONFile(t, path, &document)
				permissions, ok := document["permissions"].(map[string]any)
				if !ok {
					t.Fatalf("permissions = %#v, want object", document["permissions"])
				}
				managed := testClaudeManagedTools(t, ShellPermissionAsk)
				if len(managed.Allow) == 0 {
					t.Fatal("ClaudeManagedTools().Allow is empty")
				}
				permissions["deny"] = []string{managed.Allow[0]}
				writeJSONFile(t, path, document)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := applyVerificationWorkspace(t)
			path := resolvedTestPath(
				t,
				filepath.Join(root, filepath.FromSlash(test.relativePath)),
			)
			test.change(t, root, path)
			_, err := VerifyManagedSurfaces(root)
			if err == nil ||
				!strings.Contains(err.Error(), "managed configuration in "+path+" was edited") ||
				!strings.Contains(err.Error(), "keep your own entries separate") {
				t.Fatalf(
					"VerifyManagedSurfaces() error = %v, want owned-entry refusal for %s",
					err,
					path,
				)
			}
		})
	}
}

func TestVerifyManagedSurfacesReportsMalformedManagedConfiguration(t *testing.T) {
	tests := []struct {
		name         string
		relativePath string
		change       func(*testing.T, string)
		wantCause    string
	}{
		{
			name:         "invalid MCP JSON",
			relativePath: mcpConfig,
			change: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantCause: "decode managed .mcp.json entry",
		},
		{
			name:         "duplicated instruction markers",
			relativePath: "AGENTS.md",
			change: func(t *testing.T, path string) {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = append(data, []byte(canonicalBlock(false))...)
				// #nosec G703 -- path is a fixed config path under the test's temporary root.
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantCause: "managed block markers are malformed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := applyVerificationWorkspace(t)
			path := resolvedTestPath(
				t,
				filepath.Join(root, filepath.FromSlash(test.relativePath)),
			)
			test.change(t, path)
			_, err := VerifyManagedSurfaces(root)
			wantRecovery := wantManagedManifestRecovery(root)
			if err == nil ||
				!strings.Contains(err.Error(), "managed configuration in "+path+" is malformed") ||
				!strings.Contains(err.Error(), test.wantCause) ||
				!strings.Contains(err.Error(), wantRecovery) ||
				strings.Contains(err.Error(), "was edited") {
				t.Fatalf(
					"VerifyManagedSurfaces() error = %v, want malformed cause %q and recovery",
					err,
					test.wantCause,
				)
			}
		})
	}
}

func TestVerifyManagedSurfacesRejectsChangedGeneratedConfigurationOnce(t *testing.T) {
	root := applyVerificationWorkspace(t)
	manifest, _ := readManagedManifest(t, root)
	manifest.Surfaces[0].SHA256 = strings.Repeat("0", 64)
	writeJSONFile(t, filepath.Join(root, manifestFile), manifest)

	_, err := VerifyManagedSurfaces(root)
	wantPath := resolvedTestPath(
		t,
		filepath.Join(root, filepath.FromSlash(manifest.Surfaces[0].Path)),
	)
	if err == nil ||
		!strings.Contains(err.Error(), "generated configuration changed since it was written") ||
		!strings.Contains(err.Error(), wantPath) ||
		!strings.Contains(err.Error(), "recorded by just-mcp-work "+manifest.Release) ||
		strings.Count(err.Error(), "generated configuration changed") != 1 {
		t.Fatalf(
			"VerifyManagedSurfaces() error = %v, want one generated-change refusal for %s",
			err,
			wantPath,
		)
	}
}

func TestVerifyManagedSurfacesAcceptsCRLFWorkspace(t *testing.T) {
	root := applyVerificationWorkspace(t)
	manifest, _ := readManagedManifest(t, root)
	converted := make(map[string]struct{}, len(manifest.Surfaces))
	for _, surface := range manifest.Surfaces {
		path := filepath.Join(root, filepath.FromSlash(surface.Path))
		if _, done := converted[path]; done {
			continue
		}
		converted[path] = struct{}{}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
		// #nosec G703 -- path is selected from this test's temporary manifest.
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("VerifyManagedSurfaces() CRLF error = %v, want nil", err)
	}
}

func TestVerifyManagedSurfacesRejectsUnreadableAndTooNewManifest(t *testing.T) {
	tests := []struct {
		name     string
		wantText string
		manifest []byte
	}{
		{name: "unparsable", manifest: []byte("{not json"), wantText: "is unreadable"},
		{
			name: "unknown schema",
			manifest: []byte(
				"{\"schema_version\":2,\"release\":\"future\",\"surfaces\":[]}",
			),
			wantText: "is too new",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, manifestFile)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, test.manifest, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := VerifyManagedSurfaces(root)
			wantRecovery := wantManagedManifestRecovery(root)
			if err == nil ||
				!strings.Contains(err.Error(), path) ||
				!strings.Contains(err.Error(), test.wantText) ||
				!strings.Contains(err.Error(), wantRecovery) {
				t.Fatalf(
					"VerifyManagedSurfaces() error = %v, want manifest path, %q, and recovery",
					err,
					test.wantText,
				)
			}
		})
	}
}

func TestVerifyManagedSurfacesTreatsReleaseAsContextOnly(t *testing.T) {
	root := applyVerificationWorkspace(t)
	manifest, _ := readManagedManifest(t, root)
	manifest.Release = "a different release"
	writeJSONFile(t, filepath.Join(root, manifestFile), manifest)
	if _, err := VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("VerifyManagedSurfaces() compared releases: %v", err)
	}
}

func TestVerifyManagedSurfacesDoesNotSearchParent(t *testing.T) {
	parent := applyVerificationWorkspace(t)
	manifest, _ := readManagedManifest(t, parent)
	manifest.Surfaces[0].SHA256 = strings.Repeat("0", 64)
	writeJSONFile(t, filepath.Join(parent, manifestFile), manifest)

	root := filepath.Join(parent, "nested")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("VerifyManagedSurfaces() searched above root: %v", err)
	}
}

func TestVerifyManagedSurfacesUsesOnlyProvidedRoot(t *testing.T) {
	root := applyVerificationWorkspace(t)
	ambientRoot := t.TempDir()
	ambientManifest := filepath.Join(ambientRoot, manifestFile)
	if err := os.MkdirAll(filepath.Dir(ambientManifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ambientManifest, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(ambientRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	previousArgs := os.Args
	os.Args = []string{"just-mcp-work", "serve", "--root", ambientRoot}
	t.Cleanup(func() { os.Args = previousArgs })
	t.Setenv("JMW_ROOT", ambientRoot)

	if _, err := VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("VerifyManagedSurfaces() used process state instead of root: %v", err)
	}
}

func applyVerificationWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if _, err := Apply(Options{
		ShellPermission:   ShellPermissionAsk,
		Dir:               root,
		Agents:            []string{"claude", "codex"},
		WriteMCPConfig:    true,
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	}); err != nil {
		t.Fatal(err)
	}
	return root
}
