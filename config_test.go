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
	// every platform resolves the config dir from one of these, and all of
	// them are cached nowhere, so this moves it
	t.Setenv("XDG_CONFIG_HOME", dir) // linux and the unix fallback
	t.Setenv("HOME", dir)            // darwin, and unix without XDG
	t.Setenv("AppData", dir)         // windows

	cfgDir, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no config dir: %v", err)
	}
	return filepath.Join(cfgDir, "chill", "stations.json")
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

	before := len(stations)
	t.Cleanup(func() { stations = stations[:before] })

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
