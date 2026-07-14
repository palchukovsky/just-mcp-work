// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package version

import "testing"

func TestDetectPrefersReleaseAndRejectsDevelopmentVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		release       string
		module        string
		wantTag       string
		wantAvailable bool
	}{
		{
			name:          "release tag",
			release:       "v0.2.1",
			module:        "(devel)",
			wantTag:       "v0.2.1",
			wantAvailable: true,
		},
		{
			name:          "go install build information",
			release:       "dev",
			module:        "v1.2.3",
			wantTag:       "v1.2.3",
			wantAvailable: true,
		},
		{
			name:          "pseudo version",
			release:       "dev",
			module:        "v0.0.0-20260718123456-abcdef123456",
			wantAvailable: false,
		},
		{
			name:          "development build",
			release:       "dev",
			module:        "(devel)",
			wantAvailable: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := Detect(test.release, test.module)
			if got.Available() != test.wantAvailable || got.Tag != test.wantTag {
				t.Fatalf("Detect(%q, %q) = %#v", test.release, test.module, got)
			}
		})
	}
}
