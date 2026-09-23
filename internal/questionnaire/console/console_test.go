// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package console_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/questionnaire"
	"github.com/palchukovsky/just-mcp-work/internal/questionnaire/console"
)

func colorQuestion() questionnaire.Question {
	return questionnaire.Question{
		ID:      "color",
		Title:   "Which color?",
		Context: []string{"Pick one."},
		Choices: []questionnaire.Choice{
			{Value: "red", Description: "warm"},
			{Value: "blue", Label: "Blue", Description: "cold", Warning: "hard to see"},
		},
		Defaults: []string{"red"},
		Offer:    []string{"red"},
		Label:    "Color",
		Flag:     "--color red|blue",
		Parse: func(value string) (string, bool) {
			if value == "red" || value == "blue" {
				return value, true
			}
			return "", false
		},
		Unsupported: func(value string) error {
			return fmt.Errorf("unsupported color %q", value)
		},
		UnsupportedPrompt: func(value string) string {
			return fmt.Sprintf("Unsupported color %q.\n", value)
		},
		ReadDescription:       "color",
		UnansweredDescription: "color",
	}
}

func applyQuestion() questionnaire.Question {
	return questionnaire.Question{
		ID:                    "apply",
		Kind:                  questionnaire.Confirm,
		Title:                 "Apply the change?",
		Defaults:              []string{questionnaire.No},
		Offer:                 []string{questionnaire.No},
		Label:                 "Apply?",
		Flag:                  "--apply yes|no",
		UnansweredNote:        "No answer; nothing is applied.",
		ReadDescription:       "apply confirmation",
		UnansweredDescription: "apply",
	}
}

func ask(
	input string,
	questions ...questionnaire.Question,
) (questionnaire.Answers, string, error) {
	var output bytes.Buffer
	answers, err := console.New(strings.NewReader(input), &output).Ask(
		context.Background(),
		questions,
	)
	return answers, output.String(), err
}

func TestAskTakesTheOfferOnAnEmptyAnswer(t *testing.T) {
	answers, output, err := ask("\n", colorQuestion())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(answers["color"], []string{"red"}) {
		t.Fatalf("answers = %v, want the offered red", answers)
	}
	const want = "\nWhich color?\nPick one.\n" +
		"  red (default) - warm\n" +
		"  blue - Blue: cold\n" +
		"    WARNING: hard to see\n" +
		"Color [red, default]: "
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestAskMarksTheCurrentOffer(t *testing.T) {
	question := colorQuestion()
	question.Offer = []string{"blue"}
	question.Current = true
	_, output, err := ask("\n", question)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  blue (current) - Blue: cold\n", "Color [blue, current]: "} {
		if !strings.Contains(output, want) {
			t.Fatalf("output lacks %q:\n%s", want, output)
		}
	}
}

func TestAskAsksAgainAfterAnUnsupportedAnswer(t *testing.T) {
	answers, output, err := ask("green\nblue\n", colorQuestion())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(answers["color"], []string{"blue"}) {
		t.Fatalf("answers = %v, want blue", answers)
	}
	if !strings.Contains(output, `Unsupported color "green".`) {
		t.Fatalf("output lacks the rejection prompt:\n%s", output)
	}
}

func TestAskFailsWhenInputEndsUnanswered(t *testing.T) {
	_, _, err := ask("", colorQuestion())
	const want = "color was unanswered at end of input; use --color red|blue"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestAskFailsWithTheRejectionWhenInputEndsOnIt(t *testing.T) {
	_, _, err := ask("green", colorQuestion())
	if err == nil || err.Error() != `unsupported color "green"` {
		t.Fatalf("error = %v, want the rejection of green", err)
	}
}

func TestAskMultipleAcceptsNumbersAndNames(t *testing.T) {
	question := colorQuestion()
	question.Multiple = true
	answers, output, err := ask("1,red\n2 red\n", question)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(answers["color"], []string{"blue", "red"}) {
		t.Fatalf("answers = %v, want blue and red", answers)
	}
	for _, want := range []string{
		"Answer with the numbers or the names, in any order.\n",
		"  1) red (default) - warm\n",
		"     WARNING: hard to see\n",
		`"red" is named twice.`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output lacks %q:\n%s", want, output)
		}
	}
}

