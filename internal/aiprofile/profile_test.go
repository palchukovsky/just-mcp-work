// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package aiprofile

import (
	"slices"
	"strings"
	"testing"
)

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

// TestCanonicalKeepsAbsenceAndRejectsOtherProfiles pins that the absence of a
// profile stays an absence instead of turning into a named placeholder family,
// and that the placeholder older releases wrote is not accepted as a family.
func TestCanonicalKeepsAbsenceAndRejectsOtherProfiles(t *testing.T) {
	got, err := Canonical(Profile{})
	if err != nil || got.Declared() {
		t.Fatalf("Canonical(zero) = %#v, %v, want no profile", got, err)
	}

	modified := profile(FamilyCodex)
	modified.Version = "2"
	if _, err := Canonical(modified); err == nil {
		t.Fatal("Canonical accepted a modified profile")
	}
	retired := Profile{
		Family:    "unknown",
		ID:        "jmw/unknown",
		Version:   profileVersion,
		Transport: stdioTransport,
	}
	if _, err := Canonical(retired); err == nil {
		t.Fatal("Canonical accepted the placeholder profile of older releases")
	}
}

func TestParseSelectionCanonicalizesDeclaredFamilies(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []string
		want   Selection
	}{
		{name: "one", values: []string{"claude"}, want: Selection{FamilyClaude}},
		{
			name:   "declared order",
			values: []string{"claude", "codex"},
			want:   Selection{FamilyCodex, FamilyClaude},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseSelection(test.values)
			if err != nil || !slices.Equal(got, test.want) {
				t.Fatalf("ParseSelection(%#v) = %#v, %v, want %#v", test.values, got, err, test.want)
			}
		})
	}
}

func TestParseSelectionRejectsUnusableAnswers(t *testing.T) {
	for _, test := range []struct {
		name   string
		want   string
		values []string
	}{
		{
			name:   "nothing selected",
			values: nil,
			want:   "no AI family is selected; choose any of codex, claude",
		},
		{
			name:   "repeated family",
			values: []string{"codex", "codex"},
			want:   `AI family "codex" is selected twice`,
		},
		{
			name:   "placeholder of older releases",
			values: []string{"unknown"},
			want:   `unsupported AI family "unknown"; must be one of codex, claude`,
		},
		{
			name:   "undeclarable family",
			values: []string{"gemini"},
			want:   `unsupported AI family "gemini"`,
		},
		{
			name:   "empty family",
			values: []string{""},
			want:   `unsupported AI family ""`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseSelection(test.values)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseSelection(%#v) error = %v, want %q", test.values, err, test.want)
			}
		})
	}
}

// TestSelectionProfileForServesOnlyDeclaredFamilies pins what each generated
// configuration gets: its own family's profile when it is declared, and no
// profile when it is not, so one client's declaration never reaches another's
// file.
func TestSelectionProfileForServesOnlyDeclaredFamilies(t *testing.T) {
	selection := Selection{FamilyCodex}
	if got := selection.ProfileFor(FamilyCodex); got != profile(FamilyCodex) {
		t.Fatalf("ProfileFor(codex) = %#v, want the codex profile", got)
	}
	if got := selection.ProfileFor(FamilyClaude); got.Declared() {
		t.Fatalf("ProfileFor(claude) = %#v, want no profile", got)
	}
}
