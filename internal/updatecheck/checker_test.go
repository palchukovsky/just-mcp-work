// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/version"
)

func TestNotificationOnlyReportsMinorAndMajorUpdates(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		latest     string
		wantNotice bool
	}{
		{name: "patch", current: "v0.1.0", latest: "v0.1.1"},
		{name: "minor", current: "v0.1.7", latest: "v0.2.0", wantNotice: true},
		{name: "major", current: "v0.2.3", latest: "v1.0.0", wantNotice: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checker, _ := newTestChecker(t, test.current, [][]string{{test.latest}})
			checker.CheckNow(context.Background())
			got := checker.Notification()
			if (got != nil) != test.wantNotice {
				t.Fatalf("Notification() = %#v, want notice %t", got, test.wantNotice)
			}
			if got != nil && !strings.Contains(got.Message(), "@"+test.latest) {
				t.Fatalf("notification command does not use the exact tag: %s", got.Message())
			}
		})
	}
}

func TestNotificationThrottleSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version.json")
	clock := newTestClock()
	client := &fakeHTTPClient{responses: [][]string{{"v0.2.0"}}}
	checker := newChecker(path, "v0.1.0", client, clock)
	checker.CheckNow(context.Background())
	if checker.Notification() == nil {
		t.Fatal("first notification is missing")
	}

	restarted := newChecker(path, "v0.1.0", client, clock)
	if got := restarted.Notification(); got != nil {
		t.Fatalf("notification after restart = %#v, want nil", got)
	}
	clock.Advance(24 * time.Hour)
	if got := restarted.Notification(); got == nil {
		t.Fatal("notification is still throttled after one day")
	}
}

func TestObserveStartsOneAsynchronousCheckAndPublishesNextResponse(t *testing.T) {
	client := &fakeHTTPClient{
		responses: [][]string{{"v0.2.0"}},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	checker, _ := newTestCheckerWithClient(t, "v0.1.0", client)
	checker.Start(t.Context())
	defer checker.Close()

	started := time.Now()
	checker.Observe()
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("Observe blocked for %s", elapsed)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("background GitHub check did not start")
	}
	if got := checker.Notification(); got != nil {
		t.Fatalf("notification before completed check = %#v", got)
	}
	close(client.release)
	waitFor(t, func() bool { return checker.Notification() != nil })
}

func TestParallelObserveStartsOnlyOneBackgroundRequest(t *testing.T) {
	client := &fakeHTTPClient{
		responses: [][]string{{"v0.2.0"}},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	checker, _ := newTestCheckerWithClient(t, "v0.1.0", client)
	checker.Start(context.Background())
	defer checker.Close()

	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			checker.Observe()
		})
	}
	group.Wait()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("background GitHub check did not start")
	}
	if calls := client.Calls(); calls != 1 {
		t.Fatalf("GitHub calls = %d, want 1", calls)
	}
	close(client.release)
}

func TestStatusChecksEveryCallAndDoesNotConsumeBackgroundNotification(t *testing.T) {
	checker, client := newTestChecker(
		t,
		"v0.1.0",
		[][]string{{"v0.1.1"}, {"v0.2.0"}},
	)
	first := checker.CheckNow(context.Background())
	if !first.UpdateAvailable || first.UpdateType != "patch" {
		t.Fatalf("first version status = %#v", first)
	}
	second := checker.CheckNow(context.Background())
	if !second.UpdateAvailable || second.UpdateType != "minor" {
		t.Fatalf("second version status = %#v", second)
	}
	if calls := client.Calls(); calls != 2 {
		t.Fatalf("GitHub calls = %d, want 2", calls)
	}
	if checker.state.LastNotifiedAt != (time.Time{}) {
		t.Fatalf("version_status marked a notification as shown: %#v", checker.state)
	}
	if got := checker.Notification(); got == nil {
		t.Fatal("next normal response did not receive the pending minor notification")
	}
}

