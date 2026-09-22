package main

import (
	"fmt"
	"math"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/audio"
)

type replEqualizer struct {
	open    bool
	cursor  int
	config  equalizerConfig
	sending bool
	request uint64
	done    chan struct{}
	note    string
}

type equalizerResultMsg struct {
	request uint64
	sent    equalizerConfig
	err     error
}

func (t *tui) openEqualizer() {
	t.closeProviders()
	t.closeAudio()
	// An in-flight save owns the newest curve even if disk and the next status
	// poll still describe the prior one. Reopening must not roll it back.
	if t.eq.sending {
		t.eq.open = true
		t.viz.focused, t.viz.fullscreen = false, false
		t.sel, t.flashing, t.dragging = selection{}, false, false
		return
	}
	settings, err := loadPlaybackSettings()
	if err != nil {
		settings = defaultPlaybackSettings()
		t.eq.note = err.Error()
	} else {
		t.eq.note = ""
	}
	cfg := settings.equalizer()
	if s := t.status; s != nil && s.EQPreset != "" {
		cfg.Preset = s.EQPreset
		if strings.EqualFold(s.EQPreset, customEqualizerPreset) {
			cfg.Custom = s.EQBands
		}
	}
	t.eq.config = normalizeEqualizerConfig(cfg)
	t.eq.open = true
	t.viz.focused, t.viz.fullscreen = false, false
	t.sel, t.flashing, t.dragging = selection{}, false, false
}

func (t *tui) closeEqualizer() {
	t.eq.open = false
}

func (t *tui) beginEqualizerSave() tea.Cmd {
	if t.eq.sending {
		return nil
	}
	t.eq.sending = true
	t.eq.request++
	id, sent := t.eq.request, t.eq.config
	done := make(chan struct{})
	t.eq.done = done
	result := make(chan equalizerResultMsg, 1)
	go func() {
		defer close(done)
		result <- equalizerResultMsg{request: id, sent: sent, err: clientSetEqualizerState(sent)}
	}()
	return func() tea.Msg {
		return <-result
	}
}

func (t *tui) setEqualizerConfig(next equalizerConfig) tea.Cmd {
	next = normalizeEqualizerConfig(next)
	if next == t.eq.config {
		return nil
	}
	t.eq.config = next
	t.eq.note = ""
	return t.beginEqualizerSave()
}

func (t *tui) equalizerResult(msg equalizerResultMsg) tea.Cmd {
	if msg.request != t.eq.request {
		return nil
	}
	t.eq.sending = false
	t.eq.done = nil
	if msg.err != nil {
		t.eq.note = msg.err.Error()
	} else {
		t.eq.note = ""
	}
	if t.eq.config != msg.sent {
		return t.beginEqualizerSave()
	}
	return refreshStatus
}

func (t *tui) equalizerKey(msg tea.KeyPressMsg) tea.Cmd {
	keyName := t.presentation.mapKey("equalizer", msg.String())
	switch keyName {
	case "ctrl+q":
		return tea.Quit
	case "f4", "esc", "enter":
		t.closeEqualizer()
		return nil
	case "f2":
		t.closeEqualizer()
		t.visualizerCommand("")
		return nil
	case "f3":
		t.closeEqualizer()
		return t.openPodcasts("")
	case "f1":
		t.closeEqualizer()
		return t.promptKey(msg)
	case "left", "h":
		t.eq.cursor = max(0, t.eq.cursor-1)
	case "right", "l":
		t.eq.cursor = min(audio.EqualizerBandCount-1, t.eq.cursor+1)
	case "up", "k", "down", "j", "0":
		bands := t.eq.config.activeBands()
		gain := bands[t.eq.cursor]
		switch keyName {
		case "up", "k":
			gain++
		case "down", "j":
			gain--
		case "0":
			gain = 0
		}
		gain = audio.ClampEqualizerGain(gain)
		bands[t.eq.cursor] = gain
		return t.setEqualizerConfig(equalizerConfig{Preset: customEqualizerPreset, Custom: bands})
	case "e":
		return t.setEqualizerConfig(equalizerCycle(t.eq.config, 1))
	case "E", "shift+e":
		return t.setEqualizerConfig(equalizerCycle(t.eq.config, -1))
	case "r":
		next := t.eq.config
		next.Preset = equalizerPresets[0].Name
		return t.setEqualizerConfig(next)
	case "c":
		next := t.eq.config
		next.Preset = customEqualizerPreset
		return t.setEqualizerConfig(next)
	case "space":
		if !t.running {
			return t.start("toggle")
		}
	}
	return nil
}

func equalizerCell(text string, width int, style lipgloss.Style) string {
	text = ansi.Truncate(text, width, "")
	left := max(0, (width-lipgloss.Width(text))/2)
	right := max(0, width-lipgloss.Width(text)-left)
	return style.Render(strings.Repeat(" ", left) + text + strings.Repeat(" ", right))
}

