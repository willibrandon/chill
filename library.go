package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/willibrandon/chill/internal/podcast"
)

// MediaKind describes how a queue item is resolved and presented.
type MediaKind string

const (
	// MediaStation is a non-seekable live radio stream.
	MediaStation MediaKind = "station"
	// MediaPodcast is a finite podcast episode.
	MediaPodcast MediaKind = "podcast"
	// MediaTrack is a local audio file.
	MediaTrack MediaKind = "track"
	// MediaURL is a finite direct HTTP(S) audio source.
	MediaURL MediaKind = "url"
)

// MediaItem is the source-neutral unit used by playback, queues and playlists.
// Station and Episode retain source-specific metadata without making queue
// operations depend on either source.
type MediaItem struct {
	ID             string           `json:"id"`                        // ID is stable across queue and playlist copies.
	Kind           MediaKind        `json:"kind"`                      // Kind selects source-specific playback behavior.
	Source         string           `json:"source"`                    // Source is a local path or HTTP(S) address.
	Title          string           `json:"title"`                     // Title is the primary display label.
	Artist         string           `json:"artist,omitempty"`          // Artist names the performer or publisher.
	Album          string           `json:"album,omitempty"`           // Album groups finite tracks.
	Genre          string           `json:"genre,omitempty"`           // Genre carries optional source metadata.
	Artwork        string           `json:"artwork,omitempty"`         // Artwork is a local path or remote URL.
	EmbeddedLyrics string           `json:"embedded_lyrics,omitempty"` // EmbeddedLyrics retains file-tag lyrics.
	Duration       float64          `json:"duration,omitempty"`        // Duration is the best known length in seconds.
	Station        *Station         `json:"station,omitempty"`         // Station retains live-radio metadata.
	Episode        *podcast.Episode `json:"episode,omitempty"`         // Episode retains podcast identity.
	AddedAt        time.Time        `json:"added_at,omitzero"`         // AddedAt records insertion time.
}

func mediaID(kind MediaKind, source string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(string(kind)+"\x00"+source)))
}

func itemFromStation(station Station) MediaItem {
	stationCopy := station
	return MediaItem{ID: mediaID(MediaStation, station.URL), Kind: MediaStation, Source: station.URL,
		Title: station.Desc, Album: station.Name, Genre: station.Tags, Artwork: station.Artwork,
		Station: &stationCopy, AddedAt: time.Now().UTC()}
}

func itemFromEpisode(e podcast.Episode) MediaItem {
	episodeCopy := e
	return MediaItem{ID: e.Key(), Kind: MediaPodcast, Source: e.URL, Title: e.Title,
		Artist: e.Author, Album: e.Show, Artwork: e.Artwork, Duration: e.Duration,
		Episode: &episodeCopy, AddedAt: time.Now().UTC()}
}

func itemFromURL(raw string) (MediaItem, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return MediaItem{}, fmt.Errorf("media URL must be an absolute HTTP(S) URL")
	}
	title := filepath.Base(strings.TrimSuffix(u.Path, "/"))
	if title == "." || title == "/" || title == "" {
		title = u.Hostname()
	}
	return MediaItem{ID: mediaID(MediaURL, u.String()), Kind: MediaURL, Source: u.String(), Title: title, AddedAt: time.Now().UTC()}, nil
}

func (item MediaItem) normalized() (MediaItem, error) {
	item.Source = strings.TrimSpace(item.Source)
	item.Title = strings.TrimSpace(item.Title)
	if item.Source == "" {
		return MediaItem{}, fmt.Errorf("media item has no source")
	}
	switch item.Kind {
	case MediaStation:
		if item.Station == nil {
			return MediaItem{}, fmt.Errorf("invalid station item")
		}
		station := *item.Station
		station.Name = strings.TrimSpace(station.Name)
		station.URL = strings.TrimSpace(station.URL)
		station.Desc = strings.TrimSpace(station.Desc)
		if station.Name == "" || station.URL == "" || station.URL != item.Source {
			return MediaItem{}, fmt.Errorf("invalid station item")
		}
		if station.Desc == "" {
			station.Desc = station.Name
		}
		item.Station = &station
	case MediaPodcast:
		if item.Episode == nil || !podcast.ValidURL(item.Episode.FeedURL) || !podcast.ValidURL(item.Source) {
			return MediaItem{}, fmt.Errorf("invalid podcast item")
		}
	case MediaTrack:
		absolute, err := filepath.Abs(item.Source)
		if err != nil {
			return MediaItem{}, err
		}
		item.Source = filepath.Clean(absolute)
	case MediaURL:
		if _, err := itemFromURL(item.Source); err != nil {
			return MediaItem{}, err
		}
	default:
		return MediaItem{}, fmt.Errorf("unknown media kind %q", item.Kind)
	}
	if item.Title == "" {
		if item.Station != nil {
			item.Title = item.Station.Desc
		} else {
			item.Title = filepath.Base(item.Source)
		}
	}
	if item.ID == "" {
		if item.Episode != nil {
			item.ID = item.Episode.Key()
		} else {
			item.ID = mediaID(item.Kind, item.Source)
		}
	}
	if item.AddedAt.IsZero() {
		item.AddedAt = time.Now().UTC()
	}
	return item, nil
}

