// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package updatecheck checks the public release tags without delaying MCP tools.
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
	"regexp"
	"sync"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/version"
)

const (
	defaultEndpoint   = "https://api.github.com/repos/palchukovsky/just-mcp-work/tags?per_page=100"
	defaultModulePath = "github.com/palchukovsky/just-mcp-work/cmd/just-mcp-work"
	defaultInterval   = 24 * time.Hour
	defaultTimeout    = 5 * time.Second
	releaseTagPattern = `^v[0-9]+\.[0-9]+\.[0-9]+$`
)

// HTTPClient is the small part of http.Client required for GitHub tag requests.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Config controls release lookups and makes the checker deterministic in tests.
type Config struct {
	Client         HTTPClient
	Now            func() time.Time
	Logger         *slog.Logger
	CurrentVersion version.Info
	StatePath      string
	Endpoint       string
	ModulePath     string
	Interval       time.Duration
	Timeout        time.Duration
}

// State is the durable version-check record stored in the service state directory.
//
//nolint:govet // Field order follows the stable on-disk schema.
type State struct {
	LastCheckStartedAt           time.Time `json:"last_check_started_at"`
	LastBackgroundCheckStartedAt time.Time `json:"last_background_check_started_at"`
	LastCheckCompletedAt         time.Time `json:"last_check_completed_at"`
	LatestStableVersion          string    `json:"latest_stable_version,omitempty"`
	LastCheckError               string    `json:"last_check_error,omitempty"`
	LastNotifiedVersion          string    `json:"last_notified_version,omitempty"`
	LastNotifiedAt               time.Time `json:"last_notified_at"`
}

// Status is the result returned by the synchronous version_status tool.
//
//nolint:govet // Field order follows the stable MCP response shape.
type Status struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	UpdateType      string `json:"update_type,omitempty"`
	UpdateCommand   string `json:"update_command,omitempty"`
	Message         string `json:"message"`
	CheckError      string `json:"check_error,omitempty"`
}

// Notification is a separate agent-facing message for a regular MCP tool response.
type Notification struct {
	CurrentVersion string
	LatestVersion  string
	UpdateCommand  string
}

// Message asks the agent to relay a version update to the user.
func (n Notification) Message() string {
	return "IMPORTANT FOR THE AGENT: A newer version of just-mcp-work is available: " +
		n.CurrentVersion + " → " + n.LatestVersion + ". Inform the user that an update is " +
		"available and provide this command:\n" + n.UpdateCommand
}

// Checker owns one workspace's durable update state.
type Checker struct {
	state      State
	client     HTTPClient
	background context.Context
	now        func() time.Time
	cancel     context.CancelFunc
	logger     *slog.Logger
	current    version.Info
	statePath  string
	endpoint   string
	modulePath string

	interval time.Duration
	timeout  time.Duration
	wg       sync.WaitGroup
	mu       sync.Mutex
	checking bool
}

// New creates a checker. State errors are logged and never prevent MCP startup.
func New(config Config) *Checker {
	if config.Endpoint == "" {
		config.Endpoint = defaultEndpoint
	}
	if config.ModulePath == "" {
		config.ModulePath = defaultModulePath
	}
	if config.Client == nil {
		config.Client = http.DefaultClient
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Interval <= 0 {
		config.Interval = defaultInterval
	}
	if config.Timeout <= 0 {
		config.Timeout = defaultTimeout
	}
	checker := &Checker{
		statePath:  config.StatePath,
		endpoint:   config.Endpoint,
		modulePath: config.ModulePath,
		current:    config.CurrentVersion,
		client:     config.Client,
		now:        config.Now,
		logger:     config.Logger,
		interval:   config.Interval,
		timeout:    config.Timeout,
	}
	checker.state = checker.loadState()
	return checker
}

// Start attaches background checks to the server lifecycle.
func (c *Checker) Start(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return
	}
	c.background, c.cancel = context.WithCancel(ctx)
}

