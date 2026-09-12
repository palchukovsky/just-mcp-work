// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package executor

import (
	"slices"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

func TestRunMetaReturnsDeepCopy(t *testing.T) {
	run := &Run{meta: runstore.Meta{
		Args:       []string{"argument"},
		WriteScope: []string{"/scope"},
	}}

	snapshot := run.Meta()
	snapshot.Args[0] = "changed argument"
	snapshot.WriteScope[0] = "/changed-scope"

	meta := run.Meta()
	if !slices.Equal(meta.Args, []string{"argument"}) {
		t.Fatalf("Meta Args after caller mutation = %q, want independent copy", meta.Args)
	}
	if !slices.Equal(meta.WriteScope, []string{"/scope"}) {
		t.Fatalf("Meta WriteScope after caller mutation = %q, want independent copy", meta.WriteScope)
	}
}

func TestTailRetainsNewestBytes(t *testing.T) {
	tail := NewTail(5)
	assertWrite(t, tail, "abc")
	if tail.Truncated() {
		t.Fatal("short initial write was marked truncated")
	}
	assertWrite(t, tail, "def")
	if got := tail.String(); got != "bcdef" {
		t.Fatalf("tail = %q, want %q", got, "bcdef")
	}
	if !tail.Truncated() {
		t.Fatal("overflow was not marked truncated")
	}
}

func TestTailExactCapacityIsNotTruncated(t *testing.T) {
	tail := NewTail(5)
	assertWrite(t, tail, "abcde")
	if got := tail.String(); got != "abcde" {
		t.Fatalf("tail = %q, want %q", got, "abcde")
	}
	if tail.Truncated() {
		t.Fatal("exact-capacity initial write was marked truncated")
	}
}

func TestTailOversizedAndZeroCapacity(t *testing.T) {
	tail := NewTail(4)
	assertWrite(t, tail, "abcdefgh")
	if got := tail.String(); got != "efgh" || !tail.Truncated() {
		t.Fatalf("oversized tail = %q, truncated = %v", got, tail.Truncated())
	}

	empty := NewTail(-1)
	assertWrite(t, empty, "discarded")
	if got := empty.String(); got != "" || !empty.Truncated() {
		t.Fatalf("zero-capacity tail = %q, truncated = %v", got, empty.Truncated())
	}
}

func assertWrite(t *testing.T, tail *Tail, value string) {
	t.Helper()
	n, err := tail.Write([]byte(value))
	if err != nil || n != len(value) {
		t.Fatalf("Write(%q) = %d, %v", value, n, err)
	}
}
