// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package form

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/palchukovsky/just-mcp-work/internal/questionnaire"
)

// applyRow marks the item that applies the form rather than answering a
// question.
const applyRow = -1

// item is one row the cursor can stand on: a question, one choice of a
// question that takes several, or the apply row.
type item struct {
	question int
	// choice is the choice a Multiple question's row toggles; it is -1 on
	// every other row.
	choice int
}

// model is the form's state. Every answer starts as the question's offer, so
// applying an untouched form answers exactly what the console dialog would
// with every answer left empty.
type model struct {
	title string
	// draft is what the operator types into the focused List answer while
	// editing is set; the answer itself changes only when the edit is
	// finished.
	draft     string
	questions []questionnaire.Question
	values    [][]string
	// items are the rows of the questions that apply to the answers as they
	// stand, followed by the apply row; changing an answer rebuilds them.
	items     []item
	cursor    int
	width     int
	height    int
	editing   bool
	showAll   bool
	submitted bool
}

func newModel(title string, questions []questionnaire.Question) model {
	values := make([][]string, len(questions))
	for index, question := range questions {
		values[index] = slices.Clone(question.Offer)
	}
	m := model{title: title, questions: questions, values: values}
	m.items = m.rows()
	return m
}

// rows lists a row per question that applies - one per choice for a question
// that takes several - and the apply row last.
func (m model) rows() []item {
	items := make([]item, 0, len(m.questions)+1)
	applies := m.applicable()
	for index, question := range m.questions {
		if !applies[index] {
			continue
		}
		if !question.Multiple {
			items = append(items, item{question: index, choice: -1})
			continue
		}
		for choice := range question.Choices {
			items = append(items, item{question: index, choice: choice})
		}
	}
	return append(items, item{question: applyRow, choice: -1})
}

// applicable reports, by question index, whether each question applies to
// the answers of the applicable questions before it, the order the console
// dialog asks them in.
func (m model) applicable() []bool {
	applies := make([]bool, len(m.questions))
	answers := make(questionnaire.Answers, len(m.questions))
	for index, question := range m.questions {
		if !question.Applies(answers) {
			continue
		}
		applies[index] = true
		answers[question.ID] = m.values[index]
	}
	return applies
}

// Init starts the form without a command.
func (m model) Init() tea.Cmd {
	return nil
}

// Update moves the cursor, changes answers, and applies or closes the form.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		if m.editing {
			return m.edit(msg)
		}
		return m.press(msg.String())
	case tea.PasteMsg:
		// A pasted list may come one value per line; the draft is one line,
		// so each line break separates values the way a comma does.
		if m.editing {
			m.draft += strings.NewReplacer("\r\n", ", ", "\n", ", ", "\r", ", ").
				Replace(msg.Content)
		}
	}
	return m, nil
}

func (m model) press(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "enter":
		return m.enter()
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, len(m.items)-1)
	case "left", "h":
		m.change(-1, false)
	case "right", "l":
		m.change(1, false)
	case "space":
		if m.focusedList() {
			m.startEdit()
			break
		}
		m.change(1, true)
	case "?":
		m.showAll = !m.showAll
	}
	return m, nil
}

// edit types into the focused List answer. Enter keeps the typed values and
// moves to the next row, esc drops the edit, backspace erases the last
// character and ctrl+u the whole line; ctrl+c still closes the form.
func (m model) edit(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		m.values[m.items[m.cursor].question] = questionnaire.SplitList(m.draft)
		m.editing, m.draft = false, ""
		m.items = m.rows()
		m.cursor++
	case "esc":
		m.editing, m.draft = false, ""
	case "backspace":
		runes := []rune(m.draft)
		m.draft = string(runes[:max(len(runes)-1, 0)])
	case "ctrl+u":
		m.draft = ""
	default:
		m.draft += msg.Text
	}
	return m, nil
}

func (m model) focusedList() bool {
	current := m.items[m.cursor]
	return current.question != applyRow &&
		m.questions[current.question].Kind == questionnaire.List
}

