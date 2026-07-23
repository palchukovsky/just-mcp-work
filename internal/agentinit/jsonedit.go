// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// This file edits a JSON document as text instead of decoding and re-encoding
// it. Only the managed spans are rewritten, so key order, indentation, blank
// lines, and every entry this server does not own survive an init untouched.

// jsonSpan is a half-open byte range of a JSON document.
type jsonSpan struct {
	start int
	end   int
}

// jsonMember is one object member: span covers the whole `"key": value` text,
// value covers the member value alone.
type jsonMember struct {
	key   string
	span  jsonSpan
	value jsonSpan
}

// jsonDocumentSpan returns the span of the single top-level value, without the
// surrounding whitespace.
func jsonDocumentSpan(data []byte) (jsonSpan, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	start := jsonSkipSeparators(data, 0)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return jsonSpan{}, fmt.Errorf("read document value: %w", err)
	}
	end := jsonTrimRight(data, start, int(decoder.InputOffset()))
	return jsonSpan{start: start, end: end}, nil
}

// jsonObjectMembers lists the members of the object at span in document order.
func jsonObjectMembers(data []byte, span jsonSpan) ([]jsonMember, error) {
	fragment := data[span.start:span.end]
	decoder := json.NewDecoder(bytes.NewReader(fragment))
	if err := jsonExpectDelim(decoder, '{'); err != nil {
		return nil, err
	}
	var members []jsonMember
	// A duplicate key makes the document ambiguous: a JSON parser keeps the last
	// one while an edit here would reach the first. Refuse instead of guessing.
	seen := map[string]struct{}{}
	cursor := int(decoder.InputOffset())
	for decoder.More() {
		keyStart := jsonSkipSeparators(fragment, cursor)
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("read object key: %w", err)
		}
		key, isText := token.(string)
		if !isText {
			return nil, fmt.Errorf("object key is not a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("object key %q occurs more than once", key)
		}
		seen[key] = struct{}{}
		valueStart := jsonSkipSeparators(fragment, int(decoder.InputOffset()))
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, fmt.Errorf("read value of %q: %w", key, err)
		}
		valueEnd := jsonTrimRight(fragment, valueStart, int(decoder.InputOffset()))
		members = append(members, jsonMember{
			key:   key,
			span:  jsonSpan{start: span.start + keyStart, end: span.start + valueEnd},
			value: jsonSpan{start: span.start + valueStart, end: span.start + valueEnd},
		})
		cursor = valueEnd
	}
	return members, nil
}

// jsonArrayElements lists the element spans of the array at span.
func jsonArrayElements(data []byte, span jsonSpan) ([]jsonSpan, error) {
	fragment := data[span.start:span.end]
	decoder := json.NewDecoder(bytes.NewReader(fragment))
	if err := jsonExpectDelim(decoder, '['); err != nil {
		return nil, err
	}
	var elements []jsonSpan
	cursor := int(decoder.InputOffset())
	for decoder.More() {
		start := jsonSkipSeparators(fragment, cursor)
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, fmt.Errorf("read array element: %w", err)
		}
		end := jsonTrimRight(fragment, start, int(decoder.InputOffset()))
		elements = append(elements, jsonSpan{start: span.start + start, end: span.start + end})
		cursor = end
	}
	return elements, nil
}

func jsonExpectDelim(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read %q: %w", want, err)
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim || delim != want {
		return fmt.Errorf("value does not open with %q", want)
	}
	return nil
}

// jsonFindMember returns the member with the requested key.
func jsonFindMember(members []jsonMember, key string) (jsonMember, bool) {
	for _, member := range members {
		if member.key == key {
			return member, true
		}
	}
	return jsonMember{}, false
}

