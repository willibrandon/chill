package main

import (
	"encoding/json"
	"fmt"

	"github.com/willibrandon/chill/internal/radio"
)

type favoriteSetRequest struct {
	Item MediaItem `json:"item"` // Item is the media whose state is being set.
	// Favorite is the desired state rather than a toggle.
	Favorite bool `json:"favorite"`
}

func mergeLegacyRadioFavorites(library *libraryState, favorites []radio.Station) error {
	for _, favorite := range favorites {
		item, err := itemFromStation(stationFromCatalog(favorite)).normalized()
		if err != nil {
			return fmt.Errorf("migrate favorite %q: %w", favorite.Name, err)
		}
		desired := true
		if _, err := library.setMarked(item, false, &desired); err != nil {
			return err
		}
	}
	return nil
}

func hydrateLegacyRadioFavorites(library *libraryState) error {
	if library.RadioFavoritesMigrated {
		return nil
	}
	legacy, err := radio.DefaultStore().Load()
	if err != nil {
		return err
	}
	return mergeLegacyRadioFavorites(library, legacy.Favorites)
}

// migrateRadioFavorites commits the merged collection before clearing the old
// copy, so an interrupted migration cannot lose a favorite or duplicate it.
func migrateRadioFavorites(library *libraryState) error {
	store := radio.DefaultStore()
	legacy, err := store.Load()
	if err != nil {
		return fmt.Errorf("read radio preferences: %w", err)
	}
	if !library.RadioFavoritesMigrated {
		if err := mergeLegacyRadioFavorites(library, legacy.Favorites); err != nil {
			return err
		}
		library.RadioFavoritesMigrated = true
		if err := library.commit(); err != nil {
			return fmt.Errorf("save migrated favorites: %w", err)
		}
	}
	if len(legacy.Favorites) > 0 {
		if _, err := store.ClearFavorites(); err != nil {
			return fmt.Errorf("finish favorite migration: %w", err)
		}
	}
	return nil
}

func canonicalRadioFavorites(library *libraryState) []radio.Station {
	items, _ := libraryCollection(library, "favorites")
	favorites := make([]radio.Station, 0, len(items))
	for _, item := range items {
		if item.Kind == MediaStation && item.Station != nil {
			favorites = append(favorites, catalogFromStation(*item.Station))
		}
	}
	return favorites
}

func attachRadioFavorites(preferences radio.Library) (radio.Library, error) {
	library, err := fetchLibraryState()
	if err != nil {
		return preferences, err
	}
	preferences.Favorites = canonicalRadioFavorites(library)
	return preferences, nil
}

func loadRadioViewLibrary(store *radio.Store) (radio.Library, error) {
	preferences, err := store.Load()
	if err != nil {
		return radio.Library{}, err
	}
	return attachRadioFavorites(preferences)
}

// updateRadioFavorite changes the canonical collection and returns the
// station-filtered radio view, its final state, and whether it changed.
func updateRadioFavorite(store *radio.Store, station radio.Station, desired *bool) (radio.Library, bool, bool, error) {
	item, err := itemFromStation(stationFromCatalog(station)).normalized()
	if err != nil {
		return radio.Library{}, false, false, err
	}
	if err := ensureDaemon(); err != nil {
		return radio.Library{}, false, false, err
	}
	before, err := fetchLibraryState()
	if err != nil {
		return radio.Library{}, false, false, err
	}
	wasFavorite := before.Favorites[item.ID]
	var command string
	if desired == nil {
		data, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			return radio.Library{}, false, false, marshalErr
		}
		command = "favorite " + string(data)
	} else {
		data, marshalErr := json.Marshal(favoriteSetRequest{Item: item, Favorite: *desired})
		if marshalErr != nil {
			return radio.Library{}, false, false, marshalErr
		}
		command = "favorite-set " + string(data)
	}
	if _, err := ask(command); err != nil {
		return radio.Library{}, false, false, err
	}
	after, err := fetchLibraryState()
	if err != nil {
		return radio.Library{}, false, false, err
	}
	isFavorite := after.Favorites[item.ID]
	preferences, err := store.Load()
	if err != nil {
		return radio.Library{}, false, false, err
	}
	preferences.Favorites = canonicalRadioFavorites(after)
	return preferences, isFavorite, wasFavorite != isFavorite, nil
}
