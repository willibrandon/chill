// config.go loads user-defined stations from a JSON config file.
//
// The file is ~/.config/chill/stations.json (os.UserConfigDir), shaped like:
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
)

// config is the JSON document on disk.
type config struct {
	Stations []Station `json:"stations"`
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
		for i := range stations {
			if strings.EqualFold(stations[i].Name, s.Name) {
				stations[i] = s
				continue stations
			}
		}
		stations = append(stations, s)
	}
	return nil
}
