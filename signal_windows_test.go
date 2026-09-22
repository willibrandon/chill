//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestDaemonConsoleHelper launches a detached child from a process with a real console.
func TestDaemonConsoleHelper(t *testing.T) {
	role := os.Getenv("CHILL_CONSOLE_TEST")
	if role == "" {
		return
	}
	var pids [16]uint32
	count, _, err := kernel32.NewProc("GetConsoleProcessList").Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if role == "daemon" {
		if count != 0 || err != windows.ERROR_INVALID_HANDLE {
			fmt.Fprintf(os.Stderr, "daemon retained a console: count=%d error=%v", count, err)
			os.Exit(1)
		}
		fmt.Println("detached")
		os.Exit(0)
	}
	if count == 0 {
		fmt.Fprintln(os.Stderr, "launcher has no console:", err)
		os.Exit(1)
	}
	os.Setenv("CHILL_CONSOLE_TEST", "daemon")
	exe, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	cmd := exec.Command(exe, "-test.run=^TestDaemonConsoleHelper$")
	configureDaemonProcess(cmd)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// TestDaemonDoesNotInheritWindowsConsole checks detachment even when its launcher owns a console.
func TestDaemonDoesNotInheritWindowsConsole(t *testing.T) {
	t.Setenv("CHILL_CONSOLE_TEST", "launcher")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestDaemonConsoleHelper$")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}
	if output, err := cmd.CombinedOutput(); err != nil || string(output) != "detached\n" {
		t.Fatalf("console detachment: %q (%v)", output, err)
	}
}
