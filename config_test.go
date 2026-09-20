package main

import (
	"os"
	"path/filepath"
	"testing"
)

// withConfigDir points os.UserConfigDir at a temp directory for the test and
// returns where stations.json should land in it.
func withConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldStations, oldDefault := stationSnapshot(), defaultStation()
	t.Cleanup(func() {
		stationsMu.Lock()
		stations, configuredDefault = oldStations, oldDefault
		stationsMu.Unlock()
	})
	// every platform resolves the config dir from one of these, and all of
	// them are cached nowhere, so this moves it
	t.Setenv("XDG_CONFIG_HOME", dir) // linux and the unix fallback
	t.Setenv("HOME", dir)            // darwin, and unix without XDG
	t.Setenv("AppData", dir)         // windows
	// Keep config commands from notifying a real daemon during tests.
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)

	cfgDir, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no config dir: %v", err)
	}
	return filepath.Join(cfgDir, "chill", "stations.json")
}

func TestReloadRemovesDeletedStationsAndRestoresBuiltins(t *testing.T) {
	path := withConfigDir(t)
	writeConfig(t, path, `{"stations":[{"name":"custom","url":"https://example.com"},{"name":"lofi-girl","url":"https://example.com/override"}],"default_station":"custom"}`)
	if err := loadUserStations(); err != nil {
		t.Fatal(err)
	}
	if defaultStation() != "custom" {
		t.Fatal("default was not loaded")
	}
	writeConfig(t, path, `{"stations":[]}`)
	if err := loadUserStations(); err != nil {
		t.Fatal(err)
	}
	if findStation("custom") != nil || findStation("lofi-girl").URL != builtinStations[0].URL || defaultStation() != "lofi-girl" {
		t.Fatal("reload retained removed config")
	}
	writeConfig(t, path, `{"default_station":"missing"}`)
	if err := loadUserStations(); err == nil {
		t.Fatal("invalid default accepted")
	}
	if defaultStation() != "lofi-girl" || len(stationSnapshot()) != len(builtinStations) {
		t.Fatal("failed reload changed active configuration")
	}
}

func TestStationManagement(t *testing.T) {
	path := withConfigDir(t)
	if _, err := saveStation([]string{"My-Mix", "https://example.com", "My mix"}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveDefaultStation([]string{"my-mix"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil || cfg.DefaultStation != "my-mix" {
		t.Fatalf("default not saved: %+v, %v", cfg, err)
	}
	if _, err := removeStation([]string{"MY-MIX"}); err != nil {
		t.Fatal(err)
	}
	if findStation("my-mix") != nil || defaultStation() != "lofi-girl" {
		t.Fatal("removal did not reset the default")
	}
	if _, err := removeStation([]string{"lofi-girl"}); err == nil {
		t.Fatal("removed a builtin without an override")
	}
	writeConfig(t, path, `{"stations":[{"name":"lofi-girl","url":"https://example.com"}]}`)
	if _, err := removeStation([]string{"lofi-girl"}); err != nil {
		t.Fatal(err)
	}
	if findStation("lofi-girl").URL != builtinStations[0].URL {
		t.Fatal("override removal did not restore builtin")
	}
}

func TestRememberedVolume(t *testing.T) {
	withConfigDir(t)
	if rememberedVolume() != defaultVolume {
		t.Fatal("missing state should use default")
	}
	d := &Daemon{volume: defaultVolume}
	wantReply(t, d.setVolume(23), true, "23")
	wantReply(t, d.mute(), true, "muted")
	if rememberedVolume() != 23 {
		t.Fatal("volume was not remembered, or mute overwrote it")
	}
	wantReply(t, d.setVolume(0), true, "0")
	if rememberedVolume() != 0 {
		t.Fatal("zero volume must be remembered")
	}
	if err := writeJSON(volumePath(), playbackSettings{Volume: 200}); err != nil {
		t.Fatal(err)
	}
	if rememberedVolume() != defaultVolume {
		t.Fatal("invalid saved volume should use default")
	}
}

func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigMissing(t *testing.T) {
	withConfigDir(t)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Stations) != 0 {
		t.Errorf("stations = %v, want none", cfg.Stations)
	}
}

func TestLoadConfigBadJSON(t *testing.T) {
	path := withConfigDir(t)
	writeConfig(t, path, "{nope")

	if _, err := loadConfig(); err == nil {
		t.Fatal("bad JSON should be an error")
	}
}

func TestLoadUserStations(t *testing.T) {
	path := withConfigDir(t)
	writeConfig(t, path, `{"stations": [
		{"name": "My-Mix", "url": "https://example.com/mix", "desc": "My mix"},
		{"name": "lofi-girl", "url": "https://example.com/override", "desc": "Overridden"},
		{"name": "", "url": "https://example.com/blank"},
		{"name": "nodesc", "url": "https://example.com/nodesc"}
	]}`)

	before := len(builtinStations)

	if err := loadUserStations(); err != nil {
		t.Fatalf("loadUserStations: %v", err)
	}

	// two new stations, the blank name is skipped
	if len(stations) != before+2 {
		t.Errorf("len(stations) = %d, want %d", len(stations), before+2)
	}

	// names are lowercased
	mix := findStation("my-mix")
	if mix == nil || mix.Desc != "My mix" {
		t.Errorf("findStation(my-mix) = %+v", mix)
	}

	// a built-in gets overridden, not duplicated
	girl := findStation("lofi-girl")
	if girl == nil || girl.URL != "https://example.com/override" {
		t.Errorf("findStation(lofi-girl) = %+v", girl)
	}
	count := 0
	for _, s := range stations {
		if s.Name == "lofi-girl" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("lofi-girl appears %d times", count)
	}

	// a missing description falls back to the name
	if s := findStation("nodesc"); s == nil || s.Desc != "nodesc" {
		t.Errorf("findStation(nodesc) = %+v", s)
	}
}