func (item MediaItem) finite() bool { return item.Kind != MediaStation }

func (item MediaItem) display() string {
	if item.Artist != "" {
		return item.Artist + " — " + item.Title
	}
	if item.Album != "" && item.Album != item.Title {
		return item.Album + " — " + item.Title
	}
	return item.Title
}

type queueSnapshot struct {
	Queue    []MediaItem `json:"queue"`           // Queue is the regular pending lane.
	PlayNext []MediaItem `json:"play_next"`       // PlayNext is the priority lane.
	Cycle    []MediaItem `json:"cycle,omitempty"` // Cycle is the repeat-all sequence.
}

type savedPlaylist struct {
	Name      string      `json:"name"`       // Name preserves user-facing capitalization.
	Items     []MediaItem `json:"items"`      // Items are stored in playback order.
	CreatedAt time.Time   `json:"created_at"` // CreatedAt records creation time.
	UpdatedAt time.Time   `json:"updated_at"` // UpdatedAt records the latest mutation.
}

type resumePoint struct {
	Position float64   `json:"position"`           // Position is the saved playhead in seconds.
	Duration float64   `json:"duration,omitempty"` // Duration is the measured length.
	Played   bool      `json:"played"`             // Played marks completed media.
	Updated  time.Time `json:"updated"`            // Updated drives retention ordering.
}

type libraryState struct {
	Version   int                      `json:"version"`         // Version identifies the persistent schema.
	Queue     []MediaItem              `json:"queue"`           // Queue holds regular pending items.
	PlayNext  []MediaItem              `json:"play_next"`       // PlayNext holds priority items.
	Cycle     []MediaItem              `json:"cycle,omitempty"` // Cycle preserves the repeat-all sequence.
	Undo      []queueSnapshot          `json:"undo,omitempty"`  // Undo stores bounded queue snapshots.
	Shuffle   bool                     `json:"shuffle"`         // Shuffle randomizes regular queue selection.
	Repeat    string                   `json:"repeat"`          // Repeat is off, all, or one.
	Playlists map[string]savedPlaylist `json:"playlists"`       // Playlists maps normalized names to collections.
	Favorites map[string]bool          `json:"favorites"`       // Favorites is the global item favorite set.
	// FavoriteOrder preserves the order in which media was favorited.
	FavoriteOrder []string        `json:"favorite_order,omitempty"`
	Bookmarks     map[string]bool `json:"bookmarks"` // Bookmarks is the global bookmark set.
	// BookmarkOrder preserves the order in which media was bookmarked.
	BookmarkOrder []string               `json:"bookmark_order,omitempty"`
	Items         map[string]MediaItem   `json:"items,omitempty"` // Items retains metadata for marked media.
	Recent        []MediaItem            `json:"recent"`          // Recent lists source-neutral listening history.
	Resume        map[string]resumePoint `json:"resume"`          // Resume stores finite-media playheads.
	// RadioFavoritesMigrated records completion of the one-time favorites merge.
	RadioFavoritesMigrated bool `json:"radio_favorites_migrated,omitempty"`
}

func libraryPath() string {
	if p := configPath(); p != "" {
		return filepath.Join(filepath.Dir(p), "library.json")
	}
	return ""
}

