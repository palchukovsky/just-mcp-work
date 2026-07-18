// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build windows

package runstore

import (
	"errors"
	"fmt"
	"math"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

func processIdentity(pid int) (string, bool) {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return "", false
	}
	// #nosec G115 -- the bounds check above makes the conversion lossless.
	processID := uint32(pid)
	process, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		processID,
	)
	if err != nil {
		return "", !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer func() {
		//nolint:errcheck // A failed close must not make cleanup delete a live run.
		_ = windows.CloseHandle(process)
	}()
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process, &exitCode); err != nil {
		return "", true
	}
	if exitCode != windowsStillActive {
		return "", false
	}
	var creation windows.Filetime
	var exit windows.Filetime
	var kernel windows.Filetime
	var user windows.Filetime
	if err := windows.GetProcessTimes(process, &creation, &exit, &kernel, &user); err != nil {
		return "", true
	}
	created := uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)
	return fmt.Sprintf("windows:%d", created), true
}
