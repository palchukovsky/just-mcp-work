// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package form

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/palchukovsky/just-mcp-work/internal/questionnaire"
)

func testQuestions() []questionnaire.Question {
	return []questionnaire.Question{
		{
			ID:      "target",
			Subject: "Target",
			Title:   "Where should it go?",
			Choices: []questionnaire.Choice{
				{Value: "project", Description: "the project"},
				{Value: "workspace", Description: "the workspace"},
			},
			Defaults: []string{"workspace"},
			Offer:    []string{"workspace"},
			Flag:     "--target project|workspace",
		},
		{
			ID:       "families",
			Subject:  "Families",
			Title:    "Which families?",
			Multiple: true,
			Choices: []questionnaire.Choice{
				{Value: "codex", Description: "declare codex"},
				{Value: "claude", Description: "declare claude"},
			},
			Defaults: []string{"codex", "claude"},
			Offer:    []string{"codex", "claude"},
		},
		{
			ID:      "runner:go",
			Section: "Runners",
			Subject: "go",
			Title:   "go runner: choose access.",
			Choices: []questionnaire.Choice{
				{Value: "safe", Label: "Reduced access", Description: "fixed tasks"},
				{Value: "all", Label: "All commands", Description: "any argv", Warning: "risky"},
				{Value: "disabled", Label: "Disabled", Description: "hidden"},
			},
			Defaults: []string{"safe"},
			Offer:    []string{"safe"},
		},
		{
			ID:      "runner:just",
			Section: "Runners",
			Subject: "just",
			Title:   "just runner: choose access.",
			Choices: []questionnaire.Choice{
				{Value: "all", Label: "Current access", Description: "every recipe"},
				{Value: "disabled", Label: "Disabled", Description: "hidden"},
			},
			Defaults: []string{"all"},
			Offer:    []string{"all"},
		},
		{
			ID:       "apply-permissions",
			Kind:     questionnaire.Confirm,
			Subject:  "Permissions",
			Title:    "Apply the permissions?",
			Defaults: []string{questionnaire.No},
			Offer:    []string{questionnaire.No},
		},
	}
}

func press(t *testing.T, current model, keys ...tea.KeyPressMsg) (model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, key := range keys {
		var next tea.Model
		next, cmd = current.Update(key)
		updated, ok := next.(model)
		if !ok {
			t.Fatalf("Update returned %T, want model", next)
		}
		current = updated
	}
	return current, cmd
}

func key(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}

func text(value string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: []rune(value)[0], Text: value}
}

func assertQuits(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("command = nil, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("command does not quit the form")
	}
}

func TestFormAppliesTheOffersUntouched(t *testing.T) {
	current := newModel("init", testQuestions())
	for range len(current.items) - 1 {
		current, _ = press(t, current, key(tea.KeyEnter))
	}
	current, cmd := press(t, current, key(tea.KeyEnter))
	if !current.submitted {
		t.Fatal("enter on the apply row did not apply the form")
	}
	assertQuits(t, cmd)
	want := questionnaire.Answers{
		"target":            {"workspace"},
		"families":          {"codex", "claude"},
		"runner:go":         {"safe"},
		"runner:just":       {"all"},
		"apply-permissions": {questionnaire.No},
	}
	got := current.answers()
	for id, values := range want {
		if !slices.Equal(got[id], values) {
			t.Fatalf("answer %s = %v, want %v", id, got[id], values)
		}
	}
}

func TestFormArrowsAndSpaceChangeAnswers(t *testing.T) {
	current := newModel("init", testQuestions())
	current, _ = press(
		t,
		current,
		key(tea.KeyLeft),                   // target: workspace -> project
		key(tea.KeyDown),                   // families: codex
		key(tea.KeySpace),                  // uncheck codex
		key(tea.KeyDown), key(tea.KeyDown), // runner:go
		key(tea.KeyRight), key(tea.KeyRight), // safe -> all -> disabled
		key(tea.KeyRight),                   // stays at disabled
		key(tea.KeyDown), key(tea.KeySpace), // runner:just cycles all -> disabled
		key(tea.KeyDown), key(tea.KeyLeft), // permissions: no -> yes
	)
	got := current.answers()
	for id, want := range map[string][]string{
		"target":            {"project"},
		"families":          {"claude"},
		"runner:go":         {"disabled"},
		"runner:just":       {"disabled"},
		"apply-permissions": {questionnaire.Yes},
	} {
		if !slices.Equal(got[id], want) {
			t.Fatalf("answer %s = %v, want %v", id, got[id], want)
		}
	}
}

