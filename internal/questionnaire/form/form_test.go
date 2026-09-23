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
	for _, want := range []string{
		"● safe (default) - Reduced access",
		"     fixed tasks",
		"○ all - All commands",
		"○ disabled - Disabled",
	} {
		if !strings.Contains(screen, want) {
			t.Fatalf("details lack %q:\n%s", want, screen)
		}
	}
	for _, unasked := range []string{"any argv", "WARNING: risky", "hidden"} {
		if strings.Contains(screen, unasked) {
			t.Fatalf("details explain %q before it was asked for:\n%s", unasked, screen)
		}
	}
	current, _ = press(t, current, text("?"))
	screen = ansi.Strip(current.render())
	for _, want := range []string{
		"     fixed tasks",
		"     any argv",
		"     WARNING: risky",
		"     hidden",
	} {
		if !strings.Contains(screen, want) {
			t.Fatalf("details lack %q after ?:\n%s", want, screen)
		}
	}
}

func TestFormNamesChoicesByTheirLabels(t *testing.T) {
	questions := testQuestions()
	questions[1].Choices[0].Label = "Codex"
	questions[1].Choices[1].Label = "Claude Code"
	current := newModel("init", questions)
	current, _ = press(t, current, key(tea.KeyDown))
	screen := ansi.Strip(current.render())
	for _, want := range []string{
		"[x] Codex",
		"[x] Claude Code",
		"[x] codex (default) - Codex",
		"     declare codex",
	} {
		if !strings.Contains(screen, want) {
			t.Fatalf("screen lacks %q:\n%s", want, screen)
		}
	}
}

func TestFormDrawsAMixedSectionAsAHeadingOverRows(t *testing.T) {
	questions := []questionnaire.Question{
		{
			ID:      "shell",
			Section: "Permissions",
			Subject: "Shell commands",
			Title:   "Run shell commands without asking?",
			Choices: []questionnaire.Choice{
				{Value: "allow", Label: "Run without asking", Description: "the allow list"},
				{Value: "ask", Label: "Ask every time", Description: "the ask list"},
			},
			Defaults: []string{"ask"},
			Offer:    []string{"ask"},
		},
		{
			ID:      "claude",
			Kind:    questionnaire.Confirm,
			Section: "Permissions",
			Subject: "Claude Code",
			Title:   "Allow the tools?",
			Choices: []questionnaire.Choice{
				{Value: questionnaire.Yes, Label: "Allow the tools", Description: "write the entries"},
				{Value: questionnaire.No, Label: "Ask every time", Description: "remove the entries"},
			},
			Defaults: []string{questionnaire.No},
			Offer:    []string{questionnaire.No},
		},
	}
	current := newModel("init", questions)
	lines := strings.Split(ansi.Strip(current.render()), "\n")
	heading := slices.IndexFunc(lines, func(line string) bool {
		return strings.TrimSpace(line) == "Permissions"
	})
	if heading < 0 || heading+2 >= len(lines) {
		t.Fatalf("the section has no heading of its own:\n%s", strings.Join(lines, "\n"))
	}
	for offset, want := range [][]string{
		{"Shell commands", "‹ ask ›  Ask every time"},
		{"Claude Code", "‹ no ›  Ask every time"},
	} {
		row := lines[heading+1+offset]
		if !strings.Contains(row, want[0]) || !strings.Contains(row, want[1]) {
			t.Fatalf("row %d under the heading = %q, want %q", offset, row, want)
		}
	}
	current, _ = press(t, current, key(tea.KeyDown), key(tea.KeyLeft))
	if got := current.answers()["claude"]; !slices.Equal(got, []string{questionnaire.Yes}) {
		t.Fatalf("claude answer = %v, want yes", got)
	}
	screen := ansi.Strip(current.render())
	for _, want := range []string{"● yes - Allow the tools", "     write the entries", "○ no (default) - Ask every time"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("details lack %q:\n%s", want, screen)
		}
	}
}

func TestFormDrawsALoneSectionQuestionAsAHeadingOverItsRow(t *testing.T) {
	questions := testQuestions()
	questions = append(questions[:3], questions[4:]...) // runner:go stays alone
	lines := strings.Split(ansi.Strip(newModel("init", questions).render()), "\n")
	heading := slices.IndexFunc(lines, func(line string) bool {
		return strings.TrimSpace(line) == "Runners"
	})
	if heading < 0 || !strings.Contains(lines[heading+1], "go") ||
		!strings.Contains(lines[heading+1], "‹ safe ›  Reduced access") {
		t.Fatalf("a lone section question is not a heading over its row:\n%s", strings.Join(lines, "\n"))
	}
	for index := 1; index < len(lines); index++ {
		if lines[index] == "" && lines[index-1] == "" {
			t.Fatalf("the form has two blank lines in a row at %d:\n%s", index, strings.Join(lines, "\n"))
		}
	}
}

