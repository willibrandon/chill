package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestEqualizerPanelPreservesPromptAndCoalescesSaves checks focus and live edits.
func TestEqualizerPanelPreservesPromptAndCoalescesSaves(t *testing.T) {
	withConfigDir(t)
	m := newTUI()
	m.width, m.height = 100, 24
	m.setInput("vol 50")
	m.promptKey(tea.KeyPressMsg{Code: tea.KeyF4})
	if !m.eq.open || m.input.Value() != "vol 50" || m.eq.config.Preset != "Flat" {
		t.Fatalf("opening panel lost state: open=%v prompt=%q eq=%+v", m.eq.open, m.input.Value(), m.eq.config)
	}

	first := m.equalizerKey(vizKey("k"))
	if first == nil || !m.eq.sending || m.eq.config.Preset != customEqualizerPreset || m.eq.config.Custom[0] != 1 {
		t.Fatalf("first live edit = %+v", m.eq)
	}
	if next := m.equalizerKey(vizKey("k")); next != nil || m.eq.config.Custom[0] != 2 {
		t.Fatalf("rapid edit was not coalesced: %+v", m.eq)
	}
	m.closeEqualizer()
	m.openEqualizer()
	if m.eq.config.Custom[0] != 2 {
		t.Fatalf("reopening during a save rolled the curve back: %+v", m.eq.config)
	}
	result, ok := first().(equalizerResultMsg)
	if !ok || result.err != nil {
		t.Fatalf("saving first edit: %#v", result)
	}
	latest := m.equalizerResult(result)
	if latest == nil {
		t.Fatal("latest coalesced edit was not scheduled")
	}
	result, ok = latest().(equalizerResultMsg)
	if !ok || result.err != nil {
		t.Fatalf("saving latest edit: %#v", result)
	}
	m.equalizerResult(result)
	settings, err := loadPlaybackSettings()
	if err != nil || settings.EQPreset != customEqualizerPreset || settings.EQBands[0] != 2 {
		t.Fatalf("saved curve = %+v, %v", settings, err)
	}

	m.equalizerKey(tea.KeyPressMsg{Code: tea.KeyF4})
	if m.eq.open || m.input.Value() != "vol 50" {
		t.Fatal("closing panel did not restore the prompt draft")
	}
	m.setInput("eq")
	m.submit()
	if !m.eq.open {
		t.Fatal("bare eq command did not open the panel")
	}
}

// TestEqualizerPanelKeysAndLayout checks presets, band selection, and all sizes.
func TestEqualizerPanelKeysAndLayout(t *testing.T) {
	withConfigDir(t)
	m := newTUI()
	m.eq.open = true
	m.eq.config = equalizerConfig{Preset: "Rock"}
	m.equalizerKey(vizKey("l"))
	if m.eq.cursor != 1 {
		t.Fatal("band cursor did not move")
	}
	cmd := m.equalizerKey(vizKey("j"))
	if cmd == nil || m.eq.config.Preset != customEqualizerPreset || m.eq.config.Custom[1] != 3 {
		t.Fatalf("band edit did not derive Custom from preset: %+v", m.eq.config)
	}
	if result, ok := cmd().(equalizerResultMsg); !ok || result.err != nil {
		t.Fatalf("persisting panel edit: %#v", result)
	}

	for _, size := range [][2]int{{1, 1}, {10, 2}, {20, 4}, {49, 9}, {50, 10}, {100, 24}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		lines := strings.Split(view.Content, "\n")
		if len(lines) != size[1] {
			t.Fatalf("equalizer at %dx%d rendered %d lines", size[0], size[1], len(lines))
		}
		for _, line := range lines {
			if width := ansi.StringWidth(line); width > size[0] {
				t.Fatalf("equalizer at %dx%d rendered a %d-cell line", size[0], size[1], width)
			}
		}
	}
	m.width, m.height = 100, 24
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "equalizer") || !strings.Contains(plain, "70") || !strings.Contains(plain, "16k") {
		t.Fatalf("full panel omitted controls or bands: %q", plain)
	}
}

// TestEqualizerShutdownFlushesLatestEdit checks quitting cannot leave an older save last.
func TestEqualizerShutdownFlushesLatestEdit(t *testing.T) {
	withConfigDir(t)
	m := newTUI()
	m.eq.open = true
	m.eq.config = defaultEqualizerConfig()
	if cmd := m.equalizerKey(vizKey("k")); cmd == nil {
		t.Fatal("first edit did not begin saving")
	}
	m.equalizerKey(vizKey("k"))
	m.shutdown()
	settings, err := loadPlaybackSettings()
	if err != nil || settings.EQPreset != customEqualizerPreset || settings.EQBands[0] != 2 {
		t.Fatalf("shutdown saved %+v, %v", settings, err)
	}
}
