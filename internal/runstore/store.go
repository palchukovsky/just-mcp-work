// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package runstore persists compact run metadata and append-only process logs.
package runstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	stateDirName = ".just-mcp-work"
	logDirName   = "log"
)

// Status describes the terminal state of a task process.
type Status string

const (
	StatusRunning    Status = "running"
	StatusOK         Status = "ok"
	StatusNonzero    Status = "nonzero"
	StatusTimeout    Status = "timeout"
	StatusCancelled  Status = "cancelled"
	StatusSpawnError Status = "spawn_error"
)

// ErrFinalMetadataPersistence marks a terminal metadata write failure. Other
// finalization errors, such as closing a log, do not require ledger repair.
var ErrFinalMetadataPersistence = errors.New("final run metadata persistence failed")

// Meta is persisted as <run>/meta.json.
//
//nolint:govet // Field order follows the stable on-disk metadata schema.
type Meta struct {
	RunID           string    `json:"run_id"`
	ProjectPath     string    `json:"project_path,omitempty"`
	Runner          string    `json:"runner,omitempty"`
	TaskID          string    `json:"task_id,omitempty"`
	Args            []string  `json:"args,omitempty"`
	CWD             string    `json:"cwd,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at"`
	DurationMS      int64     `json:"duration_ms,omitempty"`
	TaskTimeoutMS   *int64    `json:"task_timeout_ms,omitempty"`
	ExitCode        int       `json:"exit_code"`
	Status          Status    `json:"status"`
	RunnerVersion   string    `json:"runner_version,omitempty"`
	OwnerPID        int       `json:"owner_pid,omitempty"`
	OwnerIdentity   string    `json:"owner_identity,omitempty"`
	PID             int       `json:"pid,omitempty"`
	ProcessIdentity string    `json:"process_identity,omitempty"`
	StdoutBytes     int64     `json:"stdout_bytes"`
	StderrBytes     int64     `json:"stderr_bytes"`
	StdoutTruncated bool      `json:"stdout_truncated,omitempty"`
	StderrTruncated bool      `json:"stderr_truncated,omitempty"`
	Error           string    `json:"error,omitempty"`
}

// LogState is a filesystem-derived snapshot of a run's two log files.
//
//nolint:govet // Field order follows the status response shape.
type LogState struct {
	StdoutBytes  int64
	StderrBytes  int64
	LastOutputAt time.Time
	NoOutputYet  bool
}

// Store owns one workspace's run ledger.
//
//nolint:govet // Field order keeps synchronization state next to the active-run map.
type Store struct {
	root      string
	stateRoot string
	logRoot   string
	mu        sync.Mutex
	active    map[string]struct{}
}

// New creates the state and log directories below root.
func New(root string) (*Store, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve run-store root: %w", err)
	}
	stateRoot := filepath.Join(absRoot, stateDirName)
	if err := makeSafeDir(stateRoot); err != nil {
		return nil, fmt.Errorf("create state root: %w", err)
	}
	logRoot := filepath.Join(stateRoot, logDirName)
	if err := makeSafeDir(logRoot); err != nil {
		return nil, fmt.Errorf("create log root: %w", err)
	}
	return &Store{
		root:      filepath.Clean(absRoot),
		stateRoot: stateRoot,
		logRoot:   logRoot,
		active:    make(map[string]struct{}),
	}, nil
}

// StateRoot returns the directory containing durable service state.
func (s *Store) StateRoot() string { return s.stateRoot }

// LogRoot returns the directory containing all run directories.
func (s *Store) LogRoot() string { return s.logRoot }

// Handle is an active run with open log files.
type Handle struct {
	store  *Store
	dir    string
	stdout *os.File
	stderr *os.File
	Meta   Meta
}