func TestFormExplainsTheFocusedChoiceEvenWhenItIsOff(t *testing.T) {
	current := newModel("init", testQuestions())
	current, _ = press(t, current, key(tea.KeyDown), key(tea.KeySpace))
	screen := ansi.Strip(current.render())
	if !strings.Contains(screen, "[ ] codex (default) - declare codex") {
		t.Fatalf("the unchecked choice is not explained:\n%s", screen)
	}
}

func TestFormKeepsHalfTheScreenForTheBodyWithTheBlockHeading(t *testing.T) {
	questions := make([]questionnaire.Question, 0, 6)
	for _, name := range []string{"r1", "r2", "r3", "r4", "r5", "r6"} {
		questions = append(questions, questionnaire.Question{
			ID:      "runner:" + name,
			Section: "Runners",
			Subject: name,
			Title:   "Which mode should " + name + " use?",
			Context: []string{"What " + name + " runs.", "What a runner is."},
			Choices: []questionnaire.Choice{
				{Value: "all", Label: "Current access", Description: "every command", Warning: "not a sandbox"},
				{Value: "disabled", Label: "Disabled", Description: "hidden"},
			},
			Defaults: []string{"all"},
			Offer:    []string{"all"},
			Flag:     "--runner-mode " + name + "=<mode>",
		})
	}
	current := newModel("init", questions)
	current.width, current.height = 80, 14
	current.cursor = 2
	lines := strings.Split(ansi.Strip(current.render()), "\n")
	if len(lines) > 14 {
		t.Fatalf("screen has %d lines on a 14-line terminal", len(lines))
	}
	screen := strings.Join(lines, "\n")
	for _, want := range []string{"Runners", "► r3", "What r3 runs.", "● all (default) - Current access", "WARNING: not a sandbox"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("screen lacks %q:\n%s", want, screen)
		}
	}
	for _, dropped := range []string{"What a runner is.", "○ disabled", "flag: --runner-mode"} {
		if strings.Contains(screen, dropped) {
			t.Fatalf("compact details still show %q:\n%s", dropped, screen)
		}
	}
	current.showAll = true
	if screen = ansi.Strip(current.render()); !strings.Contains(screen, "What a runner is.") {
		t.Fatalf("? did not bring every detail back:\n%s", screen)
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

func discoveryQuestions() []questionnaire.Question {
	return []questionnaire.Question{
		{
			ID:      "mode",
			Section: "Discovery",
			Subject: "Skipped",
			Title:   "What should discovery skip?",
			Badge:   "2 recommended found",
			Choices: []questionnaire.Choice{
				{Value: "none", Label: "Nothing else", Description: "skip nothing"},
				{Value: "custom", Label: "Your list", Description: "skip what you list"},
			},
			Defaults: []string{"none"},
			Offer:    []string{"none"},
		},
		{
			ID:      "dirs",
			Kind:    questionnaire.List,
			Section: "Discovery",
			Subject: "Your list",
			Title:   "Which directories?",
			Offer:   []string{"a", "b", "c", "d"},
			When: func(answers questionnaire.Answers) bool {
				return slices.Equal(answers["mode"], []string{"custom"})
			},
			Validate: func(values []string) error {
				if len(values) == 0 {
					return errors.New("list at least one directory")
				}
				return nil
			},
		},
	}
}

func ctrl(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl}
}

func TestFormShowsAQuestionOnlyWhileItApplies(t *testing.T) {
	current := newModel("init", discoveryQuestions())
	if len(current.items) != 2 {
		t.Fatalf("rows = %d, want the mode row and the apply row", len(current.items))
	}
	screen := ansi.Strip(current.render())
	if !strings.Contains(screen, "‹ none ›  Nothing else  2 recommended found") {
		t.Fatalf("the mode row does not show its badge:\n%s", screen)
	}
	if strings.Contains(screen, "‹ a, b") {
		t.Fatalf("a question that does not apply is shown:\n%s", screen)
	}
	current, _ = press(t, current, key(tea.KeyRight))
	if len(current.items) != 3 {
		t.Fatalf("rows = %d, want the list row to appear", len(current.items))
	}
	if screen = ansi.Strip(current.render()); !strings.Contains(
		screen,
		"Your list     ‹ a, b, c, +1 more ›",
	) {
		t.Fatalf("the list row does not show its values in short:\n%s", screen)
	}
	current, _ = press(t, current, key(tea.KeyLeft), key(tea.KeyEnter), key(tea.KeyEnter))
	if !current.submitted {
		t.Fatal("the form did not apply")
	}
	if answers := current.answers(); !slices.Equal(answers["mode"], []string{"none"}) ||
		len(answers) != 1 {
		t.Fatalf("answers = %v, want only the mode", answers)
	}
}

