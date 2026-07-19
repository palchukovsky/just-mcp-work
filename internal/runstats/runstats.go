// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package runstats derives short-lived duration summaries from the run ledger.
package runstats

import (
	"fmt"
	"sync"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

const (
	maxScannedRuns = 2000
	cacheTTL       = 5 * time.Second
	shellTaskID    = "shell:command"
)

// Aggregate summarizes terminal runs sharing one key.
//
//nolint:govet // Field order follows the stable MCP JSON response shape.
type Aggregate struct {
	Runs           int             `json:"runs"`
	MeasuredRuns   int             `json:"measured_runs"`
	LastDurationMS *int64          `json:"last_duration_ms,omitempty"`
	AvgDurationMS  *int64          `json:"avg_duration_ms,omitempty"`
	MinDurationMS  *int64          `json:"min_duration_ms,omitempty"`
	MaxDurationMS  *int64          `json:"max_duration_ms,omitempty"`
	LastStatus     runstore.Status `json:"last_status"`
	LastRunAt      time.Time       `json:"last_run_at"`
	AbortedRuns    int             `json:"aborted_runs"`
}

// Stats contains argument-specific and task-wide duration summaries.
type Stats struct {
	Exact *Aggregate `json:"exact,omitempty"`
	Task  *Aggregate `json:"task,omitempty"`
}

// Collector caches a bounded view of the on-disk run ledger.
//
//nolint:govet // The mutex deliberately protects the cache state below it.
type Collector struct {
	store *runstore.Store

	mu         sync.Mutex
	expires    time.Time
	runs       []runstore.Meta
	generation uint64
}

// New creates a collector backed by store.
func New(store *runstore.Store) *Collector { return &Collector{store: store} }

// For returns summaries matching projectPath, taskID, and args.
func (c *Collector) For(projectPath, taskID string, args []string) (*Stats, error) {
	runs, err := c.recent()
	if err != nil {
		return nil, err
	}
	exact := aggregate(runs, func(meta runstore.Meta) bool {
		return meta.ProjectPath == projectPath && meta.TaskID == taskID && sameArgs(meta.Args, args)
	})
	stats := &Stats{Exact: exact}
	if taskID != shellTaskID {
		stats.Task = aggregate(runs, func(meta runstore.Meta) bool {
			return meta.ProjectPath == projectPath && meta.TaskID == taskID
		})
	}
	if stats.Exact == nil && stats.Task == nil {
		//nolint:nilnil // A nil summary is the intentional JSON omission for cold history.
		return nil, nil
	}
	return stats, nil
}

// Task returns the argument-independent aggregate for a task, when meaningful.
func (c *Collector) Task(projectPath, taskID string) (*Aggregate, error) {
	if taskID == shellTaskID {
		//nolint:nilnil // Shell commands intentionally have no task-wide aggregate.
		return nil, nil
	}
	runs, err := c.recent()
	if err != nil {
		return nil, err
	}
	return aggregate(runs, func(meta runstore.Meta) bool {
		return meta.ProjectPath == projectPath && meta.TaskID == taskID
	}), nil
}

// Invalidate drops the cached ledger view so the next read rescans the store.
// The server calls it when a run finishes, so back-to-back runs do not report
// stale history while the time-based expiry is still pending.
func (c *Collector) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.expires = time.Time{}
}

func (c *Collector) recent() ([]runstore.Meta, error) {
	for {
		now := time.Now()
		c.mu.Lock()
		if now.Before(c.expires) {
			runs := append([]runstore.Meta(nil), c.runs...)
			c.mu.Unlock()
			return runs, nil
		}
		generation := c.generation
		c.mu.Unlock()

		runs, err := c.store.ListRecent(maxScannedRuns)
		if err != nil {
			return nil, fmt.Errorf("list recent runs: %w", err)
		}

		c.mu.Lock()
		if c.generation != generation {
			c.mu.Unlock()
			continue
		}
		c.runs = append(c.runs[:0], runs...)
		c.expires = time.Now().Add(cacheTTL)
		cached := append([]runstore.Meta(nil), c.runs...)
		c.mu.Unlock()
		return cached, nil
	}
}

// sameArgs compares argument vectors the way the ledger stores them: an omitted
// list and an empty list are the same invocation, so a caller that sends
// "arguments": [] still matches its own history.
func sameArgs(stored, wanted []string) bool {
	if len(stored) != len(wanted) {
		return false
	}
	for index := range stored {
		if stored[index] != wanted[index] {
			return false
		}
	}
	return true
}

func aggregate(runs []runstore.Meta, matches func(runstore.Meta) bool) *Aggregate {
	result := &Aggregate{}
	var (
		durationCount int
		durationSum   int64
	)
	for _, meta := range runs {
		if meta.Status == runstore.StatusRunning || !matches(meta) {
			continue
		}
		result.Runs++
		if result.Runs == 1 {
			result.LastStatus = meta.Status
			result.LastRunAt = meta.StartedAt
		}
		if meta.Status != runstore.StatusOK && meta.Status != runstore.StatusNonzero {
			result.AbortedRuns++
			continue
		}
		if durationCount == 0 {
			result.LastDurationMS = int64Pointer(meta.DurationMS)
			result.MinDurationMS = int64Pointer(meta.DurationMS)
			result.MaxDurationMS = int64Pointer(meta.DurationMS)
		} else {
			if meta.DurationMS < *result.MinDurationMS {
				result.MinDurationMS = int64Pointer(meta.DurationMS)
			}
			if meta.DurationMS > *result.MaxDurationMS {
				result.MaxDurationMS = int64Pointer(meta.DurationMS)
			}
		}
		durationCount++
		durationSum += meta.DurationMS
	}
	if result.Runs == 0 {
		return nil
	}
	if durationCount > 0 {
		average := durationSum / int64(durationCount)
		result.AvgDurationMS = int64Pointer(average)
	}
	result.MeasuredRuns = durationCount
	return result
}

func int64Pointer(value int64) *int64 { return &value }
