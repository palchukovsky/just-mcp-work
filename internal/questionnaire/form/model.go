// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package form

import (
	"slices"

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
	title     string
	questions []questionnaire.Question
	values    [][]string
	items     []item
	cursor    int
	width     int
	height    int
	showAll   bool
	submitted bool
}

func newModel(title string, questions []questionnaire.Question) model {
	values := make([][]string, len(questions))
	items := make([]item, 0, len(questions)+1)
	for index, question := range questions {
		values[index] = slices.Clone(question.Offer)
		if !question.Multiple {
			items = append(items, item{question: index, choice: -1})
			continue
		}
		for choice := range question.Choices {
			items = append(items, item{question: index, choice: choice})
		}
	}
	items = append(items, item{question: applyRow, choice: -1})
	return model{title: title, questions: questions, values: values, items: items}
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
		return m.press(msg.String())
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
		m.change(1, true)
	case "?":
		m.showAll = !m.showAll
	}
	return m, nil
}

// enter moves to the next row, and on the apply row applies the form unless
// an answer still needs attention; then it takes the cursor there instead.
func (m model) enter() (tea.Model, tea.Cmd) {
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
	if question.Multiple {
		value := question.Choices[current.choice].Value
		selected := slices.Contains(m.values[current.question], value)
		if cycle {
			selected = !selected
		} else {
			selected = delta > 0
		}
		m.values[current.question] = withChoice(question, m.values[current.question], value, selected)
		return
	}
	values := choiceValues(question)
	m.values[current.question] = []string{step(values, m.values[current.question][0], delta, cycle)}
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
// never accepts an empty answer to it, and Validate may refuse the rest.
func (m model) problems() map[int]string {
	problems := make(map[int]string)
	for index, question := range m.questions {
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

func (m model) answers() questionnaire.Answers {
	answers := make(questionnaire.Answers, len(m.questions))
	for index, question := range m.questions {
		answers[question.ID] = slices.Clone(m.values[index])
	}
	return answers
}
