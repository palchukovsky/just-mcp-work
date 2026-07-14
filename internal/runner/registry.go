// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runner

import "fmt"

// Registry contains enabled runners keyed by their stable name.
type Registry struct {
	runners map[string]Runner
	ordered []Runner
}

// NewRegistry builds a registry and rejects duplicate runner names.
func NewRegistry(runners ...Runner) (*Registry, error) {
	r := &Registry{runners: make(map[string]Runner, len(runners))}
	for _, candidate := range runners {
		if candidate == nil {
			return nil, fmt.Errorf("runner must not be nil")
		}
		name := candidate.Name()
		if name == "" {
			return nil, fmt.Errorf("runner name must not be empty")
		}
		if _, exists := r.runners[name]; exists {
			return nil, fmt.Errorf("duplicate runner %q", name)
		}
		r.runners[name] = candidate
		r.ordered = append(r.ordered, candidate)
	}
	return r, nil
}

// Get returns a runner by name.
func (r *Registry) Get(name string) (Runner, bool) {
	runner, ok := r.runners[name]
	return runner, ok
}

// All returns runners in registration order.
func (r *Registry) All() []Runner {
	return append([]Runner(nil), r.ordered...)
}
