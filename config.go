// config.go loads user-defined stations from a JSON config file.
//
// The file is chill/stations.json beneath os.UserConfigDir, shaped like:
//
//	{
//	  "stations": [
//	    {"name": "synthwave", "url": "https://www.youtube.com/watch?v=4xDzrJKXOOY", "desc": "Synthwave Radio"}
//	  ]
//	}
//
// The config belongs to each process: the CLI and REPL read it on startup,
// and the daemon reads it again on a reload command, so editing the file
// never requires a restart of either.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/willibrandon/chill/internal/audio"
)

// config is the JSON document on disk.
type config struct {
	// Stations contains custom stations and overrides of built-ins.
	Stations []Station `json:"stations"`
	// DefaultStation selects the station played without an explicit name.
	DefaultStation string `json:"default_station,omitempty"`
}

var stationsMu sync.RWMutex
var stations = append([]Station(nil), builtinStations...)
var configuredDefault = "lofi-girl"

func stationSnapshot() []Station {
	stationsMu.RLock()
	defer stationsMu.RUnlock()
	return append([]Station(nil), stations...)
}

func defaultStation() string {
	stationsMu.RLock()
	defer stationsMu.RUnlock()
	return configuredDefault
}

// configPath is where stations.json lives, "" when there is no config dir.
func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "chill", "stations.json")
}

// loadConfig reads the config file. A missing file is not an error.
func loadConfig() (config, error) {
	var cfg config

	path := configPath()
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// loadUserStations applies the config to the shared station list. Names are
// lowercased, and a user station with the name of a built-in overrides it.
func loadUserStations() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	next := append([]Station(nil), builtinStations...)
stations:
	for _, s := range cfg.Stations {
		s.Name = strings.ToLower(strings.TrimSpace(s.Name))
		if s.Name == "" || s.URL == "" {
			continue
		}
		if s.Desc == "" {
			s.Desc = s.Name
		}

		// range values are copies, so take the slice element itself
		for i := range next {
			if strings.EqualFold(next[i].Name, s.Name) {
				next[i] = s
				continue stations
			}
		}
		next = append(next, s)
	}
	name := strings.ToLower(strings.TrimSpace(cfg.DefaultStation))
	if name == "" {
		name = "lofi-girl"
	}
	found := false
	for _, s := range next {
		found = found || s.Name == name
	}
	if !found {
		return fmt.Errorf("default station %q is not in the station list", name)
	}
	stationsMu.Lock()
	stations, configuredDefault = next, name
	stationsMu.Unlock()
	return nil
}

// writeJSON replaces the file only once the complete new contents are on disk.
func writeJSON(path string, value any) error {
	if path == "" {
		return fmt.Errorf("no config directory on this system")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".chill-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(data, '\n'))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func reloadConfig() error {
	if err := loadUserStations(); err != nil {
		return err
	}
	if isDaemonRunning() {
		if _, err := ask("reload"); err != nil {
			return fmt.Errorf("saved, but the daemon: %w", err)
		}
	}
	return nil
}

func removeStation(args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("usage: chill remove <name>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	name := strings.ToLower(args[0])
	kept := make([]Station, 0, len(cfg.Stations))
	for _, s := range cfg.Stations {
		if !strings.EqualFold(strings.TrimSpace(s.Name), name) {
			kept = append(kept, s)
		}
	}
	if len(kept) == len(cfg.Stations) {
		return "", fmt.Errorf("no custom station or override named %q", name)
	}
	cfg.Stations = kept
	if strings.EqualFold(strings.TrimSpace(cfg.DefaultStation), name) {
		builtin := false
		for _, s := range builtinStations {
			builtin = builtin || s.Name == name
		}
		if !builtin {
			cfg.DefaultStation = "" // a removed custom default returns to lofi-girl
		}
	}
	if err := writeJSON(configPath(), cfg); err != nil {
		return "", err
	}
	if err := reloadConfig(); err != nil {
		return "", err
	}
	return "removed custom station: " + name + " (any built-in is restored)", nil
}

func saveDefaultStation(args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("usage: chill default <station>")
	}
	s := findStation(args[0])
	if s == nil {
		return "", fmt.Errorf("unknown station: %s", args[0])
	}
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	cfg.DefaultStation = s.Name
	if err := writeJSON(configPath(), cfg); err != nil {
		return "", err
	}
	if err := reloadConfig(); err != nil {
		return "", err
	}
	return "default station: " + s.Name, nil
}

// Playback state is separate from the editable station list, so audio setting
// changes never rewrite station edits. Mute is temporary and is not persisted.
type playbackSettings struct {
	// Volume is the saved output level from 0 to 100.
	Volume int `json:"volume"`
	// EQPreset is a built-in preset name or Custom.
	EQPreset string `json:"eq_preset,omitempty"`
	// EQBands is the saved Custom curve, retained while a built-in is active.
	EQBands audio.EqualizerBands `json:"eq_bands"`
}

func volumePath() string {
	path := configPath()
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), "state.json")
}

func defaultPlaybackSettings() playbackSettings {
	return playbackSettings{Volume: defaultVolume, EQPreset: equalizerPresets[0].Name}
}

func normalizePlaybackSettings(settings playbackSettings) playbackSettings {
	if settings.Volume < 0 || settings.Volume > 100 {
		settings.Volume = defaultVolume
	}
	eq := normalizeEqualizerConfig(equalizerConfig{Preset: settings.EQPreset, Custom: settings.EQBands})
	settings.EQPreset, settings.EQBands = eq.Preset, eq.Custom
	return settings
}

func loadPlaybackSettings() (playbackSettings, error) {
	settings := defaultPlaybackSettings()
	data, err := os.ReadFile(volumePath())
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return settings, err
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return defaultPlaybackSettings(), err
	}
	return normalizePlaybackSettings(settings), nil
}

func savePlaybackSettings(settings playbackSettings) error {
	return writeJSON(volumePath(), normalizePlaybackSettings(settings))
}

func (settings playbackSettings) equalizer() equalizerConfig {
	return normalizeEqualizerConfig(equalizerConfig{Preset: settings.EQPreset, Custom: settings.EQBands})
}

func (settings *playbackSettings) setEqualizer(eq equalizerConfig) {
	eq = normalizeEqualizerConfig(eq)
	settings.EQPreset, settings.EQBands = eq.Preset, eq.Custom
}

func rememberedVolume() int {
	settings, err := loadPlaybackSettings()
	if err != nil {
		return defaultVolume
	}
	return settings.Volume
}