// jsonSetMember replaces the value of key in the object at span, or appends the
// member when the object does not have it yet. Every other byte is preserved.
func jsonSetMember(data []byte, span jsonSpan, key string, value any) ([]byte, error) {
	members, err := jsonObjectMembers(data, span)
	if err != nil {
		return nil, err
	}
	unit := jsonIndentUnit(data)
	lineBreak := documentLineBreak(data)
	if member, found := jsonFindMember(members, key); found {
		rendered, renderErr := jsonRender(
			value,
			jsonLineIndent(data, member.span.start),
			unit,
			lineBreak,
		)
		if renderErr != nil {
			return nil, renderErr
		}
		if bytes.Equal(data[member.value.start:member.value.end], rendered) {
			return data, nil
		}
		return jsonReplace(data, member.value, rendered), nil
	}
	if len(members) == 0 {
		rendered, renderErr := jsonRender(
			map[string]any{key: value},
			jsonLineIndent(data, span.start),
			unit,
			lineBreak,
		)
		if renderErr != nil {
			return nil, renderErr
		}
		return jsonReplace(data, span, rendered), nil
	}
	return jsonAppendMember(data, span, members, key, value, unit, lineBreak)
}

func jsonAppendMember(
	data []byte,
	span jsonSpan,
	members []jsonMember,
	key string,
	value any,
	unit string,
	lineBreak string,
) ([]byte, error) {
	last := members[len(members)-1]
	leads := jsonLeads(data, span, jsonMemberSpans(members))
	lead := jsonAfterComma(leads[len(leads)-1])
	indent := jsonLineIndent(data, last.span.start)
	if index := strings.LastIndexByte(lead, '\n'); index >= 0 {
		indent = lead[index+1:]
	} else {
		// A single-line object separates its members with a plain space, so the
		// appended member is not glued to the comma in front of it.
		lead = " "
	}
	rendered, err := jsonRender(value, indent, unit, lineBreak)
	if err != nil {
		return nil, err
	}
	encodedKey, err := json.Marshal(key)
	if err != nil {
		return nil, fmt.Errorf("encode object key: %w", err)
	}
	insertion := "," + lead + string(encodedKey) + ": " + string(rendered)
	return jsonReplace(data, jsonSpan{start: last.span.end, end: last.span.end}, []byte(insertion)), nil
}

// jsonRewriteStringList rebuilds the string array at span: elements that drop
// reports are removed and add is appended. A kept element keeps its own bytes
// and the whitespace in front of it, so foreign entries survive verbatim, and
// the appended values follow the layout the array already uses.
func jsonRewriteStringList(
	data []byte,
	span jsonSpan,
	drop func(string) bool,
	add []string,
) ([]byte, error) {
	elements, err := jsonArrayElements(data, span)
	if err != nil {
		return nil, err
	}
	kept := make([]int, 0, len(elements))
	for index, element := range elements {
		var text string
		if json.Unmarshal(data[element.start:element.end], &text) == nil && drop(text) {
			continue
		}
		kept = append(kept, index)
	}
	if len(kept) == len(elements) && len(add) == 0 {
		return data, nil
	}
	if len(kept) == 0 && len(add) == 0 {
		return jsonReplace(data, span, []byte("[]")), nil
	}
	rebuilt, err := jsonRebuildList(data, span, elements, kept, add)
	if err != nil {
		return nil, err
	}
	return jsonReplace(data, span, rebuilt), nil
}

func jsonRebuildList(
	data []byte,
	span jsonSpan,
	elements []jsonSpan,
	kept []int,
	add []string,
) ([]byte, error) {
	leads := jsonLeads(data, span, elements)
	style := jsonArrayStyle(data, span, elements, leads)
	var builder strings.Builder
	builder.WriteByte('[')
	for position, index := range kept {
		if position == 0 {
			builder.WriteString(jsonAfterComma(leads[index]))
		} else {
			builder.WriteString(leads[index])
		}
		builder.Write(data[elements[index].start:elements[index].end])
	}
	for position, value := range add {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode list value: %w", err)
		}
		if position == 0 && len(kept) == 0 {
			builder.WriteString(style.first)
		} else {
			builder.WriteString("," + style.separator)
		}
		builder.Write(encoded)
	}
	builder.WriteString(style.tail)
	builder.WriteByte(']')
	return []byte(builder.String()), nil
}

// arrayStyle is the layout of an array, so a value added to it is placed the
// way the operator places the values already there.
type arrayStyle struct {
	first     string // text in front of the first element
	separator string // text after the comma between two elements
	tail      string // text in front of the closing bracket
}

