// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package form shows questionnaire questions as one keyboard-driven form in a
// terminal: every question on one screen, answered with the arrow keys and the
// space bar, and applied only when the operator applies the whole form.
package form

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/palchukovsky/just-mcp-work/internal/questionnaire"
)

// ErrCancelled reports a form the operator closed without applying it.
var ErrCancelled = errors.New("the form was closed before its answers were applied")

// Renderer shows questions as a form on a terminal.
type Renderer struct {
	input  *os.File
	output *os.File
	title  string
}

// New returns a Renderer that reads keys from input, draws on output, and
// heads the form with title. Both files must be the terminal Detect approved.
func New(input *os.File, output *os.File, title string) *Renderer {
	return &Renderer{input: input, output: output, title: title}
}

// Ask shows every question at once and returns the answers once the operator
// applies the form. Closing the form, an interrupt, or ctx ending first
// returns ErrCancelled and no answers; any other failure of the terminal
// program returns its cause. Once the form closes, the answers are written to
// the output as plain lines, so the terminal keeps a record of them.
func (r *Renderer) Ask(
	ctx context.Context,
	questions []questionnaire.Question,
) (questionnaire.Answers, error) {
	if len(questions) == 0 {
		return questionnaire.Answers{}, nil
	}
	program := tea.NewProgram(
		newModel(r.title, questions),
		tea.WithContext(ctx),
		tea.WithInput(r.input),
		tea.WithOutput(r.output),
	)
	final, err := program.Run()
	// Only an interrupt, or ctx ending, closes the form the way q does; the
	// program also reports a failed input read or a recovered panic as
	// killed, and that cause must reach the operator.
	if errors.Is(err, tea.ErrInterrupted) ||
		(errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil) {
		return nil, ErrCancelled
	}
	if err != nil {
		return nil, fmt.Errorf("run the form: %w", err)
	}
	result, ok := final.(model)
	if !ok {
		return nil, fmt.Errorf("the form ended with an unexpected model %T", final)
	}
	if !result.submitted {
		return nil, ErrCancelled
	}
	if err := writeSummary(r.output, questions, result.values); err != nil {
		return nil, err
	}
	return result.answers(), nil
}

func writeSummary(output io.Writer, questions []questionnaire.Question, values [][]string) error {
	for index, question := range questions {
		if _, err := fmt.Fprintf(
			output,
			"%s: %s\n",
			question.Subject,
			strings.Join(values[index], ", "),
		); err != nil {
			return fmt.Errorf("write the form answers: %w", err)
		}
	}
	return nil
}