func TestAskPrintsNoticesBeforeTheQuestion(t *testing.T) {
	question := colorQuestion()
	question.Notices = []string{"The recorded color cannot be used."}
	_, output, err := ask("\n", question)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output, "\nThe recorded color cannot be used.\n\nWhich color?\n") {
		t.Fatalf("output does not open with the notice:\n%s", output)
	}
}

func TestAskShowsContextThatDependsOnEarlierAnswers(t *testing.T) {
	second := applyQuestion()
	second.ContextFor = func(answers questionnaire.Answers) []string {
		return []string{"The color is " + answers["color"][0] + "."}
	}
	_, output, err := ask("blue\ny\n", colorQuestion(), second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "\nApply the change?\nThe color is blue.\nApply? [y/N]: ") {
		t.Fatalf("output lacks the dependent context:\n%s", output)
	}
}

func TestAskStopsOnAnAnswerValidateRejects(t *testing.T) {
	rejection := errors.New("blue is not allowed here")
	question := colorQuestion()
	question.Validate = func(values []string) error {
		if values[0] == "blue" {
			return rejection
		}
		return nil
	}
	_, output, err := ask("blue\ny\n", question, applyQuestion())
	if !errors.Is(err, rejection) {
		t.Fatalf("error = %v, want the validation rejection", err)
	}
	if strings.Contains(output, "Apply the change?") {
		t.Fatalf("a later question was asked after a rejected answer:\n%s", output)
	}
}

func TestConfirmReadsYesAndNo(t *testing.T) {
	for _, testCase := range []struct {
		input    string
		want     string
		wantNote bool
	}{
		{input: "y\n", want: questionnaire.Yes},
		{input: "YES\n", want: questionnaire.Yes},
		{input: "no\n", want: questionnaire.No},
		{input: "maybe\n", want: questionnaire.No},
		{input: "\n", want: questionnaire.No, wantNote: true},
		{input: "", want: questionnaire.No, wantNote: true},
	} {
		t.Run(fmt.Sprintf("%q", testCase.input), func(t *testing.T) {
			answers, output, err := ask(testCase.input, applyQuestion())
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(answers["apply"], []string{testCase.want}) {
				t.Fatalf("answers = %v, want %s", answers, testCase.want)
			}
			if !strings.Contains(output, "Apply? [y/N]: ") {
				t.Fatalf("output lacks the prompt:\n%s", output)
			}
			if strings.Contains(output, "No answer; nothing is applied.") != testCase.wantNote {
				t.Fatalf("note shown = %t, want %t:\n%s", !testCase.wantNote, testCase.wantNote, output)
			}
		})
	}
}

func TestConfirmTakesTheOfferWhenLeftEmpty(t *testing.T) {
	question := applyQuestion()
	question.UnansweredNote = ""
	question.Offer = []string{questionnaire.Yes}
	for _, input := range []string{"\n", ""} {
		answers, output, err := ask(input, question)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(answers["apply"], []string{questionnaire.Yes}) {
			t.Fatalf("answers for %q = %v, want the offered yes", input, answers)
		}
		if !strings.Contains(output, "Apply? [Y/n]: ") {
			t.Fatalf("output lacks the yes-first prompt:\n%s", output)
		}
	}
}

func TestConfirmListsTheAnswersItDescribes(t *testing.T) {
	question := applyQuestion()
	question.Choices = []questionnaire.Choice{
		{Value: questionnaire.Yes, Label: "Apply", Description: "write it"},
		{Value: questionnaire.No, Label: "Skip", Description: "leave it", Warning: "nothing changes"},
	}
	answers, output, err := ask("y\n", question)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(answers["apply"], []string{questionnaire.Yes}) {
		t.Fatalf("answers = %v, want yes", answers)
	}
	const want = "\nApply the change?\n" +
		"  yes - Apply: write it\n" +
		"  no (default) - Skip: leave it\n" +
		"    WARNING: nothing changes\n" +
		"Apply? [y/N]: "
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestSplitAnswerTokensCutsOnSeparators(t *testing.T) {
	got := console.SplitAnswerTokens(" codex, claude;2 ")
	if !slices.Equal(got, []string{"codex", "claude", "2"}) {
		t.Fatalf("tokens = %v, want codex, claude, 2", got)
	}
}
