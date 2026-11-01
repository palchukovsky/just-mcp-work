// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"slices"
	"testing"
)

func TestDefaultAgentsReturnsIndependentOrderedSelections(t *testing.T) {
	want := []string{"claude", "codex", "cursor", "antigravity"}
	first := DefaultAgents()
	if !slices.Equal(first, want) {
		t.Fatalf("DefaultAgents() = %q, want %q", first, want)
	}
	first[0] = "modified"
	if next := DefaultAgents(); !slices.Equal(next, want) {
		t.Fatalf("changing returned defaults changed the next selection: %q", next)
	}
}

func TestClientCatalogsKeepGrokLast(t *testing.T) {
	targets := agentTargets()
	if last := targets[len(targets)-1].name; last != "grok" {
		t.Fatalf("last instruction client = %q, want grok", last)
	}
	configs := nativeMCPConfigs()
	if last := configs[len(configs)-1].agent; last != "grok" {
		t.Fatalf("last native MCP client = %q, want grok", last)
	}
}

func TestSelectedAgentsNormalizesOrDefaults(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		agents []string
		want   []string
	}{
		{name: "none named", agents: nil, want: []string{"antigravity", "claude", "codex", "cursor"}},
		{
			name:   "mixed case and duplicates",
			agents: []string{" Codex", "claude", "CLAUDE"},
			want:   []string{"claude", "codex"},
		},
		{name: "only blanks", agents: []string{" "}, want: []string{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := SelectedAgents(testCase.agents); !slices.Equal(got, testCase.want) {
				t.Fatalf("SelectedAgents(%q) = %q, want %q", testCase.agents, got, testCase.want)
			}
		})
	}
}

func TestOfferShellPermissionDefaultsToAskWithoutARecord(t *testing.T) {
	offer, current, err := OfferShellPermission(t.TempDir(), []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if offer != ShellPermissionAsk || current {
		t.Fatalf("OfferShellPermission() = (%q, %t), want (ask, false)", offer, current)
	}
}

func TestOfferShellPermissionOffersTheRecordedChoice(t *testing.T) {
	root := t.TempDir()
	if _, err := Apply(Options{
		InstructionsTarget: InstructionsTargetWorkspace,
		ShellPermission:    ShellPermissionAllow,
		Dir:                root,
		Agents:             []string{"codex"},
		WriteMCPConfig:     true,
		AIFamilies:         testAIFamilies(),
		RunnerModes:        testRunnerModes(t),
	}); err != nil {
		t.Fatal(err)
	}

	offer, current, err := OfferShellPermission(root, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if offer != ShellPermissionAllow || !current {
		t.Fatalf("OfferShellPermission() = (%q, %t), want (allow, true)", offer, current)
	}
}
