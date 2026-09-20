// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runstore

import (
	"bytes"
	"context"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSearchLogStreamsLineEndingsFinalFragmentAndEmptyLog(t *testing.T) {
	stdout := []byte("first\r\nneedle stdout\r\nfinal needle")
	stderr := []byte("needle stderr\n")
	store, runID := newLogSearchFixture(t, stdout, stderr)
	pattern := regexp.MustCompile("needle")

	stdoutResult, err := store.SearchLog(
		context.Background(),
		runID,
		"stdout",
		0,
		pattern,
		LogSearchOptions{MaxMatches: 20, ContextLines: 0, TextBudget: 64 << 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !stdoutResult.Complete || stdoutResult.NextOffset != int64(len(stdout)) {
		t.Fatalf("stdout cursor = %#v, want the snapshot end", stdoutResult)
	}
	wantStdoutOffsets := []int64{
		int64(bytes.Index(stdout, []byte("needle stdout"))),
		int64(bytes.Index(stdout, []byte("final needle"))),
	}
	if len(stdoutResult.Matches) != len(wantStdoutOffsets) {
		t.Fatalf("stdout matches = %#v, want %d", stdoutResult.Matches, len(wantStdoutOffsets))
	}
	for index, match := range stdoutResult.Matches {
		if match.Offset != wantStdoutOffsets[index] {
			t.Errorf("stdout match %d offset = %d, want %d", index, match.Offset, wantStdoutOffsets[index])
		}
		if strings.Contains(match.Text, "\r") {
			t.Errorf("stdout match %d text retained CR: %q", index, match.Text)
		}
	}

	stderrResult, err := store.SearchLog(
		context.Background(),
		runID,
		"stderr",
		0,
		pattern,
		LogSearchOptions{MaxMatches: 20, ContextLines: 0, TextBudget: 64 << 10},
	)
	if err != nil || len(stderrResult.Matches) != 1 || stderrResult.Matches[0].Offset != 0 {
		t.Fatalf("stderr search = %#v, %v", stderrResult, err)
	}

	emptyStore, emptyRunID := newLogSearchFixture(t, nil, nil)
	empty, err := emptyStore.SearchLog(
		context.Background(),
		emptyRunID,
		"stdout",
		0,
		pattern,
		LogSearchOptions{MaxMatches: 20, ContextLines: 0, TextBudget: 64 << 10},
	)
	if err != nil || !empty.Complete || empty.NextOffset != 0 || len(empty.Matches) != 0 {
		t.Fatalf("empty search = %#v, %v", empty, err)
	}
}

func TestSearchLogBeforeContextStopsAtFirstLineOutsideTextBudget(t *testing.T) {
	store, runID := newLogSearchFixture(t, []byte("old\nnewer-too-long\nhit\n"), nil)

	result, err := store.SearchLog(
		context.Background(),
		runID,
		"stdout",
		0,
		regexp.MustCompile("hit"),
		LogSearchOptions{MaxMatches: 1, ContextLines: 2, TextBudget: len("hit") + len("old")},
	)
	if err != nil || len(result.Matches) != 1 {
		t.Fatalf("search = %#v, %v", result, err)
	}
	if before := result.Matches[0].Before; len(before) != 0 {
		t.Fatalf("before context = %#v, want no context after newer line exhausts text budget", before)
	}
}

//nolint:gocyclo // The assertions cover one pass's connected context and cursor invariants.
func TestSearchLogContextCursorAndForwardPass(t *testing.T) {
	content := []byte("hit first\none\ntwo\nhit second\nthree\n")
	store, runID := newLogSearchFixture(t, content, nil)
	pattern := regexp.MustCompile("hit")

	first, err := store.SearchLog(
		context.Background(),
		runID,
		"stdout",
		0,
		pattern,
		LogSearchOptions{MaxMatches: 1, ContextLines: 2, TextBudget: 64 << 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondOffset := int64(bytes.Index(content, []byte("hit second")))
	if len(first.Matches) != 1 || len(first.Matches[0].Before) != 0 {
		t.Fatalf("first match near scan start = %#v", first.Matches)
	}
	if strings.Join(first.Matches[0].After, ",") != "one,two" {
		t.Fatalf("first match after context = %#v, want [one two]", first.Matches[0].After)
	}
	if !first.MoreMatches || first.NextOffset != secondOffset {
		t.Fatalf(
			"first cursor = more:%t offset:%d, want more:true offset:%d",
			first.MoreMatches,
			first.NextOffset,
			secondOffset,
		)
	}

	second, err := store.SearchLog(
		context.Background(),
		runID,
		"stdout",
		first.NextOffset,
		pattern,
		LogSearchOptions{MaxMatches: 1, ContextLines: 2, TextBudget: 64 << 10},
	)
	if err != nil || len(second.Matches) != 1 {
		t.Fatalf("resumed search = %#v, %v", second, err)
	}
	if second.Matches[0].Offset != secondOffset || second.Matches[0].Text != "hit second" {
		t.Fatalf(
			"resumed first match = %#v, want hit second at %d",
			second.Matches[0],
			secondOffset,
		)
	}

	forward, err := store.SearchLog(
		context.Background(),
		runID,
		"stdout",
		0,
		pattern,
		LogSearchOptions{MaxMatches: 2, ContextLines: 3, TextBudget: 64 << 10},
	)
	if err != nil || len(forward.Matches) != 2 {
		t.Fatalf("forward search = %#v, %v", forward, err)
	}
	if after := forward.Matches[0].After; len(after) != 3 || after[2] != "hit second" {
		t.Fatalf("matching after-context line = %#v, want hit second once", after)
	}
	if forward.Matches[1].Text != "hit second" {
		t.Fatalf("matching context line was not returned normally: %#v", forward.Matches[1])
	}
}

func TestSearchLogOffsetMidLineIsTheReportedLineStart(t *testing.T) {
	content := []byte("discard prefix needle suffix\n")
	store, runID := newLogSearchFixture(t, content, nil)
	offset := int64(len("discard "))
	result, err := store.SearchLog(
		context.Background(),
		runID,
		"stdout",
		offset,
		regexp.MustCompile("needle"),
		LogSearchOptions{MaxMatches: 20, ContextLines: 0, TextBudget: 64 << 10},
	)
	if err != nil || len(result.Matches) != 1 || result.Matches[0].Offset != offset {
		t.Fatalf("mid-line search = %#v, %v", result, err)
	}
}

func TestSearchLogReportsClippedLinesAndKeepsTheMatchInTheExcerpt(t *testing.T) {
	line := bytes.Repeat([]byte{'a'}, logSearchLineBytes+128)
	const matchOffset = 4096
	copy(line[matchOffset:], "needle")
	content := append([]byte(nil), line...)
	content = append(content, '\n')
	store, runID := newLogSearchFixture(t, content, nil)
	result, err := store.SearchLog(
		context.Background(),
		runID,
		"stdout",
		0,
		regexp.MustCompile("needle"),
		LogSearchOptions{MaxMatches: 20, ContextLines: 0, TextBudget: 64 << 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ClippedLines {
		t.Fatalf("clipped_lines = false for a %d-byte line", len(line))
	}
	if len(result.Matches) != 1 || !result.Matches[0].Clipped {
		t.Fatalf("long-line matches = %#v, want one clipped match", result.Matches)
	}
	if !strings.Contains(result.Matches[0].Text, "needle") {
		t.Fatalf("long-line excerpt does not contain %q", "needle")
	}

	beyondCap := bytes.Repeat([]byte{'a'}, logSearchLineBytes+128)
	copy(beyondCap[logSearchLineBytes+16:], "needle")
	beyondStore, beyondRunID := newLogSearchFixture(t, beyondCap, nil)
	beyond, err := beyondStore.SearchLog(
		context.Background(),
		beyondRunID,
		"stdout",
		0,
		regexp.MustCompile("needle"),
		LogSearchOptions{MaxMatches: 20, ContextLines: 0, TextBudget: 64 << 10},
	)
	if err != nil || !beyond.Complete || !beyond.ClippedLines || len(beyond.Matches) != 0 {
		t.Fatalf("beyond-cap search = %#v, %v", beyond, err)
	}
}

func TestSearchLogSanitisesInvalidUTF8(t *testing.T) {
	content := []byte{'b', 'e', 'f', 'o', 'r', 'e', 0xff, 'n', 'e', 'e', 'd', 'l', 'e'}
	store, runID := newLogSearchFixture(t, content, nil)
	result, err := store.SearchLog(
		context.Background(),
		runID,
		"stdout",
		0,
		regexp.MustCompile("needle"),
		LogSearchOptions{MaxMatches: 20, ContextLines: 0, TextBudget: 64 << 10},
	)
	if err != nil || len(result.Matches) != 1 {
		t.Fatalf("invalid UTF-8 search = %#v, %v", result, err)
	}
	match := result.Matches[0]
	if !match.Lossy || !utf8.ValidString(match.Text) || !strings.Contains(match.Text, "\uFFFD") {
		t.Fatalf("sanitised match = %#v", match)
	}
}

func TestSearchLogMatchExcerptContainsMatchNearLimit(t *testing.T) {
	line := bytes.Repeat([]byte{'a'}, logSearchExcerptBytes+128)
	matchOffset := logSearchExcerptBytes - 1
	copy(line[matchOffset:], "needle")
	store, runID := newLogSearchFixture(t, line, nil)
	result, err := store.SearchLog(
		context.Background(),
		runID,
		"stdout",
		0,
		regexp.MustCompile("needle"),
		LogSearchOptions{MaxMatches: 20, ContextLines: 0, TextBudget: 64 << 10},
	)
	if err != nil || len(result.Matches) != 1 {
		t.Fatalf("near-limit search = %#v, %v", result, err)
	}
	if !strings.Contains(result.Matches[0].Text, "needle") {
		t.Fatalf("match excerpt does not contain %q at offset %d", "needle", matchOffset)
	}
}

func TestSearchLogMatchExcerptKeepsInvalidPrefixAndMatch(t *testing.T) {
	prefix := bytes.Repeat([]byte{0xff, 'x'}, 257)
	line := append([]byte(nil), prefix...)
	line = append(line, []byte("needle")...)
	line = append(line, bytes.Repeat([]byte{0x80}, logSearchExcerptBytes)...)
	store, runID := newLogSearchFixture(t, line, nil)
	result, err := store.SearchLog(
		context.Background(),
		runID,
		"stdout",
		0,
		regexp.MustCompile("needle"),
		LogSearchOptions{MaxMatches: 20, ContextLines: 0, TextBudget: 64 << 10},
	)
	if err != nil || len(result.Matches) != 1 {
		t.Fatalf("invalid-prefix search = %#v, %v", result, err)
	}
	match := result.Matches[0]
	containsMatch := strings.Contains(match.Text, "needle")
	if !containsMatch || !match.Lossy {
		t.Fatalf(
			"invalid-prefix excerpt = contains_match:%t lossy:%t, want both true",
			containsMatch,
			match.Lossy,
		)
	}
}

func TestSearchLogLargeLogHasFixedAllocationAndTextCeilings(t *testing.T) {
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(Meta{TaskID: "test:large-search"})
	if err != nil {
		t.Fatal(err)
	}
	line := bytes.Repeat([]byte{'a'}, 128<<10)
	copy(line[2048:], "needle")
	line = append(line, '\n')
	for range 64 {
		if _, err = handle.Stdout().Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err = handle.Finish(StatusOK, 0, "", false, false); err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	result, err := store.SearchLog(
		context.Background(),
		handle.Meta.RunID,
		"stdout",
		0,
		regexp.MustCompile("needle"),
		LogSearchOptions{MaxMatches: 200, ContextLines: 0, TextBudget: 64 << 10},
	)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || len(result.Matches) != 64 {
		t.Fatalf("large search = complete:%t matches:%d", result.Complete, len(result.Matches))
	}
	if result.TextBytes > 64<<10 {
		t.Fatalf("reported text = %d bytes, want at most %d", result.TextBytes, 64<<10)
	}
	const allocationCeiling = 4 << 20
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated >= allocationCeiling {
		t.Fatalf(
			"search allocated %d bytes for an 8 MiB log, want less than %d",
			allocated,
			allocationCeiling,
		)
	}
}

func TestSearchLogStopsAtScanCapInsideLine(t *testing.T) {
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(Meta{TaskID: "test:scan-cap"})
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte{'a'}, 64<<10)
	const tailBytes int64 = 1 << 20
	for written := int64(0); written < logSearchScanBytes+tailBytes; written += int64(len(chunk)) {
		if count, writeErr := handle.Stdout().Write(chunk); writeErr != nil || count != len(chunk) {
			t.Fatalf("write no-newline fixture = %d, %v", count, writeErr)
		}
	}
	if err = handle.Finish(StatusOK, 0, "", false, false); err != nil {
		t.Fatal(err)
	}

	result, err := store.SearchLog(
		context.Background(),
		handle.Meta.RunID,
		"stdout",
		0,
		regexp.MustCompile("needle"),
		LogSearchOptions{MaxMatches: 20, ContextLines: 0, TextBudget: 64 << 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.ClippedLines || result.NextOffset != logSearchScanBytes {
		t.Fatalf(
			"scan cap result = complete:%t clipped_lines:%t next_offset:%d, want false true %d",
			result.Complete,
			result.ClippedLines,
			result.NextOffset,
			logSearchScanBytes,
		)
	}

	resumed, err := store.SearchLog(
		context.Background(),
		handle.Meta.RunID,
		"stdout",
		result.NextOffset,
		regexp.MustCompile("needle"),
		LogSearchOptions{MaxMatches: 20, ContextLines: 0, TextBudget: 64 << 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.NextOffset <= result.NextOffset {
		t.Fatalf(
			"resumed next_offset = %d, want greater than %d",
			resumed.NextOffset,
			result.NextOffset,
		)
	}
}

func newLogSearchFixture(t *testing.T, stdout, stderr []byte) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := NewForWorktree(root, root)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(Meta{TaskID: "test:search"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handle.Stdout().Write(stdout); err != nil {
		t.Fatal(err)
	}
	if _, err = handle.Stderr().Write(stderr); err != nil {
		t.Fatal(err)
	}
	if err = handle.Finish(StatusOK, 0, "", false, false); err != nil {
		t.Fatal(err)
	}
	return store, handle.Meta.RunID
}
