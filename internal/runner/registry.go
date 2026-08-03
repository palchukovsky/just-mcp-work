// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runner

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Mode is one explicitly reviewed permission level for a runner.
type Mode string

const (
	ModeSafe     Mode = "safe"
	ModeAll      Mode = "all"
	ModeDisabled Mode = "disabled"
)

// PermissionDeclaration records the reviewed modes and default of one runner.
// Its zero value is intentionally invalid so a registration cannot silently
// omit its permission review.
type PermissionDeclaration struct {
	defaultMode Mode
	question    string
	context     string
	choices     []PermissionChoice
	reviewed    bool
	declared    bool
}

// PermissionChoice describes one mode shown to the operator during init.
// Warning is optional and is printed before the operator chooses a mode.
type PermissionChoice struct {
	Mode        Mode
	Label       string
	Description string
	Warning     string
}

// PermissionRequest is a copied, read-only view of one runner's init prompt.
// Mutating Choices cannot affect the catalog or later requests.
type PermissionRequest struct {
	Name     string
	Question string
	Context  string
	Default  Mode
	Choices  []PermissionChoice
	Reviewed bool
}

// ReviewedPermissions declares a reviewed runner prompt and its mode choices.
func ReviewedPermissions(
	question string,
	context string,
	defaultMode Mode,
	choices ...PermissionChoice,
) PermissionDeclaration {
	return PermissionDeclaration{
		defaultMode: defaultMode,
		question:    question,
		context:     context,
		choices:     slices.Clone(choices),
		reviewed:    true,
		declared:    true,
	}
}

// UnreviewedPermissions explicitly preserves an unrestricted runner until its
// commands receive a narrower review. Such a runner supports only all and
// disabled, and remains enabled by default for compatibility.
func UnreviewedPermissions() PermissionDeclaration {
	return PermissionDeclaration{
		defaultMode: ModeAll,
		question:    "Choose command access for this runner.",
		context: "This runner has not been reviewed into a narrower safe command set; " +
			"its current command surface is unrestricted.",
		choices: []PermissionChoice{
			{
				Mode:        ModeAll,
				Label:       "Current access",
				Description: "Keep the runner's current unrestricted command surface.",
			},
			{
				Mode:        ModeDisabled,
				Label:       "Disabled",
				Description: "Do not expose this runner or any of its tasks.",
			},
		},
		declared: true,
	}
}

// RunnerFactory constructs a runner already constrained to mode.
//
//nolint:revive // The explicit name distinguishes runner factories at registration boundaries.
type RunnerFactory func(mode Mode) (Runner, error)

// Registration is the only value a Catalog accepts. Its fields are private so
// every value must pass through NewRegistration with a permission declaration.
type Registration struct {
	factory     RunnerFactory
	name        string
	permissions PermissionDeclaration
}

// NewRegistration declares a named runner, its permissions, and its
// mode-aware factory. Validation is deliberately deferred to NewCatalog so the
// full registration set fails as one boundary.
func NewRegistration(
	name string,
	permissions PermissionDeclaration,
	factory RunnerFactory,
) Registration {
	return Registration{name: name, permissions: permissions, factory: factory}
}

// StaticRegistration adapts a runner whose commands have not yet been split
// by mode. The caller must still provide an explicit permission declaration.
func StaticRegistration(candidate Runner, permissions PermissionDeclaration) Registration {
	name := ""
	if !isNilRunner(candidate) {
		name = candidate.Name()
	}
	return NewRegistration(
		name,
		permissions,
		func(Mode) (Runner, error) { return candidate, nil },
	)
}

// Selection overrides one registration's declared default mode.
type Selection struct {
	Name string
	Mode Mode
}

// ValidatedSelections is a complete, catalog-ordered runner selection set.
// Its zero value is invalid; only Catalog.CanonicalSelections can create a
// value accepted by persistence boundaries.
type ValidatedSelections struct {
	selections []Selection
	valid      bool
}

// Selections returns an independent copy of the validated selections.
func (s ValidatedSelections) Selections() ([]Selection, error) {
	if !s.valid {
		return nil, fmt.Errorf("runner selections were not validated by a catalog")
	}
	return slices.Clone(s.selections), nil
}

// Args returns repeatable --runner-mode arguments in canonical catalog order.
func (s ValidatedSelections) Args() ([]string, error) {
	selections, err := s.Selections()
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, len(selections)*2)
	for _, selection := range selections {
		args = append(args, "--runner-mode", selection.Name+"="+string(selection.Mode))
	}
	return args, nil
}

