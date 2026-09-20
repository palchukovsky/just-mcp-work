// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

func TestSearchRunLogsSearchesEachStreamAndBothInDiagnosticOrder(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	runID := writeSearchRun(t, server, []byte("stdout target\n"), []byte("stderr target\n"))
	withMCPClientSession(t, server, func(session *mcp.ClientSession) {
		for _, stream := range []string{"stdout", "stderr"} {
			output := callSearchRunLogs(t, session, map[string]any{
				"run_id": runID,
				"query":  "target",
				"stream": stream,
			})
			streams := searchRunLogStreams(t, output)
			if len(streams) != 1 || streams[0]["stream"] != stream {
				t.Fatalf("%s-only streams = %#v", stream, streams)
			}
			if matches := searchRunLogMatches(t, streams[0]); len(matches) != 1 {
				t.Fatalf("%s-only matches = %#v", stream, matches)
			}
		}

		output := callSearchRunLogs(t, session, map[string]any{
			"run_id": runID,
			"query":  "target",
		})
		streams := searchRunLogStreams(t, output)
		if len(streams) != 2 ||
			streams[0]["stream"] != "stderr" ||
			streams[1]["stream"] != "stdout" {
			t.Fatalf("both-stream order = %#v, want stderr then stdout", streams)
		}
	})
}

func TestSearchRunLogsMaxMatchesIsSharedAcrossStreams(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	runID := writeSearchRun(
		t,
		server,
		[]byte("hit stdout one\nhit stdout two\n"),
		[]byte("hit stderr one\nhit stderr two\n"),
	)
	withMCPClientSession(t, server, func(session *mcp.ClientSession) {
		output := callSearchRunLogs(t, session, map[string]any{
			"run_id":      runID,
			"query":       "hit",
			"max_matches": 3,
		})
		streams := searchRunLogStreams(t, output)
		total := 0
		for _, stream := range streams {
			total += len(searchRunLogMatches(t, stream))
		}
		if total != 3 {
			t.Fatalf("total matches across both streams = %d, want 3", total)
		}
		if len(searchRunLogMatches(t, streams[0])) != 2 ||
			len(searchRunLogMatches(t, streams[1])) != 1 {
			t.Fatalf("shared-budget streams = %#v", streams)
		}

		limited := callSearchRunLogs(t, session, map[string]any{
			"run_id":      runID,
			"query":       "hit",
			"max_matches": 1,
		})
		limitedStreams := searchRunLogStreams(t, limited)
		if len(searchRunLogMatches(t, limitedStreams[0])) != 1 ||
			len(searchRunLogMatches(t, limitedStreams[1])) != 0 ||
			limitedStreams[1]["complete"] != false ||
			searchRunLogInt64(t, limitedStreams[1], "next_offset") != 0 {
			t.Fatalf("unreached stdout stream = %#v", limitedStreams[1])
		}
		if _, present := limitedStreams[1]["more_matches"]; present {
			t.Fatalf("unreached stdout more_matches = %#v, want absent", limitedStreams[1])
		}
	})
}

func TestSearchRunLogsSharesTheResponseTextBudgetAcrossStreams(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	line := bytes.Repeat([]byte{'a'}, 2048)
	copy(line[1024:], "needle")
	line = append(line, '\n')
	log := bytes.Repeat(line, 40)
	runID := writeSearchRun(t, server, log, log)
	withMCPClientSession(t, server, func(session *mcp.ClientSession) {
		output := callSearchRunLogs(t, session, map[string]any{
			"run_id":      runID,
			"query":       "needle",
			"max_matches": 200,
		})
		streams := searchRunLogStreams(t, output)
		textBytes := 0
		for _, stream := range streams {
			for _, match := range searchRunLogMatches(t, stream) {
				text, ok := match["text"].(string)
				if !ok {
					t.Fatalf("match text = %#v", match["text"])
				}
				textBytes += len(text)
			}
		}
		if textBytes > searchRunLogsTextBudget {
			t.Fatalf(
				"response text = %d bytes, want at most %d",
				textBytes,
				searchRunLogsTextBudget,
			)
		}
		if textBytes != searchRunLogsTextBudget {
			t.Fatalf("response text = %d bytes, want the exercised budget", textBytes)
		}
	})
}