func TestTagSelectionRejectsPrereleaseAndInvalidTags(t *testing.T) {
	checker, _ := newTestChecker(
		t,
		"v0.9.9",
		[][]string{{
			"v0.9.9",
			"v0.10.0",
			"v1.0.0",
			"v1.1.0-rc.1",
			"v1.2",
			"1.3.0",
			"release-v2.0.0",
		}},
	)
	status := checker.CheckNow(context.Background())
	if status.LatestVersion != "v1.0.0" || status.UpdateType != "major" {
		t.Fatalf("version status = %#v", status)
	}
}

func TestDevelopmentBuildNeverCreatesFalseNotification(t *testing.T) {
	checker, _ := newTestChecker(t, "dev", [][]string{{"v1.0.0"}})
	status := checker.CheckNow(context.Background())
	if status.UpdateAvailable || status.LatestVersion != "v1.0.0" {
		t.Fatalf("development version status = %#v", status)
	}
	if got := checker.Notification(); got != nil {
		t.Fatalf("development build notification = %#v", got)
	}
}

func TestGitHubFailureAndDamagedStateStayContained(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version.json")
	if err := os.WriteFile(path, []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeHTTPClient{err: errors.New("offline")}
	checker := newChecker(path, "v0.1.0", client, newTestClock())
	status := checker.CheckNow(context.Background())
	if status.CheckError == "" || !strings.Contains(status.Message, "Unable") {
		t.Fatalf("GitHub failure status = %#v", status)
	}
	if err := json.Unmarshal(mustReadFile(t, path), &State{}); err != nil {
		t.Fatalf("state file is not recoverable JSON: %v", err)
	}
}

func newTestChecker(
	t *testing.T,
	current string,
	responses [][]string,
) (*Checker, *fakeHTTPClient) {
	t.Helper()
	client := &fakeHTTPClient{responses: responses}
	checker, _ := newTestCheckerWithClient(t, current, client)
	return checker, client
}

func newTestCheckerWithClient(
	t *testing.T,
	current string,
	client *fakeHTTPClient,
) (*Checker, *testClock) {
	t.Helper()
	clock := newTestClock()
	return newChecker(filepath.Join(t.TempDir(), "version.json"), current, client, clock), clock
}

func newChecker(
	path string,
	current string,
	client *fakeHTTPClient,
	clock *testClock,
) *Checker {
	parsed := version.Detect(current, "(devel)")
	return New(Config{
		StatePath:      path,
		Endpoint:       "https://example.invalid/tags",
		CurrentVersion: parsed,
		Client:         client,
		Now:            clock.Now,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

type testClock struct {
	current time.Time
	mu      sync.Mutex
}

func newTestClock() *testClock {
	return &testClock{current: time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *testClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(duration)
}

type fakeHTTPClient struct {
	err       error
	started   chan struct{}
	release   chan struct{}
	responses [][]string
	startOnce sync.Once
	mu        sync.Mutex
	calls     int
}

func (c *fakeHTTPClient) Do(request *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.calls++
	responseIndex := c.calls - 1
	if responseIndex >= len(c.responses) {
		responseIndex = len(c.responses) - 1
	}
	var tags []string
	if responseIndex >= 0 {
		tags = append([]string(nil), c.responses[responseIndex]...)
	}
	err := c.err
	started := c.started
	release := c.release
	c.mu.Unlock()
	if started != nil {
		c.startOnce.Do(func() { close(started) })
	}
	if release != nil {
		select {
		case <-release:
		case <-request.Context().Done():
			return nil, fmt.Errorf("wait for fake response: %w", request.Context().Err())
		}
	}
	if err != nil {
		return nil, err
	}
	payload, marshalErr := json.Marshal(tagNames(tags))
	if marshalErr != nil {
		return nil, fmt.Errorf("encode fake tags: %w", marshalErr)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(payload))),
	}, nil
}

func (c *fakeHTTPClient) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func tagNames(names []string) []githubTag {
	tags := make([]githubTag, 0, len(names))
	for _, name := range names {
		tags = append(tags, githubTag{Name: name})
	}
	return tags
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before the deadline")
}
