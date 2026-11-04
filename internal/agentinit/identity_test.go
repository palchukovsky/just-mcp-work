// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const identityConfig = ".agents/mcp_config.json"

func identityDocument(t *testing.T, root string) map[string]json.RawMessage {
	t.Helper()
	var doc struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(readIdentityFile(t, root, identityConfig), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Servers
}

func readIdentityFile(t *testing.T, root, path string) []byte {
	t.Helper()
	return readTestFile(t, filepath.Join(root, path))
}

func writeIdentityFile(t *testing.T, root, path string, data []byte) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, path), data)
}

func TestAntigravityIdentityRootsProfilesAndIdempotence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{serverName, "jmw-workspace-one", "jmw-worktree-two"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			options := nativeTestOptions(t, root, "antigravity")
			options.Agents = []string{"antigravity", "claude", "codex", "cursor"}
			if name != serverName {
				options.AntigravityServerName = name
			}
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			servers := identityDocument(t, root)
			var entry serverEntry
			if err := json.Unmarshal(servers[name], &entry); err != nil {
				t.Fatalf("selected identity missing: %v", err)
			}
			want := []string{"serve", "--root", root, "--ai", "antigravity"}
			if !slices.Equal(entry.Args, want) {
				t.Fatalf("identity changed client profile or root: got %q, want %q", entry.Args, want)
			}
			executable, err := os.Executable()
			if err != nil || entry.Command != executable {
				t.Fatalf("identity changed executable: %q, %v", entry.Command, err)
			}
			if len(servers) != 1 {
				t.Fatalf("unexpected servers after identity selection: %s", servers)
			}
			if !bytes.Contains(readIdentityFile(t, root, ".mcp.json"), []byte(serverName)) ||
				!bytes.Contains(readIdentityFile(t, root, ".cursor/mcp.json"), []byte(serverName)) ||
				!bytes.Contains(readIdentityFile(t, root, ".codex/config.toml"), []byte(codexTable)) {
				t.Fatal("Antigravity identity changed another client's identity")
			}
			if _, err = VerifyManagedSurfaces(root); err != nil {
				t.Fatalf("selected identity did not verify: %v", err)
			}
			options.AntigravityServerName = ""
			result, err := Apply(options)
			if err != nil || len(result.Paths) != 0 {
				t.Fatalf("recorded identity rerun is not idempotent: %v, %q", err, result.Paths)
			}
		})
	}
}

func TestAntigravityIdentityWorktreeUsesItsOwnRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	mainRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(mainRoot, ".wt", "feature")
	writeAgentInitWorktreeMarkers(t, mainRoot, worktree, "feature")
	mainOptions := nativeTestOptions(t, mainRoot, "antigravity")
	mainOptions.AntigravityServerName = "jmw-main"
	if _, err = Apply(mainOptions); err != nil {
		t.Fatal(err)
	}
	mainConfig := readIdentityFile(t, mainRoot, identityConfig)
	nested := filepath.Join(worktree, "nested")
	if err = os.MkdirAll(nested, 0o750); err != nil {
		t.Fatal(err)
	}
	options := nativeTestOptions(t, nested, "antigravity")
	options.AntigravityServerName = "jmw-worktree"
	result, err := Apply(options)
	if err != nil || result.Scope != worktree {
		t.Fatalf("worktree scope = %q, want %q: %v", result.Scope, worktree, err)
	}
	var entry serverEntry
	if err = json.Unmarshal(identityDocument(t, worktree)["jmw-worktree"], &entry); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(entry.Args, []string{"serve", "--root", worktree, "--ai", "antigravity"}) {
		t.Fatalf("worktree identity bound another root: %q", entry.Args)
	}
	if !bytes.Equal(mainConfig, readIdentityFile(t, mainRoot, identityConfig)) {
		t.Fatal("worktree init changed main checkout config")
	}
	if _, err = VerifyManagedSurfaces(worktree); err != nil {
		t.Fatal(err)
	}
}

