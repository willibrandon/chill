//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableANSI turns on the console's own handling of ANSI escape sequences.
// PowerShell and Windows Terminal already do this, the legacy cmd.exe console
// does not.
func enableANSI() {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		handle := windows.Handle(f.Fd())

		var mode uint32
		if err := windows.GetConsoleMode(handle, &mode); err != nil {
			continue // not a console
		}
		windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
}