// Begin allocates a UUIDv7 run ID and makes a running ledger entry.
//
//nolint:govet // Local error scopes keep each filesystem operation explicit.
func (s *Store) Begin(meta Meta) (*Handle, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate run id: %w", err)
	}
	meta.RunID = id.String()
	meta.Status = StatusRunning
	meta.StartedAt = time.Now().UTC()
	meta.OwnerPID = os.Getpid()
	meta.OwnerIdentity = ProcessIdentity(meta.OwnerPID)
	meta.Args = append([]string(nil), meta.Args...)

	dir, err := s.runDir(meta.RunID)
	if err != nil {
		return nil, fmt.Errorf("resolve run directory: %w", err)
	}
	if err := os.Mkdir(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create run directory: %w", err)
	}
	// #nosec G304 -- dir is derived from a validated UUIDv7 below the store root.
	stdout, err := os.OpenFile(
		filepath.Join(dir, "stdout.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("create stdout log: %w", err)
	}
	// #nosec G304 -- dir is derived from a validated UUIDv7 below the store root.
	stderr, err := os.OpenFile(
		filepath.Join(dir, "stderr.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0o600,
	)
	if err != nil {
		//nolint:errcheck // The original open failure remains the actionable error.
		_ = stdout.Close()
		return nil, fmt.Errorf("create stderr log: %w", err)
	}
	handle := &Handle{store: s, dir: dir, stdout: stdout, stderr: stderr, Meta: meta}
	s.markActive(meta.RunID, true)
	if err := s.writeMeta(dir, meta); err != nil {
		s.markActive(meta.RunID, false)
		//nolint:errcheck // Metadata write failure remains the actionable error.
		_ = stdout.Close()
		//nolint:errcheck // Metadata write failure remains the actionable error.
		_ = stderr.Close()
		return nil, fmt.Errorf("write initial run metadata: %w", err)
	}
	return handle, nil
}

// Stdout returns the append-only stdout destination.
func (h *Handle) Stdout() io.Writer { return h.stdout }

// Stderr returns the append-only stderr destination.
func (h *Handle) Stderr() io.Writer { return h.stderr }

// PersistRunning atomically publishes fields filled after Begin, such as the
// selected runner, working directory, runner version, and child PID.
func (h *Handle) PersistRunning() error {
	if h.Meta.Status != StatusRunning {
		return fmt.Errorf("cannot update terminal run %q", h.Meta.RunID)
	}
	return h.store.writeMeta(h.dir, h.Meta)
}

// PersistFinal retries publication of terminal metadata after a prior Finish
// write failed. Log files are already closed by Finish and are not touched.
func (h *Handle) PersistFinal() error {
	if h.Meta.Status == StatusRunning {
		return fmt.Errorf("cannot republish running run %q as terminal", h.Meta.RunID)
	}
	return h.store.writeMeta(h.dir, h.Meta)
}

// Finish finalizes a run atomically after both stream files have been closed.
func (h *Handle) Finish(
	status Status,
	exitCode int,
	errText string,
	stdoutTruncated bool,
	stderrTruncated bool,
) error {
	closeErr := errors.Join(h.stdout.Close(), h.stderr.Close())
	h.Meta.Status = status
	h.Meta.ExitCode = exitCode
	h.Meta.EndedAt = time.Now().UTC()
	h.Meta.DurationMS = h.Meta.EndedAt.Sub(h.Meta.StartedAt).Milliseconds()
	h.Meta.Error = errText
	h.Meta.StdoutTruncated = stdoutTruncated
	h.Meta.StderrTruncated = stderrTruncated
	if info, err := os.Stat(filepath.Join(h.dir, "stdout.log")); err == nil {
		h.Meta.StdoutBytes = info.Size()
	}
	if info, err := os.Stat(filepath.Join(h.dir, "stderr.log")); err == nil {
		h.Meta.StderrBytes = info.Size()
	}
	writeErr := h.store.writeMeta(h.dir, h.Meta)
	if writeErr != nil {
		writeErr = errors.Join(ErrFinalMetadataPersistence, writeErr)
	}
	h.store.markActive(h.Meta.RunID, false)
	return errors.Join(closeErr, writeErr)
}

// Get reads immutable or in-progress metadata for a run ID.
func (s *Store) Get(runID string) (Meta, error) {
	dir, err := s.existingRunDir(runID)
	if err != nil {
		return Meta{}, err
	}
	return readMeta(filepath.Join(dir, "meta.json"))
}

// ReadLog reads a byte range without following log-file symlinks.
func (s *Store) ReadLog(runID, stream string, offset, limit int64) ([]byte, error) {
	if offset < 0 {
		return nil, fmt.Errorf("offset must not be negative")
	}
	if limit <= 0 {
		limit = 64 << 10
	}
	if limit > 1<<20 {
		return nil, fmt.Errorf("limit exceeds 1048576 bytes")
	}
	if stream != "stdout" && stream != "stderr" {
		return nil, fmt.Errorf("stream must be stdout or stderr")
	}
	dir, err := s.existingRunDir(runID)
	if err != nil {
		return nil, fmt.Errorf("resolve run directory: %w", err)
	}
	path := filepath.Join(dir, stream+".log")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect log file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular log file")
	}
	// #nosec G304 -- path is constructed from a validated run ID and fixed stream name.
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	defer func() {
		//nolint:errcheck // Read errors take precedence and Close cannot be returned here.
		_ = file.Close()
	}()
	if _, seekErr := file.Seek(offset, io.SeekStart); seekErr != nil {
		return nil, fmt.Errorf("seek log file: %w", seekErr)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, fmt.Errorf("read log file: %w", err)
	}
	return data, nil
}

