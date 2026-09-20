// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runstore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	logSearchScanBytes       int64 = 32 << 20
	logSearchLineBytes             = 1 << 20
	logSearchExcerptBytes          = 1024
	logSearchMatchLeadBytes        = 128
	logSearchReaderBytes           = 64 << 10
	maxLogSearchContextLines       = 5
	maxLogSearchMatches            = 200
)

// LogSearchMatch is one matching log line and its optional context.
//
//nolint:govet // Field order follows the stable search response shape.
type LogSearchMatch struct {
	Offset  int64
	Text    string
	Before  []string
	After   []string
	Clipped bool
	Lossy   bool
}

// LogSearchOptions bounds one persisted-log search.
type LogSearchOptions struct {
	MaxMatches   int
	ContextLines int
	TextBudget   int
}

// LogSearchResult describes one bounded pass over one persisted log stream.
type LogSearchResult struct {
	Matches      []LogSearchMatch
	NextOffset   int64
	TextBytes    int
	Complete     bool
	MoreMatches  bool
	ClippedLines bool
}

type logSearchContextLine struct {
	data    [logSearchExcerptBytes]byte
	length  int
	clipped bool
}

type logSearchContextRing struct {
	lines [maxLogSearchContextLines]logSearchContextLine
	start int
	count int
	limit int
}

type cappedLogLineReader struct {
	reader *bufio.Reader
	line   []byte
}

// SearchLog scans one immutable snapshot of a persisted log stream.
//
//nolint:gocyclo // The single forward pass keeps each line and cursor transition in one place.
func (s *Store) SearchLog(
	ctx context.Context,
	runID string,
	stream string,
	offset int64,
	pattern *regexp.Regexp,
	options LogSearchOptions,
) (LogSearchResult, error) {
	maxMatches := options.MaxMatches
	contextLines := options.ContextLines
	textBudget := options.TextBudget
	result := LogSearchResult{
		Matches:    []LogSearchMatch{},
		NextOffset: offset,
	}
	if offset < 0 {
		return result, fmt.Errorf("offset must not be negative")
	}
	if stream != "stdout" && stream != "stderr" {
		return result, fmt.Errorf("stream must be stdout or stderr")
	}
	if pattern == nil {
		return result, fmt.Errorf("search pattern is required")
	}
	if maxMatches <= 0 || maxMatches > maxLogSearchMatches {
		return result, fmt.Errorf("max matches must be between 1 and 200")
	}
	if contextLines < 0 || contextLines > maxLogSearchContextLines {
		return result, fmt.Errorf("context lines must be between 0 and 5")
	}
	if textBudget < 0 {
		return result, fmt.Errorf("text budget must not be negative")
	}
	result.Matches = make([]LogSearchMatch, 0, maxMatches)

	dir, _, err := s.existingRun(runID)
	if err != nil {
		return result, fmt.Errorf("resolve run directory: %w", err)
	}
	path := filepath.Join(dir, stream+".log")
	pathInfo, err := safeRegularFile(path)
	if err != nil {
		return result, err
	}
	// #nosec G304 -- path is constructed from a validated run ID and fixed stream name.
	file, err := os.Open(path)
	if err != nil {
		return result, fmt.Errorf("open log file: %w", err)
	}
	defer func() {
		//nolint:errcheck // A scan error takes precedence and Close cannot be returned here.
		_ = file.Close()
	}()
	snapshot, err := file.Stat()
	if err != nil {
		return result, fmt.Errorf("stat log file: %w", err)
	}
	if !snapshot.Mode().IsRegular() || !os.SameFile(pathInfo, snapshot) {
		return result, fmt.Errorf("refusing non-regular log file")
	}
	if offset >= snapshot.Size() {
		result.Complete = true
		return result, nil
	}

	scanBytes := min(snapshot.Size()-offset, logSearchScanBytes)
	section := io.NewSectionReader(file, offset, scanBytes)
	lines := cappedLogLineReader{
		reader: bufio.NewReaderSize(section, logSearchReaderBytes),
		line:   make([]byte, 0, logSearchLineBytes),
	}
	contextRing := logSearchContextRing{limit: contextLines}
	matchLineNumbers := make([]int64, 0, maxMatches)
	remainingText := textBudget
	lineOffset := offset
	scanEnd := offset + scanBytes
	var lineNumber int64

	for lineOffset < scanEnd {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("search log: %w", err)
		}
		line, consumed, lineClipped, err := lines.next(
			ctx,
			scanEnd-lineOffset,
			scanEnd < snapshot.Size(),
		)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, fmt.Errorf("search log: %w", ctxErr)
			}
			return result, fmt.Errorf("read log file: %w", err)
		}
		if consumed == 0 {
			break
		}
		if !lineClipped && len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if lineClipped {
			result.ClippedLines = true
		}

		matchIndex := pattern.FindIndex(line)
		priorMatches := len(result.Matches)
		if matchIndex != nil {
			match := newLogSearchMatch(line, lineOffset, matchIndex[0], lineClipped)
			if priorMatches >= maxMatches || len(match.Text) > remainingText {
				addLogSearchAfterContext(
					&result,
					matchLineNumbers,
					priorMatches,
					line,
					lineClipped,
					lineNumber,
					contextLines,
					&remainingText,
				)
				result.TextBytes = textBudget - remainingText
				result.NextOffset = lineOffset
				result.MoreMatches = true
				return result, nil
			}
			remainingText -= len(match.Text)
			addLogSearchBeforeContext(&match, &contextRing, &remainingText)
			result.Matches = append(result.Matches, match)
			matchLineNumbers = append(matchLineNumbers, lineNumber)
		}

		addLogSearchAfterContext(
			&result,
			matchLineNumbers,
			priorMatches,
			line,
			lineClipped,
			lineNumber,
			contextLines,
			&remainingText,
		)
		contextRing.push(line, lineClipped)
		lineOffset += consumed
		result.NextOffset = lineOffset
		lineNumber++
	}

	result.TextBytes = textBudget - remainingText
	result.NextOffset = lineOffset
	result.Complete = lineOffset >= snapshot.Size()
	return result, nil
}

