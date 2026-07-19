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
	Timeout          time.Duration
	TimeoutUnlimited bool
	TailSize         int
	Grace            time.Duration
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

// Run is one started process that can be observed or stopped independently of a request.
//
//nolint:govet // Fields stay grouped by process lifecycle ownership.
type Run struct {
	cmd        *exec.Cmd
	handle     *runstore.Handle
	config     Config
	stdoutTail *Tail
	stderrTail *Tail
	killTree   func()
	cleanup    func()
	done       chan struct{}
	stop       chan stopRequest
	stopOnce   sync.Once

	mu                   sync.RWMutex
	meta                 runstore.Meta
	result               Result
	finalErr             error
	metadataRepairNeeded bool
}

type stopRequest struct {
	status  runstore.Status
	message string
	errText string
}

// Start starts cmd without binding its lifetime to an MCP request context.
func Start(cmd *exec.Cmd, handle *runstore.Handle, config Config) (*Run, error) {
	if cmd == nil || handle == nil {
		return nil, fmt.Errorf("command and run handle are required")
	}
	config = normalizeConfig(config)
	run := &Run{
		cmd:        cmd,
		handle:     handle,
		config:     config,
		stdoutTail: NewTail(config.TailSize),
		stderrTail: NewTail(config.TailSize),
		done:       make(chan struct{}),
		stop:       make(chan stopRequest, 1),
		meta:       handle.Meta,
	}
	cmd.Stdin = nil
	cmd.Stdout = io.MultiWriter(handle.Stdout(), run.stdoutTail)
	cmd.Stderr = io.MultiWriter(handle.Stderr(), run.stderrTail)
	cmd.WaitDelay = config.Grace
	prepare(cmd)
	if err := cmd.Start(); err != nil {
		run.complete(
			compact(
				handle.Meta,
				false,
				-1,
				runstore.StatusSpawnError,
				"Failed to start task",
				run.stderrTail.String(),
				run.stdoutTail.String(),
			),
			handle.Finish(
				runstore.StatusSpawnError,
				-1,
				err.Error(),
				run.stdoutTail.Truncated(),
				run.stderrTail.Truncated(),
			),
		)
		return run, fmt.Errorf("start task process: %w", err)
	}
	cleanup, killTree, attachErr := attach(cmd)
	run.cleanup = cleanup
	run.killTree = killTree
	if attachErr != nil {
		run.failSetup(attachErr, "Failed to prepare task process")
		return run, attachErr
	}
	handle.Meta.PID = cmd.Process.Pid
	handle.Meta.ProcessIdentity = runstore.ProcessIdentity(handle.Meta.PID)
	if err := handle.PersistRunning(); err != nil {
		// A metadata write failure must not kill a started process: the run keeps
		// going and reports the failure through its own stderr stream.
		if warningErr := writeExecutorWarning(
			handle,
			fmt.Errorf("publish running metadata: %w", err),
		); warningErr != nil {
			handle.Meta.Error = warningErr.Error()
		}
	}
	run.meta = handle.Meta
	go run.await()
	return run, nil
}

func normalizeConfig(config Config) Config {
	if config.TimeoutUnlimited {
		config.Timeout = 0
	} else if config.Timeout <= 0 {
		config.Timeout = 15 * time.Minute
	}
	if config.TailSize <= 0 {
		config.TailSize = 64 << 10
	}
	if config.Grace <= 0 {
		config.Grace = 2 * time.Second
	}
	return config
}

func (r *Run) failSetup(err error, message string) {
	if r.killTree != nil {
		r.killTree()
	} else if r.cmd.Process != nil {
		//nolint:errcheck // The setup failure remains the actionable error.
		_ = r.cmd.Process.Kill()
	}
	waitErr := r.cmd.Wait()
	if r.cleanup != nil {
		r.cleanup()
	}
	finalErr := r.handle.Finish(
		runstore.StatusSpawnError,
		-1,
		err.Error(),
		r.stdoutTail.Truncated(),
		r.stderrTail.Truncated(),
	)
	r.complete(
		compact(
			r.handle.Meta,
			false,
			-1,
			runstore.StatusSpawnError,
			message,
			r.stderrTail.String(),
			r.stdoutTail.String(),
		),
		errors.Join(err, waitErr, finalErr),
	)
}