func TestAntigravityIdentityMigrationPreservesUnownedState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	foreign := []byte(
		`{"auth":{"token":"test-only"},` +
			`"mcpServers":{"other":{"command":"other","env":{"X":"Y"}}}}`,
	)
	writeIdentityFile(t, root, identityConfig, foreign)
	options := nativeTestOptions(t, root, "antigravity")
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-identity manifest without changing its canonical digest.
	manifest, _ := readManagedManifest(t, root)
	manifest.SchemaVersion = 1
	manifest.AntigravityServerName = nil
	writeJSONFile(t, filepath.Join(root, manifestFile), manifest)
	if _, err := VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("legacy canonical manifest rejected: %v", err)
	}
	for _, name := range []string{"jmw-scoped-one", "jmw-scoped-two", serverName} {
		options.AntigravityServerName = name
		if _, err := Apply(options); err != nil {
			t.Fatalf("rename to %q: %v", name, err)
		}
		servers := identityDocument(t, root)
		if len(servers) != 2 || servers[name] == nil || servers["other"] == nil {
			t.Fatalf("migration lost foreign server or kept old managed name: %s", servers)
		}
		data := readIdentityFile(t, root, identityConfig)
		if !bytes.Contains(data, []byte(`"auth":{"token":"test-only"}`)) ||
			!bytes.Contains(data, []byte(`"other":{"command":"other","env":{"X":"Y"}}`)) {
			t.Fatalf("migration changed unrelated auth or server bytes: %s", data)
		}
		if _, err := VerifyManagedSurfaces(root); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAntigravityIdentityRefusesCollisionsAndInvalidNamesBeforeWrites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	names := []string{"taken", "bad/name", "bad name", "_prefix", strings.Repeat("a", 129)}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			before := []byte(
				`{"mcpServers":{"taken":{"command":"foreign"},` +
					`"just-mcp-work":{"command":"unowned"}}}`,
			)
			writeIdentityFile(t, root, identityConfig, before)
			options := nativeTestOptions(t, root, "antigravity")
			options.AntigravityServerName = name
			if _, err := Apply(options); err == nil {
				t.Fatal("unowned collision or invalid identity accepted")
			}
			if !bytes.Equal(before, readIdentityFile(t, root, identityConfig)) {
				t.Fatal("rejected identity changed client state")
			}
			if _, err := os.Stat(filepath.Join(root, "GEMINI.md")); !os.IsNotExist(err) {
				t.Fatalf("rejected identity wrote instructions: %v", err)
			}
		})
	}
	root := t.TempDir()
	before := []byte(`{"mcpServers":{"just-mcp-work":{"command":"unowned"}}}`)
	writeIdentityFile(t, root, identityConfig, before)
	options := nativeTestOptions(t, root, "antigravity")
	options.AntigravityServerName = "jmw-scoped"
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(identityDocument(t, root)[serverName], []byte(`{"command":"unowned"}`)) {
		t.Fatal("custom identity adopted or deleted unowned canonical server")
	}
	options.AntigravityServerName = serverName
	if _, err := Apply(options); err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("rename adopted an unowned destination: %v", err)
	}
}

func TestAntigravityIdentityDigestBindsName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	options := nativeTestOptions(t, root, "antigravity")
	options.AntigravityServerName = "jmw-scoped"
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	data := readIdentityFile(t, root, identityConfig)
	writeIdentityFile(
		t,
		root,
		identityConfig,
		bytes.ReplaceAll(data, []byte("jmw-scoped"), []byte("jmw-imposter")),
	)
	manifest, _ := readManagedManifest(t, root)
	name := "jmw-imposter"
	manifest.AntigravityServerName = &name
	writeJSONFile(t, filepath.Join(root, manifestFile), manifest)
	if _, err := VerifyManagedSurfaces(root); err == nil {
		t.Fatal("recorded digest did not bind the selected identity")
	}
}