// Catalog is a validated, immutable set of runner registrations.
type Catalog struct {
	byName        map[string]Registration
	registrations []Registration
}

// NewCatalog validates registration structure without constructing runners.
// The selected non-disabled factory is called and validated lazily by Resolve.
func NewCatalog(registrations ...Registration) (*Catalog, error) {
	catalog := &Catalog{
		registrations: slices.Clone(registrations),
		byName:        make(map[string]Registration, len(registrations)),
	}
	for _, registration := range registrations {
		if err := validateRegistration(registration); err != nil {
			return nil, err
		}
		if _, exists := catalog.byName[registration.name]; exists {
			return nil, fmt.Errorf("duplicate runner registration %q", registration.name)
		}
		catalog.byName[registration.name] = registration
	}
	return catalog, nil
}

// NewRegistry resolves declared defaults through the same validated catalog
// boundary. It accepts registrations, never raw runners.
func NewRegistry(registrations ...Registration) (*Registry, error) {
	catalog, err := NewCatalog(registrations...)
	if err != nil {
		return nil, err
	}
	return catalog.Resolve(nil)
}

func validateRegistration(registration Registration) error {
	if err := validateRegistrationName(registration.name); err != nil {
		return err
	}
	if err := validatePermissionDeclaration(registration.name, registration.permissions); err != nil {
		return err
	}
	if registration.factory == nil {
		return fmt.Errorf("runner %q has no factory", registration.name)
	}
	return nil
}

func validateRegistrationName(name string) error {
	if name == "" {
		return fmt.Errorf("runner registration name must not be empty")
	}
	if strings.ContainsAny(name, "=:") {
		return fmt.Errorf(
			"runner registration name %q must not contain '=' or ':'",
			name,
		)
	}
	return nil
}

func validatePermissionDeclaration(name string, permissions PermissionDeclaration) error {
	if !permissions.declared {
		return fmt.Errorf("runner %q has no permission declaration", name)
	}
	if permissions.question == "" {
		return fmt.Errorf("runner %q declares no permission question", name)
	}
	if permissions.context == "" {
		return fmt.Errorf("runner %q declares no permission context", name)
	}
	if len(permissions.choices) == 0 {
		return fmt.Errorf("runner %q declares no permission choices", name)
	}
	seen, err := validatePermissionChoices(name, permissions.choices)
	if err != nil {
		return err
	}
	if _, exists := seen[ModeDisabled]; !exists {
		return fmt.Errorf("runner %q must declare disabled mode", name)
	}
	if !validMode(permissions.defaultMode) {
		return fmt.Errorf(
			"runner %q declares invalid default mode %q",
			name,
			permissions.defaultMode,
		)
	}
	if _, exists := seen[permissions.defaultMode]; !exists {
		return fmt.Errorf(
			"runner %q default mode %q is not declared",
			name,
			permissions.defaultMode,
		)
	}
	return nil
}

func validatePermissionChoices(
	name string,
	choices []PermissionChoice,
) (map[Mode]struct{}, error) {
	seen := make(map[Mode]struct{}, len(choices))
	for _, choice := range choices {
		if !validMode(choice.Mode) {
			return nil, fmt.Errorf("runner %q declares invalid mode %q", name, choice.Mode)
		}
		if _, exists := seen[choice.Mode]; exists {
			return nil, fmt.Errorf("runner %q declares duplicate mode %q", name, choice.Mode)
		}
		if choice.Label == "" {
			return nil, fmt.Errorf(
				"runner %q mode %q declares no permission label",
				name,
				choice.Mode,
			)
		}
		if choice.Description == "" {
			return nil, fmt.Errorf(
				"runner %q mode %q declares no permission description",
				name,
				choice.Mode,
			)
		}
		seen[choice.Mode] = struct{}{}
	}
	return seen, nil
}

func validMode(mode Mode) bool {
	switch mode {
	case ModeSafe, ModeAll, ModeDisabled:
		return true
	default:
		return false
	}
}

func buildRegisteredRunner(registration Registration, mode Mode) (Runner, error) {
	candidate, err := registration.factory(mode)
	if err != nil {
		return nil, fmt.Errorf(
			"construct runner %q in mode %q: %w",
			registration.name,
			mode,
			err,
		)
	}
	if isNilRunner(candidate) {
		return nil, fmt.Errorf("runner %q factory returned nil in mode %q", registration.name, mode)
	}
	if candidate.Name() != registration.name {
		return nil, fmt.Errorf(
			"runner registration %q constructed runner named %q",
			registration.name,
			candidate.Name(),
		)
	}
	if mode == ModeSafe {
		if _, validatesInput := candidate.(TaskInputValidator); !validatesInput {
			return nil, fmt.Errorf(
				"runner %q safe mode has no side-effect-free task input validator",
				registration.name,
			)
		}
	}
	return candidate, nil
}

