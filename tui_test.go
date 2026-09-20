package main

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTUIDoctorReportsFailedChecks(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("MPV_HOME", t.TempDir())
	model := newTUI()
	model.width, model.height = 100, 30
	model.setInput("doctor")
	command := model.submit()
	if command == nil || !model.running {
		t.Fatal("doctor was not dispatched as a background command")
	}
	if strings.Contains(strings.Join(model.lines, "\n"), "[FAIL]") {
		t.Fatal("doctor ran during submit instead of through the asynchronous command")
	}
	// The prompt remains editable while a diagnostic command is in flight.
	model.setInput("doctor --help")
	result, ok := command().(resultMsg)
	if !ok || result.err == nil || !strings.Contains(result.out, "[FAIL] mpv") {
		t.Fatalf("missing diagnostic findings: %+v", result)
	}
	model.update(result)
	transcript := ansi.Strip(strings.Join(model.lines, "\n"))
	for _, want := range []string{"[FAIL] mpv", "[FAIL] yt-dlp", "no daemon running", "doctor found problems"} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript missing %q: %s", want, transcript)
		}
	}
	if model.running || model.input.Value() != "doctor --help" {
		t.Fatal("completed diagnostics left the model busy or changed the prompt")
	}
	if _, err := os.Stat(socketPath()); !os.IsNotExist(err) {
		t.Fatal("doctor started a daemon")
	}
}

func TestTUIHelpIncludesDoctor(t *testing.T) {
	if !isCommand("doctor") || !strings.Contains(replHelp(), "doctor") {
		t.Fatal("doctor is missing from the REPL command registry")
	}
	for _, example := range []string{"doctor [options]", "doctor --stations", "doctor --stream sleep", "doctor --logs", "doctor --help"} {
		if !strings.Contains(ansi.Strip(helpBody()), example) {
			t.Errorf("F1 help is missing %q", example)
		}
	}
}
