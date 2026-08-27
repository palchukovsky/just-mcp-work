// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package just

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTasksFromDumpRejectsInvalidModuleMetadata(t *testing.T) {
	_, err := tasksFromDump(justDump{
		Modules: map[string]json.RawMessage{
			"invalid": []byte("{"),
		},
		Recipes: map[string]justRecipe{
			"check": {Name: "check"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "encode just modules metadata") {
		t.Fatalf("tasksFromDump error = %v, want module metadata encoding context", err)
	}
}
