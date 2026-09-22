// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package form

import (
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
)

const (
	windowsTerminalAdvice = "Tip: Windows Terminal shows these questions as one " +
		"keyboard-driven form; run the command there to use it."
	fullTerminalAdvice = "Tip: a terminal with cursor control shows these questions as " +
		"one keyboard-driven form; this one reports TERM=dumb."
)

// Support says whether a terminal can show the form and, when it cannot,
// what to suggest instead.
type Support struct {
	// Advice names a terminal on this platform that shows the form; it is
	// empty when the form runs here or when there is nothing to suggest, such
	// as input piped from a script.
	Advice string
	// Form reports that input and output are a terminal the form can drive.
	Form bool
}

// Detect decides whether input and output can show the form. The form needs
// both to be a terminal with cursor control; on Windows it also needs a
// terminal behind a pseudo console, such as Windows Terminal or the VS Code
// terminal, rather than a classic console window. A Windows console whose
// window cannot be identified gets the plain questions without advice, since
// neither the form nor a better terminal can be vouched for there.
func Detect(input *os.File, output *os.File) Support {
	facts := terminalFacts{
		inputTerminal:  term.IsTerminal(input.Fd()),
		outputTerminal: term.IsTerminal(output.Fd()),
		dumb:           os.Getenv("TERM") == "dumb",
		ptyPipe:        isPTYPipe(input),
	}
	if facts.inputTerminal && facts.outputTerminal {
		classic, identified := consoleWindow()
		facts.classicConsole = classic
		facts.unidentifiedConsole = !identified
	}
	return decide(facts)
}

// terminalFacts is what Detect learns about the terminal before deciding.
type terminalFacts struct {
	inputTerminal  bool
	outputTerminal bool
	dumb           bool
	// classicConsole is a Windows console window of its own, as opposed to a
	// pseudo console behind a terminal application.
	classicConsole bool
	// unidentifiedConsole is a Windows console whose window could not be
	// identified as either.
	unidentifiedConsole bool
	// ptyPipe is the named pipe a Cygwin or MSYS2 terminal, such as the
	// mintty behind Git Bash, gives a program in place of a console.
	ptyPipe bool
}

func decide(facts terminalFacts) Support {
	if facts.ptyPipe {
		return Support{Advice: windowsTerminalAdvice}
	}
	if !facts.inputTerminal || !facts.outputTerminal || facts.unidentifiedConsole {
		return Support{}
	}
	if facts.classicConsole {
		return Support{Advice: windowsTerminalAdvice}
	}
	if facts.dumb {
		return Support{Advice: fullTerminalAdvice}
	}
	return Support{Form: true}
}

// isPTYPipeName reports whether a pipe name is one a Cygwin or MSYS2 pty
// creates: \msys-<hash>-pty<N>-from-master, \cygwin-<hash>-pty<N>-to-master,
// and the same under \Device\NamedPipe.
func isPTYPipeName(name string) bool {
	parts := strings.Split(name, "-")
	if len(parts) < 5 {
		return false
	}
	switch parts[0] {
	case `\msys`, `\cygwin`, `\Device\NamedPipe\msys`, `\Device\NamedPipe\cygwin`:
	default:
		return false
	}
	return parts[1] != "" &&
		strings.HasPrefix(parts[2], "pty") &&
		(parts[3] == "from" || parts[3] == "to") &&
		parts[4] == "master"
}
