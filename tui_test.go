package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
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
	var result resultMsg
	for _, cmd := range command().(tea.BatchMsg) {
		if msg, ok := cmd().(resultMsg); ok {
			result = msg
		}
	}
	if result.err == nil || !strings.Contains(result.out, "[FAIL] mpv") {
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

func TestTUICommandSpinnerLifecycle(t *testing.T) {
	withConfigDir(t)
	for _, result := range []resultMsg{{out: "done"}, {err: errors.New("check failed")}} {
		model := newTUI()
		model.width, model.height = 36, 20
		model.status = &Status{State: "playing", Station: "a-long-station-name", Volume: 70}
		model.setInput("doctor --stations")
		command := model.submit()
		if command == nil || !strings.Contains(ansi.Strip(model.statusBar()), "Running doctor...") {
			t.Fatal("busy indicator was not visible immediately on submit")
		}
		frame := model.spinner.View()
		tick := model.spinner.Tick()
		if next := model.update(tick); next == nil || model.spinner.View() == frame {
			t.Fatal("spinner did not advance and schedule its next frame")
		}
		model.update(result)
		if strings.Contains(ansi.Strip(model.statusBar()), "Running") || model.active != "" {
			t.Fatal("busy indicator remained after the command finished")
		}
		if next := model.update(tick); next != nil {
			t.Fatal("spinner kept ticking after the command finished")
		}
	}
}

func TestTUIQueuedCommandGetsFreshSpinner(t *testing.T) {
	withConfigDir(t)
	model := newTUI()
	model.width, model.height = 100, 30
	model.setInput("doctor")
	model.submit()
	oldTick := model.spinner.Tick().(spinner.TickMsg)
	model.setInput("status")
	if cmd := model.submit(); cmd != nil || model.active != "doctor" {
		t.Fatal("queued command replaced the running command")
	}
	if cmd := model.update(resultMsg{out: "done"}); cmd == nil || model.active != "status" {
		t.Fatal("queued command did not start")
	}
	if !strings.Contains(ansi.Strip(model.statusBar()), "Running status...") {
		t.Fatal("busy indicator did not switch to the queued command")
	}
	frame := model.spinner.View()
	if cmd := model.update(oldTick); cmd != nil || model.spinner.View() != frame {
		t.Fatal("late tick from the old command changed the new spinner")
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
