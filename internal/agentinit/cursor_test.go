// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
