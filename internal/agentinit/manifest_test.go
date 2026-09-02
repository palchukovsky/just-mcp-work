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
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/policy"
	"github.com/palchukovsky/just-mcp-work/internal/version"
)

func wantManagedManifestRecovery(root string) string {
	return `run just-mcp-work init --dir "` + root + `"`
}

//nolint:gocyclo // This test pins the manifest document, every surface, and idempotency together.
func TestApplyWritesManifestForEveryManagedSurfaceAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	options := Options{
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
		Dir:               dir,
		Agents:            allAgents,
		WriteMCPConfig:    true,
		RunnerModes:       testRunnerModes(t),
		ClaudePermissions: ClaudePermissionsYes,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Apply(Options{
		Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true, RunnerModes: testRunnerModes(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := resolvedTestPath(t, filepath.Join(dir, manifestFile))
	if len(result.Paths) != 1 || result.Paths[0] != manifestPath {
		t.Fatalf("narrowed apply paths = %#v, want only manifest", result.Paths)
	}
	manifest, _ := readManagedManifest(t, dir)
	want := []struct {
		path string
		kind string
	}{
		{path: "AGENTS.md", kind: manifestKindAgentInstructions},
		{path: mcpConfig, kind: manifestKindMCPConfig},
		{path: codexConfig, kind: manifestKindCodexConfig},
	}
	assertManifestSurfaces(t, manifest.Surfaces, want)
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

func TestApplyWriteMCPConfigFalseOmitsConfigSurfaces(t *testing.T) {
	dir := t.TempDir()
	modes := testRunnerModes(t)
	if _, err := Apply(Options{
		Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: true, RunnerModes: modes,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Options{
		Dir: dir, Agents: []string{"codex"}, WriteMCPConfig: false, RunnerModes: modes,
	}); err != nil {
		t.Fatal(err)
	}
	manifest, _ := readManagedManifest(t, dir)
	assertManifestSurfaces(t, manifest.Surfaces, []struct {
		path string
		kind string
	}{{path: "AGENTS.md", kind: manifestKindAgentInstructions}})
	for _, relative := range []string{mcpConfig, codexConfig} {
		if _, statErr := os.Stat(filepath.Join(dir, relative)); !os.IsNotExist(statErr) {
			t.Fatalf("removed config %s still exists: %v", relative, statErr)
		}
	}
}

func TestManifestHashesIgnoreForeignJSONContent(t *testing.T) {
	dir := t.TempDir()
	options := Options{
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
			Dir: dir, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
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
		confirm     func(string, string) (bool, error)
		name        string
		permissions ClaudePermissions
	}{
		{name: "no", permissions: ClaudePermissionsNo},
		{
			name:        "declined",
			permissions: ClaudePermissionsAsk,
			confirm:     func(string, string) (bool, error) { return false, nil },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			modes := testRunnerModes(t)
			if _, err := Apply(Options{
				Dir: dir, Agents: []string{"claude"}, RunnerModes: modes,
				ClaudePermissions: ClaudePermissionsYes,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(Options{
				Dir: dir, Agents: []string{"claude"}, RunnerModes: modes,
				ClaudePermissions: testCase.permissions, Confirm: testCase.confirm,
			}); err != nil {
				t.Fatal(err)
			}
			manifest, _ := readManagedManifest(t, dir)
			assertManifestSurfaces(t, manifest.Surfaces, []struct {
				path string
				kind string
			}{{path: "CLAUDE.md", kind: manifestKindAgentInstructions}})
		})
	}
}

func TestManifestReleaseIsContextOnly(t *testing.T) {
	previous := version.Version
	version.Version = "v9.8.7"
	t.Cleanup(func() { version.Version = previous })

	dir := t.TempDir()
	if _, err := Apply(Options{
		Dir: dir, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
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
		Dir: dir, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
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
		Dir: dir, Agents: []string{"codex"}, RunnerModes: testRunnerModes(t),
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
		Dir:         root,
		Agents:      []string{"codex"},
		BetaTest:    true,
		RunnerModes: testRunnerModes(t),
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

func TestVerifyManagedSurfacesAcceptsAndRejectsBetaBlock(t *testing.T) {
	root := t.TempDir()
	if _, err := Apply(Options{
		Dir:         root,
		Agents:      []string{"codex"},
		BetaTest:    true,
		RunnerModes: testRunnerModes(t),
	}); err != nil {
		t.Fatal(err)
	}
	betaTest, err := VerifyManagedSurfaces(root)
	if err != nil {
		t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
	}
	if !betaTest {
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
				Dir:         root,
				Agents:      []string{"codex"},
				BetaTest:    test.betaTest,
				RunnerModes: testRunnerModes(t),
			}); err != nil {
				t.Fatal(err)
			}
			betaTest, err := VerifyManagedSurfaces(root)
			if err != nil {
				t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
			}
			if betaTest != test.betaTest {
				t.Fatalf(
					"VerifyManagedSurfaces() beta test = %t, want %t",
					betaTest,
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
		betaTest, err := VerifyManagedSurfaces(root)
		if err != nil {
			t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
		}
		if betaTest {
			t.Fatal("VerifyManagedSurfaces() beta test = true, want false")
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
		betaTest, err := VerifyManagedSurfaces(root)
		if err != nil {
			t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
		}
		if betaTest {
			t.Fatal("VerifyManagedSurfaces() beta test = true without manifest, want false")
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

	betaTest, err := VerifyManagedSurfaces(root)
	if err != nil {
		t.Fatalf("VerifyManagedSurfaces() error = %v, want nil", err)
	}
	if betaTest {
		t.Fatal("VerifyManagedSurfaces() beta test = true without beta_test field, want false")
	}
}

func TestVerifyManagedSurfacesRejectsRecordedBetaModeWithPlainBlock(t *testing.T) {
	root := t.TempDir()
	if _, err := Apply(Options{
		Dir:         root,
		Agents:      []string{"codex"},
		BetaTest:    true,
		RunnerModes: testRunnerModes(t),
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
				managed := ClaudeManagedTools()
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
