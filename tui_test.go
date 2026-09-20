package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

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
	task := model.task
	defer task.stop()
	for {
		msg := task.next()
		model.update(msg)
		if result, ok := msg.(resultMsg); ok {
			if result.err == nil || result.out != "" {
				t.Fatalf("findings should stream separately from the final error: %+v", result)
			}
			break
		}
	}
	transcript := ansi.Strip(strings.Join(model.lines, "\n"))
	for _, want := range []string{"[FAIL] mpv", "[FAIL] yt-dlp", "[FAIL] ffmpeg", "no daemon running", "Doctor complete. Passed: 3, warnings: 1, failed: 3.", "doctor found problems"} {
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
		result.id = model.commandID
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
	if cmd := model.update(resultMsg{id: model.commandID, out: "done"}); cmd == nil || model.active != "status" {
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
	model.update(resultMsg{id: model.commandID, out: "done"})
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
	model.update(resultMsg{id: model.commandID})
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

func TestTUIStreamingCancellationAndNextCommand(t *testing.T) {
	withConfigDir(t)
	model := newTUI()
	model.width, model.height = 100, 30
	model.start("doctor --stations")
	model.task.cancel()
	cleaned := make(chan struct{})
	model.task = newREPLTask(model.commandID, func(ctx context.Context, out io.Writer) error {
		defer close(cleaned)
		fmt.Fprint(out, "first finding\nsecond ")
		fmt.Fprint(out, "finding\n")
		<-ctx.Done()
		return ctx.Err()
	})
	task := model.task
	defer task.stop()
	for range 2 {
		msg := task.next()
		if _, ok := msg.(outputMsg); !ok {
			t.Fatalf("expected streamed line before completion, got %#v", msg)
		}
		model.update(msg)
	}
	transcript := strings.Join(model.lines, "\n")
	if !model.running || !strings.Contains(transcript, "second finding") {
		t.Fatal("diagnostics were buffered until completion or chunks lost their line boundaries")
	}
	model.setInput("status")
	model.submit()
	model.setInput("unfinished input")
	model.promptKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !model.cancelling || len(model.pending) != 0 || model.input.Value() != "unfinished input" {
		t.Fatal("cancel did not discard the queue while preserving the draft")
	}
	msg := task.next()
	result, ok := msg.(resultMsg)
	if !ok || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("cancellation result: %#v", msg)
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("command finished before cleanup")
	}
	model.update(msg)
	if model.running || model.task != nil || !strings.Contains(strings.Join(model.lines, "\n"), "cancelled") {
		t.Fatal("cancellation left the prompt busy")
	}
	model.start("help")
	model.update(outputMsg{id: task.id, line: "stale output"})
	model.update(resultMsg{id: task.id, err: context.Canceled})
	if !model.running || model.active != "help" || strings.Contains(strings.Join(model.lines, "\n"), "stale output") {
		t.Fatal("old messages changed the next command")
	}
	model.update(run("help", model.commandID)())
	if model.running {
		t.Fatal("next command failed to complete")
	}
}

func TestREPLShutdownUnblocksDiagnosticWriter(t *testing.T) {
	withConfigDir(t)
	model := newTUI()
	model.task = newREPLTask(1, func(ctx context.Context, out io.Writer) error {
		for {
			if _, err := fmt.Fprintln(out, "finding"); err != nil {
				return err
			}
		}
	})
	model.task.next()
	done := make(chan struct{})
	go func() {
		model.shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown left a diagnostic writer blocked")
	}
}
