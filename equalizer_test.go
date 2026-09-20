package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/willibrandon/chill/internal/audio"
)

// TestEqualizerPresetCatalog checks the complete, stable built-in preset list.
func TestEqualizerPresetCatalog(t *testing.T) {
	want := []string{"Flat", "Rock", "Pop", "Jazz", "Classical", "Bass Boost", "Treble Boost", "Vocal", "Electronic", "Acoustic", "Hip-Hop", "R&B", "Loudness", "Late Night", "Podcast", "Small Speakers"}
	got := make([]string, len(equalizerPresets))
	for i, preset := range equalizerPresets {
		got[i] = preset.Name
		if found, ok := equalizerPresetByName(strings.ToLower(strings.ReplaceAll(preset.Name, " ", "-"))); !ok || found != preset {
			t.Errorf("normalized lookup failed for %q", preset.Name)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preset catalog = %v, want %v", got, want)
	}
}

// TestEqualizerCustomCurveSurvivesPresetChanges checks built-ins never overwrite Custom.
func TestEqualizerCustomCurveSurvivesPresetChanges(t *testing.T) {
	cfg := defaultEqualizerConfig()
	rock, changed, err := updateEqualizerConfig(cfg, "rock")
	if err != nil || !changed || rock.Preset != "Rock" {
		t.Fatalf("select Rock: %+v, %v", rock, err)
	}
	custom, changed, err := updateEqualizerConfig(rock, "--band 1k +7")
	if err != nil || !changed || custom.Preset != customEqualizerPreset || custom.Custom[4] != 7 || custom.Custom[0] != 5 {
		t.Fatalf("edit Rock into Custom: %+v, %v", custom, err)
	}
	jazz, _, err := updateEqualizerConfig(custom, "Jazz")
	if err != nil || jazz.Custom != custom.Custom {
		t.Fatalf("built-in discarded Custom: %+v, %v", jazz, err)
	}
	restored, _, err := updateEqualizerConfig(jazz, "Custom")
	if err != nil || restored.activeBands() != custom.Custom {
		t.Fatalf("Custom was not restored: %+v, %v", restored, err)
	}
}

// TestEqualizerCycleIncludesCustom checks preset navigation wraps through Custom.
func TestEqualizerCycleIncludesCustom(t *testing.T) {
	cfg := equalizerConfig{Preset: equalizerPresets[len(equalizerPresets)-1].Name}
	if next := equalizerCycle(cfg, 1); next.Preset != customEqualizerPreset {
		t.Fatalf("last built-in cycled to %q", next.Preset)
	}
	if next := equalizerCycle(equalizerConfig{Preset: customEqualizerPreset}, 1); next.Preset != "Flat" {
		t.Fatalf("Custom cycled to %q", next.Preset)
	}
	if next := equalizerCycle(defaultEqualizerConfig(), -1); next.Preset != customEqualizerPreset {
		t.Fatalf("reverse Flat cycled to %q", next.Preset)
	}
}

// TestEqualizerCommandValidation checks band selectors and invalid command values.
func TestEqualizerCommandValidation(t *testing.T) {
	for _, input := range []string{"--band 10 2", "--band nope 2", "--band 0 13", "--band 0 NaN", "missing", "Rock\nstop"} {
		if _, _, err := updateEqualizerConfig(defaultEqualizerConfig(), input); err == nil {
			t.Errorf("updateEqualizerConfig accepted %q", input)
		}
	}
	for input, band := range map[string]int{"0": 0, "70": 0, "70Hz": 0, "1k": 4, "1000": 4, "16kHz": 9} {
		if got, err := equalizerBandIndex(input); err != nil || got != band {
			t.Errorf("equalizerBandIndex(%q) = %d, %v; want %d", input, got, err, band)
		}
	}
	decimal, _, err := updateEqualizerConfig(defaultEqualizerConfig(), "--band 1k 2.5")
	if err != nil || !strings.Contains(formatEqualizer(decimal), "1kHz +2.5") {
		t.Fatalf("decimal gain was not preserved: %s, %v", formatEqualizer(decimal), err)
	}
}

// TestPlaybackSettingsMigrateAndPreserveEqualizer checks state migration and merged writes.
func TestPlaybackSettingsMigrateAndPreserveEqualizer(t *testing.T) {
	path := withConfigDir(t)
	statePath := volumePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(`{"volume":23}`), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := loadPlaybackSettings()
	if err != nil || settings.Volume != 23 || settings.EQPreset != "Flat" {
		t.Fatalf("old state migration = %+v, %v", settings, err)
	}
	settings.setEqualizer(equalizerConfig{Preset: customEqualizerPreset, Custom: audio.EqualizerBands{1, 2, 3}})
	if err := savePlaybackSettings(settings); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{volume: 23, eqPreset: settings.EQPreset, eqCustom: settings.EQBands}
	wantReply(t, d.setVolume(44), true, "44")
	loaded, err := loadPlaybackSettings()
	if err != nil || loaded.Volume != 44 || loaded.EQPreset != customEqualizerPreset || loaded.EQBands != settings.EQBands {
		t.Fatalf("volume write lost EQ: %+v, %v", loaded, err)
	}
}

// TestDaemonEqualizerChangesLiveWithoutRestart checks live DSP updates in one player.
func TestDaemonEqualizerChangesLiveWithoutRestart(t *testing.T) {
	withConfigDir(t)
	d := fakeDaemon(t)
	wantReply(t, d.execute("play", "lofi-girl"), true, "loading")
	d.mu.Lock()
	p := d.player.(*fakePlayer)
	d.mu.Unlock()
	wantReply(t, d.execute("eq", "Rock"), true, "EQ: Rock")
	rock, _ := equalizerPresetByName("Rock")
	if d.player != p || p.closed || p.eq != rock.Bands {
		t.Fatalf("live preset replaced player or applied wrong bands: %+v", p.eq)
	}
	wantReply(t, d.execute("eq", "band 4 7"), true, "Custom")
	if p.eq[4] != 7 || d.eqPreset != customEqualizerPreset {
		t.Fatalf("live band edit not applied: preset=%q bands=%v", d.eqPreset, p.eq)
	}
	before := p.eq
	wantReply(t, d.execute("eq", "band 4 NaN"), false, "invalid")
	if p.eq != before || d.player != p {
		t.Fatal("invalid EQ command changed playback")
	}
	s := daemonStatus(t, d)
	if s.EQPreset != customEqualizerPreset || s.EQBands != p.eq {
		t.Fatalf("status omitted EQ: %+v", s)
	}
	wantReply(t, d.execute("play", "sleep"), true, "loading")
	d.mu.Lock()
	restarted := d.player.(*fakePlayer)
	d.mu.Unlock()
	if restarted == p || restarted.eq != p.eq {
		t.Fatalf("replacement player lost the curve: old=%p new=%p bands=%v", p, restarted, restarted.eq)
	}
}

// TestDaemonRejectsInvalidEqualizerWireState checks the private atomic state command.
func TestDaemonRejectsInvalidEqualizerWireState(t *testing.T) {
	withConfigDir(t)
	d := &Daemon{volume: 70, eqPreset: "Flat"}
	for _, raw := range []string{
		`{}`,
		`{"preset":"missing","custom":[0,0,0,0,0,0,0,0,0,0]}`,
		`{"preset":"Custom","custom":[0]}`,
		`{"preset":"Custom","custom":[0,0,0,0,0,0,0,0,0,0,0]}`,
		`{"preset":"Custom","custom":[99,0,0,0,0,0,0,0,0,0]}`,
	} {
		wantReply(t, d.execute("eq-state", raw), false, "invalid")
	}
	if d.equalizer() != defaultEqualizerConfig() {
		t.Fatalf("invalid wire state changed EQ: %+v", d.equalizer())
	}
}

// TestNormalizeEqualizerRejectsNonFinitePersistence checks corrupt gains become safe.
func TestNormalizeEqualizerRejectsNonFinitePersistence(t *testing.T) {
	cfg := equalizerConfig{Preset: customEqualizerPreset}
	cfg.Custom[0] = math.Inf(1)
	if got := normalizeEqualizerConfig(cfg); got.Custom[0] != 0 {
		t.Fatalf("non-finite persistent band normalized to %v", got.Custom[0])
	}
}

// TestStatusJSONIncludesEqualizer checks machine-readable status exposes the active curve.
func TestStatusJSONIncludesEqualizer(t *testing.T) {
	s := Status{State: "playing", EQPreset: "Rock", EQBands: audio.EqualizerBands{5, 4}}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"eq_preset":"Rock"`, `"eq_bands":[5,4`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("status JSON %s missing %s", data, want)
		}
	}
}