func TestSearchRunLogsOffsetRoundTripsThroughGetRunLogs(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	const matchingLine = "needle line\n"
	runID := writeSearchRun(t, server, []byte("alpha\n"+matchingLine+"omega\n"), nil)
	withMCPClientSession(t, server, func(session *mcp.ClientSession) {
		output := callSearchRunLogs(t, session, map[string]any{
			"run_id": runID,
			"query":  "needle",
			"stream": "stdout",
		})
		matches := searchRunLogMatches(t, searchRunLogStreams(t, output)[0])
		if len(matches) != 1 {
			t.Fatalf("search matches = %#v, want one", matches)
		}
		offset := searchRunLogInt64(t, matches[0], "offset")
		logs, err := session.CallTool(
			context.Background(),
			&mcp.CallToolParams{
				Name: "get_run_logs",
				Arguments: map[string]any{
					"run_id": runID,
					"stream": "stdout",
					"offset": offset,
					"limit":  int64(len(matchingLine)),
				},
			},
		)
		if err != nil || logs.IsError {
			t.Fatalf("get_run_logs round trip = %#v, %v", logs, err)
		}
		logsOutput, ok := logs.StructuredContent.(map[string]any)
		if !ok {
			t.Fatalf("get_run_logs structured content = %T", logs.StructuredContent)
		}
		data, ok := logsOutput["data"].(string)
		if !ok {
			t.Fatalf("get_run_logs data = %#v", logsOutput["data"])
		}
		if !strings.HasPrefix(data, matchingLine) {
			t.Fatalf(
				"offset round-trip data = %q, want prefix %q for offset %d",
				data,
				matchingLine,
				offset,
			)
		}
	})
}

func TestSearchRunLogsCursorReturnsFirstUnreturnedMatch(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	content := []byte("first hit\ncontext\nsecond hit\n")
	runID := writeSearchRun(t, server, content, nil)
	withMCPClientSession(t, server, func(session *mcp.ClientSession) {
		first := callSearchRunLogs(t, session, map[string]any{
			"run_id":        runID,
			"query":         "hit",
			"stream":        "stdout",
			"max_matches":   1,
			"context_lines": 5,
		})
		stream := searchRunLogStreams(t, first)[0]
		more, ok := stream["more_matches"].(bool)
		if !ok || !more {
			t.Fatalf("first search more_matches = %#v, want true", stream["more_matches"])
		}
		nextOffset := searchRunLogInt64(t, stream, "next_offset")
		wantOffset := int64(bytes.Index(content, []byte("second hit")))
		if nextOffset != wantOffset {
			t.Fatalf("next_offset = %d, want first unreturned match at %d", nextOffset, wantOffset)
		}

		second := callSearchRunLogs(t, session, map[string]any{
			"run_id": runID,
			"query":  "hit",
			"stream": "stdout",
			"offset": nextOffset,
		})
		matches := searchRunLogMatches(t, searchRunLogStreams(t, second)[0])
		if len(matches) == 0 || searchRunLogInt64(t, matches[0], "offset") != nextOffset {
			t.Fatalf(
				"resumed first match = %#v, want the unreturned match at %d",
				matches,
				nextOffset,
			)
		}
	})
}

