// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package aiprofile describes the caller-declared AI presentation profile.
package aiprofile

import (
	"fmt"
	"slices"
	"strings"
)

const (
	profileVersion = "1"
	stdioTransport = "mcp-stdio"
)

// Family is the closed set of AI families accepted by serve.
type Family string

const (
	FamilyCodex  Family = "codex"
	FamilyClaude Family = "claude"
)

// Declarable lists the families an operator can declare, in the order they are
// offered and stored.
func Declarable() []Family {
	return []Family{FamilyCodex, FamilyClaude}
}

// Profile is caller-declared presentation provenance, not an authenticated
// identity or a capability declaration. The zero value declares no profile; it
// is what serve runs with when it has no --ai argument.
type Profile struct {
	Family    Family `json:"family"`
	ID        string `json:"profile_id"`
	Version   string `json:"profile_version"`
	Transport string `json:"transport"`
}

// Declared reports whether the profile names a family rather than being the
// absence of one.
func (p Profile) Declared() bool {
	return p != Profile{}
}

// Parse returns the profile for an explicitly declared AI family.
func Parse(value string) (Profile, error) {
	family := Family(value)
	if !slices.Contains(Declarable(), family) {
		return Profile{}, fmt.Errorf("AI family must be one of %s", familyList(Declarable()))
	}
	return profile(family), nil
}

// Canonical validates a profile. The zero value is the absence of a profile,
// the same absence as an omitted --ai argument, and is returned unchanged.
func Canonical(value Profile) (Profile, error) {
	if !value.Declared() {
		return Profile{}, nil
	}
	if !slices.Contains(Declarable(), value.Family) {
		return Profile{}, fmt.Errorf("unsupported AI family %q", value.Family)
	}
	if value != profile(value.Family) {
		return Profile{}, fmt.Errorf("invalid AI profile for family %q", value.Family)
	}
	return value, nil
}

func profile(family Family) Profile {
	return Profile{
		Family:    family,
		ID:        "jmw/" + string(family),
		Version:   profileVersion,
		Transport: stdioTransport,
	}
}

// Selection is the set of families declared for one workspace. A workspace
// generates a server configuration per client, so several families can be
// declared at once, each in the configuration its client reads. A selection
// names at least one family.
type Selection []Family

// ParseSelection canonicalizes explicitly chosen family names.
func ParseSelection(values []string) (Selection, error) {
	selection := make(Selection, 0, len(values))
	for _, value := range values {
		selection = append(selection, Family(value))
	}
	return selection.Canonical()
}

// Canonical validates a selection and returns it in the declared order. It
// rejects an empty selection, an unsupported family, and a repeated one.
func (s Selection) Canonical() (Selection, error) {
	if len(s) == 0 {
		return nil, fmt.Errorf(
			"no AI family is selected; choose any of %s",
			familyList(Declarable()),
		)
	}
	selected := make(Selection, 0, len(s))
	for _, family := range s {
		if !slices.Contains(Declarable(), family) {
			return nil, fmt.Errorf(
				"unsupported AI family %q; must be one of %s",
				family,
				familyList(Declarable()),
			)
		}
		if slices.Contains(selected, family) {
			return nil, fmt.Errorf("AI family %q is selected twice", family)
		}
		selected = append(selected, family)
	}
	canonical := make(Selection, 0, len(selected))
	for _, family := range Declarable() {
		if slices.Contains(selected, family) {
			canonical = append(canonical, family)
		}
	}
	return canonical, nil
}

// ProfileFor returns the profile a configuration read by family should carry:
// that family's profile when it is declared, and no profile when it is not.
func (s Selection) ProfileFor(family Family) Profile {
	if !slices.Contains(s, family) {
		return Profile{}
	}
	return profile(family)
}

func familyList(families []Family) string {
	names := make([]string, 0, len(families))
	for _, family := range families {
		names = append(names, string(family))
	}
	return strings.Join(names, ", ")
}
