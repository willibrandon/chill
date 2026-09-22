package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/audio"
)

// TestForegroundEqualizerControlsPersist checks foreground edits use shared state.
func TestForegroundEqualizerControlsPersist(t *testing.T) {
	withConfigDir(t)
	m := &foregroundModel{
		station:  &Station{Name: "test", Desc: "Test audio"},
		player:   &pcmPlayer{output: &mpvPlayer{}, equalizer: audio.NewEqualizer(audio.SampleRate)},
		settings: defaultPlaybackSettings(),
		eq:       equalizerConfig{Preset: "Rock"},
		state:    "playing",
	}
	m.Update(vizKey("l"))
	m.Update(vizKey("j"))
	if m.eqCursor != 1 || m.eq.Preset != customEqualizerPreset || m.eq.Custom[1] != 3 {
		t.Fatalf("foreground edit = cursor %d, eq %+v", m.eqCursor, m.eq)
	}
	settings, err := loadPlaybackSettings()
	if err != nil || settings.EQPreset != customEqualizerPreset || settings.EQBands != m.eq.Custom {
		t.Fatalf("foreground curve was not shared: %+v, %v", settings, err)
	}
}

// TestForegroundLayoutIsBounded checks narrow terminals and the visible curve.
func TestForegroundLayoutIsBounded(t *testing.T) {
	m := &foregroundModel{
		station:  &Station{Name: "test", Desc: strings.Repeat("long ", 30)},
		player:   &pcmPlayer{output: &mpvPlayer{}},
		settings: defaultPlaybackSettings(),
		eq:       equalizerConfig{Preset: "Rock"},
		state:    "playing",
	}
	for _, size := range [][2]int{{1, 1}, {12, 3}, {40, 7}, {120, 20}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		lines := strings.Split(view.Content, "\n")
		if len(lines) != size[1] {
			t.Fatalf("foreground at %dx%d rendered %d lines", size[0], size[1], len(lines))
		}
		for _, line := range lines {
			if width := ansi.StringWidth(line); width > size[0] {
				t.Fatalf("foreground at %dx%d rendered a %d-cell line", size[0], size[1], width)
			}
		}
	}
	m.width, m.height = 120, 20
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "EQ [Rock]") || !strings.Contains(plain, "h/l band") {
		t.Fatalf("foreground controls are not discoverable: %q", plain)
	}
}

// TestForegroundStatusContentUsesInterfaceProfile checks shared status settings.
func TestForegroundStatusContentUsesInterfaceProfile(t *testing.T) {
	settings := defaultInterfaceSettings()
	settings.StatusFields = []string{"volume"}
	if err := normalizeInterfaceSettings(&settings); err != nil {
		t.Fatal(err)
	}
	m := &foregroundModel{
		station: &Station{Name: "test", Desc: "Test audio"}, player: &pcmPlayer{output: &mpvPlayer{}},
		settings: defaultPlaybackSettings(), eq: equalizerConfig{Preset: "Flat"}, state: "playing",
		presentation: settings, width: 80, height: 12, muted: true,
	}
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "vol 70 · muted") || strings.Contains(plain, "playing · 0:00") {
		t.Fatalf("foreground status fields were ignored: %q", plain)
	}
	m.presentation.ShowStatus = false
	if plain = ansi.Strip(m.View().Content); strings.Contains(plain, "vol 70") {
		t.Fatalf("foreground status remained visible: %q", plain)
	}
}

// TestForegroundQueuePreservesPlaybackModes checks finite-media status parity.
func TestForegroundQueuePreservesPlaybackModes(t *testing.T) {
	m := &foregroundMediaModel{
		items: []MediaItem{{Kind: MediaTrack, ID: "track", Title: "Track"}}, player: &pcmPlayer{output: &mpvPlayer{}},
		settings: defaultPlaybackSettings(), eq: equalizerConfig{Preset: "Flat"}, state: "playing", rate: 1,
		presentation: defaultInterfaceSettings(), width: 120, height: 20, muted: true, shuffle: true, repeat: "all",
	}
	plain := ansi.Strip(m.View().Content)
	for _, indicator := range []string{"shuffle", "repeat all", "muted"} {
		if !strings.Contains(plain, indicator) {
			t.Fatalf("foreground queue omitted %q: %q", indicator, plain)
		}
	}
}
