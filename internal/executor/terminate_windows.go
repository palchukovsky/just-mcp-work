// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build windows

package executor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
}

func attach(cmd *exec.Cmd) (func() error, func() error, error) {
	pid, err := windowsPID(cmd.Process.Pid)
	if err != nil {
		return nil, nil, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create process job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, setErr := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); setErr != nil {
		if closeErr := windows.CloseHandle(job); closeErr != nil {
			return nil, nil, errors.Join(
				fmt.Errorf("set process job limits: %w", setErr),
				fmt.Errorf("close task job: %w", closeErr),
			)
		}
		return nil, nil, fmt.Errorf("set process job limits: %w", setErr)
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		pid,
	)
	if err != nil {
		if closeErr := windows.CloseHandle(job); closeErr != nil {
			return nil, nil, errors.Join(
				fmt.Errorf("open task process: %w", err),
				fmt.Errorf("close task job: %w", closeErr),
			)
		}
		return nil, nil, fmt.Errorf("open task process: %w", err)
	}
	defer func() {
		//nolint:errcheck // The process operation error takes precedence over handle cleanup.
		// nosemgrep: discarded-error
		_ = windows.CloseHandle(process)
	}()
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		if closeErr := windows.CloseHandle(job); closeErr != nil {
			return nil, nil, errors.Join(
				fmt.Errorf("assign task process to job: %w", err),
				fmt.Errorf("close task job: %w", closeErr),
			)
		}
		return nil, nil, fmt.Errorf("assign task process to job: %w", err)
	}
	var once sync.Once
	closeJob := func() (err error) {
		once.Do(func() {
			err = windows.CloseHandle(job)
		})
		return err
	}
	if err := resumeProcess(pid); err != nil {
		return closeJob, closeJob, fmt.Errorf("resume task process: %w", err)
	}
	return closeJob, closeJob, nil
}

func windowsPID(pid int) (uint32, error) {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return 0, fmt.Errorf("invalid Windows process ID %d", pid)
	}
	// #nosec G115 -- the bounds check above makes the conversion lossless.
	return uint32(pid), nil
}

func resumeProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot process threads: %w", err)
	}
	defer func() {
		//nolint:errcheck // The earlier thread operation remains the actionable error.
		// nosemgrep: discarded-error
		_ = windows.CloseHandle(snapshot)
	}()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return fmt.Errorf("read first process thread: %w", err)
	}
	for {
		if entry.OwnerProcessID == pid {
			return resumeThread(entry.ThreadID)
		}
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return fmt.Errorf("read next process thread: %w", err)
		}
	}
	return fmt.Errorf("find suspended primary thread for process %d", pid)
}

func resumeThread(threadID uint32) error {
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, threadID)
	if err != nil {
		return fmt.Errorf("open suspended primary thread: %w", err)
	}
	defer func() {
		//nolint:errcheck // ResumeThread errors take precedence over handle cleanup.
		// nosemgrep: discarded-error
		_ = windows.CloseHandle(thread)
	}()
	previousCount, err := windows.ResumeThread(thread)
	if err != nil {
		return fmt.Errorf("resume primary thread: %w", err)
	}
	if previousCount != 1 {
		return fmt.Errorf("resume primary thread: unexpected suspend count %d", previousCount)
	}
	return nil
}

func terminate(cmd *exec.Cmd, _ time.Duration, killTree func() error) error {
	if cmd.Process == nil {
		return nil
	}
	if killTree != nil {
		return killTree()
	}
	// #nosec G204 -- the PID comes from the process started by this executor.
	if killErr := exec.CommandContext(
		context.Background(),
		"taskkill",
		"/T",
		"/F",
		"/PID",
		fmt.Sprint(cmd.Process.Pid),
	).Run(); killErr == nil {
		return nil
	} else if fallbackErr := cmd.Process.Kill(); fallbackErr != nil {
		return errors.Join(
			fmt.Errorf("terminate task tree: %w", killErr),
			fmt.Errorf("terminate task process: %w", fallbackErr),
		)
	} else {
		return fmt.Errorf("terminate task tree: %w", killErr)
	}
}
