// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"encoding/json"
	"strings"
	"testing"
)

// documentSpan is the span every test edits: the whole document.
func documentSpan(t *testing.T, text string) jsonSpan {
	t.Helper()
	span, err := jsonDocumentSpan([]byte(text))
	if err != nil {
		t.Fatalf("jsonDocumentSpan(%q) error = %v", text, err)
	}
	return span
}

// assertValidJSON keeps every rewrite honest: the byte-level editor must not
// produce text a JSON parser rejects.
func assertValidJSON(t *testing.T, data []byte) {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, data)
	}
}

// TestJSONSetMemberFollowsTheDocumentLayout checks that a written member takes
// the indentation, the line breaks, and the spacing the document already uses,
// and that everything around it survives byte for byte.
func TestJSONSetMemberFollowsTheDocumentLayout(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		before string
		key    string
		value  any
		want   string
	}{
		{
			name:   "appends to a single-line object",
			before: `{"a": 1}`,
			key:    "b",
			value:  "x",
			want:   `{"a": 1, "b": "x"}`,
		},
		{
			name:   "appends to a compact single-line object",
			before: `{"a":1}`,
			key:    "b",
			value:  "x",
			want:   `{"a":1, "b": "x"}`,
		},
		{
			name:   "appends with the document indentation",
			before: "{\n    \"a\": 1\n}\n",
			key:    "b",
			value:  "x",
			want:   "{\n    \"a\": 1,\n    \"b\": \"x\"\n}\n",
		},
		{
			name:   "appends with tab indentation",
			before: "{\n\t\"a\": 1\n}\n",
			key:    "b",
			value:  []string{"x"},
			want:   "{\n\t\"a\": 1,\n\t\"b\": [\n\t\t\"x\"\n\t]\n}\n",
		},
		{
			name:   "keeps CRLF line endings",
			before: "{\r\n  \"a\": 1\r\n}\r\n",
			key:    "b",
			value:  "x",
			want:   "{\r\n  \"a\": 1,\r\n  \"b\": \"x\"\r\n}\r\n",
		},
		{
			name:   "keeps CRLF line endings inside a rendered value",
			before: "{\r\n  \"a\": 1\r\n}\r\n",
			key:    "b",
			value:  []string{"x"},
			want:   "{\r\n  \"a\": 1,\r\n  \"b\": [\r\n    \"x\"\r\n  ]\r\n}\r\n",
		},
		{
			name:   "fills an empty CRLF object",
			before: "{}\r\n",
			key:    "b",
			value:  "x",
			want:   "{\r\n  \"b\": \"x\"\r\n}\r\n",
		},
		{
			name:   "fills an empty object",
			before: "{}\n",
			key:    "b",
			value:  "x",
			want:   "{\n  \"b\": \"x\"\n}\n",
		},
		{
			name:   "replaces the value of an existing member",
			before: "{\n  \"a\": 1,\n  \"b\": \"old\"\n}\n",
			key:    "b",
			value:  "x",
			want:   "{\n  \"a\": 1,\n  \"b\": \"x\"\n}\n",
		},
		{
			name:   "replaces a null value",
			before: "{\n  \"b\": null\n}\n",
			key:    "b",
			value:  "x",
			want:   "{\n  \"b\": \"x\"\n}\n",
		},
		{
			name:   "keeps a missing trailing newline missing",
			before: "{\n  \"a\": 1\n}",
			key:    "b",
			value:  "x",
			want:   "{\n  \"a\": 1,\n  \"b\": \"x\"\n}",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			data := []byte(testCase.before)
			got, err := jsonSetMember(data, documentSpan(t, testCase.before), testCase.key, testCase.value)
			if err != nil {
				t.Fatalf("jsonSetMember error = %v", err)
			}
			if string(got) != testCase.want {
				t.Fatalf("jsonSetMember =\n%q\nwant\n%q", got, testCase.want)
			}
			assertValidJSON(t, got)
		})
	}
}

// TestJSONSetMemberIsIdempotent pins the promise that a second init leaves the
// file alone: an unchanged value must return the original bytes.
func TestJSONSetMemberIsIdempotent(t *testing.T) {
	before := "{\n    \"a\": 1,\n    \"b\": {\n        \"c\": \"x\"\n    }\n}\n"
	got, err := jsonSetMember([]byte(before), documentSpan(t, before), "b", map[string]string{"c": "x"})
	if err != nil {
		t.Fatalf("jsonSetMember error = %v", err)
	}
	if string(got) != before {
		t.Fatalf("jsonSetMember changed an equal value:\n%q", got)
	}
}