func TestAntigravityIdentityRejectsUnsafeMigration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, failure := range []string{"missing", "edited", "removal"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			options := nativeTestOptions(t, root, "antigravity")
			options.AntigravityServerName = "jmw-old"
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			before := readIdentityFile(t, root, identityConfig)
			switch failure {
			case "missing":
				before = []byte(`{"mcpServers":{}}`)
			case "edited":
				before = bytes.ReplaceAll(before, []byte("serve"), []byte("tampered"))
			}
			writeIdentityFile(t, root, identityConfig, before)
			options.AntigravityServerName = "jmw-new"
			options.WriteMCPConfig = failure != "removal"
			if _, err := Apply(options); err == nil {
				t.Fatal("unsafe migration accepted")
			}
			if !bytes.Equal(before, readIdentityFile(t, root, identityConfig)) {
				t.Fatal("rejected migration changed client state")
			}
		})
	}
}

func TestAntigravityIdentityTamperRepairRemovalAndDeselection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	mutations := []string{"key", "args", "entry", "file", "recorded", "edited name"}
	for _, mutation := range mutations {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			options := nativeTestOptions(t, root, "antigravity")
			options.AntigravityServerName = "jmw-scoped"
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			original := readIdentityFile(t, root, identityConfig)
			manifest, _ := readManagedManifest(t, root)
			switch mutation {
			case "key":
				writeIdentityFile(
					t,
					root,
					identityConfig,
					bytes.ReplaceAll(original, []byte("jmw-scoped"), []byte("renamed")),
				)
			case "args":
				writeIdentityFile(
					t,
					root,
					identityConfig,
					bytes.ReplaceAll(original, []byte("antigravity"), []byte("claude")),
				)
			case "entry":
				writeIdentityFile(t, root, identityConfig, []byte(`{"mcpServers":{}}`))
			case "file":
				if err := os.Remove(filepath.Join(root, identityConfig)); err != nil {
					t.Fatal(err)
				}
			case "recorded":
				manifest.AntigravityServerName = nil
			case "edited name":
				name := "different"
				manifest.AntigravityServerName = &name
			}
			writeJSONFile(t, filepath.Join(root, manifestFile), manifest)
			if _, err := VerifyManagedSurfaces(root); err == nil {
				t.Fatal("tampered or missing identity escaped managed verification")
			}
			options.AntigravityServerName = ""
			_, err := Apply(options)
			switch mutation {
			case "recorded":
				assertRecordedSurfaceIdentityNotRepaired(t, options, err)
			case "edited name":
				assertEditedIdentityApplied(t, root, err)
			default:
				assertIdentityRepairLifecycle(t, root, options, err)
			}
		})
	}
}

func assertRecordedSurfaceIdentityNotRepaired(t *testing.T, options Options, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("missing recorded identity of a recorded surface silently replaced")
	}
	options.AntigravityServerName = "jmw-scoped"
	if _, err = Apply(options); err == nil {
		t.Fatal("explicit name repaired the identity of a recorded surface")
	}
}

// assertEditedIdentityApplied pins the single recorded name: init takes an
// edited one and keeps the previous entry as content it no longer owns.
func assertEditedIdentityApplied(t *testing.T, root string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("edited recorded name was not applied: %v", err)
	}
	servers := identityDocument(t, root)
	if servers["different"] == nil || servers["jmw-scoped"] == nil {
		t.Fatalf("edited name lost an entry: %s", servers)
	}
}

func assertIdentityRepairLifecycle(t *testing.T, root string, options Options, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("same-identity repair failed: %v", err)
	}
	if _, err = VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("repair did not restore verification: %v", err)
	}
	options.Agents = []string{"codex"}
	if _, err = Apply(options); err != nil {
		t.Fatal(err)
	}
	options.AntigravityServerName = "another"
	if _, err = Apply(options); err == nil {
		t.Fatal("unselected Antigravity identity changed")
	}
	options.AntigravityServerName = ""
	options.Agents = []string{"antigravity"}
	options.WriteMCPConfig = false
	if _, err = Apply(options); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyManagedSurfaces(root); err != nil {
		t.Fatal(err)
	}
	options.WriteMCPConfig = true
	if data, readErr := os.ReadFile(filepath.Join(root, identityConfig)); readErr == nil &&
		bytes.Contains(data, []byte(`"jmw-scoped"`)) {
		t.Fatal("removal kept scoped managed entry")
	}
	if _, err = Apply(options); err != nil {
		t.Fatal(err)
	}
	if identityDocument(t, root)["jmw-scoped"] == nil {
		t.Fatal("removal discarded recorded identity")
	}
}

func TestAntigravityIdentityFormatSurvivesRemoval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{serverName, "jmw-scoped"} {
		for _, test := range []struct {
			field string
			value string
		}{
			{field: "antigravity_server_name"},
			{field: "antigravity_server_name", value: "null"},
			{field: "antigravity_server_name", value: `""`},
			{field: "antigravity_server_name", value: `"bad/name"`},
			{field: "schema_version"},
			{field: "schema_version", value: "null"},
			{field: "schema_version", value: "0"},
			{field: "schema_version", value: "1"},
			{field: "schema_version", value: "3"},
		} {
			t.Run(name+"/"+test.field+"/"+test.value, func(t *testing.T) {
				root := t.TempDir()
				foreign := []byte(`{"auth":{"token":"test-only"},"mcpServers":{"other":{}}}`)
				writeIdentityFile(t, root, identityConfig, foreign)
				options := nativeTestOptions(t, root, "antigravity")
				options.AntigravityServerName = name
				if _, err := Apply(options); err != nil {
					t.Fatal(err)
				}
				options.WriteMCPConfig = false
				if _, err := Apply(options); err != nil {
					t.Fatal(err)
				}
				_, backup := readManagedManifest(t, root)
				var document map[string]json.RawMessage
				if err := json.Unmarshal(backup, &document); err != nil {
					t.Fatal(err)
				}
				delete(document, test.field)
				if test.value != "" {
					document[test.field] = json.RawMessage(test.value)
				}
				writeJSONFile(t, filepath.Join(root, manifestFile), document)
				broken := readIdentityFile(t, root, manifestFile)
				before := readIdentityFile(t, root, identityConfig)
				_, verifyErr := VerifyManagedSurfaces(root)
				options.WriteMCPConfig = true
				options.AntigravityServerName = ""
				_, err := Apply(options)
				assertRemovedIdentityRefusal(t, test.field == "schema_version", verifyErr, err)
				if !bytes.Equal(before, readIdentityFile(t, root, identityConfig)) ||
					!bytes.Equal(broken, readIdentityFile(t, root, manifestFile)) {
					t.Fatal("invalid removed identity changed client state or recorded answers")
				}
				writeIdentityFile(t, root, manifestFile, backup)
				if _, err = Apply(options); err != nil {
					t.Fatalf("known-good identity restoration failed: %v", err)
				}
				if identityDocument(t, root)[name] == nil {
					t.Fatal("known-good identity restoration selected another name")
				}
			})
		}
	}
}

// assertRemovedIdentityRefusal checks serve and init refuse damaged identity
// metadata. A damaged schema is named as the cause, before the identity is
// judged, and init will not replace a document recording a name.
func assertRemovedIdentityRefusal(t *testing.T, damagedSchema bool, verifyErr, initErr error) {
	t.Helper()
	if !damagedSchema {
		assertIdentityRecoveryError(t, verifyErr)
		assertIdentityRecoveryError(t, initErr)
		return
	}
	for _, refusal := range []error{verifyErr, initErr} {
		if refusal == nil || !strings.Contains(refusal.Error(), "schema") ||
			!strings.Contains(refusal.Error(), "Antigravity server identity") {
			t.Fatalf("damaged identity schema was not refused as one: %v", refusal)
		}
	}
	for _, recovery := range []string{"backup", "release that wrote it"} {
		if strings.Contains(verifyErr.Error(), recovery) !=
			strings.Contains(initErr.Error(), recovery) {
			t.Fatalf("serve and init recover differently: %v; %v", verifyErr, initErr)
		}
	}
}

func assertIdentityRecoveryError(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("invalid identity metadata was not explicitly refused: %v", err)
	}
	for _, want := range []string{"--antigravity-server-name", "known-good", "then rerun init"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("identity error lacks actionable recovery %q: %v", want, err)
		}
	}
}

func TestAntigravityIdentityUpgradesLegacyCarry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	options := nativeTestOptions(t, root, "antigravity")
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	manifest, _ := readManagedManifest(t, root)
	manifest.SchemaVersion = 1
	manifest.AntigravityServerName = nil
	writeJSONFile(t, filepath.Join(root, manifestFile), manifest)
	if _, err := VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("genuine pre-identity canonical manifest rejected: %v", err)
	}
	options.Agents = []string{"codex"}
	if _, err := Apply(options); err != nil {
		t.Fatalf("legacy deselected identity upgrade failed: %v", err)
	}
	upgraded, data := readManagedManifest(t, root)
	if upgraded.SchemaVersion != manifestSchemaVersion ||
		upgraded.AntigravityServerName == nil ||
		*upgraded.AntigravityServerName != serverName {
		t.Fatalf("legacy carry did not record the canonical identity: %s", data)
	}
	if _, err := VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("legacy carry lost identity metadata: %v", err)
	}
}

func TestAntigravityIdentityTooNewManifest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	options := nativeTestOptions(t, root, "antigravity")
	options.AntigravityServerName = "jmw-scoped"
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	manifest, _ := readManagedManifest(t, root)
	manifest.SchemaVersion = manifestSchemaVersion + 1
	writeJSONFile(t, filepath.Join(root, manifestFile), manifest)
	before := readIdentityFile(t, root, manifestFile)
	if _, err := VerifyManagedSurfaces(root); err == nil ||
		!strings.Contains(err.Error(), "is too new") ||
		!strings.Contains(err.Error(), "release that wrote it") {
		t.Fatalf("newer identity manifest not reported as too new: %v", err)
	}
	options.AntigravityServerName = ""
	if _, err := Apply(options); err == nil ||
		!strings.Contains(err.Error(), "is too new") ||
		!strings.Contains(err.Error(), "release that wrote it") {
		t.Fatalf("init on a newer identity manifest not refused as too new: %v", err)
	}
	if !bytes.Equal(before, readIdentityFile(t, root, manifestFile)) {
		t.Fatal("refused newer manifest was rewritten")
	}
}

func TestAntigravityIdentityRepairWithoutRecordedSurface(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, write := range []bool{true, false} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			root := t.TempDir()
			options := nativeTestOptions(t, root, "antigravity")
			options.AntigravityServerName = "jmw-lost"
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			options.WriteMCPConfig = false
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			manifest, _ := readManagedManifest(t, root)
			manifest.AntigravityServerName = nil
			writeJSONFile(t, filepath.Join(root, manifestFile), manifest)
			options.AntigravityServerName = ""
			options.WriteMCPConfig = write
			_, err := Apply(options)
			assertIdentityRecoveryError(t, err)

			for _, occupied := range []string{"jmw-found", serverName} {
				assertRepairRefusesOccupied(t, root, options, occupied)
			}

			options.AntigravityServerName = "jmw-repaired"
			if _, err = Apply(options); err != nil {
				t.Fatalf("explicit identity repair failed: %v", err)
			}
			repaired, data := readManagedManifest(t, root)
			if repaired.AntigravityServerName == nil ||
				*repaired.AntigravityServerName != "jmw-repaired" {
				t.Fatalf("identity repair recorded another name: %s", data)
			}
			if write && identityDocument(t, root)["jmw-repaired"] == nil {
				t.Fatal("identity repair did not write the selected entry")
			}
			if _, err = VerifyManagedSurfaces(root); err != nil {
				t.Fatalf("repaired identity does not verify: %v", err)
			}
		})
	}
}

