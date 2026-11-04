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
	args := append(
		initArgsWithRunnerModesWithoutAI(root),
		"--agents=antigravity",
		"--ai=antigravity",
		"--write-mcp-config=true",
		"--shell-permission=ask",
		"--antigravity-server-name=jmw-workspace-explicit",
	)
	err := initWithBetaTest(false, args, strings.NewReader(""), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".agents", "mcp_config.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantAnswers := readRecordedInitAnswers(t, root)
	args = append(
		initArgsWithRunnerModesWithoutAI(root),
		"--agents=antigravity",
		"--keep-recorded",
		"--write-mcp-config=true",
	)
	err = initWithBetaTest(false, args, strings.NewReader(""), io.Discard, io.Discard)
	if err != nil {
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
	err = initCommandWithIO(
		[]string{"--antigravity-server-name="},
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("explicit empty identity accepted: %v", err)
	}
}

func TestInitRepairsLostAntigravityIdentityWithExplicitName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	args := append(
		initArgsWithRunnerModesWithoutAI(root),
		"--agents=antigravity",
		"--ai=antigravity",
		"--write-mcp-config=false",
		"--shell-permission=ask",
		"--antigravity-server-name=jmw-lost",
	)
	err := initWithBetaTest(false, args, strings.NewReader(""), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, ".just-mcp-work", "managed.json")
	document := readManifestDocument(t, manifestPath)
	delete(document, "antigravity_server_name")
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- manifestPath is inside this test's temporary directory.
	if err = os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	rerun := append(
		initArgsWithRunnerModesWithoutAI(root),
		"--agents=antigravity",
		"--keep-recorded",
		"--write-mcp-config=true",
		"--shell-permission=ask",
	)
	err = initWithBetaTest(false, rerun, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--antigravity-server-name") {
		t.Fatalf("lost identity not refused with its repair: %v", err)
	}
	rerun = append(rerun, "--antigravity-server-name=jmw-repaired")
	err = initWithBetaTest(false, rerun, strings.NewReader(""), io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("explicit identity repair failed: %v", err)
	}
	assertRepairedIdentity(t, root, document, "jmw-repaired")
}

func readManifestDocument(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

// assertRepairedIdentity checks a repair recorded and wrote name and kept the
// recorded answers. The shell answer is recorded only once a managed shell
// surface is written, so it is not compared.
func assertRepairedIdentity(
	t *testing.T,
	root string,
	before map[string]json.RawMessage,
	name string,
) {
	t.Helper()
	repaired := readManifestDocument(t, filepath.Join(root, ".just-mcp-work", "managed.json"))
	if string(repaired["antigravity_server_name"]) != `"`+name+`"` {
		t.Fatalf("identity repair recorded %s", repaired["antigravity_server_name"])
	}
	for _, answer := range []string{"beta_test", "ai_families"} {
		if !bytes.Equal(repaired[answer], before[answer]) {
			t.Fatalf(
				"identity repair changed recorded %s: %s, want %s",
				answer,
				repaired[answer],
				before[answer],
			)
		}
	}
	config, err := os.ReadFile(filepath.Join(root, ".agents", "mcp_config.json"))
	if err != nil || !bytes.Contains(config, []byte(`"`+name+`"`)) {
		t.Fatalf("identity repair did not write the selected entry: %v, %s", err, config)
	}
	if _, err = agentinit.VerifyManagedSurfaces(root); err != nil {
		t.Fatal(err)
	}
}
