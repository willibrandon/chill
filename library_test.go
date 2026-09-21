package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/willibrandon/chill/internal/radio"
)

func testTrack(path, title string) MediaItem {
	return MediaItem{ID: mediaID(MediaTrack, path), Kind: MediaTrack, Source: path, Title: title}
}

// TestUniversalQueuePersistencePriorityAndUndo checks ordering and durable snapshots.
func TestUniversalQueuePersistencePriorityAndUndo(t *testing.T) {
	withConfigDir(t)
	d := fakeDaemon(t)
	d.library = emptyLibrary()
	station := itemFromStation(Station{Name: "live", Desc: "Live", URL: "https://example.com/live"})
	d.current, d.station = &station, station.Station
	one := testTrack(filepath.Join(t.TempDir(), "one.flac"), "One")
	two := testTrack(filepath.Join(t.TempDir(), "two.flac"), "Two")
	three := testTrack(filepath.Join(t.TempDir(), "three.flac"), "Three")
	data, _ := json.Marshal(queueRequest{Items: []MediaItem{one, two}})
	wantReply(t, d.execute("queue-append", string(data)), true, "queued 2")
	data, _ = json.Marshal(queueRequest{Items: []MediaItem{three}})
	wantReply(t, d.execute("queue-next", string(data)), true, "queued 1")
	if got := d.queueItems(); len(got) != 3 || got[0].Title != "Three" || got[1].Title != "One" {
		t.Fatalf("priority order = %+v", got)
	}
	wantReply(t, d.execute("queue-remove", "2"), true, "removed")
	if got := d.queueItems(); len(got) != 2 || got[1].Title != "Two" {
		t.Fatalf("remove order = %+v", got)
	}
	wantReply(t, d.execute("queue-undo", ""), true, "restored")
	reloaded, err := loadLibrary()
	if err != nil {
		t.Fatal(err)
	}
	got := append(cloneItems(reloaded.PlayNext), reloaded.Queue...)
	if len(got) != 3 || got[0].Title != "Three" || reloaded.Cycle[0].Title != "Live" {
		t.Fatalf("durable queue = %+v cycle=%+v", got, reloaded.Cycle)
	}
}

// TestPlaylistImportOrderAndRoundTrip checks mixed input order and PLS output.
func TestPlaylistImportOrderAndRoundTrip(t *testing.T) {
	withConfigDir(t)
	dir := t.TempDir()
	one, two := stereoFixture(t, 1), stereoFixture(t, 1)
	first := filepath.Join(dir, "first.wav")
	second := filepath.Join(dir, "second.wav")
	if data, err := os.ReadFile(one); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(first, data, 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(two); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(second, data, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "album.m3u8")
	body := "\ufeff#EXTM3U\n#EXTINF:1,Second\nsecond.wav\n#EXTINF:1,First\nfirst.wav\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := loadMediaInputs(context.Background(), []string{first, path, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 || items[0].Source != first || items[1].Title != "Second" || items[2].Title != "First" || items[3].Source != second {
		t.Fatalf("input order = %+v", items)
	}
	exported, err := writePlaylistFile(savedPlaylist{Name: "Album", Items: items[1:3]}, "pls", "-")
	if err != nil || !strings.Contains(exported, "File1="+second) || !strings.Contains(exported, "NumberOfEntries=2") {
		t.Fatalf("PLS export = %q, %v", exported, err)
	}
	exported, err = writePlaylistFile(savedPlaylist{Name: "Album", Items: items[1:3]}, "m3u8", "-")
	if err != nil || !strings.HasPrefix(exported, "#EXTM3U\n") || !strings.Contains(exported, second) {
		t.Fatalf("M3U8 export = %q, %v", exported, err)
	}
}

// TestConfiguredStationIsAUniversalInput checks stations mix with other queue sources.
func TestConfiguredStationIsAUniversalInput(t *testing.T) {
	items, err := loadMediaInputs(context.Background(), []string{"lofi-girl"})
	if err != nil || len(items) != 1 || items[0].Kind != MediaStation || items[0].Station == nil {
		t.Fatalf("station input = %+v, %v", items, err)
	}
}

// TestConfiguredLocalStationRemainsPlayable checks compatibility with saved decoder sources.
func TestConfiguredLocalStationRemainsPlayable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radio.wav")
	item, err := itemFromStation(Station{Name: "local", URL: path}).normalized()
	if err != nil || item.Source != path || item.Station == nil || item.Title != "local" {
		t.Fatalf("local station = %+v, %v", item, err)
	}
	item.Station.URL = filepath.Join(t.TempDir(), "different.wav")
	if _, err := item.normalized(); err == nil {
		t.Fatal("station item accepted mismatched sources")
	}
}