// jsonArrayStyle reads the layout off the array itself, and guesses it from the
// text between the brackets when the array has no element to read.
func jsonArrayStyle(
	data []byte,
	span jsonSpan,
	elements []jsonSpan,
	leads []string,
) arrayStyle {
	if len(elements) == 0 {
		return jsonEmptyArrayStyle(data, span)
	}
	style := arrayStyle{
		first: jsonAfterComma(leads[0]),
		tail:  string(data[elements[len(elements)-1].end : span.end-1]),
	}
	style.separator = style.first
	if len(leads) > 1 {
		style.separator = jsonAfterComma(leads[1])
	}
	if !strings.Contains(style.separator, "\n") {
		// A single-line array separates its values with a plain space, whatever
		// spacing the operator used, so an appended value is not glued to a comma.
		style.separator = " "
	}
	return style
}

func jsonEmptyArrayStyle(data []byte, span jsonSpan) arrayStyle {
	if !bytes.Contains(data[span.start:span.end], []byte("\n")) {
		return arrayStyle{first: "", separator: " ", tail: ""}
	}
	// An array with no element left no layout to copy, so the break, the
	// indentation, and the step are taken from the document around it.
	tail := documentLineBreak(data) + jsonLineIndent(data, span.start)
	lead := tail + jsonIndentUnit(data)
	return arrayStyle{first: lead, separator: lead, tail: tail}
}

// jsonLeads returns, for every item, the text between the end of the previous
// item and the item itself, including the separating comma.
func jsonLeads(data []byte, container jsonSpan, items []jsonSpan) []string {
	leads := make([]string, len(items))
	previous := container.start + 1
	for index, item := range items {
		leads[index] = string(data[previous:item.start])
		previous = item.end
	}
	return leads
}

func jsonMemberSpans(members []jsonMember) []jsonSpan {
	spans := make([]jsonSpan, 0, len(members))
	for _, member := range members {
		spans = append(spans, member.span)
	}
	return spans
}

// jsonAfterComma drops the text up to and including the last comma.
func jsonAfterComma(text string) string {
	if index := strings.LastIndexByte(text, ','); index >= 0 {
		return text[index+1:]
	}
	return text
}

// jsonRender encodes a value the way the document is written: the given
// indentation and step, and the line break the document already uses. The
// encoder always breaks lines with "\n", and a string value carries its own
// newlines escaped, so replacing the byte cannot reach inside a value.
func jsonRender(value any, indent string, unit string, lineBreak string) ([]byte, error) {
	data, err := json.MarshalIndent(value, indent, unit)
	if err != nil {
		return nil, fmt.Errorf("encode value: %w", err)
	}
	if lineBreak == "\n" {
		return data, nil
	}
	return []byte(withLineBreak(string(data), lineBreak)), nil
}

func jsonReplace(data []byte, span jsonSpan, text []byte) []byte {
	result := make([]byte, 0, len(data)-(span.end-span.start)+len(text))
	result = append(result, data[:span.start]...)
	result = append(result, text...)
	return append(result, data[span.end:]...)
}

// jsonIndentUnit reports the indentation step of the document, taken from its
// first indented line. It falls back to two spaces.
func jsonIndentUnit(data []byte) string {
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || len(trimmed) == len(line) {
			continue
		}
		return line[:len(line)-len(trimmed)]
	}
	return "  "
}

// jsonLineIndent reports the leading whitespace of the line holding offset.
func jsonLineIndent(data []byte, offset int) string {
	start := bytes.LastIndexByte(data[:offset], '\n') + 1
	line := data[start:offset]
	trimmed := bytes.TrimLeft(line, " \t")
	return string(line[:len(line)-len(trimmed)])
}

func jsonSkipSeparators(data []byte, offset int) int {
	for offset < len(data) {
		switch data[offset] {
		case ' ', '\t', '\r', '\n', ',', ':':
			offset++
		default:
			return offset
		}
	}
	return offset
}

func jsonTrimRight(data []byte, start int, end int) int {
	for end > start {
		switch data[end-1] {
		case ' ', '\t', '\r', '\n':
			end--
		default:
			return end
		}
	}
	return end
}
