// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package gorunner

import (
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

func TestCatalogRejectsInvalidGoCommandTables(t *testing.T) {
	valid := taskSpec{
		id:          "build",
		name:        "build",
		description: "Build packages.",
		argv:        []string{"build", "./..."},
		modes:       []runner.Mode{runner.ModeSafe, runner.ModeAll},
	}
	tests := []struct {
		name  string
		specs []taskSpec
	}{
		{name: "empty"},
		{name: "duplicate", specs: []taskSpec{valid, valid}},
		{
			name: "fixed without argv",
			specs: []taskSpec{{
				id:          "build",
				name:        "build",
				description: "Build.",
				modes:       []runner.Mode{runner.ModeAll},
			}},
		},
		{
			name: "arbitrary with fixed argv",
			specs: []taskSpec{{
				id:          "any",
				name:        "any",
				description: "Any.",
				argv:        []string{"test"},
				modes:       []runner.Mode{runner.ModeAll},
				arbitrary:   true,
			}},
		},
		{
			name: "safe without all",
			specs: []taskSpec{{
				id:          "build",
				name:        "build",
				description: "Build.",
				argv:        []string{"build"},
				modes:       []runner.Mode{runner.ModeSafe},
			}},
		},
		{
			name: "arbitrary in safe",
			specs: []taskSpec{{
				id:          "any",
				name:        "any",
				description: "Any.",
				modes:       []runner.Mode{runner.ModeSafe, runner.ModeAll},
				arbitrary:   true,
			}},
		},
		{
			name: "invalid mode",
			specs: []taskSpec{{
				id:          "build",
				name:        "build",
				description: "Build.",
				argv:        []string{"build"},
				modes:       []runner.Mode{runner.Mode("invalid")},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog, err := runner.NewCatalog(registration("", test.specs))
			if err != nil {
				t.Fatalf("NewCatalog structural validation: %v", err)
			}
			if _, err := catalog.Resolve(nil); err == nil {
				t.Fatal("Resolve accepted an invalid Go command table")
			}
		})
	}
}
