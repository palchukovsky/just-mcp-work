// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package form

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/palchukovsky/just-mcp-work/internal/questionnaire"
)

// The glyphs come from WGL4, the set every Windows console font carries, so
// the form draws the same in a terminal with a narrow font.
const (
	defaultWidth = 80
	focusMark    = "►"
	chosenMark   = "●"
	openMark     = "○"
	absentMark   = "·"
	keysLine     = "↑↓ move  ←→ change  space toggle  enter next / apply  ? all options  q quit"
)

// View draws the whole form on the alternate screen, so closing it restores
// the terminal as it was.
func (m model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	return view
}

func (m model) render() string {
	width := m.width
	if width <= 0 {
		width = defaultWidth
	}
	problems := m.problems()
	header := []string{bold(" " + m.title), rule(width)}
	for _, question := range m.questions {
		for _, notice := range question.Notices {
			header = append(header, wrap("! "+notice, width, warning)...)
		}
	}
	body, focus := m.body(problems)
	details := m.details(problems, width)
	room := len(body)
	if m.height > 0 {
		// A known screen always keeps the focused row: the details give way
		// first, then the body scrolls around the focus.
		fixed := len(header) + 3
		details = details[:min(len(details), max(m.height-fixed-1, 0))]
		room = max(m.height-fixed-len(details), 1)
	}
	footer := slices.Concat(
		[]string{rule(width)},
		details,
		[]string{rule(width), muted(" " + keysLine)},
	)
	body = window(body, focus, room)
	lines := slices.Concat(header, body, footer)
	for index, line := range lines {
		lines[index] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

// window keeps the focused line on screen when the body is taller than the
// room it has.
func window(body []string, focus int, room int) []string {
	if len(body) <= room {
		return body
	}
	start := min(max(focus-room/2, 0), len(body)-room)
	return body[start : start+room]
}

// body draws one line per row plus the headings of sections and of
// questions that take several choices, and returns which line is focused.
func (m model) body(problems map[int]string) ([]string, int) {
	nameWidth := m.nameWidth()
	lines := make([]string, 0, len(m.items)+len(m.questions))
	focus := 0
	row := 0
	emit := func(line string) {
		if row == m.cursor {
			focus = len(lines)
		}
		lines = append(lines, line)
		row++
	}
	for index := 0; index < len(m.questions); {
		question := m.questions[index]
		switch {
		case question.Section != "" && question.Kind == questionnaire.Choose && !question.Multiple:
			end := sectionEnd(m.questions, index)
			lines = append(lines, "", m.sectionHeading(index, end, nameWidth))
			for current := index; current < end; current++ {
				emit(m.matrixRow(current, row == m.cursor, nameWidth, problems))
			}
			lines = append(lines, "")
			index = end
			continue
		case question.Multiple:
			lines = append(lines, " "+bold(question.Subject)+problemMark(problems, index))
			for choice := range question.Choices {
				emit(m.choiceRow(index, choice, row == m.cursor, nameWidth))
			}
		default:
			emit(m.singleRow(index, row == m.cursor, nameWidth, problems))
		}
		index++
	}
	lines = append(lines, "")
	emit(applyLine(row == m.cursor, len(problems) > 0))
	return lines, focus
}

func (m model) nameWidth() int {
	width := 12
	for _, question := range m.questions {
		width = max(width, ansi.StringWidth(question.Subject))
	}
	return width
}

// sectionEnd returns the index after the run of questions that share the
// section of questions[start].
func sectionEnd(questions []questionnaire.Question, start int) int {
	end := start + 1
	for end < len(questions) &&
		questions[end].Section == questions[start].Section &&
		questions[end].Kind == questionnaire.Choose &&
		!questions[end].Multiple {
		end++
	}
	return end
}

// columns lists the answers of a section as table columns: the longest
// choice list in its own order, then any answer only a shorter list offers.
func (m model) columns(start int, end int) []string {
	longest := start
	for index := start; index < end; index++ {
		if len(m.questions[index].Choices) > len(m.questions[longest].Choices) {
			longest = index
		}
	}
	columns := choiceValues(m.questions[longest])
	for index := start; index < end; index++ {
		for _, value := range choiceValues(m.questions[index]) {
			if !slices.Contains(columns, value) {
				columns = append(columns, value)
			}
		}
	}
	return columns
}

func columnWidth(value string) int {
	return max(ansi.StringWidth(value), 3) + 2
}

func (m model) sectionHeading(start int, end int, nameWidth int) string {
	var line strings.Builder
	line.WriteString(pad(" "+bold(m.questions[start].Section), nameWidth+4))
	for _, column := range m.columns(start, end) {
		line.WriteString(muted(center(column, columnWidth(column))))
	}
	return line.String()
}

func (m model) matrixRow(index int, focused bool, nameWidth int, problems map[int]string) string {
	question := m.questions[index]
	var line strings.Builder
	line.WriteString(marker(focused) + " " + pad(name(question.Subject, focused), nameWidth) + " ")
	chosen := m.values[index][0]
	offered := choiceValues(question)
	start := slices.IndexFunc(m.questions, func(other questionnaire.Question) bool {
		return other.Section == question.Section
	})
	for _, column := range m.columns(start, sectionEnd(m.questions, start)) {
		cell := muted(absentMark)
		switch {
		case column == chosen:
			cell = accent(chosenMark)
		case slices.Contains(offered, column):
			cell = openMark
		}
		line.WriteString(center(cell, columnWidth(column)))
	}
	line.WriteString(" " + choiceLabel(question, chosen) + problemMark(problems, index))
	return line.String()
}

func (m model) choiceRow(index int, choice int, focused bool, nameWidth int) string {
	option := m.questions[index].Choices[choice]
	box := "[ ]"
	if slices.Contains(m.values[index], option.Value) {
		box = accent("[x]")
	}
	return marker(focused) + "   " + box + " " + pad(name(option.Value, focused), nameWidth-4) +
		" " + muted(option.Description)
}

func (m model) singleRow(index int, focused bool, nameWidth int, problems map[int]string) string {
	question := m.questions[index]
	return marker(focused) + " " + pad(name(question.Subject, focused), nameWidth) + "  " +
		accent("‹ "+strings.Join(m.values[index], ", ")+" ›") + problemMark(problems, index)
}

func applyLine(focused bool, blocked bool) string {
	if blocked {
		return marker(focused) + " " + muted("[ Apply ]") + danger("  answer the marked questions first")
	}
	return marker(focused) + " " + bold(accent("[ Apply ]")) + muted("  apply every answer above")
}

// choiceLabel names the chosen answer of a section row: its label, or the
// value when the choice has none.
func choiceLabel(question questionnaire.Question, value string) string {
	for _, choice := range question.Choices {
		if choice.Value == value && choice.Label != "" {
			return choice.Label
		}
	}
	return value
}

// details explains the focused row: the question, its context, the chosen
// answer with its warning, what still blocks it, and the flag that answers it
// without the form. With showAll it lists every choice instead of the chosen
// one.
func (m model) details(problems map[int]string, width int) []string {
	current := m.items[m.cursor]
	if current.question == applyRow {
		return m.applyDetails(problems, width)
	}
	question := m.questions[current.question]
	lines := wrap(question.Title, width, bold)
	context := slices.Concat(question.Context, question.DynamicContext(m.answers()))
	for _, line := range context {
		lines = append(lines, wrap(line, width, plain)...)
	}
	lines = append(lines, m.answerDetails(current, width)...)
	if problem, found := problems[current.question]; found {
		lines = append(lines, wrap(problem, width, danger)...)
	}
	if question.Flag != "" {
		lines = append(lines, wrap("flag: "+question.Flag, width, muted)...)
	}
	return lines
}

func (m model) answerDetails(current item, width int) []string {
	question := m.questions[current.question]
	var chosen []questionnaire.Choice
	switch {
	case m.showAll:
		chosen = question.Choices
	case question.Multiple:
		chosen = []questionnaire.Choice{question.Choices[current.choice]}
	default:
		for _, choice := range question.Choices {
			if slices.Contains(m.values[current.question], choice.Value) {
				chosen = append(chosen, choice)
			}
		}
	}
	var lines []string
	for _, choice := range chosen {
		text := choice.Value + marks(question, choice.Value) + " - " + choice.Text()
		lines = append(lines, wrap(text, width, plain)...)
		if choice.Warning != "" {
			lines = append(lines, wrap("WARNING: "+choice.Warning, width, warning)...)
		}
	}
	return lines
}

func (m model) applyDetails(problems map[int]string, width int) []string {
	if len(problems) == 0 {
		return wrap("Press enter to apply every answer above.", width, plain)
	}
	lines := wrap("These answers need attention first:", width, bold)
	for index, question := range m.questions {
		if problem, found := problems[index]; found {
			lines = append(lines, wrap(question.Subject+": "+problem, width, danger)...)
		}
	}
	return lines
}

func marks(question questionnaire.Question, value string) string {
	names := question.Marks(value)
	if len(names) == 0 {
		return ""
	}
	return " (" + strings.Join(names, ", ") + ")"
}

func problemMark(problems map[int]string, index int) string {
	if _, found := problems[index]; found {
		return danger("  !")
	}
	return ""
}

func marker(focused bool) string {
	if focused {
		return accent(" " + focusMark)
	}
	return "  "
}

func name(text string, focused bool) string {
	if focused {
		return bold(text)
	}
	return text
}

func rule(width int) string {
	return muted(" " + strings.Repeat("─", max(width-2, 1)))
}

// wrap breaks text to the form width with a one-space margin and styles each
// line.
func wrap(text string, width int, style func(string) string) []string {
	lines := strings.Split(ansi.Wrap(text, max(width-2, 10), ""), "\n")
	for index, line := range lines {
		lines[index] = " " + style(line)
	}
	return lines
}

func pad(text string, width int) string {
	return text + strings.Repeat(" ", max(width-ansi.StringWidth(text), 0))
}

func center(text string, width int) string {
	space := max(width-ansi.StringWidth(text), 0)
	return strings.Repeat(" ", space/2) + text + strings.Repeat(" ", space-space/2)
}

func plain(text string) string {
	return text
}

func bold(text string) string {
	return ansi.NewStyle().Bold().Styled(text)
}

func muted(text string) string {
	return ansi.NewStyle().Faint().Styled(text)
}

func accent(text string) string {
	return ansi.NewStyle().ForegroundColor(ansi.Cyan).Styled(text)
}

func warning(text string) string {
	return ansi.NewStyle().ForegroundColor(ansi.Yellow).Styled(text)
}

func danger(text string) string {
	return ansi.NewStyle().ForegroundColor(ansi.Red).Styled(text)
}
