package radio

import (
	"path/filepath"
	"testing"
)

// TestFavoriteTogglePersistsByURL verifies stable favorite identity.
func TestFavoriteTogglePersistsByURL(t *testing.T) {
	store := &Store{Path: filepath.Join(t.TempDir(), "radio.json")}
	station := Station{Name: "Example", URL: "https://example.com/live", CountryCode: "US"}
	library, added, err := store.ToggleFavorite(station)
	if err != nil || !added || len(library.Favorites) != 1 {
		t.Fatalf("add = %#v, %v, %v", library, added, err)
	}
	library, added, err = store.ToggleFavorite(Station{Name: "Renamed", URL: station.URL})
	if err != nil || added || len(library.Favorites) != 0 {
		t.Fatalf("remove = %#v, %v, %v", library, added, err)
	}
}

// TestLibraryNormalizesDuplicates filters duplicate and unsafe entries.
func TestLibraryNormalizesDuplicates(t *testing.T) {
	store := &Store{Path: filepath.Join(t.TempDir(), "radio.json")}
	if err := writeLibrary(store.Path, Library{Favorites: []Station{
		{Name: "One", URL: "https://example.com/live"},
		{Name: "Duplicate", URL: "https://example.com/live"},
		{Name: "Bad", URL: "file:///tmp/radio"},
	}}); err != nil {
		t.Fatal(err)
	}
	library, err := store.Load()
	if err != nil || len(library.Favorites) != 1 || library.Favorites[0].Name != "One" {
		t.Fatalf("library = %#v, %v", library, err)
	}
}
