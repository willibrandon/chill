package main

import (
	"bytes"
	"io"
	"os"
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
	for _, want := range []string{"[FAIL] mpv", "[FAIL] yt-dlp", "no daemon running"} {
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
