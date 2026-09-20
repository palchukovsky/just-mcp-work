// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package mcpserver

import (
	"context"
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

const (
	searchRunLogsTextBudget = 64 << 10
	// maxSearchRunLogsMatches is this tool's own ceiling. It equals the listing
	// page cap today, but the two answer different questions and must not drift
	// together: the jsonschema hint and the agent guide both state 200 here.
	maxSearchRunLogsMatches = 200
)

func searchRunLogsDescription() string {
	return "Search persisted run logs. Exactly one: literal query or RE2 regex. Case-sensitive; use regex " +
		"(?i) to ignore case. No stream: stderr before stdout; max_matches shared. Match offset is " +
		"line's first byte; send to get_run_logs. next_offset resumes; more_matches points to an " +
		"unreturned match."
}

//nolint:govet // Field order follows the stable tool input shape.
type searchRunLogsInput struct {
	RunID        string  `json:"run_id"`
	Query        *string `json:"query,omitempty" jsonschema:"literal substring; exclusive with regex"`
	Regex        *string `json:"regex,omitempty" jsonschema:"RE2 pattern; exclusive with query"`
	Stream       string  `json:"stream,omitempty" jsonschema:"stdout or stderr; default both"`
	Offset       int64   `json:"offset,omitempty" jsonschema:"resume byte; requires stream"`
	MaxMatches   *int    `json:"max_matches,omitempty" jsonschema:"total; default 20; max 200"`
	ContextLines int     `json:"context_lines,omitempty" jsonschema:"before and after; default 0; max 5"`
}

//nolint:govet // Field order follows the stable search response shape.
type searchRunLogsMatch struct {
	Offset  int64    `json:"offset"`
	Text    string   `json:"text"`
	Clipped bool     `json:"clipped,omitempty"`
	Lossy   bool     `json:"lossy,omitempty"`
	Before  []string `json:"before,omitempty"`
	After   []string `json:"after,omitempty"`
}

type searchRunLogsStream struct {
	Stream       string               `json:"stream"`
	Matches      []searchRunLogsMatch `json:"matches"`
	NextOffset   int64                `json:"next_offset"`
	Complete     bool                 `json:"complete"`
	MoreMatches  bool                 `json:"more_matches,omitempty"`
	ClippedLines bool                 `json:"clipped_lines,omitempty"`
}

//nolint:govet // Field order follows the stable search response shape.
type searchRunLogsOutput struct {
	RunID   string                `json:"run_id,omitempty"`
	Streams []searchRunLogsStream `json:"streams,omitempty"`
	Error   *toolError            `json:"error,omitempty"`
}

type validatedSearchRunLogs struct {
	pattern      *regexp.Regexp
	streams      []string
	maxMatches   int
	contextLines int
}

//nolint:gocyclo // The documented validation order is intentionally linear and explicit.
func validateSearchRunLogs(input searchRunLogsInput) (validatedSearchRunLogs, error) {
	if input.Query == nil && input.Regex == nil {
		return validatedSearchRunLogs{}, fmt.Errorf("exactly one of query or regex is required")
	}
	if input.Query != nil && input.Regex != nil {
		return validatedSearchRunLogs{}, fmt.Errorf("query and regex are mutually exclusive")
	}

	var patternText string
	if input.Query != nil {
		if *input.Query == "" {
			return validatedSearchRunLogs{}, fmt.Errorf("query must not be empty")
		}
		if !utf8.ValidString(*input.Query) {
			return validatedSearchRunLogs{}, fmt.Errorf("query must be valid UTF-8")
		}
		patternText = regexp.QuoteMeta(*input.Query)
	} else {
		patternText = *input.Regex
	}
	pattern, err := regexp.Compile(patternText)
	if err != nil {
		return validatedSearchRunLogs{}, fmt.Errorf("regex is invalid: %w", err)
	}
	if pattern.MatchString("") {
		return validatedSearchRunLogs{}, fmt.Errorf("regex must not match the empty string")
	}

	if input.Stream != "" && input.Stream != "stdout" && input.Stream != "stderr" {
		return validatedSearchRunLogs{}, fmt.Errorf("stream must be stdout or stderr")
	}
	if input.Offset < 0 {
		return validatedSearchRunLogs{}, fmt.Errorf("offset must not be negative")
	}
	if input.Offset > 0 && input.Stream == "" {
		return validatedSearchRunLogs{}, fmt.Errorf("offset requires an explicit stream")
	}

	maxMatches := 20
	if input.MaxMatches != nil {
		maxMatches = *input.MaxMatches
		if maxMatches <= 0 || maxMatches > maxSearchRunLogsMatches {
			return validatedSearchRunLogs{}, fmt.Errorf(
				"max_matches must be between 1 and %d",
				maxSearchRunLogsMatches,
			)
		}
	}
	if input.ContextLines < 0 || input.ContextLines > 5 {
		return validatedSearchRunLogs{}, fmt.Errorf("context_lines must be between 0 and 5")
	}

	streams := []string{input.Stream}
	if input.Stream == "" {
		streams = []string{"stderr", "stdout"}
	}
	return validatedSearchRunLogs{
		pattern:      pattern,
		streams:      streams,
		maxMatches:   maxMatches,
		contextLines: input.ContextLines,
	}, nil
}

func (s *Server) searchRunLogs(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input searchRunLogsInput,
) (*mcp.CallToolResult, searchRunLogsOutput, error) {
	validated, err := validateSearchRunLogs(input)
	if err != nil {
		return rejectSearchRunLogs(err)
	}

	output := searchRunLogsOutput{
		RunID:   input.RunID,
		Streams: make([]searchRunLogsStream, 0, len(validated.streams)),
	}
	remainingMatches := validated.maxMatches
	remainingText := searchRunLogsTextBudget
	for _, stream := range validated.streams {
		startOffset := int64(0)
		if input.Stream != "" {
			startOffset = input.Offset
		}
		if remainingMatches == 0 || remainingText == 0 {
			output.Streams = append(output.Streams, searchRunLogsStream{
				Stream:     stream,
				Matches:    []searchRunLogsMatch{},
				NextOffset: startOffset,
			})
			continue
		}

		search, searchErr := s.store.SearchLog(
			ctx,
			input.RunID,
			stream,
			startOffset,
			validated.pattern,
			runstore.LogSearchOptions{
				MaxMatches:   remainingMatches,
				ContextLines: validated.contextLines,
				TextBudget:   remainingText,
			},
		)
		if searchErr != nil {
			return rejectSearchRunLogs(searchErr)
		}
		streamOutput := searchRunLogsStream{
			Stream:       stream,
			Matches:      make([]searchRunLogsMatch, len(search.Matches)),
			NextOffset:   search.NextOffset,
			Complete:     search.Complete,
			MoreMatches:  search.MoreMatches,
			ClippedLines: search.ClippedLines,
		}
		for index, match := range search.Matches {
			streamOutput.Matches[index] = searchRunLogsMatch{
				Offset:  match.Offset,
				Text:    match.Text,
				Clipped: match.Clipped,
				Lossy:   match.Lossy,
				Before:  match.Before,
				After:   match.After,
			}
		}
		remainingMatches -= len(search.Matches)
		remainingText -= search.TextBytes
		output.Streams = append(output.Streams, streamOutput)
	}
	return nil, output, nil
}

func rejectSearchRunLogs(
	err error,
) (*mcp.CallToolResult, searchRunLogsOutput, error) {
	return toolErrorResult(err), searchRunLogsOutput{Error: newToolError(err)}, nil
}
