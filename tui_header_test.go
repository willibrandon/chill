package main

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestREPLHeaderSurvivesScrolling keeps the welcome and shortcuts above long output.
func TestREPLHeaderSurvivesScrolling(t *testing.T) {
	withConfigDir(t)
	model := newTUI()
	model.presentation = defaultInterfaceSettings()
	model.width, model.height = 180, 40
	model.fit()
	header := ansi.Strip(strings.Join(model.replHeader(), "\n"))
	for _, hint := range []string{"chill  type a station, path, or command", "F1 help", "F2 visualizer", "F3 podcasts", "F4 equalizer", "F5 radio", "F6 lyrics", "F7 library", "F8 providers", "F9 audio", "F10 interface"} {
		if !strings.Contains(ansi.Strip(header), hint) {
			t.Fatalf("header omitted %q", hint)
		}
	}
	checkHeader := func() {
		t.Helper()
		if !strings.HasPrefix(ansi.Strip(model.View().Content), header+"\n") {
			t.Fatal("scrolling output displaced the header")
		}
	}
	for line := range strings.SplitSeq(replHelp(), "\n") {
		model.print(line)
	}
	if model.viewport.YOffset() == 0 {
		t.Fatal("help did not fill the transcript")
	}
	checkHeader()
	for _, key := range []rune{tea.KeyPgUp, tea.KeyPgUp, tea.KeyPgDown} {
		model.Update(tea.KeyPressMsg{Code: key})
		checkHeader()
	}
	model.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	checkHeader()
	model.viewport.GotoTop()
	checkHeader()
	model.viewport.GotoBottom()
	checkHeader()

	// History pruning must not remove the header along with old command output.
	for index := range maxTranscript + 1 {
		model.print(fmt.Sprintf("output %d", index))
	}
	checkHeader()
	if !strings.Contains(ansi.Strip(model.View().Content), "output 1000") {
		t.Fatal("latest output is no longer visible")
	}
	if len(model.lines) != maxTranscript {
		t.Fatalf("history retained %d lines", len(model.lines))
	}
	model.clear()
	checkHeader()
	if len(model.lines) != 0 || len(model.rows) != 0 {
		t.Fatal("clear left content in the transcript")
	}
}

// TestREPLHeaderFitsResizedLayouts keeps both fixed bars and the cursor on screen.
func TestREPLHeaderFitsResizedLayouts(t *testing.T) {
	withConfigDir(t)
	for _, mode := range []string{"default", "suggestions", "panels and visualizer", "no status", "no hints", "simplified", "mode warning"} {
		t.Run(mode, func(t *testing.T) {
			model := newTUI()
			model.presentation = defaultInterfaceSettings()
			switch mode {
			case "panels and visualizer":
				for _, panel := range interfacePanelNames() {
					model.presentation.Panels[panel] = true
				}
				model.viz.enabled = true
				model.setInput("p")
			case "suggestions":
				model.setInput("p")
			case "no status":
				model.presentation.ShowStatus = false
			case "no hints":
				model.presentation.ShowHelp = false
			case "simplified":
				model.presentation.Simplified = true
			case "mode warning":
				model.modeNote = "visualizer disabled in low-power mode"
			}
			for index := range 100 {
				model.print(fmt.Sprintf("output %d", index))
			}
			for _, size := range [][2]int{{180, 40}, {96, 24}, {80, 18}, {60, 16}, {40, 10}, {180, 10}, {100, 30}, {180, 40}} {
				model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				view := model.View()
				rows := strings.Split(ansi.Strip(view.Content), "\n")
				if len(rows) != size[1] {
					t.Fatalf("%dx%d: rendered %d rows", size[0], size[1], len(rows))
				}
				if !strings.HasPrefix(rows[0], "chill  type a station, path, or command") {
					t.Fatalf("%dx%d: lost the welcome header: %q", size[0], size[1], rows[0])
				}
				for index, row := range rows {
					if ansi.StringWidth(row) > size[0] {
						t.Fatalf("%dx%d: row %d overflows: %q", size[0], size[1], index, row)
					}
				}
				promptRow := size[1] - 1
				if model.presentation.ShowStatus {
					promptRow--
				}
				if view.Cursor == nil || view.Cursor.Y != promptRow || !strings.HasPrefix(rows[promptRow], "chill> ") {
					t.Fatalf("%dx%d: prompt or cursor moved: %+v, %q", size[0], size[1], view.Cursor, rows[promptRow])
				}
			}
		})
	}
}

