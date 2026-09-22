package main

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/visualizer"
)

type replVisualizer struct {
	enabled, focused, fullscreen bool
	mode                         int
	renderer                     visualizer.Renderer
	stream                       *visualizerStream
	id                           uint64
	connecting, waiting          bool
	generation                   uint64
	state                        string
}

func (t *tui) visualizerHeight() int {
	if !t.viz.enabled || t.help || t.eq.open || t.podcasts.open || t.presentation.Simplified || t.presentation.LowPower || t.presentation.VisualizerHeight == 0 || t.height < 10 || t.width < 20 {
		return 0
	}
	if interfaceLayoutTier(t.width, t.height, false, t.presentation.Simplified) == "minimal" {
		return 0
	}
	if t.viz.fullscreen {
		return max(0, t.height-2)
	}
	room := t.height - 3 - t.paletteHeight() - t.panelHeight() - 4
	if room < 3 {
		return 0
	}
	return min(t.presentation.VisualizerHeight, room)
}

// syncVisualizer runs after layout. There is at most one read in flight and no
// polling while hidden. IDs dispose of late connections and stale retry ticks.
func (t *tui) syncVisualizer() tea.Cmd {
	v := &t.viz
	if t.visualizerHeight() == 0 {
		v.focused, v.fullscreen = false, false
		if v.stream != nil || v.connecting || v.waiting {
			t.closeVisualizer()
		}
		return nil
	}
	if v.stream != nil || v.connecting || v.waiting {
		return nil
	}
	v.connecting = true
	return connectVisualizer(v.id)
}

func (t *tui) closeVisualizer() {
	v := &t.viz
	if v.stream != nil {
		v.stream.conn.Close()
	}
	v.stream, v.connecting, v.waiting = nil, false, false
	v.id++
	v.renderer.Reset()
}

func (t *tui) visualizerMessage(msg tea.Msg) tea.Cmd {
	v := &t.viz
	retry := func() tea.Cmd {
		v.state = "waiting for playback"
		v.renderer.Reset()
		v.waiting = true
		id := v.id
		return tea.Tick(time.Second, func(time.Time) tea.Msg { return visualizerRetryMsg(id) })
	}
	switch msg := msg.(type) {
	case visualizerConnectedMsg:
		if msg.id != v.id || !v.enabled {
			if msg.stream != nil {
				msg.stream.conn.Close()
			}
			return nil
		}
		v.connecting = false
		if msg.err != nil {
			return retry()
		}
		v.stream = msg.stream
		return v.stream.next
	case visualizerFrameMsg:
		if msg.id != v.id || v.stream == nil {
			return nil
		}
		if msg.err != nil {
			v.stream.conn.Close()
			v.stream = nil
			return retry()
		}
		p := msg.packet
		if p.Generation != v.generation || p.State != v.state {
			v.renderer.Reset()
		}
		v.generation, v.state = p.Generation, p.State
		if p.State == "playing" {
			if p.Frame.At.IsZero() || time.Since(p.Frame.At) > 500*time.Millisecond {
				v.renderer.Reset()
				v.state = "buffering"
			} else {
				v.renderer.Advance(p.Frame, time.Now())
			}
		}
		return v.stream.next
	case visualizerRetryMsg:
		if uint64(msg) == v.id {
			v.waiting = false
		}
	}
	return nil
}

func (t *tui) visualizerCommand(arg string) {
	v := &t.viz
	switch arg {
	case "list":
		t.print(styleDim.Render("  " + strings.Join(visualizer.Modes, ", ")))
		return
	case "off":
		v.enabled, v.focused, v.fullscreen = false, false, false
		t.closeVisualizer()
		return
	case "", "on":
	case "next":
		v.mode = (v.mode + 1) % len(visualizer.Modes)
	case "prev":
		v.mode = (v.mode + len(visualizer.Modes) - 1) % len(visualizer.Modes)
	case "fullscreen":
		v.fullscreen = !v.fullscreen
	default:
		i := visualizer.Index(arg)
		if i < 0 {
			t.print(styleError.Render("  unknown visualizer; use viz list, viz <mode>, or viz off"))
			return
		}
		v.mode = i
	}
	if t.presentation.Simplified || t.presentation.LowPower {
		v.enabled, v.focused, v.fullscreen = false, false, false
		t.setPresentationModeWarning()
		return
	}
	v.enabled, v.focused = true, true
	if t.width > 0 && t.height > 0 && t.visualizerHeight() == 0 {
		v.focused, v.fullscreen = false, false
		t.print(styleDim.Render("  visualizer unavailable at this size or configured height"))
	}
	t.sel, t.flashing, t.dragging = selection{}, false, false
}

func (t *tui) visualizerKey(msg tea.KeyPressMsg) tea.Cmd {
	v := &t.viz
	switch t.presentation.mapKey("visualizer", msg.String()) {
	case "f3":
		return t.openPodcasts("")
	case "f4":
		t.openEqualizer()
		return nil
	case "ctrl+q":
		return tea.Quit
	case "ctrl+c":
		if !t.cancelCommand() {
			v.focused, v.fullscreen = false, false
		}
	case "esc":
		if v.fullscreen {
			v.fullscreen = false
		} else {
			v.focused = false
		}
	case "enter", "f2":
		v.focused, v.fullscreen = false, false
	case "v", "right":
		v.mode = (v.mode + 1) % len(visualizer.Modes)
	case "left":
		v.mode = (v.mode + len(visualizer.Modes) - 1) % len(visualizer.Modes)
	case "V", "shift+v":
		v.fullscreen = !v.fullscreen
	case "o":
		t.visualizerCommand("off")
	case "f1":
		return t.promptKey(msg)
	case "space":
		if !t.running {
			return t.start("toggle")
		}
	}
	return nil
}

func (t *tui) visualizerView(height int) string {
	v := &t.viz
	heading := fmt.Sprintf(" %s · %d/%d · post-EQ audio", visualizer.Modes[v.mode], v.mode+1, len(visualizer.Modes))
	if v.focused && !v.fullscreen && t.presentation.ShowHelp {
		heading += " · " + t.presentation.bindingHint("visualizer.next", "next") + " · " + t.presentation.bindingHint("visualizer.fullscreen", "full") + " · " + t.presentation.bindingHint("visualizer.back", "prompt")
	}
	if v.state != "playing" {
		state := v.state
		if state == "" {
			state = "connecting"
		}
		heading += " · " + state
	}
	heading = ansi.Truncate(heading, t.width, "")
	style := styleDim
	if v.focused {
		style = styleSelected
	}
	if height <= 1 {
		return style.Render(heading)
	}
	body := v.renderer.Render(visualizer.Modes[v.mode], t.width, height-1)
	// Renderers cap their work; letterbox oversized terminals so the status bar
	// still stays pinned at the physical bottom of fullscreen mode.
	body = lipgloss.NewStyle().Width(t.width).Height(height - 1).Render(body)
	return style.Render(heading) + "\n" + body
}

func (t *tui) visualizerFooter() string {
	if !t.presentation.ShowHelp {
		return ""
	}
	hints := []string{t.presentation.bindingHint("visualizer.next", "next"), t.presentation.bindingHint("visualizer.previous", "previous"), t.presentation.bindingHint("visualizer.fullscreen", "fullscreen"), t.presentation.bindingHint("visualizer.pause", "pause"), t.presentation.bindingHint("visualizer.back", "back"), t.presentation.bindingHint("visualizer.disable", "off"), t.presentation.bindingHint("global.quit", "quit")}
	return styleDim.Render(ansi.Truncate(" "+strings.Join(hints, " · "), t.width, ""))
}
