//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestPlaybackConsoleHelper checks real Windows console state through helper descendants.
func TestPlaybackConsoleHelper(t *testing.T) {
	role := os.Getenv("CHILL_PLAYBACK_CONSOLE_TEST")
	if role == "" {
		return
	}
	if window, _, _ := kernel32.NewProc("GetConsoleWindow").Call(); window != 0 {
		fmt.Fprintf(os.Stderr, "%s acquired a console window", role)
		os.Exit(1)
	}
	if role == "worker" {
		fmt.Fprintln(os.Stderr, "worker diagnostic")
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	cmd := exec.Command(exe, "-test.run=^TestPlaybackConsoleHelper$")
	if role == "shim" {
		// Real shims and yt-dlp spawn children without Chill's creation flags.
		cmd.Env = append(os.Environ(), "CHILL_PLAYBACK_CONSOLE_TEST=worker")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	cmd.Env = append(os.Environ(), "CHILL_PLAYBACK_CONSOLE_TEST=shim")
	const payload = "PCM passed through the shim and worker\n"
	var diagnostics tailBuffer
	cmd.Stderr = &diagnostics
	reader, err := startPCMProcess(t.Context(), cmd, io.NopCloser(strings.NewReader(payload)), &diagnostics)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	output, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(output) != payload || !strings.Contains(diagnostics.String(), "worker diagnostic") {
		fmt.Fprintf(os.Stderr, "helper pipeline: output=%q diagnostics=%q error=%v", output, diagnostics.String(), err)
		os.Exit(1)
	}
	if err := os.Setenv("CHILL_PLAYBACK_CONSOLE_TEST", "shim"); err != nil {
		os.Exit(1)
	}
	stdout, stderr, err := diagnosticCommand(exe, 10*time.Second, "-test.run=^TestPlaybackConsoleHelper$")
	if err != nil || stdout != "" || !strings.Contains(stderr, "worker diagnostic") {
		fmt.Fprintf(os.Stderr, "diagnostic pipeline: output=%q diagnostics=%q error=%v", stdout, stderr, err)
		os.Exit(1)
	}
	fmt.Println("helpers stayed windowless")
	os.Exit(0)
}

// TestPlaybackHelpersStayWindowless checks helpers launched by a detached daemon.
func TestPlaybackHelpersStayWindowless(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestPlaybackConsoleHelper$")
	cmd.Env = append(os.Environ(), "CHILL_PLAYBACK_CONSOLE_TEST=daemon")
	configureDaemonProcess(cmd)
	if output, err := cmd.CombinedOutput(); err != nil || string(output) != "helpers stayed windowless\n" {
		t.Fatalf("detached helper consoles: %q (%v)", output, err)
	}
}

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
