package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorOptions(t *testing.T) {
	for _, args := range [][]string{{"--stations", "--stream", "sleep"}, {"--timeout", "0s"}, {"--timeout", "-1s"}, {"extra"}, {"--unknown"}} {
		if _, err := parseDoctorOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted invalid options: %v", args)
		}
	}
	options, err := parseDoctorOptions([]string{"--stream", "sleep", "--timeout", "2s", "--logs"}, io.Discard)
	if err != nil || options.stream != "sleep" || options.timeout != 2*time.Second || !options.logs {
		t.Fatalf("options=%+v err=%v", options, err)
	}
}

func TestDoctorRejectsIgnoredConfigEntries(t *testing.T) {
	path := withConfigDir(t)
	for _, config := range []string{
		`{"stations":[{"name":"empty","url":""}]}`,
		`{"stations":[{"name":"two words","url":"https://example.com"}]}`,
		`{"stations":[{"name":"multiline","url":"https://example.com\nplay"}]}`,
		`{"default_station":"missing"}`,
		`{"stations":`,
	} {
		writeConfig(t, path, config)
		if err := checkDoctorConfig(); err == nil {
			t.Fatalf("accepted invalid config: %s", config)
		}
	}
	writeConfig(t, path, `{"stations":[{"name":"local","url":"/music/my track.wav"}],"default_station":"local"}`)
	if err := checkDoctorConfig(); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorMissingDependencies(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("MPV_HOME", t.TempDir())
	var out bytes.Buffer
	if err := runDoctor(nil, &out); err == nil {
		t.Fatal("missing dependencies should fail doctor")
	}
	for _, want := range []string{"[FAIL] mpv", "[FAIL] yt-dlp", "[FAIL] ffmpeg", strings.Join(installCommands([]string{"ffmpeg"}), "` then `"), "no daemon running"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	if _, err := os.Stat(socketPath()); !os.IsNotExist(err) {
		t.Fatal("doctor created daemon discovery data")
	}
}

func TestDiagnosticHelper(t *testing.T) {
	if os.Getenv("CHILL_DIAGNOSTIC_HELPER") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "timeout":
		time.Sleep(30 * time.Second)
	case "tree":
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "-test.run=^TestDiagnosticHelper$", "--", "child")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(1)
		}
	case "child":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("CHILL_DIAGNOSTIC_READY"), []byte(listener.Addr().String()), 0600); err != nil {
			os.Exit(3)
		}
		conn, err := listener.Accept()
		if err != nil {
			os.Exit(4)
		}
		defer conn.Close()
		time.Sleep(30 * time.Second)
	case "output":
		os.Stdout.WriteString(strings.Repeat("x", diagnosticLimit*2) + "last stdout")
		os.Stderr.WriteString(strings.Repeat("y", diagnosticLimit*2) + "last stderr")
		os.Exit(7)
	}
	os.Exit(0)
}

func TestDiagnosticCommandsAreBounded(t *testing.T) {
	t.Setenv("CHILL_DIAGNOSTIC_HELPER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := diagnosticCommand(exe, 5*time.Second, "-test.run=^TestDiagnosticHelper$", "--", "output")
	if err == nil || len(stdout) > diagnosticLimit || len(stderr) > diagnosticLimit || !strings.HasSuffix(stdout, "last stdout") || !strings.HasSuffix(stderr, "last stderr") {
		t.Fatalf("unbounded/missing diagnostics: stdout=%d stderr=%d err=%v", len(stdout), len(stderr), err)
	}
	start := time.Now()
	_, _, err = diagnosticCommand(exe, 100*time.Millisecond, "-test.run=^TestDiagnosticHelper$", "--", "timeout")
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout was not enforced: %v (%s)", err, time.Since(start))
	}
}

func TestDaemonStartupLog(t *testing.T) {
	withConfigDir(t)
	log, err := openDaemonLog()
	if err != nil {
		t.Fatal(err)
	}
	log.WriteString(strings.Repeat("x", diagnosticLimit*2) + "cannot bind socket")
	log.Close()
	if tail := readDaemonLog(); len(tail) != diagnosticLimit || !strings.HasSuffix(tail, "cannot bind socket") {
		t.Fatalf("startup log tail: length=%d", len(tail))
	}
	if err := daemonStartError("exited"); !strings.Contains(err.Error(), "cannot bind socket") || !strings.Contains(err.Error(), daemonLogPath()) {
		t.Fatal(err)
	}
	log, err = openDaemonLog()
	if err != nil {
		t.Fatal(err)
	}
	log.Close()
	if readDaemonLog() != "" {
		t.Fatal("new startup retained old diagnostics")
	}
}

func TestDiagnosticCancellationKillsDescendants(t *testing.T) {
	t.Setenv("CHILL_DIAGNOSTIC_HELPER", "1")
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("CHILL_DIAGNOSTIC_READY", ready)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, err := diagnosticCommandContext(ctx, exe, 20*time.Second, "-test.run=^TestDiagnosticHelper$", "--", "tree")
		result <- err
	}()
	var address []byte
	deadline := time.Now().Add(10 * time.Second)
	for len(address) == 0 {
		address, _ = os.ReadFile(ready)
		if time.Now().After(deadline) {
			t.Fatal("diagnostic descendant did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := net.DialTimeout("tcp", string(address), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not finish promptly")
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, err = conn.Read(make([]byte, 1))
	var netErr net.Error
	if err == nil || errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("descendant survived cancellation: %v", err)
	}
	// Cancelling before dispatch must not even try to start the command.
	_, _, err = diagnosticCommandContext(ctx, "nonexistent-diagnostic", time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled command started: %v", err)
	}
}
