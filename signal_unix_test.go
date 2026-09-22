//go:build !windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestDetachedDaemonHelper reports its session and answers after receiving a hangup.
func TestDetachedDaemonHelper(t *testing.T) {
	if os.Getenv("CHILL_DETACH_TEST") != "1" {
		return
	}
	ignoreDaemonHangup()
	session, err := unix.Getsid(0)
	if err != nil {
		os.Exit(1)
	}
	fmt.Println(session)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		fmt.Println(scanner.Text())
	}
	os.Exit(0)
}

// TestDaemonSessionSurvivesHangup checks actual session isolation and SIGHUP handling.
func TestDaemonSessionSurvivesHangup(t *testing.T) {
	t.Setenv("CHILL_DETACH_TEST", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestDetachedDaemonHelper$")
	configureDaemonProcess(cmd)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != fmt.Sprint(cmd.Process.Pid) {
		t.Fatalf("daemon did not start its own session: %q (%v)", scanner.Text(), scanner.Err())
	}
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(input, "still playing"); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() || scanner.Text() != "still playing" {
		t.Fatalf("daemon did not survive hangup: %q (%v)", scanner.Text(), scanner.Err())
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