// TestREPLHeaderMouseSelection maps clicks and drags to output below the header.
func TestREPLHeaderMouseSelection(t *testing.T) {
	withConfigDir(t)
	model := newTUI()
	model.width, model.height = 100, 30
	model.fit()
	for index := range 100 {
		model.print(fmt.Sprintf("output %d", index))
	}
	model.viewport.SetYOffset(5)
	top := model.headerHeight()
	model.mouse(tea.MouseClickMsg{X: 0, Y: top + 1, Button: tea.MouseLeft})
	model.mouse(tea.MouseMotionMsg{X: 7, Y: top + 2, Button: tea.MouseLeft})
	if text := model.selectedText(); text != "output 6\noutput 7" {
		t.Fatalf("mouse selected the wrong output: %q", text)
	}
	model.mouse(tea.MouseMotionMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if model.viewport.YOffset() != 4 || model.sel.cursor.row != 4 {
		t.Fatalf("dragging above the transcript did not scroll to its first visible row: %+v", model.sel)
	}
	for y := range top {
		model.mouse(tea.MouseClickMsg{X: 0, Y: y, Button: tea.MouseLeft})
		if model.dragging || model.sel.active {
			t.Fatalf("header row %d started a transcript selection", y)
		}
	}
	if _, ok := model.cellAt(0, model.transcriptHeight()); ok {
		t.Fatal("click below the transcript selected output")
	}
}

// TestREPLHeaderLongBindings bounds valid alternative key hints without pushing
// the transcript, prompt, or status bar outside a supported terminal size.
func TestREPLHeaderLongBindings(t *testing.T) {
	withConfigDir(t)
	settings := defaultInterfaceSettings()
	for index, action := range []string{"global.help", "global.interface", "global.keys"} {
		for alternative := range 3 {
			settings.Bindings[action] = append(settings.Bindings[action], fmt.Sprintf("ctrl+alt+shift+f%d", 11+index*3+alternative))
		}
	}
	if err := normalizeInterfaceSettings(&settings); err != nil {
		t.Fatalf("remapping fixture is invalid: %v", err)
	}
	for _, size := range [][2]int{{40, 10}, {40, 11}, {60, 16}, {96, 24}, {180, 40}} {
		for _, warning := range []string{"", "visualizer disabled in low-power mode"} {
			for _, status := range []bool{true, false} {
				model := newTUI()
				model.presentation = settings
				model.presentation.ShowStatus = status
				model.width, model.height = size[0], size[1]
				model.modeNote = warning
				model.viz.enabled = true
				model.setInput("p")
				model.print("latest output")
				model.fit()
				view := model.View()
				rows := strings.Split(ansi.Strip(view.Content), "\n")
				if len(rows) != model.height {
					t.Fatalf("%dx%d with warning %q: %d rows overflow the terminal", size[0], size[1], warning, len(rows))
				}
				for _, row := range rows {
					if ansi.StringWidth(row) > model.width {
						t.Fatalf("header wrapped outside the window: %q", row)
					}
				}
				promptY := model.height - 1
				if status {
					promptY--
				}
				if view.Cursor == nil || view.Cursor.Y != promptY || !strings.HasPrefix(rows[promptY], "chill> ") {
					t.Fatalf("%dx%d: prompt cursor moved outside its row: %+v", size[0], size[1], view.Cursor)
				}
				if !strings.HasPrefix(rows[0], "chill  type a station, path, or command") || !strings.Contains(view.Content, "latest output") {
					t.Fatal("long hints hid the welcome message or transcript")
				}
				if size == [2]int{40, 10} && !strings.Contains(strings.Join(model.replHeader(), "\n"), "…") {
					t.Fatal("shortened hints did not indicate omitted alternatives")
				}
			}
		}
	}
}
