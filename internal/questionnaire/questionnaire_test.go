// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package questionnaire_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/questionnaire"
)

func recordedQuestion(id string, offer ...string) questionnaire.Question {
	return questionnaire.Question{
		ID:       id,
		Defaults: []string{"default"},
		Offer:    offer,
		Current:  true,
	}
}

func unrecordedQuestion(id string) questionnaire.Question {
	return questionnaire.Question{
		ID:       id,
		Defaults: []string{"default"},
		Offer:    []string{"default"},
	}
}

func questionIDs(questions []questionnaire.Question) []string {
	ids := make([]string, 0, len(questions))
	for _, question := range questions {
		ids = append(ids, question.ID)
	}
	return ids
}

func customMode(answers questionnaire.Answers) bool {
	return slices.Equal(answers["mode"], []string{"custom"})
}

func TestKeepCurrentTakesEveryRecordedAnswerAndLeavesTheRest(t *testing.T) {
	first := recordedQuestion("first", "a")
	first.Notices = []string{"first notice"}
	asked := unrecordedQuestion("asked")
	asked.Notices = []string{"asked notice"}
	last := recordedQuestion("last", "b", "c")
	last.Notices = []string{"last notice"}

	kept, notices, left, err := questionnaire.KeepCurrent(
		[]questionnaire.Question{first, asked, last},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := questionnaire.Answers{"first": {"a"}, "last": {"b", "c"}}
	if !reflect.DeepEqual(kept, want) {
		t.Fatalf("kept = %v, want %v", kept, want)
	}
	if !slices.Equal(notices, []string{"first notice", "last notice"}) {
		t.Fatalf("notices = %q, want the notices of the kept questions", notices)
	}
	if !slices.Equal(questionIDs(left), []string{"asked"}) ||
		!slices.Equal(left[0].Notices, []string{"asked notice"}) {
		t.Fatalf("left = %v, want the unrecorded question with its notice", questionIDs(left))
	}
}

func TestKeepCurrentShowsAQuestionLeftTheAnswersKeptBeforeIt(t *testing.T) {
	mode := recordedQuestion("mode", "custom")
	list := unrecordedQuestion("list")
	list.When = customMode
	list.ContextFor = func(answers questionnaire.Answers) []string {
		return answers["shell"]
	}
	shell := recordedQuestion("shell", "allow")
	claude := unrecordedQuestion("claude")
	claude.ContextFor = func(answers questionnaire.Answers) []string {
		return slices.Concat(answers["shell"], answers["asked"])
	}

	_, _, left, err := questionnaire.KeepCurrent(
		[]questionnaire.Question{mode, list, shell, claude},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(questionIDs(left), []string{"list", "claude"}) {
		t.Fatalf("left = %v, want list and claude", questionIDs(left))
	}
	if !left[0].Applies(questionnaire.Answers{}) {
		t.Fatal("list does not apply to the custom mode kept before it")
	}
	if lines := left[0].DynamicContext(questionnaire.Answers{}); lines != nil {
		t.Fatalf("list sees %q, an answer kept after it", lines)
	}
	lines := left[1].DynamicContext(questionnaire.Answers{"asked": {"given"}})
	if !slices.Equal(lines, []string{"allow", "given"}) {
		t.Fatalf("claude sees %q, want the kept shell answer and the one given", lines)
	}
}

func TestKeepCurrentDecidesWhenOnTheAnswersKeptBeforeIt(t *testing.T) {
	list := recordedQuestion("list", "out")
	list.When = customMode
	kept, _, left, err := questionnaire.KeepCurrent([]questionnaire.Question{
		recordedQuestion("mode", "custom"),
		unrecordedQuestion("asked"),
		list,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kept["list"], []string{"out"}) ||
		!slices.Equal(questionIDs(left), []string{"asked"}) {
		t.Fatalf(
			"kept = %v, left = %v, want list kept past the question left",
			kept,
			questionIDs(left),
		)
	}

	kept, _, left, err = questionnaire.KeepCurrent([]questionnaire.Question{
		recordedQuestion("mode", "none"),
		list,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := kept["list"]; found || len(left) != 1 ||
		left[0].Applies(questionnaire.Answers{}) {
		t.Fatalf("kept = %v, want list left not applying to the kept mode", kept)
	}
}

func TestKeepCurrentRefusesARecordedAnswerValidateRefuses(t *testing.T) {
	refusal := errors.New("the selected agents have no machine-wide file")
	target := recordedQuestion("target", "machine")
	target.ReadDescription = "instructions target"
	target.Validate = func([]string) error {
		return refusal
	}
	_, _, _, err := questionnaire.KeepCurrent([]questionnaire.Question{target})
	const want = `refuse instructions target "machine": ` +
		"the selected agents have no machine-wide file"
	if !errors.Is(err, refusal) || err.Error() != want {
		t.Fatalf("KeepCurrent() error = %v, want %q", err, want)
	}
}
