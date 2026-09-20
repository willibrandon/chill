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

func TestTUISpinnerAppearsBesideSubmittedCommand(t *testing.T) {
	withConfigDir(t)
	model := newTUI()
	model.width, model.height = 100, 30
	model.fit()
	model.setInput("doctor --stations")
	model.submit()
	echo := "chill> doctor --stations"
	frame := model.spinner.View()
	if !strings.Contains(ansi.Strip(model.viewport.View()), echo+" "+frame) {
		t.Fatal("submitted command has no inline spinner")
	}
	model.sel = selection{active: true, lines: true,
		anchor: point{model.activeRow, 0}, cursor: point{model.activeRow, len(echo) - 1}}
	model.update(model.spinner.Tick())
	if model.spinner.View() == frame || !strings.Contains(ansi.Strip(model.viewport.View()), echo+" "+model.spinner.View()) {
		t.Fatal("inline spinner did not animate")
	}
	if !model.sel.active || model.selectedText() != echo {
		t.Fatal("animation changed the selection or copied text")
	}
	for _, line := range append(append([]string(nil), model.lines...), model.rows...) {
		if strings.ContainsAny(line, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
			t.Fatal("animation frames were stored in the transcript")
		}
	}
	model.update(resultMsg{out: "done"})
	if strings.ContainsAny(ansi.Strip(model.viewport.View()), "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatal("inline spinner remained after completion")
	}
}

func TestTUIInlineSpinnerWrappingAndClear(t *testing.T) {
	withConfigDir(t)
	model := newTUI()
	model.width, model.height = len("chill> doctor --stations")+1, 30
	model.fit()
	model.setInput("doctor --stations")
	model.submit()
	if !model.activeExtraRow || model.rows[model.activeRow] != "" {
		t.Fatal("spinner did not wrap when the command filled its row")
	}
	model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if model.activeExtraRow || !strings.Contains(ansi.Strip(model.viewport.View()), "chill> doctor --stations "+model.spinner.View()) {
		t.Fatal("resizing did not put the spinner back beside the command")
	}
	model.Update(tea.WindowSizeMsg{Width: len("chill> doctor --stations") + 1, Height: 30})
	rows := len(model.rows)
	model.update(resultMsg{})
	if len(model.rows) != rows-1 {
		t.Fatal("completion left the temporary spinner row behind")
	}
	model.start("doctor")
	model.clear()
	model.update(model.spinner.Tick())
	if model.activeRow != -1 || strings.ContainsAny(ansi.Strip(model.viewport.View()), "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatal("clearing the transcript moved the spinner to an unrelated line")
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
