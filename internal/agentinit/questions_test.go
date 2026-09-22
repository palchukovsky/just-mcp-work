// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"slices"
	"testing"
)

func TestSelectedAgentsNormalizesOrDefaults(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		agents []string
		want   []string
	}{
		{name: "none named", agents: nil, want: []string{"claude", "codex", "cursor"}},
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