func emptyLibrary() *libraryState {
	return &libraryState{Version: 1, Repeat: "off", Queue: []MediaItem{}, PlayNext: []MediaItem{},
		Playlists: map[string]savedPlaylist{}, Favorites: map[string]bool{}, Bookmarks: map[string]bool{},
		Items: map[string]MediaItem{}, Recent: []MediaItem{}, Resume: map[string]resumePoint{}}
}

func loadLibrary() (*libraryState, error) {
	library := emptyLibrary()
	data, err := os.ReadFile(libraryPath())
	if os.IsNotExist(err) {
		_ = hydrateLegacyRadioFavorites(library)
		return library, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read library: %w", err)
	}
	if err := json.Unmarshal(data, library); err != nil {
		return nil, fmt.Errorf("read library: %w", err)
	}
	if library.Version != 1 {
		return nil, fmt.Errorf("unsupported library.json version %d", library.Version)
	}
	if library.Repeat != "off" && library.Repeat != "all" && library.Repeat != "one" {
		return nil, fmt.Errorf("invalid repeat mode in library.json")
	}
	if len(library.Queue)+len(library.PlayNext) > 5000 || len(library.Cycle) > 5000 || len(library.Recent) > 200 || len(library.Undo) > 20 {
		return nil, fmt.Errorf("read library: collection limit exceeded")
	}
	for _, list := range [][]MediaItem{library.Queue, library.PlayNext, library.Cycle, library.Recent} {
		for _, item := range list {
			if _, err := item.normalized(); err != nil {
				return nil, fmt.Errorf("read library: %w", err)
			}
		}
	}
	for _, snapshot := range library.Undo {
		if len(snapshot.Queue)+len(snapshot.PlayNext) > 5000 || len(snapshot.Cycle) > 5000 {
			return nil, fmt.Errorf("read library: undo collection limit exceeded")
		}
		for _, list := range [][]MediaItem{snapshot.Queue, snapshot.PlayNext, snapshot.Cycle} {
			for _, item := range list {
				if _, err := item.normalized(); err != nil {
					return nil, fmt.Errorf("read library undo: %w", err)
				}
			}
		}
	}
	if library.Playlists == nil {
		library.Playlists = map[string]savedPlaylist{}
	}
	if len(library.Playlists) > 5000 {
		return nil, fmt.Errorf("read library: playlist limit exceeded")
	}
	for key, playlist := range library.Playlists {
		if key != playlistKey(playlist.Name) || key == "" || len(playlist.Items) > 5000 {
			return nil, fmt.Errorf("read library: invalid playlist %q", playlist.Name)
		}
		for _, item := range playlist.Items {
			if _, err := item.normalized(); err != nil {
				return nil, fmt.Errorf("read library: playlist %q: %w", playlist.Name, err)
			}
		}
	}
	if library.Favorites == nil {
		library.Favorites = map[string]bool{}
	}
	if library.Bookmarks == nil {
		library.Bookmarks = map[string]bool{}
	}
	if len(library.Favorites) > 5000 || len(library.Bookmarks) > 5000 || len(library.FavoriteOrder) > 5000 || len(library.BookmarkOrder) > 5000 {
		return nil, fmt.Errorf("read library: marked item limit exceeded")
	}
	library.FavoriteOrder = normalizeMarkOrder(library.Favorites, library.FavoriteOrder)
	library.BookmarkOrder = normalizeMarkOrder(library.Bookmarks, library.BookmarkOrder)
	if library.Items == nil {
		library.Items = map[string]MediaItem{}
	}
	if len(library.Items) > 5000 {
		return nil, fmt.Errorf("read library: item limit exceeded")
	}
	for key, item := range library.Items {
		if key != item.ID {
			return nil, fmt.Errorf("read library: invalid retained item")
		}
		if _, err := item.normalized(); err != nil {
			return nil, fmt.Errorf("read library: %w", err)
		}
	}
	if library.Resume == nil {
		library.Resume = map[string]resumePoint{}
	}
	if len(library.Resume) > 5000 {
		return nil, fmt.Errorf("read library: resume point limit exceeded")
	}
	for _, point := range library.Resume {
		if point.Position < 0 || point.Duration < 0 || point.Position > 365*24*3600 || point.Duration > 365*24*3600 || math.IsNaN(point.Position) || math.IsNaN(point.Duration) || math.IsInf(point.Position, 0) || math.IsInf(point.Duration, 0) {
			return nil, fmt.Errorf("read library: invalid resume point")
		}
	}
	if !library.RadioFavoritesMigrated {
		// Keep older station favorites visible before the first process has a
		// chance to complete the durable migration.
		_ = hydrateLegacyRadioFavorites(library)
	}
	return library, nil
}

