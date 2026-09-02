// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build !windows

package executor

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attach(*exec.Cmd) (func() error, func() error, error) { return nil, nil, nil }

func terminate(cmd *exec.Cmd, grace time.Duration, killTree func() error) error {
	if cmd.Process == nil {
		return nil
	}
	if killTree != nil {
		return killTree()
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		if processGroupGone(err) {
			return nil
		}
		return fmt.Errorf("send SIGTERM to process group: %w", err)
	}
	time.Sleep(grace)
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		if processGroupGone(err) {
			return nil
		}
		return fmt.Errorf("send SIGKILL to process group: %w", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-cmd.Process.Pid, 0); err != nil {
			if processGroupGone(err) {
				return nil
			}
			return fmt.Errorf("probe process group after SIGKILL: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("process group did not exit after SIGKILL")
}

// processGroupGone reports whether a signal or probe failure means the group
// this executor started no longer exists. ESRCH says so directly. EPERM says it
// too: every process in our own group runs as this user, so the only way the
// kernel can refuse the signal is that the group number was recycled by a
// process belonging to somebody else, which the system does readily under a
// load that starts many processes.
func processGroupGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM)
}