func (r *cappedLogLineReader) next(
	ctx context.Context,
	maxBytes int64,
	clipAtLimit bool,
) ([]byte, int64, bool, error) {
	r.line = r.line[:0]
	var consumed int64
	var contentBytes int64
	started := false
	for consumed < maxBytes {
		if err := ctx.Err(); err != nil {
			return nil, consumed, false, fmt.Errorf("check search context: %w", err)
		}
		fragment, err := r.reader.ReadSlice('\n')
		if readRemaining := maxBytes - consumed; int64(len(fragment)) > readRemaining {
			fragment = fragment[:readRemaining]
		}
		if len(fragment) > 0 {
			started = true
			consumed += int64(len(fragment))
			lineComplete := fragment[len(fragment)-1] == '\n'
			if lineComplete {
				fragment = fragment[:len(fragment)-1]
			}
			contentBytes += int64(len(fragment))
			remaining := logSearchLineBytes - len(r.line)
			if remaining > 0 {
				r.line = append(r.line, fragment[:min(len(fragment), remaining)]...)
			}
			if lineComplete {
				return r.line, consumed, contentBytes > logSearchLineBytes, nil
			}
		}
		if consumed == maxBytes {
			return r.line,
				consumed,
				contentBytes > logSearchLineBytes || clipAtLimit,
				nil
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if !started {
				return nil, 0, false, nil
			}
			return r.line, consumed, contentBytes > logSearchLineBytes, nil
		default:
			return nil,
				consumed,
				contentBytes > logSearchLineBytes,
				fmt.Errorf("read line fragment: %w", err)
		}
	}
	return r.line, consumed, contentBytes > logSearchLineBytes, nil
}

func newLogSearchMatch(
	line []byte,
	offset int64,
	matchStart int,
	lineClipped bool,
) LogSearchMatch {
	text, clipped, lossy := logSearchMatchExcerpt(line, matchStart, lineClipped)
	return LogSearchMatch{
		Offset:  offset,
		Text:    text,
		Clipped: clipped,
		Lossy:   lossy,
	}
}