func (library *libraryState) commit() error {
	library.Version = 1
	if library.Repeat == "" {
		library.Repeat = "off"
	}
	if len(library.Cycle) > 5000 {
		library.Cycle = cloneItems(library.Cycle[len(library.Cycle)-5000:])
	}
	for len(library.Resume) > 5000 {
		oldestKey, oldest := "", time.Time{}
		for key, point := range library.Resume {
			if oldestKey == "" || point.Updated.Before(oldest) {
				oldestKey, oldest = key, point.Updated
			}
		}
		delete(library.Resume, oldestKey)
	}
	return writeJSON(libraryPath(), library)
}

func cloneItems(items []MediaItem) []MediaItem { return slices.Clone(items) }

func (library *libraryState) rememberQueue() {
	library.Undo = append(library.Undo, queueSnapshot{Queue: cloneItems(library.Queue), PlayNext: cloneItems(library.PlayNext), Cycle: cloneItems(library.Cycle)})
	if len(library.Undo) > 20 {
		library.Undo = library.Undo[len(library.Undo)-20:]
	}
}

func (library *libraryState) recordRecent(item MediaItem) {
	library.rememberItem(item)
	library.Recent = slices.DeleteFunc(library.Recent, func(existing MediaItem) bool { return existing.ID == item.ID })
	library.Recent = append([]MediaItem{item}, library.Recent...)
	if len(library.Recent) > 200 {
		library.Recent = library.Recent[:200]
	}
}

func (library *libraryState) rememberItem(item MediaItem) {
	if library.Items == nil {
		library.Items = map[string]MediaItem{}
	}
	if item.ID == "" || len(library.Items) >= 5000 && library.Items[item.ID].ID == "" {
		return
	}
	library.Items[item.ID] = item
}

func normalizeMarkOrder(values map[string]bool, order []string) []string {
	seen := map[string]bool{}
	normalized := make([]string, 0, len(values))
	for _, id := range order {
		if values[id] && !seen[id] {
			seen[id] = true
			normalized = append(normalized, id)
		}
	}
	var missing []string
	for id, marked := range values {
		if !marked {
			delete(values, id)
		} else if !seen[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return append(normalized, missing...)
}

func (library *libraryState) setMarked(item MediaItem, bookmark bool, desired *bool) (bool, error) {
	if library.Items == nil {
		library.Items = map[string]MediaItem{}
	}
	if library.Favorites == nil {
		library.Favorites = map[string]bool{}
	}
	if library.Bookmarks == nil {
		library.Bookmarks = map[string]bool{}
	}
	values, order, label := library.Favorites, &library.FavoriteOrder, "favorite"
	if bookmark {
		values, order, label = library.Bookmarks, &library.BookmarkOrder, "bookmark"
	}
	marked := !values[item.ID]
	if desired != nil {
		marked = *desired
	}
	if marked && !values[item.ID] && len(values) >= 5000 {
		return false, fmt.Errorf("%s limit reached", label)
	}
	if marked && library.Items[item.ID].ID == "" && len(library.Items) >= 5000 {
		return false, fmt.Errorf("marked item metadata limit reached")
	}
	if marked {
		library.rememberItem(item)
		values[item.ID] = true
		if !slices.Contains(*order, item.ID) {
			*order = append(*order, item.ID)
		}
	} else {
		delete(values, item.ID)
		*order = slices.DeleteFunc(*order, func(id string) bool { return id == item.ID })
		if !library.Favorites[item.ID] && !library.Bookmarks[item.ID] {
			delete(library.Items, item.ID)
		}
	}
	return marked, nil
}

func (library *libraryState) knownItems() []MediaItem {
	seen := map[string]bool{}
	var items []MediaItem
	add := func(values []MediaItem) {
		for _, item := range values {
			if item.ID != "" && !seen[item.ID] {
				seen[item.ID] = true
				items = append(items, item)
			}
		}
	}
	add(library.Recent)
	add(library.PlayNext)
	add(library.Queue)
	for _, playlist := range library.Playlists {
		add(playlist.Items)
	}
	for _, item := range library.Items {
		add([]MediaItem{item})
	}
	return items
}

func playlistKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }
