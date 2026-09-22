// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

//go:build windows

package form

import (
	"encoding/binary"
	"os"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const (
	// classicConsoleClass is the window class of a console window conhost
	// draws itself; a pseudo console behind a terminal application reports
	// another one.
	classicConsoleClass = "ConsoleWindowClass"
	classNameLength     = 256
	// fileNameInfoSize holds FILE_NAME_INFO: a byte length, then the name.
	fileNameInfoSize = 4 + 2*windows.MAX_PATH
)

// consoleWindow reports whether this process's console is a classic console
// window and whether its window could be identified at all. Detect gives an
// unidentified console the plain questions and no advice.
func consoleWindow() (bool, bool) {
	getConsoleWindow := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	if getConsoleWindow.Find() != nil {
		return false, false
	}
	// Call always returns GetLastError; the window handle alone reports
	// whether the call found one.
	//nolint:errcheck // GetConsoleWindow reports failure only through a zero handle.
	// nosemgrep: discarded-error
	window, _, _ := getConsoleWindow.Call()
	if window == 0 {
		return false, false
	}
	var className [classNameLength]uint16
	length, err := windows.GetClassName(windows.HWND(window), &className[0], classNameLength)
	if err != nil || length <= 0 {
		return false, false
	}
	return windows.UTF16ToString(className[:length]) == classicConsoleClass, true
}

// isPTYPipe reports whether file is the named pipe of a Cygwin or MSYS2 pty.
func isPTYPipe(file *os.File) bool {
	handle := windows.Handle(file.Fd())
	fileType, err := windows.GetFileType(handle)
	if err != nil || fileType != windows.FILE_TYPE_PIPE {
		return false
	}
	var info [fileNameInfoSize]byte
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileNameInfo,
		&info[0],
		fileNameInfoSize,
	); err != nil {
		return false
	}
	nameBytes := binary.LittleEndian.Uint32(info[:4])
	if nameBytes > fileNameInfoSize-4 {
		return false
	}
	name := make([]uint16, 0, nameBytes/2)
	for offset := uint32(4); offset+1 < 4+nameBytes; offset += 2 {
		name = append(name, binary.LittleEndian.Uint16(info[offset:offset+2]))
	}
	return isPTYPipeName(string(utf16.Decode(name)))
}