// TestJSONRewriteStringListFollowsTheArrayLayout checks the list rewrite that
// drops the retired entries of this server and appends the current ones.
func TestJSONRewriteStringListFollowsTheArrayLayout(t *testing.T) {
	dropX := func(text string) bool { return strings.HasPrefix(text, "x") }
	for _, testCase := range []struct {
		name   string
		before string
		want   string
		add    []string
	}{
		{
			name:   "appends to a single-line array with a space",
			before: `["a", "b"]`,
			add:    []string{"c"},
			want:   `["a", "b", "c"]`,
		},
		{
			name:   "appends to a compact single-line array",
			before: `["a","b"]`,
			add:    []string{"c"},
			want:   `["a","b", "c"]`,
		},
		{
			name:   "appends to an empty single-line array",
			before: `[]`,
			add:    []string{"c", "d"},
			want:   `["c", "d"]`,
		},
		{
			name:   "appends to an empty multi-line array",
			before: "{\n  \"k\": [\n  ]\n}",
			add:    []string{"c"},
			want:   "{\n  \"k\": [\n    \"c\"\n  ]\n}",
		},
		{
			name:   "keeps the multi-line layout",
			before: "{\n  \"k\": [\n    \"a\",\n    \"x1\"\n  ]\n}",
			add:    []string{"c"},
			want:   "{\n  \"k\": [\n    \"a\",\n    \"c\"\n  ]\n}",
		},
		{
			name:   "drops the first element and keeps the rest verbatim",
			before: "{\n  \"k\": [\n    \"x1\",\n    \"a\",\n    \"b\"\n  ]\n}",
			add:    nil,
			want:   "{\n  \"k\": [\n    \"a\",\n    \"b\"\n  ]\n}",
		},
		{
			name:   "empties an array that keeps nothing",
			before: `["x1", "x2"]`,
			add:    nil,
			want:   `[]`,
		},
		{
			name:   "refills an array that keeps nothing",
			before: "{\n  \"k\": [\n    \"x1\"\n  ]\n}",
			add:    []string{"c"},
			want:   "{\n  \"k\": [\n    \"c\"\n  ]\n}",
		},
		{
			name:   "keeps elements that are not strings",
			before: `[1, "x1", {"a": 2}]`,
			add:    []string{"c"},
			want:   `[1, {"a": 2}, "c"]`,
		},
		{
			name:   "keeps CRLF line endings",
			before: "{\r\n  \"k\": [\r\n    \"a\"\r\n  ]\r\n}",
			add:    []string{"c"},
			want:   "{\r\n  \"k\": [\r\n    \"a\",\r\n    \"c\"\r\n  ]\r\n}",
		},
		{
			// An empty array has no layout to copy, so the break comes from the
			// document. Taking a bare newline here would mix the line endings.
			name:   "keeps CRLF line endings in an empty array",
			before: "{\r\n  \"k\": [\r\n  ]\r\n}",
			add:    []string{"c"},
			want:   "{\r\n  \"k\": [\r\n    \"c\"\r\n  ]\r\n}",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			data := []byte(testCase.before)
			span := listSpan(t, data)
			got, err := jsonRewriteStringList(data, span, dropX, testCase.add)
			if err != nil {
				t.Fatalf("jsonRewriteStringList error = %v", err)
			}
			if string(got) != testCase.want {
				t.Fatalf("jsonRewriteStringList =\n%q\nwant\n%q", got, testCase.want)
			}
			assertValidJSON(t, got)
		})
	}
}

// TestJSONRewriteStringListKeepsAnUntouchedArray checks the shortcut that makes
// a repeated init a no-op instead of a rewrite.
func TestJSONRewriteStringListKeepsAnUntouchedArray(t *testing.T) {
	before := []byte(`["a", "b"]`)
	got, err := jsonRewriteStringList(
		before,
		documentSpan(t, string(before)),
		func(string) bool { return false },
		nil,
	)
	if err != nil {
		t.Fatalf("jsonRewriteStringList error = %v", err)
	}
	if string(got) != string(before) {
		t.Fatalf("jsonRewriteStringList changed an untouched array:\n%q", got)
	}
}

// TestJSONObjectMembersRejectsDuplicateKeys covers the ambiguity a text edit
// cannot resolve: a parser keeps the last member, an edit reaches the first.
func TestJSONObjectMembersRejectsDuplicateKeys(t *testing.T) {
	before := `{"a": 1, "a": 2}`
	_, err := jsonObjectMembers([]byte(before), documentSpan(t, before))
	if err == nil || !strings.Contains(err.Error(), "occurs more than once") {
		t.Fatalf("jsonObjectMembers error = %v", err)
	}
}

// TestJSONDocumentSpanRejectsBrokenInput keeps the span readers from reporting
// a span for text they could not read.
func TestJSONDocumentSpanRejectsBrokenInput(t *testing.T) {
	for _, text := range []string{"", "{", "[1,"} {
		if _, err := jsonDocumentSpan([]byte(text)); err == nil {
			t.Errorf("jsonDocumentSpan(%q) error = nil", text)
		}
	}
}

// TestJSONArrayElementsRejectsNonArray keeps a list edit from starting on a
// value that is not a list.
func TestJSONArrayElementsRejectsNonArray(t *testing.T) {
	before := `{"a": 1}`
	if _, err := jsonArrayElements([]byte(before), documentSpan(t, before)); err == nil {
		t.Fatal("jsonArrayElements error = nil")
	}
}

// listSpan returns the span of the only array in the document, whether the
// document is that array or an object holding it under "k".
func listSpan(t *testing.T, data []byte) jsonSpan {
	t.Helper()
	span := documentSpan(t, string(data))
	if data[span.start] == '[' {
		return span
	}
	members, err := jsonObjectMembers(data, span)
	if err != nil {
		t.Fatalf("jsonObjectMembers error = %v", err)
	}
	member, found := jsonFindMember(members, "k")
	if !found {
		t.Fatal(`document has no "k" member`)
	}
	return member.value
}