// Close cancels and waits for a background request started by Observe.
func (c *Checker) Close() {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.background = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.wg.Wait()
}

// Observe starts at most one throttled asynchronous GitHub request.
func (c *Checker) Observe() {
	c.mu.Lock()
	if !c.current.Available() || c.background == nil || c.checking {
		c.mu.Unlock()
		return
	}
	now := c.now().UTC()
	if !c.state.LastBackgroundCheckStartedAt.IsZero() &&
		now.Sub(c.state.LastBackgroundCheckStartedAt) < c.interval {
		c.mu.Unlock()
		return
	}
	c.state.LastBackgroundCheckStartedAt = now
	c.state.LastCheckStartedAt = now
	c.checking = true
	c.persistStateLocked()
	ctx := c.background
	c.wg.Add(1)
	c.mu.Unlock()

	go c.checkInBackground(ctx)
}

// CheckNow performs an unthrottled GitHub request for version_status.
func (c *Checker) CheckNow(ctx context.Context) Status {
	c.mu.Lock()
	c.state.LastCheckStartedAt = c.now().UTC()
	c.persistStateLocked()
	c.mu.Unlock()

	latest, err := c.fetch(ctx)
	now := c.now().UTC()
	c.mu.Lock()
	c.recordCompletionLocked(now, latest, err)
	status := c.statusLocked()
	c.mu.Unlock()
	return status
}

// Notification returns one major or minor update notification when it is due.
func (c *Checker) Notification() *Notification {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.LastCheckError != "" {
		return nil
	}
	latest, ok := parseReleaseTag(c.state.LatestStableVersion)
	if !ok {
		return nil
	}
	updateType := updateType(c.current, latest)
	if updateType != "minor" && updateType != "major" {
		return nil
	}
	now := c.now().UTC()
	if !c.state.LastNotifiedAt.IsZero() && now.Sub(c.state.LastNotifiedAt) < c.interval {
		return nil
	}
	notification := &Notification{
		CurrentVersion: c.current.Tag,
		LatestVersion:  latest.Tag,
		UpdateCommand:  c.installCommand(latest.Tag),
	}
	c.state.LastNotifiedAt = now
	c.state.LastNotifiedVersion = latest.Tag
	c.persistStateLocked()
	return notification
}

func (c *Checker) checkInBackground(ctx context.Context) {
	defer c.wg.Done()
	latest, err := c.fetch(ctx)
	now := c.now().UTC()
	c.mu.Lock()
	c.checking = false
	c.recordCompletionLocked(now, latest, err)
	c.mu.Unlock()
}

func (c *Checker) recordCompletionLocked(now time.Time, latest version.Info, err error) {
	c.state.LastCheckCompletedAt = now
	c.state.LastCheckError = ""
	if err != nil {
		c.state.LastCheckError = err.Error()
		c.logger.Warn("GitHub version check failed", "error", err)
	} else {
		c.state.LatestStableVersion = latest.Tag
	}
	c.persistStateLocked()
}

func (c *Checker) statusLocked() Status {
	status := Status{CurrentVersion: c.current.Display()}
	if c.state.LastCheckError != "" {
		status.Message = "Unable to check GitHub for updates."
		status.CheckError = c.state.LastCheckError
		return status
	}
	latest, ok := parseReleaseTag(c.state.LatestStableVersion)
	if !ok {
		status.Message = "GitHub returned no stable release tags."
		return status
	}
	status.LatestVersion = latest.Tag
	if !c.current.Available() {
		status.Message = "The installed version is a development or unknown build; no update comparison was made."
		return status
	}
	status.UpdateType = updateType(c.current, latest)
	if status.UpdateType == "" {
		status.Message = "just-mcp-work is up to date."
		return status
	}
	status.UpdateAvailable = true
	status.UpdateCommand = c.installCommand(latest.Tag)
	status.Message = "A newer version of just-mcp-work is available."
	return status
}

