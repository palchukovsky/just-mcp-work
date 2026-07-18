// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package executor runs runner-provided commands and streams their output to a run store.
package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/runstore"
)

// Config controls generic process execution behavior.
type Config struct {
	Timeout  time.Duration
	TailSize int
	Grace    time.Duration
}

// Result is the compact receipt returned to callers.
//
//nolint:govet // Field order follows the stable MCP receipt response shape.
type Result struct {
	RunID      string          `json:"run_id"`
	OK         bool            `json:"ok"`
	ExitCode   int             `json:"exit_code"`
	DurationMS int64           `json:"duration_ms"`
	Message    string          `json:"message"`
	Status     runstore.Status `json:"status"`
	StderrTail string          `json:"stderr_tail,omitempty"`
	StdoutTail string          `json:"stdout_tail,omitempty"`
	LogsReady  bool            `json:"logs_available"`
}

// Execute starts cmd without changing the process working directory.
//
//nolint:gocyclo // Process completion, timeout, and cancellation have distinct states.
func Execute(
	ctx context.Context,
	cmd *exec.Cmd,
	handle *runstore.Handle,
	config Config,
) (Result, error) {
	if cmd == nil || handle == nil {
		return Result{}, fmt.Errorf("command and run handle are required")
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		finalErr := handle.Finish(
			runstore.StatusCancelled,
			-1,
			ctxErr.Error(),
			false,
			false,
		)
		return compact(
			handle.Meta,
			false,
			-1,
			runstore.StatusCancelled,
			"Task cancelled",
			"",
			"",
		), finalErr
	}
	if config.Timeout <= 0 {
		config.Timeout = 15 * time.Minute
	}
	if config.TailSize <= 0 {
		config.TailSize = 64 << 10
	}
	if config.Grace <= 0 {
		config.Grace = 2 * time.Second
	}
	stdoutTail := NewTail(config.TailSize)
	stderrTail := NewTail(config.TailSize)
	cmd.Stdin = nil
	cmd.Stdout = io.MultiWriter(handle.Stdout(), stdoutTail)
	cmd.Stderr = io.MultiWriter(handle.Stderr(), stderrTail)
	cmd.WaitDelay = config.Grace
	prepare(cmd)
	if err := cmd.Start(); err != nil {
		finalErr := handle.Finish(
			runstore.StatusSpawnError,
			-1,
			err.Error(),
			stdoutTail.Truncated(),
			stderrTail.Truncated(),
		)
		return compact(
			handle.Meta,
			false,
			-1,
			runstore.StatusSpawnError,
			"Failed to start task",
			stderrTail.String(),
			stdoutTail.String(),
		), errors.Join(err, finalErr)
	}
	handle.Meta.PID = cmd.Process.Pid
	handle.Meta.ProcessIdentity = runstore.ProcessIdentity(handle.Meta.PID)
	if err := handle.PersistRunning(); err != nil {
		if warningErr := writeExecutorWarning(
			handle,
			fmt.Errorf("publish running metadata: %w", err),
		); warningErr != nil {
			handle.Meta.Error = warningErr.Error()
		}
	}
	cleanup, killTree, attachErr := attach(cmd)
	if attachErr != nil {
		if killTree != nil {
			killTree()
		} else if cmd.Process != nil {
			//nolint:errcheck // The setup failure remains the actionable error.
			_ = cmd.Process.Kill()
		}
		waitErr := cmd.Wait()
		finalErr := handle.Finish(
			runstore.StatusSpawnError,
			-1,
			attachErr.Error(),
			stdoutTail.Truncated(),
			stderrTail.Truncated(),
		)
		return compact(
			handle.Meta,
			false,
			-1,
			runstore.StatusSpawnError,
			"Failed to prepare task process",
			stderrTail.String(),
			stdoutTail.String(),
		), errors.Join(attachErr, waitErr, finalErr)
	}
	if cleanup != nil {
		defer cleanup()
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(config.Timeout)
	defer timer.Stop()

	var waitErr error
	status := runstore.StatusOK
	message := "OK"
	select {
	case waitErr = <-done:
		if waitErr != nil {
			status = runstore.StatusNonzero
			message = "Task exited with a non-zero status"
		}
	case <-timer.C:
		status = runstore.StatusTimeout
		message = "Task timed out"
		terminate(cmd, config.Grace, killTree)
		waitErr = <-done
	case <-ctx.Done():
		status = runstore.StatusCancelled
		message = "Task cancelled"
		terminate(cmd, config.Grace, killTree)
		waitErr = <-done
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		waitErr = nil
	}

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if status == runstore.StatusOK && waitErr != nil {
		status = runstore.StatusNonzero
		message = "Task exited with a non-zero status"
	}
	finalErr := handle.Finish(
		status,
		exitCode,
		errorText(waitErr, status),
		stdoutTail.Truncated(),
		stderrTail.Truncated(),
	)
	return compact(
		handle.Meta,
		status == runstore.StatusOK,
		exitCode,
		status,
		message,
		stderrTail.String(),
		stdoutTail.String(),
	), finalErr
}

func compact(
	meta runstore.Meta,
	ok bool,
	exitCode int,
	status runstore.Status,
	message string,
	stderr string,
	stdout string,
) Result {
	duration := meta.DurationMS
	if duration == 0 && status == runstore.StatusRunning {
		duration = time.Since(meta.StartedAt).Milliseconds()
	}
	result := Result{
		RunID:      meta.RunID,
		OK:         ok,
		ExitCode:   exitCode,
		DurationMS: duration,
		Message:    message,
		Status:     status,
		LogsReady:  true,
	}
	if !ok {
		result.StderrTail = stderr
		result.StdoutTail = stdout
	}
	return result
}

func errorText(waitErr error, status runstore.Status) string {
	if waitErr == nil || status == runstore.StatusOK {
		return ""
	}
	return waitErr.Error()
}

func writeExecutorWarning(handle *runstore.Handle, err error) error {
	message := "just-mcp-work: process-tree setup warning: " + err.Error() + "\n"
	_, writeErr := handle.Stderr().Write([]byte(message))
	if writeErr != nil {
		return fmt.Errorf("write executor warning: %w", writeErr)
	}
	return nil
}

// Tail retains only the newest bytes written to it.
//
//nolint:govet // Field order keeps the protected buffer fields together.
type Tail struct {
	mu        sync.Mutex
	buffer    []byte
	capacity  int
	truncated bool
}

// NewTail creates a bounded stream tail.
func NewTail(capacity int) *Tail {
	if capacity < 0 {
		capacity = 0
	}
	return &Tail{capacity: capacity}
}

// Write implements io.Writer.
func (t *Tail) Write(data []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.capacity == 0 {
		t.truncated = t.truncated || len(data) > 0
		return len(data), nil
	}
	if len(data) > t.capacity {
		t.buffer = append(t.buffer[:0], data[len(data)-t.capacity:]...)
		t.truncated = true
		return len(data), nil
	}
	overflow := len(t.buffer) + len(data) - t.capacity
	if overflow > 0 {
		copy(t.buffer, t.buffer[overflow:])
		t.buffer = t.buffer[:len(t.buffer)-overflow]
		t.truncated = true
	}
	t.buffer = append(t.buffer, data...)
	return len(data), nil
}

// String returns a stable snapshot.
func (t *Tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buffer)
}

// Truncated reports whether old stream bytes were discarded.
func (t *Tail) Truncated() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.truncated
}
