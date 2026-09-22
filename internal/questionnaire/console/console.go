// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package console asks questionnaire questions as plain text, one at a time,
// each answered by typing a line. It needs nothing from the terminal, so it
// serves pipes, scripts, and consoles that cannot show a form.
package console

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/palchukovsky/just-mcp-work/internal/questionnaire"
)

// Renderer asks questions on a line-based console.
type Renderer struct {
	input  *bufio.Reader
	output io.Writer
}

// New returns a Renderer that reads answers from input and writes questions to
// output. One Renderer keeps one buffer over input, so every question reads
// from where the previous answer stopped.
func New(input io.Reader, output io.Writer) *Renderer {
	return &Renderer{input: bufio.NewReader(input), output: output}
}

// Ask asks each question in order. A typed answer that names no choice is
// asked again; an answer Validate refuses, and input ending before a Choose
// question is answered, stop it with an error.
func (r *Renderer) Ask(
	_ context.Context,
	questions []questionnaire.Question,
) (questionnaire.Answers, error) {
	answers := make(questionnaire.Answers, len(questions))
	for _, question := range questions {
		values, err := r.ask(question, answers)
		if err != nil {
			return nil, err
		}
		if question.Validate != nil {
			if validateErr := question.Validate(values); validateErr != nil {
				return nil, fmt.Errorf(
					"refuse %s %q: %w",
					question.ReadDescription,
					strings.Join(values, ","),
					validateErr,
				)
			}
		}
		answers[question.ID] = values
	}
	return answers, nil
}

// SplitAnswerTokens cuts a typed answer on everything a choice name cannot
// contain, so numbers and names may be separated by commas, spaces, or both.
func SplitAnswerTokens(answer string) []string {
	return strings.FieldsFunc(answer, func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '-' && char != '_'
	})
}

func (r *Renderer) ask(
	question questionnaire.Question,
	answers questionnaire.Answers,
) ([]string, error) {
	if len(question.Notices) > 0 {
		if err := r.write("\n"); err != nil {
			return nil, err
		}
		for _, notice := range question.Notices {
			if err := r.write("%s\n", notice); err != nil {
				return nil, err
			}
		}
	}
	if err := r.write("\n%s\n", question.Title); err != nil {
		return nil, err
	}
	lines := slices.Concat(question.Context, question.DynamicContext(answers))
	for _, line := range lines {
		if err := r.write("%s\n", line); err != nil {
			return nil, err
		}
	}
	if question.Kind == questionnaire.Confirm {
		return r.confirm(question)
	}
	return r.choose(question)
}

func (r *Renderer) choose(question questionnaire.Question) ([]string, error) {
	if question.Multiple {
		if err := r.write("Answer with the numbers or the names, in any order.\n"); err != nil {
			return nil, err
		}
	}
	for index, choice := range question.Choices {
		// Only a question answered with several choices numbers them, because
		// only its answer needs a short way to name more than one.
		number, warningIndent := "", "    "
		if question.Multiple {
			number, warningIndent = fmt.Sprintf("%d) ", index+1), "     "
		}
		if err := r.write(
			"  %s%s%s - %s\n",
			number,
			choice.Value,
			choiceMarks(question, choice.Value),
			choice.Text(),
		); err != nil {
			return nil, err
		}
		if choice.Warning != "" {
			if err := r.write("%sWARNING: %s\n", warningIndent, choice.Warning); err != nil {
				return nil, err
			}
		}
	}
	return r.readChoice(question)
}

func choiceMarks(question questionnaire.Question, value string) string {
	marks := question.Marks(value)
	if len(marks) == 0 {
		return ""
	}
	return " (" + strings.Join(marks, ", ") + ")"
}

