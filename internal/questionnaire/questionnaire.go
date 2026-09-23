// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package questionnaire describes questions independently of how they are
// shown. A Renderer asks the same questions - their choices, defaults, offered
// answers, and checks - as a console dialog, a terminal form, or any other
// front end, so changing the front end never changes what is asked.
package questionnaire

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Kind says how a question is answered.
type Kind int

const (
	// Choose picks one of the question's choices, or several when Multiple is
	// set.
	Choose Kind = iota
	// Confirm answers Yes or No.
	Confirm
	// List answers with values the operator types, separated by commas; it
	// has no Choices, and Validate decides which lists it accepts.
	List
)

// Yes and No are the answers of a Confirm question.
const (
	Yes = "yes"
	No  = "no"
)

// Choice is one answer a Choose question offers.
type Choice struct {
	// Value is the answer itself, spelled the way a flag spells it.
	Value string
	// Label names the choice in a few words; it is empty when Value says
	// enough.
	Label string
	// Description says what choosing it does.
	Description string
	// Warning names a risk the choice carries; it is empty when there is none.
	Warning string
}

// Question is one decision, described without any presentation.
//
//nolint:govet // Field order follows how a question reads, not memory layout.
type Question struct {
	// ID keys the question's answer in Answers.
	ID   string
	Kind Kind
	// Section groups related questions, such as every runner; a front end
	// that shows several questions at once shows a section together.
	Section string
	// Subject names what is being decided in a word or two.
	Subject string
	// Title is the question itself.
	Title string
	// Context explains the question, the line that matters most first: a front
	// end short of room may show only that one.
	Context []string
	// ContextFor adds explanation that depends on the answers given so far;
	// nil means the question has none.
	ContextFor func(Answers) []string
	// Notices are what the operator should know before answering, such as a
	// recorded answer that could not be reused.
	Notices []string
	// Badge is a short note shown beside the answer, such as how many
	// recommended values were found; empty shows none.
	Badge string
	// When decides from the answers given so far whether the question applies;
	// nil means it always does. A question that does not apply is not shown
	// and has no answer.
	When func(Answers) bool
	// Choices are the answers a Choose question accepts. A Confirm question
	// may describe its Yes and No here; its answers stay those two either way.
	Choices []Choice
	// Defaults are the answers the question proposes on its own.
	Defaults []string
	// Offer is the answer proposed to the operator: a recorded one when
	// Current is set, otherwise Defaults.
	Offer []string
	// Multiple lets a Choose question take several choices.
	Multiple bool
	// Current marks Offer as the recorded answer rather than Defaults.
	Current bool
	// Label names one answer to the question in a prompt.
	Label string
	// Flag is the command-line flag that answers the question without asking.
	Flag string
	// UnansweredNote tells the operator what leaving a Confirm question empty
	// did: an empty answer, like an untouched form, takes Offer.
	UnansweredNote string
	// Validate rejects an answer the choices alone cannot rule out; nil
	// accepts every answer the choices allow.
	Validate func(values []string) error

	// Parse turns one typed name into the Choice.Value it means, for a front
	// end that reads answers as text.
	Parse func(value string) (string, bool)
	// Unsupported is the error for a typed name no choice matches.
	Unsupported func(value string) error
	// UnsupportedPrompt asks again after a typed name no choice matches.
	UnsupportedPrompt func(value string) string
	// ReadDescription names the answer in a failure to read it.
	ReadDescription string
	// UnansweredDescription names the question when input ends before an
	// answer.
	UnansweredDescription string
}

// Answers holds the chosen values by question ID.
type Answers map[string][]string

// Renderer asks every question in order and returns the answers by question
// ID. It refuses an answer that Validate rejects and fails when the operator
// stops before every question is answered, so it never returns a partial set.
type Renderer interface {
	Ask(ctx context.Context, questions []Question) (Answers, error)
}

// Applies reports whether question is asked after answers.
func (question Question) Applies(answers Answers) bool {
	return question.When == nil || question.When(answers)
}

// SplitList reads a typed List answer: the values between its commas, with
// surrounding spaces removed and empty values left out.
func SplitList(text string) []string {
	values := make([]string, 0, strings.Count(text, ",")+1)
	for value := range strings.SplitSeq(text, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// Summary lists values on a line that has to stay short: the first limit of
// them, then how many more there are.
func Summary(values []string, limit int) string {
	if len(values) <= limit {
		return strings.Join(values, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(values[:limit], ", "), len(values)-limit)
}

// DynamicContext returns the explanation of question that depends on answers,
// or nothing when it has none.
func (question Question) DynamicContext(answers Answers) []string {
	if question.ContextFor == nil {
		return nil
	}
	return question.ContextFor(answers)
}

// Marks names what value is to the question: current when it is part of a
// recorded offer, default when the question proposes it on its own.
func (question Question) Marks(value string) []string {
	marks := make([]string, 0, 2)
	if question.Current && slices.Contains(question.Offer, value) {
		marks = append(marks, "current")
	}
	if slices.Contains(question.Defaults, value) {
		marks = append(marks, "default")
	}
	return marks
}

// Text says what the choice is: its label and description, or the
// description alone when it has no label.
func (choice Choice) Text() string {
	if choice.Label == "" {
		return choice.Description
	}
	return choice.Label + ": " + choice.Description
}
