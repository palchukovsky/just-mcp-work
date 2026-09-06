// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package aiprofile

import "testing"

func TestParseAcceptsOnlyDeclaredFamilies(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  Profile
	}{
		{name: "codex", value: "codex", want: profile(FamilyCodex)},
		{name: "claude", value: "claude", want: profile(FamilyClaude)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.value)
			if err != nil || got != test.want {
				t.Fatalf("Parse(%q) = %#v, %v, want %#v", test.value, got, err, test.want)
			}
		})
	}

	for _, value := range []string{"", "unknown", "Codex", " codex", "gemini"} {
		t.Run("reject_"+value, func(t *testing.T) {
			if _, err := Parse(value); err == nil {
				t.Fatalf("Parse(%q) accepted an unsupported explicit value", value)
			}
		})
	}
}

func TestCanonicalMapsOnlyAbsenceToUnknown(t *testing.T) {
	got, err := Canonical(Profile{})
	if err != nil || got != Unknown() {
		t.Fatalf("Canonical(zero) = %#v, %v, want %#v", got, err, Unknown())
	}

	invalid := Unknown()
	invalid.Version = "2"
	if _, err := Canonical(invalid); err == nil {
		t.Fatal("Canonical accepted a modified profile")
	}
}