func (r *Renderer) readChoice(question questionnaire.Question) ([]string, error) {
	source := "default"
	if question.Current {
		source = "current"
	}
	for {
		if err := r.write(
			"%s [%s, %s]: ",
			question.Label,
			strings.Join(question.Offer, ","),
			source,
		); err != nil {
			return nil, err
		}
		answer, err := r.input.ReadString('\n')
		trimmed := strings.TrimSpace(answer)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read %s: %w", question.ReadDescription, err)
		}
		if trimmed == "" {
			if errors.Is(err, io.EOF) {
				if writeErr := r.write("\n"); writeErr != nil {
					return nil, writeErr
				}
				return nil, unansweredError(question)
			}
			return question.Offer, nil
		}
		values, prompt, answerErr := resolveAnswer(question, trimmed)
		if answerErr == nil {
			return values, nil
		}
		if errors.Is(err, io.EOF) {
			return nil, answerErr
		}
		if writeErr := r.write("%s", prompt); writeErr != nil {
			return nil, writeErr
		}
	}
}

// resolveAnswer turns one typed answer into the values it names. It returns
// the prompt for a rejected answer beside the error carrying the same
// rejection, so a closed console fails with the reason it would have printed.
// A single-choice question reads the whole answer as one name, so only a
// question answered with several choices splits it or accepts a number.
func resolveAnswer(
	question questionnaire.Question,
	answer string,
) ([]string, string, error) {
	if !question.Multiple {
		value, found := question.Parse(answer)
		if !found {
			return nil, question.UnsupportedPrompt(answer), question.Unsupported(answer)
		}
		return []string{value}, "", nil
	}
	tokens := SplitAnswerTokens(answer)
	if len(tokens) == 0 {
		return nil, question.UnsupportedPrompt(answer), question.Unsupported(answer)
	}
	values := make([]string, 0, len(tokens))
	for _, token := range tokens {
		value, found := resolveToken(question, token)
		if !found {
			return nil, question.UnsupportedPrompt(token), question.Unsupported(token)
		}
		if slices.Contains(values, value) {
			return nil, fmt.Sprintf("%q is named twice.\n", value), fmt.Errorf(
				"%q is named twice",
				value,
			)
		}
		values = append(values, value)
	}
	return values, "", nil
}

// resolveToken accepts either the number printed beside a choice or the
// choice's own name, so an operator never has to retype a name to answer.
func resolveToken(question questionnaire.Question, token string) (string, bool) {
	if index, err := strconv.Atoi(token); err == nil {
		if index < 1 || index > len(question.Choices) {
			return "", false
		}
		return question.Choices[index-1].Value, true
	}
	return question.Parse(token)
}

// confirm reads a yes-or-no answer. Only y or yes, in any case, answers yes;
// any other typed answer answers no, and an empty one - an empty line or the
// end of input - takes the offer and prints the question's UnansweredNote.
func (r *Renderer) confirm(question questionnaire.Question) ([]string, error) {
	choices := "[y/N]"
	if slices.Equal(question.Offer, []string{questionnaire.Yes}) {
		choices = "[Y/n]"
	}
	if err := r.write("%s %s: ", question.Label, choices); err != nil {
		return nil, err
	}
	answer, err := r.input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read %s: %w", question.ReadDescription, err)
	}
	trimmed := strings.TrimSpace(answer)
	if trimmed != "" {
		switch strings.ToLower(trimmed) {
		case "y", "yes":
			return []string{questionnaire.Yes}, nil
		default:
			return []string{questionnaire.No}, nil
		}
	}
	if question.UnansweredNote != "" {
		if writeErr := r.write("\n%s\n", question.UnansweredNote); writeErr != nil {
			return nil, writeErr
		}
	}
	return question.Offer, nil
}

func unansweredError(question questionnaire.Question) error {
	return fmt.Errorf(
		"%s was unanswered at end of input; use %s for non-interactive init",
		question.UnansweredDescription,
		question.Flag,
	)
}

func (r *Renderer) write(format string, arguments ...any) error {
	if _, err := fmt.Fprintf(r.output, format, arguments...); err != nil {
		return fmt.Errorf("write init output: %w", err)
	}
	return nil
}