// ReadLogTail reads up to maxBytes from the end of a log without splitting a UTF-8 rune.
func (s *Store) ReadLogTail(runID, stream string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("max bytes must not be negative")
	}
	if maxBytes == 0 {
		return nil, nil
	}
	if stream != "stdout" && stream != "stderr" {
		return nil, fmt.Errorf("stream must be stdout or stderr")
	}
	dir, err := s.existingRunDir(runID)
	if err != nil {
		return nil, fmt.Errorf("resolve run directory: %w", err)
	}
	path := filepath.Join(dir, stream+".log")
	info, err := safeRegularFile(path)
	if err != nil {
		return nil, err
	}
	start := max(info.Size()-maxBytes, 0)
	// #nosec G304 -- path is constructed from a validated run ID and fixed stream name.
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	defer func() {
		//nolint:errcheck // Read errors take precedence and Close cannot be returned here.
		_ = file.Close()
	}()
	if _, seekErr := file.Seek(start, io.SeekStart); seekErr != nil {
		return nil, fmt.Errorf("seek log tail: %w", seekErr)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("read log tail: %w", err)
	}
	if start > 0 {
		for len(data) > 0 && !utf8.RuneStart(data[0]) {
			data = data[1:]
		}
	}
	return data, nil
}

// LogState returns byte counts and the newest non-empty log-file modification time.
func (s *Store) LogState(runID string) (LogState, error) {
	dir, err := s.existingRunDir(runID)
	if err != nil {
		return LogState{}, fmt.Errorf("resolve run directory: %w", err)
	}
	stdout, err := safeRegularFile(filepath.Join(dir, "stdout.log"))
	if err != nil {
		return LogState{}, err
	}
	stderr, err := safeRegularFile(filepath.Join(dir, "stderr.log"))
	if err != nil {
		return LogState{}, err
	}
	state := LogState{StdoutBytes: stdout.Size(), StderrBytes: stderr.Size()}
	if stdout.Size() > 0 {
		state.LastOutputAt = stdout.ModTime().UTC()
	}
	if stderr.Size() > 0 && stderr.ModTime().After(state.LastOutputAt) {
		state.LastOutputAt = stderr.ModTime().UTC()
	}
	state.NoOutputYet = state.LastOutputAt.IsZero()
	return state, nil
}

// ListRecent returns up to limit persisted runs in reverse UUIDv7 order.
func (s *Store) ListRecent(limit int) ([]Meta, error) {
	runs, _, err := s.ListRecentPage(limit, "")
	return runs, err
}