func isNilRunner(candidate Runner) bool {
	if candidate == nil {
		return true
	}
	value := reflect.ValueOf(candidate)
	kind := value.Kind()
	nilable := kind == reflect.Chan ||
		kind == reflect.Func ||
		kind == reflect.Interface ||
		kind == reflect.Map ||
		kind == reflect.Pointer ||
		kind == reflect.Slice ||
		kind == reflect.UnsafePointer
	return nilable && value.IsNil()
}

// Names returns registration names in their declared order.
func (c *Catalog) Names() []string {
	names := make([]string, 0, len(c.registrations))
	for _, registration := range c.registrations {
		names = append(names, registration.name)
	}
	return names
}

// PermissionRequests returns validated init prompts in registration order.
// Both the result and every Choices slice are independent copies.
func (c *Catalog) PermissionRequests() []PermissionRequest {
	requests := make([]PermissionRequest, 0, len(c.registrations))
	for _, registration := range c.registrations {
		permissions := registration.permissions
		requests = append(requests, PermissionRequest{
			Name:     registration.name,
			Question: permissions.question,
			Context:  permissions.context,
			Reviewed: permissions.reviewed,
			Default:  permissions.defaultMode,
			Choices:  slices.Clone(permissions.choices),
		})
	}
	return requests
}

// CanonicalSelections applies explicit overrides to declared defaults and
// returns one selection per registration in catalog order.
func (c *Catalog) CanonicalSelections(overrides []Selection) (ValidatedSelections, error) {
	selected := make(map[string]Mode, len(c.registrations))
	for _, registration := range c.registrations {
		selected[registration.name] = registration.permissions.defaultMode
	}
	overridden := make(map[string]struct{}, len(overrides))
	for _, selection := range overrides {
		registration, exists := c.byName[selection.Name]
		if !exists {
			return ValidatedSelections{}, fmt.Errorf("unknown runner selection %q", selection.Name)
		}
		if _, duplicate := overridden[selection.Name]; duplicate {
			return ValidatedSelections{}, fmt.Errorf("duplicate runner selection %q", selection.Name)
		}
		if !registration.permissions.allows(selection.Mode) {
			return ValidatedSelections{}, fmt.Errorf(
				"runner %q does not support mode %q",
				selection.Name,
				selection.Mode,
			)
		}
		overridden[selection.Name] = struct{}{}
		selected[selection.Name] = selection.Mode
	}
	selections := make([]Selection, 0, len(c.registrations))
	for _, registration := range c.registrations {
		selections = append(selections, Selection{
			Name: registration.name,
			Mode: selected[registration.name],
		})
	}
	return ValidatedSelections{selections: selections, valid: true}, nil
}

// Resolve applies explicit selections over declared defaults and creates the
// runtime registry. Unknown names, duplicate selections, and unsupported modes
// fail closed. Disabled registrations are not constructed or registered.
func (c *Catalog) Resolve(selections []Selection) (*Registry, error) {
	canonical, err := c.CanonicalSelections(selections)
	if err != nil {
		return nil, err
	}
	validated, err := canonical.Selections()
	if err != nil {
		return nil, err
	}
	selected := make(map[string]Mode, len(validated))
	for _, selection := range validated {
		selected[selection.Name] = selection.Mode
	}

	registry := &Registry{runners: make(map[string]Runner, len(c.registrations))}
	for _, registration := range c.registrations {
		mode := selected[registration.name]
		if mode == ModeDisabled {
			continue
		}
		candidate, err := buildRegisteredRunner(registration, mode)
		if err != nil {
			return nil, err
		}
		registry.runners[registration.name] = candidate
		registry.ordered = append(registry.ordered, candidate)
	}
	return registry, nil
}

func (p PermissionDeclaration) allows(wanted Mode) bool {
	return slices.ContainsFunc(p.choices, func(choice PermissionChoice) bool {
		return choice.Mode == wanted
	})
}

// Registry contains enabled runners keyed by their stable name. It can only be
// produced by resolving a validated Catalog.
type Registry struct {
	runners map[string]Runner
	ordered []Runner
}

// Get returns a runner by name.
func (r *Registry) Get(name string) (Runner, bool) {
	candidate, ok := r.runners[name]
	return candidate, ok
}

// All returns runners in registration order.
func (r *Registry) All() []Runner {
	return slices.Clone(r.ordered)
}
