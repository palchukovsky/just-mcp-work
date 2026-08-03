// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package agentinit

import (
	"bytes"
	"strings"
)

// This file holds the line-ending rules shared by every merge in this package.
// A file written with CRLF is edited with CRLF, so an init never mixes the two
// endings in one document.

// documentLineBreak reports the line break of a document, taken from its first
// line. It falls back to the bare newline, which is what a file created by init
// uses.
func documentLineBreak(data []byte) string {
	if index := bytes.IndexByte(data, '\n'); index > 0 && data[index-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// withLineBreak rewrites the newlines of generated text into the line break of
// the document it goes into. The generated text is built with "\n" alone, so
// the replacement cannot reach anything but a line break.
func withLineBreak(text string, lineBreak string) string {
	if lineBreak == "\n" {
		return text
	}
	return strings.ReplaceAll(text, "\n", lineBreak)
}

// normalizeTrailingLineBreak rewrites the final line break at a managed block
// boundary. Earlier init versions always emitted LF, so this also repairs the
// boundary when a managed block is refreshed in a CRLF document.
func normalizeTrailingLineBreak(text string, lineBreak string) string {
	switch {
	case strings.HasSuffix(text, "\r\n"):
		return text[:len(text)-len("\r\n")] + lineBreak
	case strings.HasSuffix(text, "\n"):
		return text[:len(text)-len("\n")] + lineBreak
	default:
		return text
	}
}

// trimLeadingLineBreak removes one line break in either supported form.
func trimLeadingLineBreak(text string) (string, bool) {
	switch {
	case strings.HasPrefix(text, "\r\n"):
		return text[len("\r\n"):], true
	case strings.HasPrefix(text, "\n"):
		return text[len("\n"):], true
	default:
		return text, false
	}
}

// trimTrailingLineBreak removes one line break in either supported form.
func trimTrailingLineBreak(text string) (string, bool) {
	switch {
	case strings.HasSuffix(text, "\r\n"):
		return text[:len(text)-len("\r\n")], true
	case strings.HasSuffix(text, "\n"):
		return text[:len(text)-len("\n")], true
	default:
		return text, false
	}
}

// trimManagedSeparator removes the extra line break that init writes between
// newline-terminated foreign content and an appended managed block. It leaves
// one line break behind because an earlier non-newline-terminated file is
// indistinguishable after the append and still needs a valid text boundary.
func trimManagedSeparator(text string) string {
	trimmed, found := trimTrailingLineBreak(text)
	if !found {
		return text
	}
	if _, hasPrevious := trimTrailingLineBreak(trimmed); !hasPrevious {
		return text
	}
	return trimmed
}