// TestLibraryRejectsInvalidSavedCollections checks strict persistent-state validation.
func TestLibraryRejectsInvalidSavedCollections(t *testing.T) {
	withConfigDir(t)
	invalid := emptyLibrary()
	invalid.Playlists["wrong-key"] = savedPlaylist{Name: "Right Name", Items: []MediaItem{testTrack("/tmp/a.flac", "A")}}
	if err := writeJSON(libraryPath(), invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLibrary(); err == nil {
		t.Fatal("invalid playlist key was accepted")
	}
}

// TestMarkedCollectionsPreserveUserOrder checks toggles retain deterministic browsing order.
func TestMarkedCollectionsPreserveUserOrder(t *testing.T) {
	library := emptyLibrary()
	one := testTrack(filepath.Join(t.TempDir(), "one.flac"), "One")
	two := testTrack(filepath.Join(t.TempDir(), "two.flac"), "Two")
	if _, err := library.setMarked(one, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := library.setMarked(two, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := library.setMarked(one, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := library.setMarked(one, false, nil); err != nil {
		t.Fatal(err)
	}
	items, err := libraryCollection(library, "favorites")
	if err != nil || len(items) != 2 || items[0].Title != "Two" || items[1].Title != "One" {
		t.Fatalf("favorites = %+v, %v", items, err)
	}
}

// TestRadioFavoritesMigrateIntoUniversalLibrary checks the one-time merge is lossless and idempotent.
func TestRadioFavoritesMigrateIntoUniversalLibrary(t *testing.T) {
	withConfigDir(t)
	store := radio.DefaultStore()
	pin := radio.Pin{Kind: "tag", Name: "Jazz"}
	favorites := []radio.Station{
		{Name: "First", URL: "https://example.com/first", CountryCode: "US"},
		{Name: "Second", URL: "https://example.com/second", CountryCode: "CA"},
	}
	if err := writeJSON(store.Path, radio.Library{Favorites: favorites, Pins: []radio.Pin{pin}, Country: "US"}); err != nil {
		t.Fatal(err)
	}
	library := emptyLibrary()
	if err := migrateRadioFavorites(library); err != nil {
		t.Fatal(err)
	}
	if !library.RadioFavoritesMigrated || len(library.Favorites) != 2 || len(library.FavoriteOrder) != 2 {
		t.Fatalf("migrated library = %+v", library)
	}
	stations := canonicalRadioFavorites(library)
	if len(stations) != 2 || stations[0].Name != "First" || stations[1].Name != "Second" {
		t.Fatalf("station view = %+v", stations)
	}
	preferences, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(preferences.Favorites) != 0 || preferences.Country != "US" || len(preferences.Pins) != 1 || preferences.Pins[0].Name != pin.Name {
		t.Fatalf("radio preferences = %+v", preferences)
	}
	if err := migrateRadioFavorites(library); err != nil {
		t.Fatal(err)
	}
	if len(library.Favorites) != 2 || len(library.FavoriteOrder) != 2 {
		t.Fatalf("repeated migration changed favorites: %+v", library.FavoriteOrder)
	}
	reloaded, err := loadLibrary()
	if err != nil || len(reloaded.Favorites) != 2 || !reloaded.RadioFavoritesMigrated {
		t.Fatalf("reloaded library = %+v, %v", reloaded, err)
	}
}

// TestFavoriteSetUsesTheUniversalCollection checks radio callers can request an idempotent state.
func TestFavoriteSetUsesTheUniversalCollection(t *testing.T) {
	withConfigDir(t)
	d := fakeDaemon(t)
	d.library = emptyLibrary()
	item := itemFromStation(Station{Name: "live", Desc: "Live", URL: "https://example.com/live"})
	request, _ := json.Marshal(favoriteSetRequest{Item: item, Favorite: true})
	wantReply(t, d.execute("favorite-set", string(request)), true, "favorite: true")
	wantReply(t, d.execute("favorite-set", string(request)), true, "favorite: true")
	items, err := libraryCollection(d.library, "favorites")
	if err != nil || len(items) != 1 || items[0].ID != item.ID || len(d.library.FavoriteOrder) != 1 {
		t.Fatalf("favorites = %+v, %v", items, err)
	}
}

// TestLocalSeekSpeedAndStatus checks finite controls do not depend on podcast state.
func TestLocalSeekSpeedAndStatus(t *testing.T) {
	withConfigDir(t)
	d := fakeDaemon(t)
	item := testTrack(filepath.Join(t.TempDir(), "long.flac"), "Long")
	item.Duration = 180
	request, _ := json.Marshal(struct {
		Item MediaItem `json:"item"`
	}{Item: item})
	wantReply(t, d.execute("media-play", string(request)), true, "loading")
	d.mu.Lock()
	first := d.player.(*fakePlayer)
	d.mu.Unlock()
	first.event <- playerEvent{loaded: true}
	waitState(t, d, "playing", 2*time.Second)
	wantReply(t, d.execute("speed", "1.25"), true, "1.25x")
	wantReply(t, d.execute("seek", "30"), true, "0:30")
	status := daemonStatus(t, d)
	if status.Item == nil || status.Item.Kind != MediaTrack || status.Position != 30 || status.Speed != 1.25 || !status.Seekable {
		t.Fatalf("local status = %+v", status)
	}
}
