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
	"runtime"
	"slices"
	"strings"
	"testing"
)

func cursorPermissionsContent(t *testing.T, root string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, cursorPermissions))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeCursorPermissions(t *testing.T, root string, data []byte) {
	t.Helper()
	path := filepath.Join(root, cursorPermissions)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func cursorAllowEntries(t *testing.T, content []byte) []string {
	t.Helper()
	var document struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	return document.Permissions.Allow
}

func cursorPermissionsFragmentForTest(t *testing.T, data []byte) []byte {
	t.Helper()
	fragment, err := json.Marshal(map[string][]string{"allow": cursorAllowEntries(t, data)})
	if err != nil {
		t.Fatal(err)
	}
	return fragment
}

func assertCursorPermissionManifest(t *testing.T, root string, shell ShellPermission, data []byte) {
	t.Helper()
	manifest, _ := readManagedManifest(t, root)
	surface := manifestSurfaceByPath(t, manifest.Surfaces, cursorPermissions)
	if surface.Kind != manifestKindCursorPermissions || manifest.ShellPermission != string(shell) {
		t.Fatalf("Cursor manifest kind or shell permission = %#v, %q", surface, manifest.ShellPermission)
	}
	if surface.SHA256 != managedFragmentDigest(cursorPermissionsFragmentForTest(t, data)) {
		t.Fatal("Cursor manifest does not hash exactly the managed allow entries")
	}
}

func TestCursorPermissionsFreshSelection(t *testing.T) {
	for _, test := range []struct {
		shell ShellPermission
		count int
	}{
		{shell: ShellPermissionAllow, count: 15},
		{shell: ShellPermissionAsk, count: 12},
	} {
		t.Run(string(test.shell), func(t *testing.T) {
			root := t.TempDir()
			options := nativeTestOptions(t, root, "cursor")
			options.ShellPermission = test.shell
			options.ClaudePermissions = ""
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			data := cursorPermissionsContent(t, root)
			allow := cursorAllowEntries(t, data)
			managed, err := ClaudeManagedTools(test.shell)
			if err != nil {
				t.Fatal(err)
			}
			want := make([]string, 0, len(managed.Allow))
			for _, rule := range managed.Allow {
				want = append(want, "Mcp(just-mcp-work:"+strings.TrimPrefix(rule, ClaudeToolPrefix)+")")
			}
			if len(allow) != test.count || !slices.Equal(allow, want) {
				t.Fatalf("Cursor allow = %q, want exactly %d roster entries %q", allow, test.count, want)
			}
			if bytes.Contains(data, []byte(`"deny"`)) || bytes.Contains(data, []byte(`"ask"`)) {
				t.Fatalf("fresh Cursor permissions created another list: %s", data)
			}
			info, err := os.Stat(filepath.Join(root, cursorPermissions))
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Fatalf("Cursor permissions mode = %o, want 600", info.Mode().Perm())
			}
			if _, err = os.Stat(filepath.Join(root, claudeSettings)); !os.IsNotExist(err) {
				t.Fatalf("Cursor-only selection created Claude settings: %v", err)
			}
			assertCursorPermissionManifest(t, root, test.shell, data)
			if _, err = VerifyManagedSurfaces(root); err != nil {
				t.Fatalf("verify fresh Cursor permissions: %v", err)
			}
		})
	}
}

func TestCursorPermissionsManifestRecordsShellWithoutCodex(t *testing.T) {
	root := t.TempDir()
	surface, err := generatedCursorPermissions(ShellPermissionAllow)
	if err != nil {
		t.Fatal(err)
	}
	edits, _, err := planManifest(
		root,
		nil,
		[]manifestSurface{surface},
		nil,
		false,
		false,
		ShellPermissionAllow,
		testAIFamilies(),
		map[string]struct{}{"cursor": {}},
		InstructionsTargetWorkspace,
		serverName,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 {
		t.Fatalf("Cursor-only manifest plan = %#v", edits)
	}
	var manifest managedManifest
	if err = json.Unmarshal(edits[0].after, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ShellPermission != string(ShellPermissionAllow) {
		t.Fatalf("Cursor-only manifest shell permission = %q, want allow", manifest.ShellPermission)
	}
}

func TestCursorPermissionsUnselected(t *testing.T) {
	root := t.TempDir()
	before := []byte("{ \"permissions\" : \"invalid but unselected\" }\n")
	writeCursorPermissions(t, root, before)
	options := nativeTestOptions(t, root, "codex")
	for _, write := range []bool{true, false} {
		options.WriteMCPConfig = write
		if _, err := Apply(options); err != nil {
			t.Fatalf("unselected Cursor permissions were read: %v", err)
		}
		if !bytes.Equal(before, cursorPermissionsContent(t, root)) {
			t.Fatal("unselected Cursor permissions changed")
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs elevated privileges on Windows")
	}
	path := filepath.Join(root, cursorPermissions)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.json"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(options); err != nil {
		t.Fatalf("unselected Cursor permissions path was resolved: %v", err)
	}
}

func TestCursorPermissionsCarryAndShellRefusal(t *testing.T) {
	root := t.TempDir()
	options := nativeTestOptions(t, root, "cursor")
	options.ShellPermission = ShellPermissionAllow
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	before := cursorPermissionsContent(t, root)
	manifest, _ := readManagedManifest(t, root)
	surface := manifestSurfaceByPath(t, manifest.Surfaces, cursorPermissions)
	options.Agents = []string{"codex"}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	manifest, manifestBefore := readManagedManifest(t, root)
	if got := manifestSurfaceByPath(t, manifest.Surfaces, cursorPermissions); got != surface {
		t.Fatalf("carried Cursor surface changed: %#v, want %#v", got, surface)
	}
	if !bytes.Equal(before, cursorPermissionsContent(t, root)) {
		t.Fatal("carried Cursor permissions changed")
	}
	codexBefore, err := os.ReadFile(filepath.Join(root, codexConfig))
	if err != nil {
		t.Fatal(err)
	}
	options.ShellPermission = ShellPermissionAsk
	_, err = Apply(options)
	if err == nil || !strings.Contains(err.Error(), cursorPermissions) ||
		!strings.Contains(err.Error(), "re-run with --agents codex,cursor") {
		t.Fatalf("excluded Cursor shell change was not refused with its path and agent: %v", err)
	}
	_, manifestAfter := readManagedManifest(t, root)
	codexAfter, readErr := os.ReadFile(filepath.Join(root, codexConfig))
	if readErr != nil || !bytes.Equal(manifestBefore, manifestAfter) ||
		!bytes.Equal(codexBefore, codexAfter) ||
		!bytes.Equal(before, cursorPermissionsContent(t, root)) {
		t.Fatalf("refused Cursor shell change wrote a file: %v", readErr)
	}
}

func TestCursorPermissionsIdempotent(t *testing.T) {
	root := t.TempDir()
	options := nativeTestOptions(t, root, "cursor")
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	before := cursorPermissionsContent(t, root)
	_, manifestBefore := readManagedManifest(t, root)
	result, err := Apply(options)
	if err != nil {
		t.Fatal(err)
	}
	_, manifestAfter := readManagedManifest(t, root)
	if len(result.Paths) != 0 || !bytes.Equal(before, cursorPermissionsContent(t, root)) ||
		!bytes.Equal(manifestBefore, manifestAfter) {
		t.Fatalf("identical Cursor init changed bytes or paths: %q", result.Paths)
	}
}

func TestCursorPermissionsPreserveForeignTextAndDeny(t *testing.T) {
	root := t.TempDir()
	prefix := "{\r\n  \"version\" : 1,\r\n  \"editor\" : { \"theme\" : \"dark\" },\r\n" +
		"  \"permissions\" : {\r\n    \"allow\" : "
	foreign := `"Shell(ls)", "Mcp(other:*)", "Mcp(just-mcp-work-other:x)", ` +
		`"Mcp(*:x)", "Mcp(*:*)"`
	suffix := ",\r\n    \"deny\" : [ \"Mcp(just-mcp-work:run_shell_command)\", \"Shell(rm)\" ]," +
		"\r\n    \"owner\" : { \"keep\" : true }\r\n  }\r\n}\r\n"
	before := []byte(prefix + "[ " + foreign +
		`, " Mcp(just-mcp-work:old_tool) ", "Mcp(just-mcp-work:*)" ]` + suffix)
	writeCursorPermissions(t, root, before)
	options := nativeTestOptions(t, root, "cursor")
	options.ShellPermission = ShellPermissionAllow
	for _, shell := range []ShellPermission{ShellPermissionAllow, ShellPermissionAsk} {
		options.ShellPermission = shell
		if _, err := Apply(options); err != nil {
			t.Fatal(err)
		}
		after := cursorPermissionsContent(t, root)
		if !bytes.HasPrefix(after, []byte(prefix)) || !bytes.HasSuffix(after, []byte(suffix)) ||
			!bytes.Contains(after, []byte(foreign)) {
			t.Fatalf("Cursor init changed text outside managed allow entries: %q", after)
		}
		if bytes.Contains(after, []byte("old_tool")) ||
			bytes.Contains(after, []byte("Mcp(just-mcp-work:*)")) {
			t.Fatalf("Cursor init kept stale managed allow entries: %s", after)
		}
		allow := cursorAllowEntries(t, after)
		wantCount := 20
		if shell == ShellPermissionAsk {
			wantCount = 17
		}
		if len(allow) != wantCount {
			t.Fatalf("preserved Cursor allow count = %d, want %d", len(allow), wantCount)
		}
		if shell == ShellPermissionAsk {
			for _, entry := range allow {
				if strings.HasPrefix(entry, "Mcp(just-mcp-work:") && strings.Contains(entry, "shell") {
					t.Fatalf("Cursor ask kept a shell grant: %q", entry)
				}
			}
		}
		if _, err := VerifyManagedSurfaces(root); err != nil {
			t.Fatalf("foreign Cursor content entered the manifest hash: %v", err)
		}
	}
}

func TestCursorPermissionsDenyDoesNotAffectHash(t *testing.T) {
	root := t.TempDir()
	writeCursorPermissions(
		t,
		root,
		[]byte(`{"permissions":{"deny":["Mcp(just-mcp-work:run_shell_command)"]}}`),
	)
	options := nativeTestOptions(t, root, "cursor")
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	manifest, _ := readManagedManifest(t, root)
	prior := manifestSurfaceByPath(t, manifest.Surfaces, cursorPermissions)
	changed := bytes.ReplaceAll(
		cursorPermissionsContent(t, root),
		[]byte("Mcp(just-mcp-work:run_shell_command)"),
		[]byte("Mcp(just-mcp-work:other_deny)"),
	)
	writeCursorPermissions(t, root, changed)
	if _, err := VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("Cursor deny was hashed: %v", err)
	}
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	manifest, _ = readManagedManifest(t, root)
	if got := manifestSurfaceByPath(t, manifest.Surfaces, cursorPermissions); got != prior {
		t.Fatal("Cursor deny changed the managed hash")
	}
}

func TestCursorPermissionsCleanupPreservesConfigFileSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	root := t.TempDir()
	path := filepath.Join(root, cursorPermissions)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "shared-config")
	content := []byte(`{"permissions":{"allow":["Mcp(just-mcp-work:*)"]}}`)
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", filepath.Base(target)), path); err != nil {
		t.Fatal(err)
	}
	options := nativeTestOptions(t, root, "cursor")
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	options.WriteMCPConfig = false
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	assertFileSymlink(t, path)
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(after) {
		t.Fatalf("cleaned Cursor permissions symlink target is not valid JSON: %q", after)
	}
	if string(bytes.TrimSpace(after)) != "{}" {
		t.Fatalf("cleaned Cursor permissions symlink target = %q, want {}", after)
	}
	if _, err = VerifyManagedSurfaces(root); err != nil {
		t.Fatal(err)
	}
	options.WriteMCPConfig = true
	if _, err = Apply(options); err != nil {
		t.Fatalf("reinitialize cleaned Cursor permissions: %v", err)
	}
	assertFileSymlink(t, path)
	if _, err = VerifyManagedSurfaces(root); err != nil {
		t.Fatal(err)
	}
}

func TestCursorPermissionsCleanup(t *testing.T) {
	for _, test := range []struct {
		name   string
		before string
		remove bool
	}{
		{
			name:   "managed only",
			before: `{"permissions":{"allow":["Mcp(just-mcp-work:*)"]}}`,
			remove: true,
		},
		{name: "foreign root", before: `{"version":1,"permissions":{"allow":["Mcp(just-mcp-work:x)"]}}`},
		{
			name: "deny only",
			before: `{"permissions":{"allow":["Mcp(just-mcp-work:x)"],` +
				`"deny":["Mcp(just-mcp-work:x)"]}}`,
		},
		{name: "foreign allow", before: `{"permissions":{"allow":["Shell(ls)","Mcp(just-mcp-work:x)"]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeCursorPermissions(t, root, []byte(test.before))
			options := nativeTestOptions(t, root, "cursor")
			options.WriteMCPConfig = false
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(filepath.Join(root, cursorPermissions))
			if test.remove {
				if !os.IsNotExist(err) {
					t.Fatalf("JMW-only Cursor permissions were not deleted: %v", err)
				}
				return
			}
			after := cursorPermissionsContent(t, root)
			want := strings.ReplaceAll(test.before, `"Mcp(just-mcp-work:x)"`, "")
			switch test.name {
			case "deny only":
				want = `{"permissions":{"allow":[],"deny":["Mcp(just-mcp-work:x)"]}}`
			case "foreign allow":
				want = `{"permissions":{"allow":["Shell(ls)"]}}`
			}
			if string(after) != want {
				t.Fatalf("Cursor cleanup changed foreign content: %s, want %s", after, want)
			}
			manifest, _ := readManagedManifest(t, root)
			for _, surface := range manifest.Surfaces {
				if surface.Kind == manifestKindCursorPermissions {
					t.Fatal("Cursor cleanup retained its manifest surface")
				}
			}
		})
	}
}

func TestCursorPermissionsMalformedRefusesBeforeWrites(t *testing.T) {
	for _, before := range []string{
		`{"permissions":1}`,
		`{"permissions":{"allow":{}}}`,
		`{"permissions":{"allow":[1]}}`,
		`{"permissions":{"allow":[null]}}`,
	} {
		for _, write := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/write=%t", before, write), func(t *testing.T) {
				root := t.TempDir()
				writeCursorPermissions(t, root, []byte(before))
				options := nativeTestOptions(t, root, "cursor")
				options.WriteMCPConfig = write
				_, err := Apply(options)
				if err == nil || !strings.Contains(err.Error(), cursorPermissions) ||
					!strings.Contains(err.Error(), "fix that entry and run init again") {
					t.Fatalf("malformed Cursor permissions were not refused with repair guidance: %v", err)
				}
				if string(cursorPermissionsContent(t, root)) != before {
					t.Fatal("malformed Cursor permissions were changed")
				}
				for _, relative := range []string{
					mcpConfig,
					codexConfig,
					manifestFile,
					guideFile,
					".cursor/mcp.json",
					".cursor/rules/just-mcp-work.mdc",
					".just-mcp-work.json",
				} {
					if _, err = os.Stat(filepath.Join(root, relative)); !os.IsNotExist(err) {
						t.Fatalf("refused Cursor init wrote %s: %v", relative, err)
					}
				}
			})
		}
	}
}

func TestCursorPermissionsNullAndUnmanagedRestrictions(t *testing.T) {
	for _, before := range []string{
		`{"permissions":null}`,
		`{"permissions":{"allow":null,"deny":42,"ask":{"owner":true}}}`,
	} {
		t.Run(before, func(t *testing.T) {
			root := t.TempDir()
			writeCursorPermissions(t, root, []byte(before))
			if _, err := Apply(nativeTestOptions(t, root, "cursor")); err != nil {
				t.Fatalf("Cursor init read unmanaged permission values: %v", err)
			}
			if _, err := VerifyManagedSurfaces(root); err != nil {
				t.Fatalf("Cursor verification read unmanaged permission values: %v", err)
			}
		})
	}
}

func TestCursorPermissionsVerifyEditedAllow(t *testing.T) {
	root := t.TempDir()
	if _, err := Apply(nativeTestOptions(t, root, "cursor")); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyManagedSurfaces(root); err != nil {
		t.Fatal(err)
	}
	before := cursorPermissionsContent(t, root)
	after := bytes.Replace(before, []byte(`"Mcp(just-mcp-work:run_task)",`), nil, 1)
	if bytes.Equal(before, after) {
		t.Fatal("test did not remove its managed allow entry")
	}
	writeCursorPermissions(t, root, after)
	if _, err := VerifyManagedSurfaces(root); err == nil ||
		!strings.Contains(err.Error(), cursorPermissions) || !strings.Contains(err.Error(), "edited") {
		t.Fatalf("edited Cursor allow entry escaped verification: %v", err)
	}
}

func TestFreshInitDefaultsToCursorAndKeepsGrokExplicit(t *testing.T) {
	for _, test := range []struct {
		name     string
		agents   []string
		wantGrok bool
	}{
		{name: "defaults"},
		{name: "explicit Grok", agents: []string{"cursor", "grok"}, wantGrok: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			options := nativeTestOptions(t, root, "cursor")
			options.Agents = test.agents
			if _, err := Apply(options); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{
				".cursor/rules/just-mcp-work.mdc",
				".cursor/mcp.json",
			} {
				if _, err := os.Stat(filepath.Join(root, path)); err != nil {
					t.Fatalf("Cursor surface %s is missing: %v", path, err)
				}
			}
			for _, path := range []string{"AGENT.md", ".grok/config.toml"} {
				_, err := os.Stat(filepath.Join(root, path))
				if test.wantGrok && err != nil {
					t.Fatalf("explicit Grok surface %s is missing: %v", path, err)
				}
				if !test.wantGrok && !os.IsNotExist(err) {
					t.Fatalf("default init created Grok surface %s: %v", path, err)
				}
			}
			if _, err := VerifyManagedSurfaces(root); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCursorMCPPreservesForeignJSONText(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	foreign := `"other" : { "command" : "owner-tool", "args" : [ "a", "b" ] }`
	owner := `"owner" : { "setting" : true }`
	before := []byte("{\r\n  " + owner + ",\r\n  \"mcpServers\" : { " + foreign + " }\r\n}\r\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	options := nativeTestOptions(t, root, "cursor")
	for _, write := range []bool{true, false} {
		options.WriteMCPConfig = write
		if _, err := Apply(options); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(after, []byte(foreign)) || !bytes.Contains(after, []byte(owner)) ||
			!bytes.Contains(after, []byte("\r\n")) {
			t.Fatalf("Cursor init changed foreign JSON text: %q", after)
		}
		if bytes.Contains(after, []byte(serverName)) != write {
			t.Fatalf("Cursor managed entry after write=%v: %q", write, after)
		}
		if _, err = VerifyManagedSurfaces(root); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCursorMCPStaysManagedWhenDeselectedAndRecovers(t *testing.T) {
	root := t.TempDir()
	options := nativeTestOptions(t, root, "cursor")
	if _, err := Apply(options); err != nil {
		t.Fatal(err)
	}
	path := resolvedTestPath(t, filepath.Join(root, ".cursor", "mcp.json"))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	options.Agents = []string{"codex"}
	options.WriteMCPConfig = false
	if _, err = Apply(options); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("deselected Cursor config changed: %q, %v", after, err)
	}
	// #nosec G703 -- path is the fixed config under this test's temporary root.
	if err = os.WriteFile(path, bytes.ReplaceAll(before, []byte("serve"),
		[]byte("tampered")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyManagedSurfaces(root); err == nil ||
		!strings.Contains(err.Error(), path) {
		t.Fatalf("deselected Cursor config escaped verification: %v", err)
	}
	options.Agents = []string{"cursor"}
	options.WriteMCPConfig = true
	if _, err = Apply(options); err != nil {
		t.Fatalf("recover Cursor config: %v", err)
	}
	if _, err = VerifyManagedSurfaces(root); err != nil {
		t.Fatalf("verify recovered Cursor config: %v", err)
	}
}

func TestCursorRuleMetadataPreservesOwnerTextAndMigratesLegacyHeader(t *testing.T) {
	for _, lineBreak := range []string{"\n", "\r\n"} {
		t.Run(strings.ReplaceAll(lineBreak, "\r", "CR"), func(t *testing.T) {
			for _, header := range []string{
				"---\ndescription: Use workspace tasks through just-mcp-work\n---\n\n",
				"---\ndescription: Owner description\nalwaysApply: true\nowner: kept\n---\n\n",
				"---\n'alwaysApply': true # Owner comment\nowner: kept\n---\n\n",
				"---\n\"alwaysApply\" : true # Owner comment\nowner: kept\n---\n\n",
				"---\nalwaysApply : true # Owner comment\nowner: kept\n---\n\n",
			} {
				root := t.TempDir()
				path := filepath.Join(root, ".cursor", "rules", "just-mcp-work.mdc")
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				owner := "Owner instructions." + lineBreak
				before := withLineBreak(header, lineBreak) + owner
				if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
					t.Fatal(err)
				}
				options := nativeTestOptions(t, root, "cursor")
				if _, err := Apply(options); err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Contains(after, []byte(owner)) {
					t.Fatalf("Cursor rule = %q, %v", after, err)
				}
				if strings.Contains(header, "owner: kept") {
					if !bytes.HasPrefix(after, []byte(before)) {
						t.Fatalf("Cursor init changed owner metadata: %q", after)
					}
				} else if !bytes.Contains(after, []byte("alwaysApply: true"+lineBreak)) {
					t.Fatalf("Cursor init did not upgrade legacy metadata: %q", after)
				}
				if result, applyErr := Apply(options); applyErr != nil || len(result.Paths) != 0 {
					t.Fatalf("repeat Cursor init = %q, %v", result.Paths, applyErr)
				}
			}
		})
	}
}

func TestCursorRuleRefusesCustomMetadataThatDoesNotAlwaysApply(t *testing.T) {
	for _, test := range []struct {
		name   string
		before string
	}{
		{
			name:   "multiline root scalar",
			before: "---\n'\nalwaysApply: true\n'\n---\nOwner text.\n",
		},
		{name: "no front matter", before: "Owner text without front matter.\n"},
		{name: "missing key", before: "---\ndescription: Owner\n---\nOwner text.\n"},
		{name: "false", before: "---\nalwaysApply: false\n---\nOwner text.\n"},
		{name: "sequence", before: "---\n- alwaysApply: true\n---\nOwner text.\n"},
		{name: "null", before: "---\nnull\n---\nOwner text.\n"},
		{name: "boolean scalar", before: "---\ntrue\n---\nOwner text.\n"},
		{
			name:   "string true",
			before: "---\nalwaysApply: \"true\"\n---\nOwner text.\n",
		},
		{
			name:   "plain duplicate",
			before: "---\nalwaysApply: true\nalwaysApply: false\n---\nOwner text.\n",
		},
		{
			name:   "quoted duplicate",
			before: "---\nalwaysApply: true\n'alwaysApply': false\n---\nOwner text.\n",
		},
		{
			name:   "spaced duplicate",
			before: "---\nalwaysApply: true\nalwaysApply : false\n---\nOwner text.\n",
		},
		{
			name:   "double quoted duplicate",
			before: "---\n\"alwaysApply\": false\nalwaysApply: true\n---\nOwner text.\n",
		},
		{
			name:   "equal duplicate",
			before: "---\nalwaysApply: true\n\"alwaysApply\" : true\n---\nOwner text.\n",
		},
		{
			name:   "malformed mapping",
			before: "---\nalwaysApply: true\nowner: [unterminated\n---\nOwner text.\n",
		},
		{
			name: "end marker trailing malformed content",
			before: "---\nalwaysApply: true\n...\nowner: [unterminated\n" +
				"---\nOwner text.\n",
		},
		{
			name: "additional document before exact delimiter",
			before: "---\nalwaysApply: true\n--- \nowner: kept\n" +
				"---\nOwner text.\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".cursor", "rules", "just-mcp-work.mdc")
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.before), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(nativeTestOptions(t, root, "cursor")); err == nil ||
				!strings.Contains(err.Error(), "alwaysApply: true") {
				t.Fatalf("Cursor accepted inactive custom rule: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != test.before {
				t.Fatalf("refused Cursor init changed owner rule: %q, %v", after, err)
			}
			if _, err = os.Stat(filepath.Join(root, ".cursor", "mcp.json")); !os.IsNotExist(err) {
				t.Fatalf("refused Cursor init wrote native config: %v", err)
			}
		})
	}
}

func TestCursorRuleMetadataErrorsUseFileLineNumbers(t *testing.T) {
	for _, lineBreak := range []string{"\n", "\r\n"} {
		before := withLineBreak(
			"---\ndescription: Owner\nalwaysApply: true\nowner: \"\\q\"\n---\nOwner text.\n",
			lineBreak,
		)
		_, err := cursorRuleMetadata([]byte(before))
		if err == nil || !strings.Contains(err.Error(), "line 4:") ||
			!strings.Contains(err.Error(), "alwaysApply: true") {
			t.Fatalf("Cursor metadata diagnostic uses the wrong file line: %v", err)
		}
	}
}

func TestCursorMCPRejectsEscapingConfigSymlinkBeforeWrites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	root := t.TempDir()
	out := filepath.Join(t.TempDir(), "mcp.json")
	before := []byte("{\"owner\":true}\n")
	if err := os.WriteFile(out, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".cursor"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(root, ".cursor", "mcp.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(nativeTestOptions(t, root, "cursor")); err == nil {
		t.Fatal("Cursor init accepted config outside workspace")
	}
	after, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("Cursor init changed escaped config: %q, %v", after, err)
	}
	if _, err = os.Stat(filepath.Join(root, ".cursor", "rules")); !os.IsNotExist(err) {
		t.Fatalf("failed preflight wrote Cursor rules: %v", err)
	}
}
