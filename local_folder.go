package main

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func loadMediaFolder(ctx context.Context, path string) ([]MediaItem, error) {
	var sources []string
	var firstError error
	err := filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if candidate == path {
				return walkErr
			}
			if firstError == nil {
				firstError = walkErr
			}
			return nil
		}
		if !entry.IsDir() && isAudioPath(candidate) {
			sources = append(sources, candidate)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	probed, failures := probeMediaSources(ctx, sources)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	items := make([]MediaItem, 0, len(probed))
	for i, item := range probed {
		if failures[i] != nil {
			if firstError == nil {
				firstError = failures[i]
			}
			continue
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		if firstError != nil {
			return nil, fmt.Errorf("no readable audio files in %s: %w", path, firstError)
		}
		return nil, fmt.Errorf("no audio files in %s", path)
	}
	slices.SortFunc(items, compareFolderTracks)
	return items, nil
}

func compareFolderTracks(a, b MediaItem) int {
	// Keep folders together, including multi-disc directories. Within each
	// folder, group albums and honor disc/track tags before filename order.
	dirA, dirB := filepath.Dir(a.Source), filepath.Dir(b.Source)
	if order := cmp.Or(compareNatural(dirA, dirB), strings.Compare(dirA, dirB)); order != 0 {
		return order
	}
	albumA, albumB := normalizedAlbumIdentity(a.Album), normalizedAlbumIdentity(b.Album)
	if order := cmp.Or(compareNatural(albumA, albumB), strings.Compare(albumA, albumB)); order != 0 {
		return order
	}
	if albumA != "" {
		artistA, artistB := folderAlbumArtist(a), folderAlbumArtist(b)
		if order := cmp.Or(compareNatural(artistA, artistB), strings.Compare(artistA, artistB)); order != 0 {
			return order
		}
	}
	if order := cmp.Compare(max(1, a.DiscNumber), max(1, b.DiscNumber)); order != 0 {
		return order
	}
	if (a.TrackNumber > 0) != (b.TrackNumber > 0) {
		if a.TrackNumber > 0 {
			return -1
		}
		return 1
	}
	if order := cmp.Compare(a.TrackNumber, b.TrackNumber); order != 0 {
		return order
	}
	if order := compareNatural(filepath.Base(a.Source), filepath.Base(b.Source)); order != 0 {
		return order
	}
	return strings.Compare(a.Source, b.Source)
}

func normalizedAlbumIdentity(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func folderAlbumArtist(item MediaItem) string {
	if artist := normalizedAlbumIdentity(item.AlbumArtist); artist != "" {
		return artist
	}
	if item.Compilation {
		return "" // A compilation can have a different performer on every track.
	}
	return normalizedAlbumIdentity(item.Artist)
}

// compareNatural compares digit runs by value without overflowing on long
// filenames. Case and leading zeroes do not change the numeric ordering.
func compareNatural(a, b string) int {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for len(a) > 0 && len(b) > 0 {
		if a[0] >= '0' && a[0] <= '9' && b[0] >= '0' && b[0] <= '9' {
			endA, endB := 0, 0
			for endA < len(a) && a[endA] >= '0' && a[endA] <= '9' {
				endA++
			}
			for endB < len(b) && b[endB] >= '0' && b[endB] <= '9' {
				endB++
			}
			numberA, numberB := strings.TrimLeft(a[:endA], "0"), strings.TrimLeft(b[:endB], "0")
			if order := cmp.Compare(len(numberA), len(numberB)); order != 0 {
				return order
			}
			if order := strings.Compare(numberA, numberB); order != 0 {
				return order
			}
			a, b = a[endA:], b[endB:]
			continue
		}
		if order := cmp.Compare(a[0], b[0]); order != 0 {
			return order
		}
		a, b = a[1:], b[1:]
	}
	return cmp.Compare(len(a), len(b))
}
