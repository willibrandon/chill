package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/willibrandon/chill/internal/audio"
)

const customEqualizerPreset = "Custom"

type equalizerPreset struct {
	// Name is the display and command name.
	Name string
	// Bands contains its ten gains in decibels.
	Bands audio.EqualizerBands
}

// The order is also the order used by interactive preset cycling.
var equalizerPresets = []equalizerPreset{
	{Name: "Flat", Bands: audio.EqualizerBands{0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
	{Name: "Rock", Bands: audio.EqualizerBands{5, 4, 2, -1, -2, 2, 4, 5, 5, 5}},
	{Name: "Pop", Bands: audio.EqualizerBands{-1, 2, 4, 5, 4, 1, -1, -1, 1, 2}},
	{Name: "Jazz", Bands: audio.EqualizerBands{3, 4, 2, 1, -1, -1, 1, 2, 3, 4}},
	{Name: "Classical", Bands: audio.EqualizerBands{3, 2, 1, 0, -1, -1, 0, 2, 3, 4}},
	{Name: "Bass Boost", Bands: audio.EqualizerBands{8, 6, 4, 2, 0, 0, 0, 0, 0, 0}},
	{Name: "Treble Boost", Bands: audio.EqualizerBands{0, 0, 0, 0, 0, 1, 3, 5, 6, 7}},
	{Name: "Vocal", Bands: audio.EqualizerBands{-2, -1, 1, 4, 5, 4, 2, 0, -1, -2}},
	{Name: "Electronic", Bands: audio.EqualizerBands{6, 4, 1, -1, -2, 1, 3, 4, 5, 6}},
	{Name: "Acoustic", Bands: audio.EqualizerBands{3, 3, 2, 0, 1, 2, 3, 3, 2, 1}},
	{Name: "Hip-Hop", Bands: audio.EqualizerBands{7, 5, 3, 1, -1, -1, 1, 3, 3, 3}},
	{Name: "R&B", Bands: audio.EqualizerBands{4, 6, 3, 1, -1, 1, 2, 2, 1, 0}},
	{Name: "Loudness", Bands: audio.EqualizerBands{6, 4, 1, 0, -2, -1, 1, 4, 5, 5}},
	{Name: "Late Night", Bands: audio.EqualizerBands{5, 3, 1, 0, -2, -1, 0, 2, 3, 3}},
	{Name: "Podcast", Bands: audio.EqualizerBands{-3, -1, 2, 4, 4, 3, 1, -1, -2, -3}},
	{Name: "Small Speakers", Bands: audio.EqualizerBands{7, 5, 4, 2, 1, 0, -1, 0, 1, 2}},
}

var equalizerBandLabels = [audio.EqualizerBandCount]string{"70", "180", "320", "600", "1k", "3k", "6k", "12k", "14k", "16k"}

type equalizerConfig struct {
	// Preset is a built-in name or Custom.
	Preset string
	// Custom retains the user-edited curve while built-ins are selected.
	Custom audio.EqualizerBands
}

type equalizerWireState struct {
	// Preset is a built-in name or Custom.
	Preset string `json:"preset"`
	// Custom is the complete persistent user curve.
	Custom []float64 `json:"custom"`
}

func defaultEqualizerConfig() equalizerConfig {
	return equalizerConfig{Preset: equalizerPresets[0].Name}
}

func equalizerKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.NewReplacer("-", " ", "_", " ").Replace(name)
	return strings.Join(strings.Fields(name), " ")
}

func equalizerPresetByName(name string) (equalizerPreset, bool) {
	want := equalizerKey(name)
	for _, preset := range equalizerPresets {
		if equalizerKey(preset.Name) == want {
			return preset, true
		}
	}
	return equalizerPreset{}, false
}

func normalizeEqualizerConfig(cfg equalizerConfig) equalizerConfig {
	for i := range cfg.Custom {
		cfg.Custom[i] = audio.ClampEqualizerGain(cfg.Custom[i])
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Preset), customEqualizerPreset) {
		cfg.Preset = customEqualizerPreset
		return cfg
	}
	if preset, ok := equalizerPresetByName(cfg.Preset); ok {
		cfg.Preset = preset.Name
		return cfg
	}
	cfg.Preset = equalizerPresets[0].Name
	return cfg
}

func (cfg equalizerConfig) activeBands() audio.EqualizerBands {
	cfg = normalizeEqualizerConfig(cfg)
	if cfg.Preset == customEqualizerPreset {
		return cfg.Custom
	}
	preset, _ := equalizerPresetByName(cfg.Preset)
	return preset.Bands
}