func TestFormRefusesToApplyAnEmptyMultipleAnswer(t *testing.T) {
	current := newModel("init", testQuestions())
	current, _ = press(t, current, key(tea.KeyDown), key(tea.KeySpace))
	current, _ = press(t, current, key(tea.KeyDown), key(tea.KeySpace))
	current.cursor = len(current.items) - 1
	current, cmd := press(t, current, key(tea.KeyEnter))
	if current.submitted || cmd != nil {
		t.Fatal("the form applied a question that takes several choices with none chosen")
	}
	if focused := current.items[current.cursor]; focused.question != 1 {
		t.Fatalf("cursor rests on question %d, want the families question", focused.question)
	}
	if !strings.Contains(ansi.Strip(current.render()), "Choose at least one.") {
		t.Fatalf("the refusal is not shown:\n%s", ansi.Strip(current.render()))
	}
}

func TestFormRefusesAnAnswerValidateRejects(t *testing.T) {
	questions := testQuestions()
	questions[0].Validate = func(values []string) error {
		if values[0] == "project" {
			return errors.New("project is not allowed with these agents")
		}
		return nil
	}
	current := newModel("init", questions)
	current, _ = press(t, current, key(tea.KeyLeft))
	if !strings.Contains(ansi.Strip(current.render()), "project is not allowed with these agents") {
		t.Fatalf("the rejection is not shown:\n%s", ansi.Strip(current.render()))
	}
	current.cursor = len(current.items) - 1
	current, _ = press(t, current, key(tea.KeyEnter))
	if current.submitted || current.cursor != 0 {
		t.Fatalf("submitted = %t, cursor = %d; want a refusal at the target", current.submitted, current.cursor)
	}
}

func TestFormQuitsWithoutApplying(t *testing.T) {
	for _, quit := range []tea.KeyPressMsg{
		text("q"),
		key(tea.KeyEscape),
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		current, cmd := press(t, newModel("init", testQuestions()), quit)
		if current.submitted {
			t.Fatalf("%s applied the form", quit)
		}
		assertQuits(t, cmd)
	}
}

func TestFormDrawsEveryQuestionOnOneScreen(t *testing.T) {
	screen := ansi.Strip(newModel("just-mcp-work init", testQuestions()).render())
	for _, want := range []string{
		"just-mcp-work init",
		"Target",
		"‹ workspace ›",
		"Families",
		"[x] codex",
		"Runners",
		"safe",
		"all",
		"disabled",
		"Reduced access",
		"Current access",
		"Permissions",
		"‹ no ›",
		"[ Apply ]",
		"Where should it go?",
		"workspace (default) - the workspace",
		"flag: --target project|workspace",
	} {
		if !strings.Contains(screen, want) {
			t.Fatalf("screen lacks %q:\n%s", want, screen)
		}
	}
}

func TestFormShowsEveryChoiceOnRequest(t *testing.T) {
	current := newModel("init", testQuestions())
	current, _ = press(t, current, key(tea.KeyDown), key(tea.KeyDown), key(tea.KeyDown))
	screen := ansi.Strip(current.render())
	if strings.Contains(screen, "all - All commands") {
		t.Fatalf("details show other choices before they were asked for:\n%s", screen)
	}
	current, _ = press(t, current, text("?"))
	screen = ansi.Strip(current.render())
	for _, want := range []string{
		"safe (default) - Reduced access: fixed tasks",
		"all - All commands: any argv",
		"WARNING: risky",
		"disabled - Disabled: hidden",
	} {
		if !strings.Contains(screen, want) {
			t.Fatalf("details lack %q after ?:\n%s", want, screen)
		}
	}
}

