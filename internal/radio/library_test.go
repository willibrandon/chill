package radio

import (
	"path/filepath"
	"testing"
)

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

// TestClearFavoritesPreservesPreferences checks migration only removes the legacy list.
func TestClearFavoritesPreservesPreferences(t *testing.T) {
	store := &Store{Path: filepath.Join(t.TempDir(), "radio.json")}
	station := Station{Name: "Example", URL: "https://example.com/live"}
	if err := writeLibrary(store.Path, Library{Favorites: []Station{station}, Pins: []Pin{{Kind: "tag", Name: "Jazz"}}, Country: "US"}); err != nil {
		t.Fatal(err)
	}
	library, err := store.ClearFavorites()
	if err != nil || len(library.Favorites) != 0 || len(library.Pins) != 1 || library.Country != "US" {
		t.Fatalf("cleared library = %+v, %v", library, err)
	}
}
