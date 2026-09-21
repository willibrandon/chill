package tracklog

import (
	"path/filepath"
	"testing"
	"time"
)

// TestRecordOrdersNewestFirstAndCoalesces repeats.
func TestRecordOrdersNewestFirstAndCoalesces(t *testing.T) {
	store := &Store{Path: filepath.Join(t.TempDir(), "tracks.json")}
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	one := Entry{PlayedAt: when, Station: "Radio", StationURL: "https://example.com", Artist: "A", Title: "One", Raw: "A - One"}
	if err := store.Record(one); err != nil {
		t.Fatal(err)
	}
	one.PlayedAt = when.Add(time.Minute)
	if err := store.Record(one); err != nil {
		t.Fatal(err)
	}
	two := Entry{PlayedAt: when.Add(2 * time.Minute), Station: "Radio", StationURL: one.StationURL, Title: "Two", Raw: "Two"}
	if err := store.Record(two); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Load(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Title != "Two" || !entries[1].PlayedAt.Equal(one.PlayedAt) {
		t.Fatalf("entries = %#v", entries)
	}
}

// TestClearWritesAnEmptyHistory verifies deterministic clear behavior.
func TestClearWritesAnEmptyHistory(t *testing.T) {
	store := &Store{Path: filepath.Join(t.TempDir(), "tracks.json")}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Load(0)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
}

// TestRecordRequiresReplayableStation verifies history never saves dead replay rows.
func TestRecordRequiresReplayableStation(t *testing.T) {
	store := &Store{Path: filepath.Join(t.TempDir(), "tracks.json")}
	if err := store.Record(Entry{Station: "Radio", Title: "Song", Raw: "Song"}); err == nil {
		t.Fatal("entry without a station URL was accepted")
	}
}