func equalizerBandIndex(value string) (int, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if index, err := strconv.Atoi(value); err == nil && index >= 0 && index < audio.EqualizerBandCount {
		return index, nil
	}
	want := strings.TrimSuffix(value, "hz")
	for i, label := range equalizerBandLabels {
		if want == label || want == strings.TrimSuffix(label, "k")+"000" && strings.HasSuffix(label, "k") {
			return i, nil
		}
	}
	return 0, fmt.Errorf("unknown EQ band %q; use 0-9 or a center frequency", value)
}

func equalizerCycle(cfg equalizerConfig, step int) equalizerConfig {
	cfg = normalizeEqualizerConfig(cfg)
	current := len(equalizerPresets) // Custom follows all built-ins.
	for i, preset := range equalizerPresets {
		if preset.Name == cfg.Preset {
			current = i
			break
		}
	}
	total := len(equalizerPresets) + 1
	next := (current + step%total + total) % total
	if next == len(equalizerPresets) {
		cfg.Preset = customEqualizerPreset
	} else {
		cfg.Preset = equalizerPresets[next].Name
	}
	return cfg
}

func updateEqualizerConfig(cfg equalizerConfig, arg string) (equalizerConfig, bool, error) {
	cfg = normalizeEqualizerConfig(cfg)
	if strings.ContainsAny(arg, "\r\n") {
		return cfg, false, fmt.Errorf("EQ command must be one line")
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return cfg, false, nil
	}
	fields := strings.Fields(arg)
	switch strings.ToLower(fields[0]) {
	case "next":
		if len(fields) != 1 {
			return cfg, false, fmt.Errorf("usage: eq next")
		}
		next := equalizerCycle(cfg, 1)
		return next, next != cfg, nil
	case "prev", "previous":
		if len(fields) != 1 {
			return cfg, false, fmt.Errorf("usage: eq prev")
		}
		next := equalizerCycle(cfg, -1)
		return next, next != cfg, nil
	case "band", "--band":
		if len(fields) != 3 {
			return cfg, false, fmt.Errorf("usage: eq --band <0-9|frequency> <-12..12>")
		}
		band, err := equalizerBandIndex(fields[1])
		if err != nil {
			return cfg, false, err
		}
		gain, err := strconv.ParseFloat(fields[2], 64)
		if err != nil || math.IsNaN(gain) || math.IsInf(gain, 0) {
			return cfg, false, fmt.Errorf("invalid EQ gain %q", fields[2])
		}
		if gain < audio.EqualizerMinGain || gain > audio.EqualizerMaxGain {
			return cfg, false, fmt.Errorf("EQ gain must be between %.0f and +%.0f dB", audio.EqualizerMinGain, audio.EqualizerMaxGain)
		}
		bands := cfg.activeBands()
		bands[band] = gain
		next := equalizerConfig{Preset: customEqualizerPreset, Custom: bands}
		return next, next != cfg, nil
	}

	if strings.EqualFold(arg, customEqualizerPreset) {
		next := cfg
		next.Preset = customEqualizerPreset
		return next, next != cfg, nil
	}
	preset, ok := equalizerPresetByName(arg)
	if !ok {
		return cfg, false, fmt.Errorf("unknown EQ preset %q; use eq list", arg)
	}
	next := cfg
	next.Preset = preset.Name
	return next, next != cfg, nil
}

func equalizerPresetList() string {
	names := make([]string, 0, len(equalizerPresets)+1)
	for _, preset := range equalizerPresets {
		names = append(names, preset.Name)
	}
	names = append(names, customEqualizerPreset)
	return "EQ presets: " + strings.Join(names, ", ")
}

func formatEqualizerGain(gain float64) string {
	if gain == 0 {
		return "+0"
	}
	value := strconv.FormatFloat(gain, 'f', -1, 64)
	if gain >= 0 {
		return "+" + value
	}
	return value
}

func formatEqualizer(cfg equalizerConfig) string {
	cfg = normalizeEqualizerConfig(cfg)
	bands := cfg.activeBands()
	parts := make([]string, audio.EqualizerBandCount)
	for i, label := range equalizerBandLabels {
		parts[i] = fmt.Sprintf("%sHz %s", label, formatEqualizerGain(bands[i]))
	}
	return fmt.Sprintf("EQ: %s │ %s dB", cfg.Preset, strings.Join(parts, "  "))
}
