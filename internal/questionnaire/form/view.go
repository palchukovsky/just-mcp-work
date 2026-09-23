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
	keysLine     = "↑↓ move  ←→ change  space toggle  enter next / apply  ? all details  q quit"
	answerMargin = "    "
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
	body, focus, anchor := m.body(problems)
	details := m.details(problems, width, false)
	room := len(body)
	if m.height > 0 {
		// A known screen always keeps the focused row. Unless ? asked for
		// every detail, the body also keeps half of the screen: details that
		// do not fit beside it shrink to their essentials. What still does
		// not fit is cut, and the body scrolls around the focus.
		fixed := len(header) + 3
		available := max(m.height-fixed, 1)
		if !m.showAll && len(details) > available-min(len(body), available/2) {
			details = m.details(problems, width, true)
		}
		details = details[:min(len(details), available-1)]
		room = max(available-len(details), 1)
	}
	footer := slices.Concat(
		[]string{rule(width)},
		details,
		[]string{rule(width), muted(" " + keysLine)},
	)
	body = window(body, focus, anchor, room)
	lines := slices.Concat(header, body, footer)
	for index, line := range lines {
		lines[index] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

// window keeps the focused line on screen when the body is taller than the
// room it has, and with it the anchor - the heading of its block - when both
// fit.
func window(body []string, focus int, anchor int, room int) []string {
	if len(body) <= room {
		return body
	}
	start := min(max(focus-room/2, 0), len(body)-room)
	if anchor < start && focus-anchor < room {
		start = anchor
	}
	return body[start : start+room]
}

// body draws one line per row plus the headings of sections and of
// questions that take several choices. It returns which line is focused and
// which line anchors it: the heading of its block, or the focused line itself.
// Blocks of rows under a heading stand apart from the rest by a blank line.
func (m model) body(problems map[int]string) ([]string, int, int) {
	nameWidth := m.nameWidth()
	lines := make([]string, 0, len(m.items)+2*len(m.questions))
	focus, anchor, heading := 0, 0, -1
	row := 0
	emit := func(line string) {
		if row == m.cursor {
			focus, anchor = len(lines), len(lines)
			if heading >= 0 {
				anchor = heading
			}
		}
		lines = append(lines, line)
		row++
	}
	gap := func() {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
	}
	title := func(line string) {
		heading = len(lines)
		lines = append(lines, line)
	}
	for index := 0; index < len(m.questions); {
		question := m.questions[index]
		switch {
		case question.Section != "" && !question.Multiple:
			end := sectionEnd(m.questions, index)
			gap()
			if tabular(m.questions[index:end]) {
				title(m.sectionHeading(index, end, nameWidth))
				for current := index; current < end; current++ {
					emit(m.matrixRow(current, row == m.cursor, nameWidth, problems))
				}
			} else {
				title(" " + bold(question.Section))
				for current := index; current < end; current++ {
					emit(m.singleRow(current, row == m.cursor, nameWidth, problems))
				}
			}
			gap()
			heading = -1
			index = end
			continue
		case question.Multiple:
			gap()
			title(" " + bold(question.Subject) + problemMark(problems, index))
			for choice := range question.Choices {
				emit(m.choiceRow(index, choice, row == m.cursor, nameWidth))
			}
			gap()
			heading = -1
		default:
			emit(m.singleRow(index, row == m.cursor, nameWidth, problems))
		}
		index++
	}
	gap()
	emit(applyLine(row == m.cursor, len(problems) > 0))
	return lines, focus, anchor
}

func (m model) nameWidth() int {
	width := 12
	for _, question := range m.questions {
		width = max(width, ansi.StringWidth(question.Subject))
	}
	return width
}

// sectionEnd returns the index after the run of single-answer questions that
// share the section of questions[start].
func sectionEnd(questions []questionnaire.Question, start int) int {
	end := start + 1
	for end < len(questions) &&
		questions[end].Section == questions[start].Section &&
		!questions[end].Multiple {
		end++
	}
	return end
}

// tabular reports whether a section is drawn as a table: it needs at least
// two rows to compare, each choosing one of its own choices.
func tabular(section []questionnaire.Question) bool {
	if len(section) < 2 {
		return false
	}
	for _, question := range section {
		if question.Kind != questionnaire.Choose {
			return false
		}
	}
	return true
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
	return marker(focused) + "   " + box + " " + pad(name(choiceName(option), focused), nameWidth-4) +
		" " + muted(option.Description)
}

// singleRow shows a single answer, followed by its label when it has one.
func (m model) singleRow(index int, focused bool, nameWidth int, problems map[int]string) string {
	question := m.questions[index]
	line := marker(focused) + " " + pad(name(question.Subject, focused), nameWidth) + "  " +
		accent("‹ "+strings.Join(m.values[index], ", ")+" ›")
	if label := answerLabel(question, m.values[index][0]); label != "" {
		line += "  " + label
	}
	return line + problemMark(problems, index)
}

func applyLine(focused bool, blocked bool) string {
	if blocked {
		return marker(focused) + " " + muted("[ Apply ]") + danger("  answer the marked questions first")
	}
	return marker(focused) + " " + bold(accent("[ Apply ]")) + muted("  apply every answer above")
}

// answerLabel returns the label of the choice value names; it is empty when
// that choice has none.
func answerLabel(question questionnaire.Question, value string) string {
	for _, choice := range question.Choices {
		if choice.Value == value {
			return choice.Label
		}
	}
	return ""
}

// choiceLabel names the chosen answer of a section row: its label, or the
// value when the choice has none.
func choiceLabel(question questionnaire.Question, value string) string {
	if label := answerLabel(question, value); label != "" {
		return label
	}
	return value
}

// choiceName names a choice in its own row: its label, or its value when the
// value says enough.
func choiceName(choice questionnaire.Choice) string {
	if choice.Label != "" {
		return choice.Label
	}
	return choice.Value
}

// details explains the focused row in blocks separated by blank lines: the
// question with its context, its answers, what still blocks it, and the flag
// that answers it without the form. Compact details keep only the question,
// the lead line of its context, the answers explained in full, and what still
// blocks it, with no blank lines between them.
func (m model) details(problems map[int]string, width int, compact bool) []string {
	current := m.items[m.cursor]
	if current.question == applyRow {
		return m.applyDetails(problems, width)
	}
	question := m.questions[current.question]
	context := slices.Concat(question.Context, question.DynamicContext(m.answers()))
	if compact {
		context = context[:min(len(context), 1)]
	}
	intro := wrap(question.Title, width, bold)
	for _, line := range context {
		intro = append(intro, wrap(line, width, plain)...)
	}
	blocks := [][]string{intro, m.answerDetails(current, width, compact)}
	if problem, found := problems[current.question]; found {
		blocks = append(blocks, wrap(problem, width, danger))
	}
	if question.Flag != "" && !compact {
		blocks = append(blocks, wrap("flag: "+question.Flag, width, muted))
	}
	var lines []string
	for _, block := range blocks {
		if len(block) == 0 {
			continue
		}
		if len(lines) > 0 && !compact {
			lines = append(lines, "")
		}
		lines = append(lines, block...)
	}
	return lines
}

// answerDetails lists the answers of the focused row, one line each. An
// answer explained in full - a chosen one, the choice of a row that toggles
// one, or every answer with showAll - also shows its description and warning
// under it; the others stay one faint line, and compact details leave them
// out. A row of a question that takes several choices explains only its own
// choice.
func (m model) answerDetails(current item, width int, compact bool) []string {
	question := m.questions[current.question]
	choices := question.Choices
	if question.Multiple {
		choices = question.Choices[current.choice : current.choice+1]
	}
	var lines []string
	for _, choice := range choices {
		chosen := slices.Contains(m.values[current.question], choice.Value)
		head := answerMark(question, chosen) + " " + choice.Value + marks(question, choice.Value) +
			" - " + choiceSummary(choice)
		if !chosen && !question.Multiple && !m.showAll {
			if !compact {
				lines = append(lines, wrap(head, width, muted)...)
			}
			continue
		}
		lines = append(lines, wrap(head, width, plain)...)
		if choice.Label != "" && choice.Description != "" {
			lines = append(lines, indent(choice.Description, width, plain)...)
		}
		if choice.Warning != "" {
			lines = append(lines, indent("WARNING: "+choice.Warning, width, warning)...)
		}
	}
	return lines
}

// answerMark shows whether an answer is chosen, the way its row does.
func answerMark(question questionnaire.Question, chosen bool) string {
	switch {
	case question.Multiple && chosen:
		return "[x]"
	case question.Multiple:
		return "[ ]"
	case chosen:
		return chosenMark
	default:
		return openMark
	}
}

// choiceSummary is what an answer line says after the value: the label, which
// the description then explains, or the description itself.
func choiceSummary(choice questionnaire.Choice) string {
	if choice.Label != "" {
		return choice.Label
	}
	return choice.Description
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

// indent wraps text under an answer line, set in by answerMargin so it reads
// as part of that answer.
func indent(text string, width int, style func(string) string) []string {
	lines := strings.Split(ansi.Wrap(text, max(width-2-len(answerMargin), 10), ""), "\n")
	for index, line := range lines {
		lines[index] = " " + answerMargin + style(line)
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
