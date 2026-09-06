// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package aiprofile describes the caller-declared AI presentation profile.
package aiprofile

import "fmt"

const (
	profileVersion = "1"
	stdioTransport = "mcp-stdio"
)

// Family is the closed set of AI families accepted by serve.
type Family string

const (
	FamilyUnknown Family = "unknown"
	FamilyCodex   Family = "codex"
	FamilyClaude  Family = "claude"
)

// Profile is caller-declared presentation provenance, not an authenticated
// identity or a capability declaration.
type Profile struct {
	Family    Family `json:"family"`
	ID        string `json:"profile_id"`
	Version   string `json:"profile_version"`
	Transport string `json:"transport"`
}

// Unknown returns the profile used when serve has no --ai argument.
func Unknown() Profile {
	return profile(FamilyUnknown)
}

// Parse returns the profile for an explicitly declared AI family.
func Parse(value string) (Profile, error) {
	family := Family(value)
	switch family {
	case FamilyCodex, FamilyClaude:
		return profile(family), nil
	case FamilyUnknown:
		// Listed so the closed set stays exhaustive; unknown is not declarable
		// and shares the rejection below with every unsupported value.
	}
	return Profile{}, fmt.Errorf("AI family must be one of codex, claude")
}

// Canonical validates a profile and maps an absent internal value to unknown.
// The zero value represents the same absence as an omitted --ai argument.
func Canonical(value Profile) (Profile, error) {
	if value == (Profile{}) {
		return Unknown(), nil
	}
	switch value.Family {
	case FamilyUnknown, FamilyCodex, FamilyClaude:
	default:
		return Profile{}, fmt.Errorf("unsupported AI family %q", value.Family)
	}
	want := profile(value.Family)
	if value != want {
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