func TestFormEditsAListAnswer(t *testing.T) {
	current := newModel("init", discoveryQuestions())
	current, _ = press(t, current, key(tea.KeyRight), key(tea.KeyDown), key(tea.KeyEnter))
	if !current.editing || current.draft != "a, b, c, d" {
		t.Fatalf("editing = %v with draft %q, want the values to edit", current.editing, current.draft)
	}
	// Keys that move or quit the form are text while a list is typed.
	current, cmd := press(
		t,
		current,
		ctrl('u'),
		text("q"),
		text(","),
		text(" "),
		text("j"),
		text("x"),
		key(tea.KeyBackspace),
	)
	if cmd != nil {
		t.Fatal("a typed key produced a command")
	}
	screen := ansi.Strip(current.render())
	if !strings.Contains(screen, "‹ q, j▌ ›") || !strings.Contains(screen, "enter done") {
		t.Fatalf("the list being typed is not shown:\n%s", screen)
	}
	current, _ = press(t, current, key(tea.KeyEnter))
	if current.editing || !slices.Equal(current.values[1], []string{"q", "j"}) {
		t.Fatalf("values = %v, editing = %v, want the typed values kept", current.values[1], current.editing)
	}
	if current.items[current.cursor].question != applyRow {
		t.Fatal("finishing the list did not move to the next row")
	}
	current, _ = press(t, current, key(tea.KeyEnter))
	if answers := current.answers(); !current.submitted ||
		!slices.Equal(answers["dirs"], []string{"q", "j"}) {
		t.Fatalf("submitted = %v, answers = %v, want the typed list", current.submitted, answers)
	}
}

func paste(t *testing.T, current model, content string) model {
	t.Helper()
	next, _ := current.Update(tea.PasteMsg{Content: content})
	updated, ok := next.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", next)
	}
	return updated
}

func TestFormPastesIntoAListOnlyWhileEditing(t *testing.T) {
	current := newModel("init", discoveryQuestions())
	current, _ = press(t, current, key(tea.KeyRight), key(tea.KeyDown))
	current = paste(t, current, "ignored")
	current, _ = press(t, current, key(tea.KeyEnter), ctrl('u'))
	current = paste(t, current, "out, tools/*/gen\ngitlab-runner\r\n")
	current, _ = press(t, current, key(tea.KeyEnter))
	want := []string{"out", "tools/*/gen", "gitlab-runner"}
	if !slices.Equal(current.values[1], want) {
		t.Fatalf("values = %v, want the pasted lines as values %v", current.values[1], want)
	}
}

func TestFormEscapeDropsAListEdit(t *testing.T) {
	current := newModel("init", discoveryQuestions())
	current, _ = press(t, current, key(tea.KeyRight), key(tea.KeyDown), key(tea.KeySpace))
	current, cmd := press(t, current, text("z"), key(tea.KeyEscape))
	if cmd != nil || current.editing {
		t.Fatal("escape closed the form or kept editing, want the edit dropped")
	}
	if !slices.Equal(current.values[1], []string{"a", "b", "c", "d"}) {
		t.Fatalf("values = %v, want them unchanged", current.values[1])
	}
}

func TestFormRefusesAListValidateRejects(t *testing.T) {
	current := newModel("init", discoveryQuestions())
	current, _ = press(
		t,
		current,
		key(tea.KeyRight),
		key(tea.KeyDown),
		key(tea.KeyEnter),
		ctrl('u'),
		key(tea.KeyEnter),
		key(tea.KeyEnter),
	)
	if current.submitted {
		t.Fatal("the form applied an empty list its question refuses")
	}
	if current.items[current.cursor].question != 1 {
		t.Fatal("the cursor did not return to the refused list")
	}
	screen := ansi.Strip(current.render())
	if !strings.Contains(screen, "‹ none ›") || !strings.Contains(screen, "list at least one directory") {
		t.Fatalf("the refused list is not shown:\n%s", screen)
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
