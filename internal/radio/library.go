package radio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Pin keeps a country, region, or tag near the top of the browser.
type Pin struct {
	// Kind is country, state, or tag.
	Kind string `json:"kind"`
	// Name is the label shown in the browser.
	Name string `json:"name"`
	// CountryCode identifies a country or a state's parent country.
	CountryCode string `json:"country_code,omitempty"`
	// State identifies a pinned region.
	State string `json:"state,omitempty"`
}

// Library is the user's durable radio collection.
type Library struct {
	// Favorites contains stations deduplicated by playback URL.
	Favorites []Station `json:"favorites"`
	// Pins contains shortcuts to places and tags.
	Pins []Pin `json:"pins,omitempty"`
	// Country is an ISO code, NONE after refusal, or empty before consent.
	Country string `json:"country,omitempty"`
}

// Store serializes library mutations across processes.
type Store struct {
	// Path is the JSON library location.
	Path string
	mu   sync.Mutex
}

// DefaultStore uses the user's standard configuration directory.
func DefaultStore() *Store {
	dir, err := os.UserConfigDir()
	if err != nil {
		return &Store{}
	}
	return &Store{Path: filepath.Join(dir, "chill", "radio.json")}
}

func (s *Store) loadUnlocked() (Library, error) {
	var library Library
	if s.Path == "" {
		return library, fmt.Errorf("no config directory on this system")
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return library, nil
		}
		return library, err
	}
	if err := json.Unmarshal(data, &library); err != nil {
		return library, fmt.Errorf("%s: %w", s.Path, err)
	}
	library.normalize()
	return library, nil
}

// Load returns a normalized snapshot.
func (s *Store) Load() (Library, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked()
}

func (l *Library) normalize() {
	seen := map[string]bool{}
	favorites := l.Favorites[:0]
	for _, station := range l.Favorites {
		station.Name, station.URL = strings.TrimSpace(station.Name), strings.TrimSpace(station.URL)
		if station.Name == "" || !validHTTP(station.URL) || seen[station.URL] {
			continue
		}
		seen[station.URL] = true
		favorites = append(favorites, station)
	}
	l.Favorites = favorites
	l.Country = strings.ToUpper(strings.TrimSpace(l.Country))
	if len(l.Country) != 2 && l.Country != "NONE" {
		l.Country = ""
	}
	seen = map[string]bool{}
	pins := l.Pins[:0]
	for _, pin := range l.Pins {
		pin.Kind = strings.ToLower(strings.TrimSpace(pin.Kind))
		pin.Name = strings.TrimSpace(pin.Name)
		pin.CountryCode = strings.ToUpper(strings.TrimSpace(pin.CountryCode))
		pin.State = strings.TrimSpace(pin.State)
		key := pin.Kind + "\x00" + pin.CountryCode + "\x00" + strings.ToLower(pin.Name) + "\x00" + strings.ToLower(pin.State)
		if pin.Name == "" || (pin.Kind != "country" && pin.Kind != "state" && pin.Kind != "tag") || seen[key] {
			continue
		}
		seen[key] = true
		pins = append(pins, pin)
	}
	l.Pins = pins
}

func writeLibrary(path string, library Library) error {
	library.normalize()
	data, err := json.MarshalIndent(library, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".radio-*")
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
	return os.Rename(temp, path)
}

func (s *Store) mutate(fn func(*Library) error) (Library, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Path == "" {
		return Library{}, fmt.Errorf("no config directory on this system")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return Library{}, err
	}
	lock, err := openLibraryLock(s.Path + ".lock")
	if err != nil {
		return Library{}, err
	}
	defer lock.Close()
	library, err := s.loadUnlocked()
	if err != nil {
		return Library{}, err
	}
	if err := fn(&library); err != nil {
		return Library{}, err
	}
	if err := writeLibrary(s.Path, library); err != nil {
		return Library{}, err
	}
	return library, nil
}

// ToggleFavorite adds a station by URL or removes the existing entry.
func (s *Store) ToggleFavorite(station Station) (Library, bool, error) {
	station.Name, station.URL = strings.TrimSpace(station.Name), strings.TrimSpace(station.URL)
	if station.Name == "" || !validHTTP(station.URL) {
		return Library{}, false, fmt.Errorf("favorite needs a station name and HTTP(S) stream URL")
	}
	added := false
	library, err := s.mutate(func(l *Library) error {
		i := slices.IndexFunc(l.Favorites, func(item Station) bool { return item.URL == station.URL })
		if i >= 0 {
			l.Favorites = append(l.Favorites[:i], l.Favorites[i+1:]...)
			return nil
		}
		l.Favorites = append(l.Favorites, station)
		added = true
		return nil
	})
	return library, added, err
}

// RemoveFavorite removes a station URL without toggling it back on.
func (s *Store) RemoveFavorite(rawURL string) (Library, bool, error) {
	removed := false
	library, err := s.mutate(func(l *Library) error {
		i := slices.IndexFunc(l.Favorites, func(item Station) bool { return item.URL == rawURL })
		if i >= 0 {
			l.Favorites = append(l.Favorites[:i], l.Favorites[i+1:]...)
			removed = true
		}
		return nil
	})
	return library, removed, err
}

// TogglePin adds or removes an exact browser shortcut.
func (s *Store) TogglePin(pin Pin) (Library, bool, error) {
	added := false
	pin.Kind, pin.Name = strings.ToLower(strings.TrimSpace(pin.Kind)), strings.TrimSpace(pin.Name)
	library, err := s.mutate(func(l *Library) error {
		matches := func(item Pin) bool {
			return strings.EqualFold(item.Kind, pin.Kind) && strings.EqualFold(item.Name, pin.Name) &&
				strings.EqualFold(item.CountryCode, pin.CountryCode) && strings.EqualFold(item.State, pin.State)
		}
		i := slices.IndexFunc(l.Pins, matches)
		if i >= 0 {
			l.Pins = append(l.Pins[:i], l.Pins[i+1:]...)
			return nil
		}
		l.Pins = append(l.Pins, pin)
		added = true
		return nil
	})
	return library, added, err
}

// SetCountry stores an ISO country code, NONE to disable suggestions, or an empty value to ask again.
func (s *Store) SetCountry(code string) (Library, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code != "" && code != "NONE" && len(code) != 2 {
		return Library{}, fmt.Errorf("country must be a two-letter code, none, or ask")
	}
	return s.mutate(func(l *Library) error { l.Country = code; return nil })
}