func (c *Checker) fetch(ctx context.Context) (version.Info, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return version.Info{}, fmt.Errorf("create GitHub tag request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "just-mcp-work/version-check")
	response, err := c.client.Do(request)
	if err != nil {
		return version.Info{}, fmt.Errorf("request GitHub tags: %w", err)
	}
	if response == nil {
		return version.Info{}, errors.New("GitHub tag request returned no response")
	}
	defer func() {
		//nolint:errcheck // Read and decode failures remain the actionable errors.
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusForbidden && response.Header.Get("X-RateLimit-Remaining") == "0" {
			return version.Info{}, errors.New("GitHub API rate limit exceeded")
		}
		return version.Info{}, fmt.Errorf("GitHub tag request returned HTTP %d", response.StatusCode)
	}
	var tags []githubTag
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&tags); err != nil {
		return version.Info{}, fmt.Errorf("decode GitHub tags: %w", err)
	}
	var latest version.Info
	for _, tag := range tags {
		candidate, ok := parseReleaseTag(tag.Name)
		if !ok || latest.Available() && !candidate.Semantic.GreaterThan(latest.Semantic) {
			continue
		}
		latest = candidate
	}
	if !latest.Available() {
		return version.Info{}, errors.New("GitHub returned no stable release tags")
	}
	return latest, nil
}

type githubTag struct {
	Name string `json:"name"`
}

func parseReleaseTag(tag string) (version.Info, bool) {
	matches, err := regexp.MatchString(releaseTagPattern, tag)
	if err != nil || !matches {
		return version.Info{}, false
	}
	return version.ParseStable(tag)
}

func updateType(current, latest version.Info) string {
	if !current.Available() || !latest.Available() || !latest.Semantic.GreaterThan(current.Semantic) {
		return ""
	}
	if latest.Semantic.Major() != current.Semantic.Major() {
		return "major"
	}
	if latest.Semantic.Minor() != current.Semantic.Minor() {
		return "minor"
	}
	return "patch"
}

func (c *Checker) installCommand(tag string) string {
	return "go install " + c.modulePath + "@" + tag
}

func (c *Checker) loadState() State {
	if c.statePath == "" {
		return State{}
	}
	info, err := os.Lstat(c.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return State{}
	}
	if err != nil {
		c.logger.Warn("inspect version-check state failed", "error", err)
		return State{}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		c.logger.Warn("ignoring non-regular version-check state")
		return State{}
	}
	data, err := os.ReadFile(c.statePath)
	if err != nil {
		c.logger.Warn("read version-check state failed", "error", err)
		return State{}
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		c.logger.Warn("decode version-check state failed", "error", err)
		return State{}
	}
	return state
}

func (c *Checker) persistStateLocked() {
	if c.statePath == "" {
		return
	}
	data, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		c.logger.Warn("encode version-check state failed", "error", err)
		return
	}
	temporary, err := os.CreateTemp(filepath.Dir(c.statePath), ".version-*.json")
	if err != nil {
		c.logger.Warn("create temporary version-check state failed", "error", err)
		return
	}
	temporaryName := temporary.Name()
	defer func() {
		//nolint:errcheck // The temporary file is best-effort cleanup after a failed publish.
		_ = os.Remove(temporaryName)
	}()
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		//nolint:errcheck // The original write error remains actionable in diagnostics.
		_ = temporary.Close()
		c.logger.Warn("write version-check state failed", "error", err)
		return
	}
	if err := temporary.Sync(); err != nil {
		//nolint:errcheck // The sync error is retained for diagnostics.
		_ = temporary.Close()
		c.logger.Warn("sync version-check state failed", "error", err)
		return
	}
	if err := temporary.Close(); err != nil {
		c.logger.Warn("close version-check state failed", "error", err)
		return
	}
	if err := os.Rename(temporaryName, c.statePath); err != nil {
		c.logger.Warn("publish version-check state failed", "error", err)
	}
}