func TestFormKeepsTheFocusedRowOnASmallScreen(t *testing.T) {
	current := newModel("init", testQuestions())
	next, _ := current.Update(tea.WindowSizeMsg{Width: 80, Height: 14})
	current, ok := next.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", next)
	}
	current.cursor = len(current.items) - 1
	lines := strings.Split(ansi.Strip(current.render()), "\n")
	if len(lines) > 14 {
		t.Fatalf("screen has %d lines on a 14-line terminal", len(lines))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "[ Apply ]") {
		t.Fatalf("the focused apply row scrolled out of view:\n%s", strings.Join(lines, "\n"))
	}
}

func TestFormKeepsTheFocusedRowWhenDetailsFillTheScreen(t *testing.T) {
	questions := testQuestions()
	for range 30 {
		questions[4].Context = append(questions[4].Context, "A long line of context.")
	}
	current := newModel("init", questions)
	current.width, current.height = 80, 12
	current.cursor = len(current.items) - 2
	lines := strings.Split(ansi.Strip(current.render()), "\n")
	if len(lines) > 12 {
		t.Fatalf("screen has %d lines on a 12-line terminal", len(lines))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "Permissions") {
		t.Fatalf("the focused row left the screen:\n%s", strings.Join(lines, "\n"))
	}
}

func TestDecideShowsTheFormOnlyInACapableTerminal(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		want  Support
		facts terminalFacts
	}{
		{
			name:  "terminal",
			facts: terminalFacts{inputTerminal: true, outputTerminal: true},
			want:  Support{Form: true},
		},
		{
			name:  "piped input",
			facts: terminalFacts{outputTerminal: true},
			want:  Support{},
		},
		{
			name:  "redirected output",
			facts: terminalFacts{inputTerminal: true},
			want:  Support{},
		},
		{
			name: "classic Windows console",
			facts: terminalFacts{
				inputTerminal:  true,
				outputTerminal: true,
				classicConsole: true,
			},
			want: Support{Advice: windowsTerminalAdvice},
		},
		{
			name:  "mintty pipe",
			facts: terminalFacts{ptyPipe: true},
			want:  Support{Advice: windowsTerminalAdvice},
		},
		{
			name: "unidentified Windows console",
			facts: terminalFacts{
				inputTerminal:       true,
				outputTerminal:      true,
				unidentifiedConsole: true,
			},
			want: Support{},
		},
		{
			name:  "dumb terminal",
			facts: terminalFacts{inputTerminal: true, outputTerminal: true, dumb: true},
			want:  Support{Advice: fullTerminalAdvice},
		},
		{
			name:  "dumb and piped",
			facts: terminalFacts{dumb: true},
			want:  Support{},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := decide(testCase.facts); got != testCase.want {
				t.Fatalf("decide(%+v) = %+v, want %+v", testCase.facts, got, testCase.want)
			}
		})
	}
}

func TestIsPTYPipeNameMatchesCygwinAndMSYSPipes(t *testing.T) {
	for name, want := range map[string]bool{
		`\msys-1888ae32e00d56aa-pty0-from-master`:                  true,
		`\cygwin-e022582115c10879-pty4-to-master`:                  true,
		`\Device\NamedPipe\msys-1888ae32e00d56aa-pty1-from-master`: true,
		`\msys-1888ae32e00d56aa-pty0-from-slave`:                   false,
		`\msys--pty0-from-master`:                                  false,
		`\pipe-1888ae32e00d56aa-pty0-from-master`:                  false,
		`\msys-1888ae32e00d56aa-tty0-from-master`:                  false,
		`\msys-1888ae32e00d56aa`:                                   false,
	} {
		if got := isPTYPipeName(name); got != want {
			t.Errorf("isPTYPipeName(%q) = %t, want %t", name, got, want)
		}
	}
}