func (r *Run) await() {
	if r.cleanup != nil {
		defer r.cleanup()
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- r.cmd.Wait() }()
	var timeout <-chan time.Time
	var timer *time.Timer
	if !r.config.TimeoutUnlimited {
		timer = time.NewTimer(r.config.Timeout)
		defer timer.Stop()
		timeout = timer.C
	}

	var (
		waitErr error
		status  = runstore.StatusOK
		message = "OK"
		errText string
	)
	select {
	case waitErr = <-waitDone:
		if waitErr != nil {
			status = runstore.StatusNonzero
			message = "Task exited with a non-zero status"
		}
	case <-timeout:
		status = runstore.StatusTimeout
		message = fmt.Sprintf(
			"Task exceeded configured timeout of %s; adjust --timeout or JMW_TIMEOUT.",
			r.config.Timeout,
		)
		errText = message
		terminate(r.cmd, r.config.Grace, r.killTree)
		waitErr = <-waitDone
	case request := <-r.stop:
		status = request.status
		message = request.message
		errText = request.errText
		terminate(r.cmd, r.config.Grace, r.killTree)
		waitErr = <-waitDone
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) && r.cmd.ProcessState != nil && r.cmd.ProcessState.Success() {
		waitErr = nil
	}
	exitCode := 0
	if r.cmd.ProcessState != nil {
		exitCode = r.cmd.ProcessState.ExitCode()
	}
	if status == runstore.StatusOK && waitErr != nil {
		status = runstore.StatusNonzero
		message = "Task exited with a non-zero status"
	}
	if errText == "" {
		errText = errorText(waitErr, status)
	}
	finalErr := r.handle.Finish(
		status,
		exitCode,
		errText,
		r.stdoutTail.Truncated(),
		r.stderrTail.Truncated(),
	)
	r.complete(
		compact(
			r.handle.Meta,
			status == runstore.StatusOK,
			exitCode,
			status,
			message,
			r.stderrTail.String(),
			r.stdoutTail.String(),
		),
		finalErr,
	)
}

func (r *Run) complete(result Result, finalErr error) {
	r.mu.Lock()
	r.meta = r.handle.Meta
	r.result = result
	r.finalErr = finalErr
	r.metadataRepairNeeded = errors.Is(finalErr, runstore.ErrFinalMetadataPersistence)
	r.mu.Unlock()
	close(r.done)
}

// Done closes when the process and its ledger entry are terminal.
func (r *Run) Done() <-chan struct{} { return r.done }

// Snapshot returns the latest non-blocking receipt.
func (r *Run) Snapshot() Result {
	r.mu.RLock()
	if r.result.RunID != "" {
		result := r.result
		r.mu.RUnlock()
		return result
	}
	meta := r.meta
	r.mu.RUnlock()
	return compact(
		meta,
		false,
		0,
		runstore.StatusRunning,
		"Task is running",
		r.stderrTail.String(),
		r.stdoutTail.String(),
	)
}

// Wait returns when the run finishes or ctx expires without stopping the process.
func (r *Run) Wait(ctx context.Context) Result {
	select {
	case <-r.done:
	case <-ctx.Done():
	}
	return r.Snapshot()
}

// Stop terminates a running process and records it as cancelled.
func (r *Run) Stop() error { return r.StopWithReason("Task cancelled") }

// StopWithReason terminates a running process and stores reason as the ledger
// error text. The receipt message stays the stable cancellation message.
func (r *Run) StopWithReason(reason string) error {
	select {
	case <-r.done:
		return r.Err()
	default:
	}
	r.stopOnce.Do(func() {
		r.stop <- stopRequest{
			status:  runstore.StatusCancelled,
			message: "Task cancelled",
			errText: reason,
		}
	})
	<-r.done
	return r.Err()
}

// Err returns a ledger finalization error, if one occurred.
func (r *Run) Err() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.finalErr
}

// NeedsMetadataRepair reports whether the terminal ledger write failed. A run
// may have another final error without needing to stay retained by the manager.
func (r *Run) NeedsMetadataRepair() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.metadataRepairNeeded
}

// Meta returns the newest in-memory metadata snapshot for this run.
func (r *Run) Meta() runstore.Meta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	meta := r.meta
	meta.Args = append([]string(nil), meta.Args...)
	return meta
}

// RepairFinalMetadata retries a terminal metadata write that failed during
// finalization. It never reopens or rewrites the run logs.
func (r *Run) RepairFinalMetadata() error {
	select {
	case <-r.done:
	default:
		return fmt.Errorf("run %q is not terminal", r.Snapshot().RunID)
	}
	if err := r.handle.PersistFinal(); err != nil {
		return fmt.Errorf("persist final run metadata: %w", err)
	}
	r.mu.Lock()
	r.metadataRepairNeeded = false
	r.mu.Unlock()
	return nil
}

// Execute starts cmd and preserves the synchronous cancellation semantics.
func Execute(
	ctx context.Context,
	cmd *exec.Cmd,
	handle *runstore.Handle,
	config Config,
) (Result, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if cmd == nil || handle == nil {
			return Result{}, fmt.Errorf("command and run handle are required")
		}
		finalErr := handle.Finish(runstore.StatusCancelled, -1, ctxErr.Error(), false, false)
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
	run, startErr := Start(cmd, handle, config)
	if run == nil {
		return Result{}, startErr
	}
	if startErr != nil {
		return run.Snapshot(), errors.Join(startErr, run.Err())
	}
	select {
	case <-run.Done():
		return run.Snapshot(), run.Err()
	case <-ctx.Done():
		stopErr := run.Stop()
		return run.Snapshot(), errors.Join(stopErr, run.Err())
	}
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