func TestSearchRunLogsContextAndLossyOutputThroughTool(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	stdout := append([]byte("before\n"), []byte{'n', 'e', 'e', 'd', 'l', 'e', 0xff, '\n'}...)
	stdout = append(stdout, []byte("after\n")...)
	runID := writeSearchRun(t, server, stdout, nil)
	withMCPClientSession(t, server, func(session *mcp.ClientSession) {
		output := callSearchRunLogs(t, session, map[string]any{
			"run_id":        runID,
			"query":         "needle",
			"stream":        "stdout",
			"context_lines": 2,
		})
		matches := searchRunLogMatches(t, searchRunLogStreams(t, output)[0])
		if len(matches) != 1 || matches[0]["lossy"] != true {
			t.Fatalf("lossy matches = %#v", matches)
		}
		before, ok := matches[0]["before"].([]any)
		if !ok || len(before) != 1 || before[0] != "before" {
			t.Fatalf("before context = %#v", matches[0]["before"])
		}
		after, ok := matches[0]["after"].([]any)
		if !ok || len(after) != 1 || after[0] != "after" {
			t.Fatalf("after context = %#v", matches[0]["after"])
		}
		encoded, err := json.Marshal(output)
		if err != nil || !utf8.Valid(encoded) || !bytes.Contains(encoded, []byte("�")) {
			t.Fatalf("lossy structured output = %s, %v", encoded, err)
		}
	})
}

func TestSearchRunLogsRejectsInvalidInputsInBand(t *testing.T) {
	server := newShellTestServer(t, t.TempDir())
	tests := []struct {
		name      string
		arguments map[string]any
		message   string
	}{
		{
			name:      "neither selector",
			arguments: map[string]any{"run_id": "missing"},
			message:   "exactly one of query or regex",
		},
		{
			name: "both selectors",
			arguments: map[string]any{
				"run_id": "missing",
				"query":  "x",
				"regex":  "x",
			},
			message: "query and regex are mutually exclusive",
		},
		{
			name:      "empty query",
			arguments: map[string]any{"run_id": "missing", "query": ""},
			message:   "query must not be empty",
		},
		{
			name:      "bad regex",
			arguments: map[string]any{"run_id": "missing", "regex": "["},
			message:   "regex is invalid",
		},
		{
			name:      "empty-matching regex",
			arguments: map[string]any{"run_id": "missing", "regex": "a*"},
			message:   "regex must not match the empty string",
		},
		{
			name: "bad stream",
			arguments: map[string]any{
				"run_id": "missing",
				"query":  "x",
				"stream": "combined",
			},
			message: "stream must be stdout or stderr",
		},
		{
			name: "negative offset",
			arguments: map[string]any{
				"run_id": "missing",
				"query":  "x",
				"stream": "stdout",
				"offset": -1,
			},
			message: "offset must not be negative",
		},
		{
			name: "offset without stream",
			arguments: map[string]any{
				"run_id": "missing",
				"query":  "x",
				"offset": 1,
			},
			message: "offset requires an explicit stream",
		},
		{
			name: "max matches zero",
			arguments: map[string]any{
				"run_id":      "missing",
				"query":       "x",
				"max_matches": 0,
			},
			message: "max_matches must be between 1 and 200",
		},
		{
			name: "max matches below range",
			arguments: map[string]any{
				"run_id":      "missing",
				"query":       "x",
				"max_matches": -1,
			},
			message: "max_matches must be between 1 and 200",
		},
		{
			name: "max matches above range",
			arguments: map[string]any{
				"run_id":      "missing",
				"query":       "x",
				"max_matches": 201,
			},
			message: "max_matches must be between 1 and 200",
		},
		{
			name: "context below range",
			arguments: map[string]any{
				"run_id":        "missing",
				"query":         "x",
				"context_lines": -1,
			},
			message: "context_lines must be between 0 and 5",
		},
		{
			name: "context above range",
			arguments: map[string]any{
				"run_id":        "missing",
				"query":         "x",
				"context_lines": 6,
			},
			message: "context_lines must be between 0 and 5",
		},
	}
	withMCPClientSession(t, server, func(session *mcp.ClientSession) {
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				result, err := session.CallTool(
					context.Background(),
					&mcp.CallToolParams{Name: "search_run_logs", Arguments: test.arguments},
				)
				if err != nil || result == nil || !result.IsError {
					t.Fatalf("rejection = %#v, %v", result, err)
				}
				output, ok := result.StructuredContent.(map[string]any)
				if !ok {
					t.Fatalf("rejection structured content = %T", result.StructuredContent)
				}
				errorObject, ok := output["error"].(map[string]any)
				if !ok {
					t.Fatalf("rejection error = %#v", output["error"])
				}
				message, ok := errorObject["message"].(string)
				if !ok || !strings.Contains(message, test.message) {
					t.Fatalf("rejection output = %#v, want %q", output, test.message)
				}
			})
		}
	})

	invalidQuery := string([]byte{0xff})
	result, output, err := server.searchRunLogs(
		context.Background(),
		nil,
		searchRunLogsInput{RunID: "missing", Query: &invalidQuery},
	)
	if err != nil || result == nil || !result.IsError || output.Error == nil ||
		!strings.Contains(output.Error.Message, "query must be valid UTF-8") {
		t.Fatalf("invalid UTF-8 query rejection = %#v, %#v, %v", result, output, err)
	}
}

