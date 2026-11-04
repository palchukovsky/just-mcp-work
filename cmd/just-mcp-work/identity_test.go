// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/agentinit"
)

func TestInitRejectsAntigravityIdentityBeforeQuestions(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
		args []string
	}{
		{name: "empty", args: []string{"--antigravity-server-name="}, want: "must not be empty"},
		{
			name: "syntax",
			args: []string{"--antigravity-server-name=bad/name"},
			want: "invalid Antigravity server name",
		},
		{
			name: "selection",
			args: []string{"--agents=codex", "--antigravity-server-name=jmw-custom"},
			want: "--antigravity-server-name requires --agents antigravity",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			err := initCommandWithIO(test.args, strings.NewReader(""), io.Discard, &diagnostics)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("identity flag did not fail before questions: %v, want %q", err, test.want)
			}
			if diagnostics.Len() != 0 {
				t.Fatalf("invalid identity reached questionnaire: %s", diagnostics.String())
			}
		})
	}
}

func TestInitAntigravityServerNamePersistsRecordedChoices(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	args := append(initArgsWithRunnerModesWithoutAI(root),
		"--agents", "antigravity", "--ai", "antigravity",
		"--write-mcp-config=true", "--shell-permission", "ask",
		"--antigravity-server-name", "jmw-workspace-explicit")
	if err := initWithBetaTest(false, args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".agents", "mcp_config.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantAnswers := readRecordedInitAnswers(t, root)
	args = append(initArgsWithRunnerModesWithoutAI(root),
		"--agents", "antigravity", "--keep-recorded", "--write-mcp-config=true")
	if err = initWithBetaTest(false, args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatalf("recorded scoped rerun failed: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("recorded identity changed config: %v, %s", err, after)
	}
	if got := readRecordedInitAnswers(t, root); !reflect.DeepEqual(got, wantAnswers) {
		t.Fatalf("identity rerun changed other recorded answers: %+v, want %+v", got, wantAnswers)
	}
	var config struct {
		Servers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err = json.Unmarshal(after, &config); err != nil {
		t.Fatal(err)
	}
	want := []string{"serve", "--root", root, "--ai", "antigravity"}
	if !slices.Equal(config.Servers["jmw-workspace-explicit"].Args, want) {
		t.Fatalf("scoped identity lost Antigravity profile: %s", after)
	}
	if _, err = agentinit.VerifyManagedSurfaces(root); err != nil {
		t.Fatal(err)
	}
	if err = initCommandWithIO([]string{"--antigravity-server-name="},
		strings.NewReader(""), io.Discard, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("explicit empty identity accepted: %v", err)
	}
}
