package main

import (
	"net"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/audio"
)

func vizKey(s string) tea.KeyPressMsg {
	if s == "esc" {
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

func TestVisualizerFocusPreservesPrompt(t *testing.T) {
	withConfigDir(t)
	tui := newTUI()
	tui.width, tui.height = 100, 30
	for _, s := range []string{"v", "o", "l"} {
		tui.update(vizKey(s))
	}
	if tui.input.Value() != "vol" || tui.viz.enabled {
		t.Fatal("visualizer stole normal command typing")
	}
	tui.promptKey(tea.KeyPressMsg{Code: tea.KeyF2})
	if !tui.viz.enabled || !tui.viz.focused {
		t.Fatal("F2 did not open focused visualizer")
	}
	tui.update(vizKey("v"))
	if tui.viz.mode != 1 || tui.input.Value() != "vol" {
		t.Fatal("cycle changed prompt")
	}
	tui.update(vizKey("V"))
	if !tui.viz.fullscreen {
		t.Fatal("V did not enter fullscreen")
	}
	tui.update(vizKey("esc"))
	if tui.viz.fullscreen || !tui.viz.focused {
		t.Fatal("first Esc did not restore compact mode")
	}
	tui.update(vizKey("esc"))
	if tui.viz.focused || tui.input.Value() != "vol" {
		t.Fatal("Esc did not restore draft")
	}
	tui.setInput("viz led")
	tui.submit()
	if !strings.Contains(tui.visualizerView(8), "led") {
		t.Fatal("named mode not selected")
	}
	tui.visualizerCommand("off")
	if tui.viz.enabled || tui.viz.focused {
		t.Fatal("off retained focus")
	}
}

func TestVisualizerLayoutAndCursor(t *testing.T) {
	withConfigDir(t)
	tui := newTUI()
	tui.width, tui.height = 100, 30
	tui.visualizerCommand("")
	tui.viz.focused = false
	tui.fit()
	view := tui.View()
	if view.Cursor == nil || view.Cursor.Y != tui.height-2 {
		t.Fatalf("compact cursor moved off prompt: %+v", view.Cursor)
	}
	if strings.Count(view.Content, "\n")+1 != tui.height {
		t.Fatal("compact layout overflows terminal")
	}
	tui.viz.fullscreen, tui.viz.focused = true, true
	view = tui.View()
	if view.Cursor != nil || strings.Count(view.Content, "\n")+1 != tui.height {
		t.Fatal("fullscreen cursor/layout incorrect")
	}
	if !strings.Contains(ansi.Strip(view.Content), "V fullscreen") {
		t.Fatal("fullscreen controls undiscoverable")
	}
	for _, size := range [][2]int{{1, 1}, {10, 5}, {60, 12}, {600, 200}} {
		tui.width, tui.height = size[0], size[1]
		tui.fit()
		view := tui.View() // resize must not panic, including tiny terminals
		if size[1] >= 10 && strings.Count(view.Content, "\n")+1 != size[1] {
			t.Fatal("oversized fullscreen lost pinned status bar")
		}
	}
	tui.width, tui.height = 80, 18
	tui.viz.fullscreen, tui.viz.focused = false, false
	tui.setInput("viz ")
	tui.fit()
	if strings.Count(tui.View().Content, "\n")+1 != tui.height {
		t.Fatal("suggestions and compact visualizer overflowed screen")
	}
}

func TestVisualizerHidingClosesAndRejectsLateConnections(t *testing.T) {
	withConfigDir(t)
	tui := newTUI()
	tui.width, tui.height = 100, 30
	tui.visualizerCommand("")
	client, server := net.Pipe()
	defer server.Close()
	tui.viz.stream = &visualizerStream{conn: client, id: tui.viz.id}
	oldID := tui.viz.id
	tui.help = true
	tui.syncVisualizer()
	if tui.viz.stream != nil || tui.viz.id == oldID {
		t.Fatal("help retained subscription")
	}
	late, peer := net.Pipe()
	defer peer.Close()
	tui.visualizerMessage(visualizerConnectedMsg{id: oldID, stream: &visualizerStream{conn: late}})
	peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("late connection not closed")
	}
	if tui.viz.stream != nil {
		t.Fatal("late result revived subscription")
	}
}

func TestVisualizerPausedAndStaleFramesClear(t *testing.T) {
	withConfigDir(t)
	tui := newTUI()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	tui.viz.enabled = true
	tui.viz.stream = &visualizerStream{conn: client}
	f := audio.Frame{At: time.Now(), Sequence: 1, Peak: [2]float64{0.9, 0.2}}
	tui.visualizerMessage(visualizerFrameMsg{packet: visualizerPacket{State: "playing", Frame: f}})
	if tui.viz.renderer.Frame.Peak[0] == 0 {
		t.Fatal("live frame not shown")
	}
	tui.visualizerMessage(visualizerFrameMsg{packet: visualizerPacket{State: "paused"}})
	if tui.viz.renderer.Frame != (audio.Frame{}) {
		t.Fatal("paused view retained signal")
	}
	f.At = time.Now().Add(-time.Second)
	tui.visualizerMessage(visualizerFrameMsg{packet: visualizerPacket{State: "playing", Frame: f}})
	if tui.viz.state != "buffering" || tui.viz.renderer.Frame != (audio.Frame{}) {
		t.Fatal("stale view retained signal")
	}
}