// assertRepairRefusesOccupied checks an identity repair never adopts an existing
// entry, the canonical one included: with the identity lost, nothing shows init
// ever owned it.
func assertRepairRefusesOccupied(t *testing.T, root string, options Options, name string) {
	t.Helper()
	occupied := []byte(`{"mcpServers":{"` + name + `":{"command":"foreign"}}}`)
	writeIdentityFile(t, root, identityConfig, occupied)
	options.AntigravityServerName = name
	if _, err := Apply(options); err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("identity repair adopted occupied %q: %v", name, err)
	}
	if !bytes.Equal(occupied, readIdentityFile(t, root, identityConfig)) {
		t.Fatal("refused identity repair changed client state")
	}
	if err := os.Remove(filepath.Join(root, identityConfig)); err != nil {
		t.Fatal(err)
	}
}

func TestAntigravityIdentityTransitionsAfterRemoval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, write := range []bool{true, false} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			root := t.TempDir()
			foreign := []byte(
				`{"auth":{"token":"test-only"},` +
					`"mcpServers":{"just-mcp-work":{"command":"foreign"}}}`,
			)
			writeIdentityFile(t, root, identityConfig, foreign)
			options := nativeTestOptions(t, root, "antigravity")
			options.AntigravityServerName = "jmw-A"
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			options.WriteMCPConfig = false
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			before := readIdentityFile(t, root, identityConfig)
			_, recorded := readManagedManifest(t, root)
			options.AntigravityServerName = serverName
			options.WriteMCPConfig = write
			_, err := Apply(options)
			want := "not managed"
			if !write {
				want = "while removing"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("removed custom identity adopted foreign canonical destination: %v", err)
			}
			if !bytes.Equal(before, readIdentityFile(t, root, identityConfig)) ||
				!bytes.Equal(recorded, readIdentityFile(t, root, manifestFile)) {
				t.Fatal("rejected transition changed foreign bytes, auth or recorded answers")
			}
		})
	}

}

func TestAntigravityIdentityReaddAfterRemoval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	options := nativeTestOptions(t, root, "antigravity")
	options.AntigravityServerName = "jmw-A"
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	options.WriteMCPConfig = false
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, identityConfig)); !os.IsNotExist(err) {
		t.Fatalf("native file was not removed: %v", err)
	}
	options.AntigravityServerName = "jmw-B"
	if _, err := Apply(options); err == nil || !strings.Contains(err.Error(), "while removing") {
		t.Fatalf("absent file bypassed no-rename-while-removing: %v", err)
	}
	options.WriteMCPConfig = true
	for _, name := range []string{"jmw-A", "jmw-B", serverName} {
		options.AntigravityServerName = name
		if _, err := Apply(options); err != nil {
			t.Fatalf("allowed re-add or rename to %q failed: %v", name, err)
		}
		options.WriteMCPConfig = false
		if _, err := Apply(options); err != nil {
			t.Fatal(err)
		}
		options.WriteMCPConfig = true
	}
}

func TestAntigravityIdentityBlankNativeFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{serverName, "jmw-scoped"} {
		for _, write := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%t", name, write), func(t *testing.T) {
				root := t.TempDir()
				writeIdentityFile(t, root, identityConfig, []byte(" \t\r\n"))
				options := nativeTestOptions(t, root, "antigravity")
				options.AntigravityServerName = name
				options.WriteMCPConfig = write
				if _, err := Apply(options); err != nil {
					t.Fatalf("blank native config was not treated as empty: %v", err)
				}
				if write && identityDocument(t, root)[name] == nil {
					t.Fatal("blank native config did not receive selected identity")
				}
				if !write {
					if !bytes.Equal(readIdentityFile(t, root, identityConfig), []byte(" \t\r\n")) {
						t.Fatal("blank native config removal changed unowned empty bytes")
					}
					return
				}
				writeIdentityFile(t, root, identityConfig, []byte(" \t\r\n"))
				options.AntigravityServerName = "jmw-other"
				if _, err := Apply(options); err == nil ||
					!strings.Contains(err.Error(), "managed entry is missing") ||
					!strings.Contains(err.Error(), "plan "+identityConfig+":") {
					t.Fatalf("blank owned config rename lacks native-path refusal: %v", err)
				}
				options.AntigravityServerName = name
				if _, err := Apply(options); err != nil {
					t.Fatalf("same-name blank config repair failed: %v", err)
				}
			})
		}
	}
}