func (m *model) startEdit() {
	m.editing = true
	m.draft = strings.Join(m.values[m.items[m.cursor].question], ", ")
}

// enter moves to the next row, and on the apply row applies the form unless
// an answer still needs attention; then it takes the cursor there instead. On
// a List row it starts typing that answer.
func (m model) enter() (tea.Model, tea.Cmd) {
	if m.focusedList() {
		m.startEdit()
		return m, nil
	}
	if m.items[m.cursor].question != applyRow {
		m.cursor++
		return m, nil
	}
	problems := m.problems()
	if len(problems) == 0 {
		m.submitted = true
		return m, tea.Quit
	}
	for index, current := range m.items {
		if _, found := problems[current.question]; found {
			m.cursor = index
			break
		}
	}
	return m, nil
}

// change steps the focused answer by delta. With cycle set it wraps around
// and a choice of a Multiple question toggles; without it the answer stops at
// either end and the arrows switch a choice on and off.
func (m *model) change(delta int, cycle bool) {
	current := m.items[m.cursor]
	if current.question == applyRow {
		return
	}
	question := m.questions[current.question]
	switch {
	case question.Kind == questionnaire.List:
		return
	case question.Multiple:
		value := question.Choices[current.choice].Value
		selected := slices.Contains(m.values[current.question], value)
		if cycle {
			selected = !selected
		} else {
			selected = delta > 0
		}
		m.values[current.question] = withChoice(question, m.values[current.question], value, selected)
	default:
		values := choiceValues(question)
		m.values[current.question] = []string{step(values, m.values[current.question][0], delta, cycle)}
	}
	// A changed answer may bring in or leave out questions that follow it;
	// the focused row itself stays where it is.
	m.items = m.rows()
}

// choiceValues lists the answers a single-answer question accepts, in the
// order the form steps through them.
func choiceValues(question questionnaire.Question) []string {
	if question.Kind == questionnaire.Confirm {
		return []string{questionnaire.Yes, questionnaire.No}
	}
	values := make([]string, 0, len(question.Choices))
	for _, choice := range question.Choices {
		values = append(values, choice.Value)
	}
	return values
}

func step(values []string, current string, delta int, cycle bool) string {
	index := slices.Index(values, current) + delta
	if cycle {
		return values[(index+len(values))%len(values)]
	}
	return values[min(max(index, 0), len(values)-1)]
}

// withChoice returns the answer of a Multiple question with value switched on
// or off, keeping the choices in the order the question lists them.
func withChoice(
	question questionnaire.Question,
	current []string,
	value string,
	selected bool,
) []string {
	result := make([]string, 0, len(question.Choices))
	for _, choice := range question.Choices {
		if choice.Value == value {
			if selected {
				result = append(result, value)
			}
			continue
		}
		if slices.Contains(current, choice.Value) {
			result = append(result, choice.Value)
		}
	}
	return result
}

// problems returns, by question index, why an answer cannot be applied yet: a
// question that takes several choices needs at least one, the way the console
// never accepts an empty answer to it, and Validate may refuse the rest. A
// question that does not apply has no answer to refuse.
func (m model) problems() map[int]string {
	problems := make(map[int]string)
	applies := m.applicable()
	for index, question := range m.questions {
		if !applies[index] {
			continue
		}
		if question.Multiple && len(m.values[index]) == 0 {
			problems[index] = "Choose at least one."
			continue
		}
		if question.Validate == nil {
			continue
		}
		if err := question.Validate(m.values[index]); err != nil {
			problems[index] = err.Error()
		}
	}
	return problems
}

// answers returns the answers of the questions that apply.
func (m model) answers() questionnaire.Answers {
	answers := make(questionnaire.Answers, len(m.questions))
	applies := m.applicable()
	for index, question := range m.questions {
		if applies[index] {
			answers[question.ID] = slices.Clone(m.values[index])
		}
	}
	return answers
}
