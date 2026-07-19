// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package runstats

import (
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

//nolint:gocyclo // This test pins the aggregate rules in one readable scenario.
func TestCollectorExcludesAbortedDurationsAndOmitsShellTaskAggregate(t *testing.T) {
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finish := func(taskID string, args []string, status runstore.Status, duration time.Duration) {
		t.Helper()
		handle, beginErr := store.Begin(runstore.Meta{
			ProjectPath: "project",
			TaskID:      taskID,
			Args:        args,
		})
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		handle.Meta.StartedAt = time.Now().UTC().Add(-duration)
		if finishErr := handle.Finish(status, 0, "", false, false); finishErr != nil {
			t.Fatal(finishErr)
		}
	}
	finish("just:test", []string{"one"}, runstore.StatusOK, 10*time.Millisecond)
	finish("just:test", []string{"one"}, runstore.StatusTimeout, 20*time.Millisecond)
	finish("just:test", []string{"two"}, runstore.StatusNonzero, 30*time.Millisecond)
	finish("shell:command", []string{"echo one"}, runstore.StatusOK, 5*time.Millisecond)

	collector := New(store)
	stats, err := collector.For("project", "just:test", []string{"one"})
	if err != nil || stats == nil || stats.Exact == nil || stats.Task == nil {
		t.Fatalf("task stats = %#v, %v", stats, err)
	}
	if stats.Exact.Runs != 2 ||
		stats.Exact.MeasuredRuns != 1 ||
		stats.Exact.AbortedRuns != 1 ||
		stats.Exact.AvgDurationMS == nil ||
		*stats.Exact.AvgDurationMS <= 0 {
		t.Fatalf("exact stats = %#v", stats.Exact)
	}
	if stats.Task.Runs != 3 ||
		stats.Task.MeasuredRuns != 2 ||
		stats.Task.AbortedRuns != 1 ||
		stats.Task.MaxDurationMS == nil ||
		stats.Task.MinDurationMS == nil ||
		*stats.Task.MaxDurationMS < *stats.Task.MinDurationMS {
		t.Fatalf("task aggregate = %#v", stats.Task)
	}
	shell, err := collector.For("project", "shell:command", []string{"echo one"})
	if err != nil || shell == nil || shell.Exact == nil || shell.Task != nil {
		t.Fatalf("shell stats = %#v, %v", shell, err)
	}
}

func TestAggregateOmitsDurationsWhenOnlyAbortedRunsMatch(t *testing.T) {
	runs := []runstore.Meta{
		{Status: runstore.StatusTimeout, DurationMS: 10},
		{Status: runstore.StatusCancelled, DurationMS: 20},
	}
	aggregate := aggregate(runs, func(runstore.Meta) bool { return true })
	if aggregate == nil ||
		aggregate.Runs != 2 ||
		aggregate.MeasuredRuns != 0 ||
		aggregate.AbortedRuns != 2 ||
		aggregate.LastDurationMS != nil ||
		aggregate.AvgDurationMS != nil ||
		aggregate.MinDurationMS != nil ||
		aggregate.MaxDurationMS != nil {
		t.Fatalf("aggregate = %#v", aggregate)
	}
}

func TestCollectorTreatsOmittedAndEmptyArgumentsAsOneKey(t *testing.T) {
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Begin(runstore.Meta{ProjectPath: "project", TaskID: "just:probe"})
	if err != nil {
		t.Fatal(err)
	}
	if finishErr := handle.Finish(runstore.StatusOK, 0, "", false, false); finishErr != nil {
		t.Fatal(finishErr)
	}
	collector := New(store)
	for _, args := range [][]string{nil, {}} {
		stats, statsErr := collector.For("project", "just:probe", args)
		if statsErr != nil || stats == nil || stats.Exact == nil || stats.Exact.Runs != 1 {
			t.Fatalf("stats for %#v = %#v, %v, want one exact run", args, stats, statsErr)
		}
	}
	stats, err := collector.For("project", "just:probe", []string{"other"})
	if err != nil || stats == nil || stats.Exact != nil {
		t.Fatalf("stats for other arguments = %#v, %v, want no exact aggregate", stats, err)
	}
}
