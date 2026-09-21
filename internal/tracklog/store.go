// Package tracklog persists live-radio title changes.
package tracklog

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxEntries = 200

func validStationURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// Entry is one observed radio title transition.
type Entry struct {
	// PlayedAt is when the metadata first became current.
	PlayedAt time.Time `json:"played_at"`
	// Station is the station display name.
	Station string `json:"station"`
	// StationURL is the original station address.
	StationURL string `json:"station_url"`
	// Artist is the parsed artist when one was supplied.
	Artist string `json:"artist,omitempty"`
	// Title is the parsed song or program title.
	Title string `json:"title"`
	// Raw is the exact sanitized stream title.
	Raw string `json:"raw"`
	// Artwork is the station image URL when available.
	Artwork string `json:"artwork,omitempty"`
}

// Store serializes the bounded track log.
type Store struct {
	// Path is the JSON history location.
	Path string
	mu   sync.Mutex
}

// DefaultStore uses the user's standard configuration directory.
func DefaultStore() *Store {
	dir, err := os.UserConfigDir()
	if err != nil {
		return &Store{}
	}
	return &Store{Path: filepath.Join(dir, "chill", "tracks.json")}
}

func (s *Store) read() ([]Entry, error) {
	if s.Path == "" {
		return nil, fmt.Errorf("no config directory on this system")
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, nil
		}
		return nil, err
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("%s: %w", s.Path, err)
	}
	valid := entries[:0]
	for _, entry := range entries {
		entry.Station, entry.StationURL = strings.TrimSpace(entry.Station), strings.TrimSpace(entry.StationURL)
		entry.Artist, entry.Title, entry.Raw, entry.Artwork = strings.TrimSpace(entry.Artist), strings.TrimSpace(entry.Title), strings.TrimSpace(entry.Raw), strings.TrimSpace(entry.Artwork)
		if entry.Artwork != "" && !validStationURL(entry.Artwork) {
			entry.Artwork = ""
		}
		if entry.Station != "" && validStationURL(entry.StationURL) && entry.Title != "" && entry.Raw != "" && !entry.PlayedAt.IsZero() {
			valid = append(valid, entry)
		}
	}
	if len(valid) > maxEntries {
		valid = valid[len(valid)-maxEntries:]
	}
	return valid, nil
}

func (s *Store) write(entries []Entry) error {
	if len(entries) > maxEntries {
		entries = entries[len(entries)-maxEntries:]
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tracks-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(temp, s.Path)
}

// Load returns newest entries first and applies an optional limit.
func (s *Store) Load(limit int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.read()
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// Record appends a transition, coalescing a repeated title within five minutes.
func (s *Store) Record(entry Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Path == "" {
		return fmt.Errorf("no config directory on this system")
	}
	if entry.PlayedAt.IsZero() {
		entry.PlayedAt = time.Now().UTC()
	} else {
		entry.PlayedAt = entry.PlayedAt.UTC()
	}
	entry.Station, entry.StationURL = strings.TrimSpace(entry.Station), strings.TrimSpace(entry.StationURL)
	entry.Artist, entry.Title, entry.Raw, entry.Artwork = strings.TrimSpace(entry.Artist), strings.TrimSpace(entry.Title), strings.TrimSpace(entry.Raw), strings.TrimSpace(entry.Artwork)
	if entry.Artwork != "" && !validStationURL(entry.Artwork) {
		entry.Artwork = ""
	}
	if entry.Station == "" || !validStationURL(entry.StationURL) || entry.Title == "" || entry.Raw == "" {
		return fmt.Errorf("track history needs station URL and title metadata")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return err
	}
	lock, err := openLock(s.Path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	entries, err := s.read()
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		last := &entries[len(entries)-1]
		if last.StationURL == entry.StationURL && last.Raw == entry.Raw && entry.PlayedAt.Sub(last.PlayedAt) <= 5*time.Minute {
			*last = entry
			return s.write(entries)
		}
	}
	return s.write(append(entries, entry))
}

// Clear removes all saved entries while retaining a valid empty file.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Path == "" {
		return fmt.Errorf("no config directory on this system")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return err
	}
	lock, err := openLock(s.Path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	return s.write([]Entry{})
}
