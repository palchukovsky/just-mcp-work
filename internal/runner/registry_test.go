// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runner_test

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

var _ func(...runner.Registration) (*runner.Registry, error) = runner.NewRegistry

func TestCatalogRejectsInvalidRegistrations(t *testing.T) {
	validFactory := func(runner.Mode) (runner.Runner, error) {
		return fakeRunner{name: "fake"}, nil
	}
	tests := []struct {
		name          string
		want          string
		registrations []runner.Registration
	}{
		{
			name: "empty name",
			registrations: []runner.Registration{runner.NewRegistration(
				"",
				runner.UnreviewedPermissions(),
				validFactory,
			)},
			want: "name must not be empty",
		},
		{
			name: "name breaks persisted mode",
			registrations: []runner.Registration{runner.NewRegistration(
				"fake=alias",
				runner.UnreviewedPermissions(),
				validFactory,
			)},
			want: "must not contain '=' or ':'",
		},
		{
			name: "name breaks task namespace",
			registrations: []runner.Registration{runner.NewRegistration(
				"fake:alias",
				runner.UnreviewedPermissions(),
				validFactory,
			)},
			want: "must not contain '=' or ':'",
		},
		{
			name: "missing declaration",
			registrations: []runner.Registration{runner.NewRegistration(
				"fake",
				runner.PermissionDeclaration{},
				validFactory,
			)},
			want: "no permission declaration",
		},
		{
			name: "invalid mode",
			registrations: []runner.Registration{runner.NewRegistration(
				"fake",
				testReviewedPermissions(
					runner.ModeAll,
					runner.ModeAll,
					runner.Mode("invalid"),
					runner.ModeDisabled,
				),
				validFactory,
			)},
			want: "invalid mode",
		},
		{
			name: "invalid default",
			registrations: []runner.Registration{runner.NewRegistration(
				"fake",
				testReviewedPermissions(
					runner.Mode("invalid"),
					runner.ModeAll,
					runner.ModeDisabled,
				),
				validFactory,
			)},
			want: "invalid default",
		},
		{
			name: "missing question",
			registrations: []runner.Registration{runner.NewRegistration(
				"fake",
				runner.ReviewedPermissions(
					"",
					"Reviewed context.",
					runner.ModeAll,
					testPermissionChoice(runner.ModeAll),
					testPermissionChoice(runner.ModeDisabled),
				),
				validFactory,
			)},
			want: "no permission question",
		},
		{
			name: "missing context",
			registrations: []runner.Registration{runner.NewRegistration(
				"fake",
				runner.ReviewedPermissions(
					"Choose access.",
					"",
					runner.ModeAll,
					testPermissionChoice(runner.ModeAll),
					testPermissionChoice(runner.ModeDisabled),
				),
				validFactory,
			)},
			want: "no permission context",
		},
		{
			name: "missing choices",
			registrations: []runner.Registration{runner.NewRegistration(
				"fake",
				runner.ReviewedPermissions("Choose access.", "Reviewed context.", runner.ModeAll),
				validFactory,
			)},
			want: "no permission choices",
		},
		{
			name: "missing choice label",
			registrations: []runner.Registration{runner.NewRegistration(
				"fake",
				runner.ReviewedPermissions(
					"Choose access.",
					"Reviewed context.",
					runner.ModeAll,
					runner.PermissionChoice{
						Mode:        runner.ModeAll,
						Description: "All commands.",
					},
					testPermissionChoice(runner.ModeDisabled),
				),
				validFactory,
			)},
			want: "no permission label",
		},
		{
			name: "missing choice description",
			registrations: []runner.Registration{runner.NewRegistration(
				"fake",
				runner.ReviewedPermissions(
					"Choose access.",
					"Reviewed context.",
					runner.ModeAll,
					runner.PermissionChoice{Mode: runner.ModeAll, Label: "All"},
					testPermissionChoice(runner.ModeDisabled),
				),
				validFactory,
			)},
			want: "no permission description",
		},
		{
			name: "duplicate name",
			registrations: []runner.Registration{
				runner.NewRegistration(
					"fake",
					runner.UnreviewedPermissions(),
					validFactory,
				),
				runner.NewRegistration(
					"fake",
					runner.UnreviewedPermissions(),
					validFactory,
				),
			},
			want: "duplicate runner registration",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := runner.NewCatalog(test.registrations...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewCatalog error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCatalogPermissionRequestsAreCopied(t *testing.T) {
	choices := []runner.PermissionChoice{
		testPermissionChoice(runner.ModeSafe),
		testPermissionChoice(runner.ModeDisabled),
	}
	declaration := runner.ReviewedPermissions(
		"Choose fake access.",
		"The fake runner was reviewed.",
		runner.ModeSafe,
		choices...,
	)
	choices[0].Label = "mutated input"
	catalog, err := runner.NewCatalog(runner.NewRegistration(
		"fake",
		declaration,
		func(runner.Mode) (runner.Runner, error) { return fakeRunner{name: "fake"}, nil },
	))
	if err != nil {
		t.Fatal(err)
	}
	requests := catalog.PermissionRequests()
	if len(requests) != 1 || requests[0].Name != "fake" || !requests[0].Reviewed ||
		requests[0].Default != runner.ModeSafe || requests[0].Choices[0].Label != "safe label" {
		t.Fatalf("permission requests = %#v", requests)
	}
	requests[0].Choices[0].Label = "mutated output"
	requests[0].Choices = nil
	again := catalog.PermissionRequests()
	if len(again[0].Choices) != 2 || again[0].Choices[0].Label != "safe label" {
		t.Fatalf("catalog request was mutated through returned copy: %#v", again)
	}
}

func TestCatalogRejectsTypedNilStaticRunnerBeforeCallingName(t *testing.T) {
	var candidate *nilPanickingRunner
	registration := runner.StaticRegistration(candidate, runner.UnreviewedPermissions())
	if _, err := runner.NewCatalog(registration); err == nil {
		t.Fatal("NewCatalog accepted a typed-nil static runner")
	}
}

func TestCatalogDoesNotConstructDisabledRunner(t *testing.T) {
	calls := 0
	catalog, err := runner.NewCatalog(runner.NewRegistration(
		"fake",
		runner.UnreviewedPermissions(),
		func(runner.Mode) (runner.Runner, error) {
			calls++
			return fakeRunner{name: "fake"}, nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("NewCatalog called factory %d times", calls)
	}
	registry, err := catalog.Resolve(
		[]runner.Selection{{Name: "fake", Mode: runner.ModeDisabled}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(registry.All()) != 0 {
		t.Fatalf("disabled resolution called factory %d times and returned %#v", calls, registry.All())
	}
	if _, err := catalog.Resolve(nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("enabled resolution called factory %d times, want 1", calls)
	}
}

func TestSelectedRunnerFactoryFailuresFailClosed(t *testing.T) {
	sentinel := errors.New("factory failed")
	var typedNil *nilPanickingRunner
	tests := []struct {
		factory runner.RunnerFactory
		name    string
	}{
		{
			name:    "error",
			factory: func(runner.Mode) (runner.Runner, error) { return nil, sentinel },
		},
		{
			name:    "typed nil",
			factory: func(runner.Mode) (runner.Runner, error) { return typedNil, nil },
		},
		{
			name: "wrong name",
			factory: func(runner.Mode) (runner.Runner, error) {
				return fakeRunner{name: "other"}, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog, err := runner.NewCatalog(runner.NewRegistration(
				"fake",
				runner.UnreviewedPermissions(),
				test.factory,
			))
			if err != nil {
				t.Fatalf("structural catalog validation called the factory: %v", err)
			}
			if _, err := catalog.Resolve(nil); err == nil {
				t.Fatal("Resolve accepted an invalid selected runner")
			}
		})
	}
}

func TestSafeRunnerRequiresTaskInputValidator(t *testing.T) {
	catalog, err := runner.NewCatalog(runner.NewRegistration(
		"fake",
		testReviewedPermissions(runner.ModeSafe, runner.ModeSafe, runner.ModeDisabled),
		func(runner.Mode) (runner.Runner, error) { return fakeRunner{name: "fake"}, nil },
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Resolve(nil); err == nil || !strings.Contains(err.Error(), "validator") {
		t.Fatalf("Resolve safe runner without input validator error = %v", err)
	}
}

func TestCatalogSelectionsFailClosed(t *testing.T) {
	catalog, err := runner.NewCatalog(
		runner.StaticRegistration(fakeRunner{name: "fake"}, runner.UnreviewedPermissions()),
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		selections []runner.Selection
	}{
		{name: "unknown", selections: []runner.Selection{{Name: "missing", Mode: runner.ModeAll}}},
		{
			name: "duplicate",
			selections: []runner.Selection{
				{Name: "fake", Mode: runner.ModeAll},
				{Name: "fake", Mode: runner.ModeDisabled},
			},
		},
		{name: "invalid mode", selections: []runner.Selection{{Name: "fake", Mode: "invalid"}}},
		{name: "unsupported safe", selections: []runner.Selection{{Name: "fake", Mode: runner.ModeSafe}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := catalog.Resolve(test.selections); err == nil {
				t.Fatal("Resolve accepted an invalid selection")
			}
		})
	}
}

func TestCatalogCanonicalSelectionsAreCompleteAndOrdered(t *testing.T) {
	catalog, err := runner.NewCatalog(
		runner.StaticRegistration(fakeRunner{name: "first"}, runner.UnreviewedPermissions()),
		runner.StaticRegistration(fakeRunner{name: "second"}, runner.UnreviewedPermissions()),
	)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := catalog.CanonicalSelections(
		[]runner.Selection{{Name: "second", Mode: runner.ModeDisabled}},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := validated.Selections()
	if err != nil {
		t.Fatal(err)
	}
	want := []runner.Selection{
		{Name: "first", Mode: runner.ModeAll},
		{Name: "second", Mode: runner.ModeDisabled},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("canonical selections = %#v, want %#v", got, want)
	}
	got[0].Mode = runner.ModeDisabled
	again, err := validated.Selections()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(again, want) {
		t.Fatalf("validated selections were mutated through returned copy: %#v", again)
	}
	wantArgs := []string{
		"--runner-mode", "first=all",
		"--runner-mode", "second=disabled",
	}
	args, err := validated.Args()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(args, wantArgs) {
		t.Fatalf("validated selection args = %#v, want %#v", args, wantArgs)
	}
}

func TestValidatedSelectionsZeroValueIsInvalid(t *testing.T) {
	var selections runner.ValidatedSelections
	if _, err := selections.Selections(); err == nil {
		t.Fatal("zero validated selections exposed raw selections")
	}
	if _, err := selections.Args(); err == nil {
		t.Fatal("zero validated selections exposed server args")
	}
}

func TestUnreviewedRegistrationDefaultsToAllAndCanBeDisabled(t *testing.T) {
	candidate := fakeRunner{name: "fake"}
	catalog, err := runner.NewCatalog(
		runner.StaticRegistration(candidate, runner.UnreviewedPermissions()),
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := catalog.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved, found := registry.Get("fake")
	if !found || resolved.Name() != candidate.Name() || len(registry.All()) != 1 {
		t.Fatalf("default registry = %#v, found %v", registry.All(), found)
	}
	registry, err = catalog.Resolve(
		[]runner.Selection{{Name: "fake", Mode: runner.ModeDisabled}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Get("fake"); found || len(registry.All()) != 0 {
		t.Fatalf("disabled registry still contains fake: %#v", registry.All())
	}
}

type fakeRunner struct {
	name string
}

func testReviewedPermissions(
	defaultMode runner.Mode,
	modes ...runner.Mode,
) runner.PermissionDeclaration {
	choices := make([]runner.PermissionChoice, 0, len(modes))
	for _, mode := range modes {
		choices = append(choices, testPermissionChoice(mode))
	}
	return runner.ReviewedPermissions(
		"Choose fake access.",
		"The fake runner was reviewed.",
		defaultMode,
		choices...,
	)
}

func testPermissionChoice(mode runner.Mode) runner.PermissionChoice {
	return runner.PermissionChoice{
		Mode:        mode,
		Label:       string(mode) + " label",
		Description: string(mode) + " description",
	}
}

type nilPanickingRunner struct {
	name string
}

func (r *nilPanickingRunner) Name() string { return r.name }

func (*nilPanickingRunner) Detect(string) (bool, error) { return true, nil }

func (*nilPanickingRunner) ListTasks(context.Context, string) ([]runner.Task, error) {
	return nil, nil
}

func (*nilPanickingRunner) BuildCommand(
	context.Context,
	string,
	runner.Task,
	[]string,
) (*exec.Cmd, error) {
	return nil, errors.New("nil runner command must not be built")
}

func (r fakeRunner) Name() string { return r.name }

func (fakeRunner) Detect(string) (bool, error) { return true, nil }

func (fakeRunner) ListTasks(context.Context, string) ([]runner.Task, error) { return nil, nil }

func (fakeRunner) BuildCommand(
	context.Context,
	string,
	runner.Task,
	[]string,
) (*exec.Cmd, error) {
	return nil, errors.New("fake runner command must not be built")
}