func TestSearchRunLogsDescriptionDefinesCallSemantics(t *testing.T) {
	description := searchRunLogsDescription()
	for _, expected := range []string{
		"Search persisted run logs",
		"literal query or RE2 regex",
		"Case-sensitive",
		"(?i)",
		"stderr before stdout",
		"max_matches shared",
		"line's first byte",
		"get_run_logs",
		"next_offset resumes",
		"unreturned match",
	} {
		if !strings.Contains(description, expected) {
			t.Errorf("search_run_logs description does not define %q", expected)
		}
	}
}

func writeSearchRun(t *testing.T, server *Server, stdout, stderr []byte) string {
	t.Helper()
	handle, err := server.store.Begin(runstore.Meta{TaskID: "test:search-logs"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handle.Stdout().Write(stdout); err != nil {
		t.Fatal(err)
	}
	if _, err = handle.Stderr().Write(stderr); err != nil {
		t.Fatal(err)
	}
	if err = handle.Finish(runstore.StatusOK, 0, "", false, false); err != nil {
		t.Fatal(err)
	}
	return handle.Meta.RunID
}

func callSearchRunLogs(
	t *testing.T,
	session *mcp.ClientSession,
	arguments map[string]any,
) map[string]any {
	t.Helper()
	result, err := session.CallTool(
		context.Background(),
		&mcp.CallToolParams{Name: "search_run_logs", Arguments: arguments},
	)
	if err != nil || result == nil || result.IsError {
		t.Fatalf("search_run_logs = %#v, %v", result, err)
	}
	output, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("search_run_logs structured content = %T", result.StructuredContent)
	}
	return output
}

func searchRunLogStreams(t *testing.T, output map[string]any) []map[string]any {
	t.Helper()
	values, ok := output["streams"].([]any)
	if !ok {
		t.Fatalf("search streams = %#v", output["streams"])
	}
	streams := make([]map[string]any, len(values))
	for index, value := range values {
		stream, isMap := value.(map[string]any)
		if !isMap {
			t.Fatalf("search stream %d = %T", index, value)
		}
		streams[index] = stream
	}
	return streams
}

func searchRunLogMatches(t *testing.T, stream map[string]any) []map[string]any {
	t.Helper()
	values, ok := stream["matches"].([]any)
	if !ok {
		t.Fatalf("search matches = %#v", stream["matches"])
	}
	matches := make([]map[string]any, len(values))
	for index, value := range values {
		match, isMap := value.(map[string]any)
		if !isMap {
			t.Fatalf("search match %d = %T", index, value)
		}
		matches[index] = match
	}
	return matches
}

func searchRunLogInt64(t *testing.T, object map[string]any, field string) int64 {
	t.Helper()
	value, ok := object[field].(float64)
	if !ok {
		t.Fatalf("search field %s = %#v", field, object[field])
	}
	return int64(value)
}