func addLogSearchBeforeContext(
	match *LogSearchMatch,
	ring *logSearchContextRing,
	remainingText *int,
) {
	if ring.limit == 0 {
		return
	}
	var selected [maxLogSearchContextLines]string
	selectedCount := 0
	for index := ring.count - 1; index >= 0; index-- {
		text, clipped, lossy := ring.excerpt(index)
		if len(text) > *remainingText {
			break
		}
		*remainingText -= len(text)
		selected[selectedCount] = text
		selectedCount++
		match.Clipped = match.Clipped || clipped
		match.Lossy = match.Lossy || lossy
	}
	match.Before = make([]string, 0, selectedCount)
	for index := selectedCount - 1; index >= 0; index-- {
		match.Before = append(match.Before, selected[index])
	}
}

func addLogSearchAfterContext(
	result *LogSearchResult,
	matchLineNumbers []int64,
	matchCount int,
	line []byte,
	lineClipped bool,
	lineNumber int64,
	contextLines int,
	remainingText *int,
) {
	if contextLines == 0 || matchCount == 0 {
		return
	}
	first := matchCount
	for first > 0 && lineNumber-matchLineNumbers[first-1] <= int64(contextLines) {
		first--
	}
	if first == matchCount {
		return
	}
	text, clipped, lossy := logSearchContextExcerpt(line, lineClipped)
	for index := first; index < matchCount; index++ {
		if len(text) > *remainingText {
			break
		}
		*remainingText -= len(text)
		match := &result.Matches[index]
		match.After = append(match.After, text)
		match.Clipped = match.Clipped || clipped
		match.Lossy = match.Lossy || lossy
	}
}

func (r *logSearchContextRing) push(line []byte, lineClipped bool) {
	if r.limit == 0 {
		return
	}
	index := (r.start + r.count) % r.limit
	if r.count == r.limit {
		index = r.start
		r.start = (r.start + 1) % r.limit
	} else {
		r.count++
	}
	end := logSearchWindowEnd(line, 0)
	slot := &r.lines[index]
	slot.length = copy(slot.data[:], line[:end])
	slot.clipped = lineClipped || end < len(line)
}

func (r *logSearchContextRing) excerpt(index int) (string, bool, bool) {
	slot := &r.lines[(r.start+index)%r.limit]
	text, sanitisedClipped, lossy := sanitiseLogSearchExcerpt(slot.data[:slot.length])
	return text, slot.clipped || sanitisedClipped, lossy
}

func logSearchMatchExcerpt(
	line []byte,
	matchStart int,
	lineClipped bool,
) (string, bool, bool) {
	start := 0
	if matchStart > logSearchMatchLeadBytes {
		start = matchStart - logSearchMatchLeadBytes
		for start < matchStart && !utf8.RuneStart(line[start]) {
			start++
		}
	}
	return logSearchExcerptFrom(line, start, lineClipped)
}

func logSearchContextExcerpt(line []byte, lineClipped bool) (string, bool, bool) {
	return logSearchExcerptFrom(line, 0, lineClipped)
}

func logSearchExcerptFrom(line []byte, start int, lineClipped bool) (string, bool, bool) {
	end := logSearchWindowEnd(line, start)
	text, sanitisedClipped, lossy := sanitiseLogSearchExcerpt(line[start:end])
	return text, lineClipped || start > 0 || end < len(line) || sanitisedClipped, lossy
}

func logSearchWindowEnd(line []byte, start int) int {
	hardEnd := min(start+logSearchExcerptBytes, len(line))
	if hardEnd == len(line) || utf8.RuneStart(line[hardEnd]) {
		return hardEnd
	}
	for backoff := 1; backoff <= utf8.UTFMax-1 && hardEnd-backoff >= start; backoff++ {
		candidate := hardEnd - backoff
		if utf8.RuneStart(line[candidate]) {
			return candidate
		}
	}
	return hardEnd
}

func sanitiseLogSearchExcerpt(data []byte) (string, bool, bool) {
	lossy := !utf8.Valid(data)
	text := string(data)
	if lossy {
		text = strings.ToValidUTF8(text, "\uFFFD")
	}
	if len(text) <= logSearchExcerptBytes {
		return text, false, lossy
	}
	end := logSearchExcerptBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end], true, lossy
}
