// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package runmanager owns live asynchronous executor runs for one server process.
package runmanager

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/palchukovsky/just-mcp-work/internal/executor"
)

// MaxConcurrentRuns bounds live process ownership for one server. It covers
// synchronous runs too, because a synchronous run can still be promoted.
const MaxConcurrentRuns = 32

// Manager tracks live runs by their durable run IDs.
//
//nolint:govet // The mutex deliberately protects the adjacent run map.
type Manager struct {
	onFinish func()
	maxRuns  int

	mu       sync.Mutex
	runs     map[string]*executor.Run
	reserved map[string]struct{}
	terminal map[string]*executor.Run
	repairMu sync.Mutex
}

// New creates an empty run manager. onFinish, when set, is called once per run
// after it becomes terminal, so derived ledger views can drop stale caches.
// A non-positive maxRuns selects MaxConcurrentRuns.
func New(onFinish func(), maxRuns int) *Manager {
	if maxRuns <= 0 {
		maxRuns = MaxConcurrentRuns
	}
	return &Manager{
		onFinish: onFinish,
		maxRuns:  maxRuns,
		runs:     make(map[string]*executor.Run),
		reserved: make(map[string]struct{}),
		terminal: make(map[string]*executor.Run),
	}
}

// Reserve claims a concurrency slot before an operating-system process starts.
func (m *Manager) Reserve(runID string) error {
	if runID == "" {
		return fmt.Errorf("run ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.runs[runID]; exists {
		return fmt.Errorf("run %q is already managed", runID)
	}
	if _, exists := m.reserved[runID]; exists {
		return fmt.Errorf("run %q is already reserved", runID)
	}
	if len(m.runs)+len(m.reserved) >= m.maxRuns {
		return fmt.Errorf("too many concurrent runs: limit is %d", m.maxRuns)
	}
	m.reserved[runID] = struct{}{}
	return nil
}

// Release gives up a reservation when a process could not be started.
func (m *Manager) Release(runID string) {
	m.mu.Lock()
	delete(m.reserved, runID)
	m.mu.Unlock()
}

// Start registers a started run and promotes a prior reservation when present.
// It releases the concurrency slot automatically once terminal.
func (m *Manager) Start(run *executor.Run) error {
	if run == nil {
		return fmt.Errorf("run is required")
	}
	runID := run.Snapshot().RunID
	if runID == "" {
		return fmt.Errorf("run ID is required")
	}
	select {
	case <-run.Done():
		m.mu.Lock()
		delete(m.reserved, runID)
		if run.NeedsMetadataRepair() {
			m.terminal[runID] = run
		}
		m.mu.Unlock()
		return nil
	default:
	}
	m.mu.Lock()
	if _, reserved := m.reserved[runID]; reserved {
		delete(m.reserved, runID)
	} else {
		if _, exists := m.runs[runID]; exists {
			m.mu.Unlock()
			return fmt.Errorf("run %q is already managed", runID)
		}
		if len(m.runs)+len(m.reserved) >= m.maxRuns {
			m.mu.Unlock()
			return fmt.Errorf("too many concurrent runs: limit is %d", m.maxRuns)
		}
	}
	if _, exists := m.runs[runID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("run %q is already managed", runID)
	}
	m.runs[runID] = run
	m.mu.Unlock()
	go func() {
		<-run.Done()
		m.releaseFinishedRun(runID, run)
	}()
	return nil
}

// releaseFinishedRun removes one completed run and reports completion exactly
// once, regardless of whether the executor watcher or Shutdown reaches it first.
func (m *Manager) releaseFinishedRun(runID string, run *executor.Run) {
	needsMetadataRepair := run.NeedsMetadataRepair()

	m.mu.Lock()
	current, tracked := m.runs[runID]
	if !tracked || current != run {
		m.mu.Unlock()
		return
	}
	delete(m.runs, runID)
	if needsMetadataRepair {
		m.terminal[runID] = run
	}
	m.mu.Unlock()

	if m.onFinish != nil {
		m.onFinish()
	}
}

// Get returns a locally owned live run.
func (m *Manager) Get(runID string) (*executor.Run, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	return run, ok
}

// Terminal returns a locally-owned terminal run whose metadata needs repair.
// Terminal fallbacks do not count against the live-run concurrency limit.
func (m *Manager) Terminal(runID string) (*executor.Run, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.terminal[runID]
	return run, ok
}

// ReleaseTerminal forgets a repaired fallback when it still identifies the
// same run. It is safe to call after a concurrent observer repaired it first.
func (m *Manager) ReleaseTerminal(runID string, run *executor.Run) {
	m.mu.Lock()
	if current, ok := m.terminal[runID]; ok && current == run {
		delete(m.terminal, runID)
	}
	m.mu.Unlock()
}

// RepairTerminals retries every retained final metadata write and releases the
// entries that were repaired. Failed entries remain available for later retry.
func (m *Manager) RepairTerminals() (int, error) {
	// Several unrelated RPCs can request best-effort repair at once. Only one
	// pass should hit degraded storage; concurrent callers can retry later.
	if !m.repairMu.TryLock() {
		return 0, nil
	}
	defer m.repairMu.Unlock()

	m.mu.Lock()
	runs := make(map[string]*executor.Run, min(len(m.terminal), m.maxRuns))
	for runID, run := range m.terminal {
		runs[runID] = run
		if len(runs) == m.maxRuns {
			break
		}
	}
	m.mu.Unlock()

	repaired := 0
	var repairErr error
	for runID, run := range runs {
		if err := run.RepairFinalMetadata(); err != nil {
			repairErr = errors.Join(
				repairErr,
				fmt.Errorf("repair terminal run %q: %w", runID, err),
			)
			continue
		}
		m.ReleaseTerminal(runID, run)
		repaired++
	}
	return repaired, repairErr
}

// Shutdown terminates all locally owned runs or returns when ctx expires.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	runs := make(map[string]*executor.Run, len(m.runs))
	maps.Copy(runs, m.runs)
	m.mu.Unlock()
	if len(runs) == 0 {
		return
	}
	done := make(chan struct{})
	go func() {
		var wait sync.WaitGroup
		wait.Add(len(runs))
		for runID, run := range runs {
			go func(runID string, run *executor.Run) {
				defer wait.Done()
				//nolint:errcheck // Shutdown is best-effort; the ledger error is already recorded.
				_ = run.StopWithReason("server shutdown")
				m.releaseFinishedRun(runID, run)
			}(runID, run)
		}
		wait.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