// ListRecentPage returns a page of persisted runs in reverse UUIDv7 order.
//
// Cursor is an exclusive UUIDv7 boundary returned by a previous page. hasMore
// means a later page may contain additional valid ledger entries.
//
//nolint:gocyclo // Each filesystem validation branch protects the ledger boundary.
func (s *Store) ListRecentPage(limit int, cursor string) ([]Meta, bool, error) {
	if limit <= 0 {
		return []Meta{}, false, nil
	}
	if cursor != "" {
		parsed, err := uuid.Parse(cursor)
		if err != nil || parsed.Version() != 7 || parsed.String() != cursor {
			return nil, false, fmt.Errorf("invalid run cursor %q", cursor)
		}
	}
	entries, err := os.ReadDir(s.logRoot)
	if err != nil {
		return nil, false, fmt.Errorf("list run logs: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		parsed, parseErr := uuid.Parse(entry.Name())
		if parseErr != nil || parsed.Version() != 7 || parsed.String() != entry.Name() {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	runs := make([]Meta, 0, min(limit, len(names)))
	for _, name := range names {
		if cursor != "" && name >= cursor {
			continue
		}
		dir, dirErr := s.existingRunDir(name)
		if dirErr != nil {
			continue
		}
		meta, metaErr := readMeta(filepath.Join(dir, "meta.json"))
		if metaErr != nil {
			continue
		}
		if len(runs) == limit {
			return runs, true, nil
		}
		runs = append(runs, meta)
	}
	return runs, false, nil
}

func safeRegularFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect log file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular log file")
	}
	return info, nil
}

// Cleanup deletes safely-contained, terminal runs older than retention.
func (s *Store) Cleanup(retention time.Duration) error {
	if retention <= 0 {
		return nil
	}
	entries, err := os.ReadDir(s.logRoot)
	if err != nil {
		return fmt.Errorf("list run logs: %w", err)
	}
	deadline := time.Now().UTC().Add(-retention)
	var result error
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || s.isActive(entry.Name()) {
			continue
		}
		dir, err := s.existingRunDir(entry.Name())
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if hasSymlink(dir) {
			continue
		}
		meta, err := readMeta(filepath.Join(dir, "meta.json"))
		if err != nil {
			continue
		}
		if !expiredForCleanup(meta, deadline) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func expiredForCleanup(meta Meta, deadline time.Time) bool {
	retentionTime := meta.EndedAt
	if meta.Status == StatusRunning {
		if processMatches(meta.OwnerPID, meta.OwnerIdentity) ||
			processMatches(meta.PID, meta.ProcessIdentity) {
			return false
		}
		retentionTime = meta.StartedAt
	}
	return !retentionTime.IsZero() && retentionTime.Before(deadline)
}

func (s *Store) runDir(runID string) (string, error) {
	parsed, err := uuid.Parse(runID)
	if err != nil || parsed.Version() != 7 || parsed.String() != runID {
		return "", fmt.Errorf("invalid run_id")
	}
	path := filepath.Join(s.logRoot, runID)
	rel, err := filepath.Rel(s.logRoot, path)
	if err != nil || rel == "." || stringsHasParent(rel) {
		return "", fmt.Errorf("run_id escapes log root")
	}
	return path, nil
}

func (s *Store) existingRunDir(runID string) (string, error) {
	dir, err := s.runDir(runID)
	if err != nil {
		return "", fmt.Errorf("validate run ID: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("inspect run directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("refusing non-directory or symlink run path")
	}
	return dir, nil
}

func (s *Store) writeMeta(dir string, meta Meta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run metadata: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".meta-*.json")
	if err != nil {
		return fmt.Errorf("create temporary metadata file: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		//nolint:errcheck // The temporary file is best-effort cleanup after a failed publish.
		_ = os.Remove(temporaryName)
	}()
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		//nolint:errcheck // The write failure remains the actionable error.
		_ = temporary.Close()
		return fmt.Errorf("write temporary metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary metadata: %w", err)
	}
	if err := os.Rename(temporaryName, filepath.Join(dir, "meta.json")); err != nil {
		return fmt.Errorf("publish run metadata: %w", err)
	}
	return nil
}

func readMeta(path string) (Meta, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Meta{}, fmt.Errorf("inspect metadata file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Meta{}, fmt.Errorf("refusing non-regular metadata file")
	}
	// #nosec G304 -- path is a fixed metadata basename below a validated run directory.
	data, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, fmt.Errorf("read metadata file: %w", err)
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return Meta{}, fmt.Errorf("decode run metadata: %w", err)
	}
	return meta, nil
}

func (s *Store) markActive(id string, active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active {
		s.active[id] = struct{}{}
		return
	}
	delete(s.active, id)
}

func (s *Store) isActive(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, active := s.active[id]
	return active
}

func hasSymlink(root string) bool {
	found := false
	//nolint:errcheck // A walk failure is treated as unsafe and therefore retains the run.
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			found = true
			return filepath.SkipDir
		}
		return nil
	})
	return found
}

func stringsHasParent(path string) bool {
	return path == ".." || len(path) > 3 && path[:3] == ".."+string(filepath.Separator)
}

//nolint:govet // Local error scopes keep each filesystem operation explicit.
func makeSafeDir(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("refusing non-directory or symlink %q", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect directory: %w", err)
	}
	if err := os.Mkdir(path, 0o750); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create directory: %w", err)
		}
	}
	info, err = os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect created directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("refusing non-directory or symlink %q", path)
	}
	return nil
}