func (t *tui) equalizerFaders(height int) []string {
	width := max(1, t.width/audio.EqualizerBandCount)
	chartRows := max(1, height-2)
	bands := t.eq.config.activeBands()
	lines := make([]string, 0, height)
	for row := range chartRows {
		line := ""
		for band, gain := range bands {
			position := int(math.Round((audio.EqualizerMaxGain - gain) * float64(chartRows-1) / (audio.EqualizerMaxGain - audio.EqualizerMinGain)))
			glyph := "│"
			if row == position {
				glyph = "●"
			} else if row == int(math.Round(audio.EqualizerMaxGain*float64(chartRows-1)/(audio.EqualizerMaxGain-audio.EqualizerMinGain))) {
				glyph = "─"
			}
			style := styleDim
			if band == t.eq.cursor {
				style = stylePrompt
			}
			line += equalizerCell(glyph, width, style)
		}
		lines = append(lines, ansi.Truncate(line, t.width, ""))
	}
	labels, gains := "", ""
	for i, label := range equalizerBandLabels {
		style := styleDim
		if i == t.eq.cursor {
			style = styleSelection
		}
		labels += equalizerCell(label, width, style)
		gains += equalizerCell(formatEqualizerGain(bands[i]), width, style)
	}
	return append(lines, ansi.Truncate(labels, t.width, ""), ansi.Truncate(gains, t.width, ""))
}

func (t *tui) equalizerList(height int) []string {
	bands := t.eq.config.activeBands()
	first := max(0, min(t.eq.cursor-height/2, audio.EqualizerBandCount-height))
	last := min(audio.EqualizerBandCount, first+height)
	lines := make([]string, 0, height)
	for i := first; i < last; i++ {
		marker, style := "  ", styleDim
		if i == t.eq.cursor {
			marker, style = "❯ ", styleSelection
		}
		barWidth := max(1, t.width-24)
		zero := barWidth / 2
		position := int(math.Round((bands[i] - audio.EqualizerMinGain) * float64(barWidth-1) / (audio.EqualizerMaxGain - audio.EqualizerMinGain)))
		bar := make([]rune, barWidth)
		for j := range bar {
			bar[j] = '─'
		}
		bar[zero], bar[max(0, min(barWidth-1, position))] = '┼', '●'
		line := fmt.Sprintf("%s%5sHz  %s dB  %s", marker, equalizerBandLabels[i], formatEqualizerGain(bands[i]), string(bar))
		lines = append(lines, style.Render(ansi.Truncate(line, t.width, "")))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

func (t *tui) equalizerView() tea.View {
	var view tea.View
	view.AltScreen = true
	if t.width == 0 || t.height == 0 {
		return view
	}
	state := "saved"
	if t.eq.sending {
		state = "saving…"
	}
	if t.eq.note != "" {
		state = "error: " + t.eq.note
	}
	heading := styleHeading.Render("equalizer") + styleDim.Render("  preset ") + styleCommand.Render("["+t.eq.config.Preset+"]") + styleDim.Render("  "+state)
	heading = ansi.Truncate(heading, t.width, "")
	if t.height == 1 {
		view.SetContent(heading)
		return view
	}
	footer := ""
	if t.presentation.ShowHelp && interfaceLayoutTier(t.width, t.height, true, t.presentation.Simplified) != "minimal" {
		hints := []string{t.presentation.bindingHint("equalizer.right", "band"), t.presentation.bindingHint("equalizer.raise", "gain"), t.presentation.bindingHint("equalizer.next-preset", "preset"), t.presentation.bindingHint("equalizer.zero", "zero"), t.presentation.bindingHint("equalizer.flat", "flat"), t.presentation.bindingHint("equalizer.custom", "custom"), t.presentation.bindingHint("equalizer.pause", "pause"), t.presentation.bindingHint("equalizer.close", "prompt")}
		footer = styleDim.Render(ansi.Truncate(strings.Join(hints, " · "), t.width, ""))
	}
	footerRows := 0
	if footer != "" {
		footerRows++
	}
	status := t.statusBar()
	if status != "" {
		footerRows++
	}
	if footerRows >= t.height {
		footer = ""
		footerRows--
	}
	bodyHeight := max(0, t.height-1-footerRows)
	var body []string
	if t.width >= 50 && bodyHeight >= 7 {
		body = t.equalizerFaders(bodyHeight)
	} else {
		body = t.equalizerList(bodyHeight)
	}
	content := append([]string{heading}, body...)
	if footer != "" {
		content = append(content, footer)
	}
	if status != "" {
		content = append(content, status)
	}
	view.SetContent(strings.Join(content, "\n"))
	return view
}
