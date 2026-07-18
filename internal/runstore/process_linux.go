// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build linux

package runstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const linuxProcessStartField = 19

func processIdentity(pid int) (string, bool) {
	return linuxProcessIdentity(pid, os.ReadFile, unixProcessAlive)
}

func linuxProcessIdentity(
	pid int,
	readFile func(string) ([]byte, error),
	processAlive func(int) bool,
) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	statPath := filepath.Join("/proc", strconv.Itoa(pid), "stat")
	// #nosec G304 -- pid is a validated positive integer below the fixed /proc root.
	stat, err := readFile(statPath)
	if err != nil {
		return "", processAlive(pid)
	}
	endCommand := strings.LastIndex(string(stat), ") ")
	if endCommand < 0 {
		return "", true
	}
	fields := strings.Fields(string(stat[endCommand+2:]))
	if len(fields) <= linuxProcessStartField {
		return "", true
	}
	bootTime := linuxBootTime()
	if bootTime == "" {
		return "linux:" + fields[linuxProcessStartField], true
	}
	return fmt.Sprintf(
		"linux:%s:%s",
		bootTime,
		fields[linuxProcessStartField],
	), true
}

func linuxBootTime() string {
	stat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(stat), "\n") {
		if value, found := strings.CutPrefix(line, "btime "); found {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
