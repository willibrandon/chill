package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/willibrandon/chill/internal/lyrics"
)

func currentLyrics(ctx context.Context) (lyrics.Result, error) {
	status, err := fetchStatus()
	if err != nil {
		return lyrics.Result{}, err
	}
	if status != nil && status.Item != nil && status.Item.Kind == MediaTrack {
		item := status.Item
		if item.EmbeddedLyrics != "" {
			return lyrics.Result{Track: item.Title, Artist: item.Artist, Album: item.Album, Plain: item.EmbeddedLyrics}, nil
		}
		return lyrics.NewClient().Get(ctx, item.Artist, item.Title)
	}
	if status == nil || status.NowPlaying == nil {
		return lyrics.Result{}, fmt.Errorf("no recognized track")
	}
	return lyrics.NewClient().Get(ctx, status.NowPlaying.Artist, status.NowPlaying.Title)
}

func runLyricsCommand(ctx context.Context, args []string, colored bool) (string, error) {
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOutput = true
		case "--help", "-h", "help":
			text := "Usage: chill lyrics [--json]"
			if colored {
				text = dim + text + reset
			}
			return text, nil
		default:
			return "", fmt.Errorf("unknown lyrics option %s", arg)
		}
	}
	result, err := currentLyrics(ctx)
	if err != nil {
		return "", err
	}
	if jsonOutput {
		data, err := json.Marshal(result)
		return string(data), err
	}
	if result.Instrumental {
		return result.Artist + " — " + result.Track + "\n\nInstrumental", nil
	}
	lines := result.Lines()
	if len(lines) == 0 {
		return "", fmt.Errorf("lyrics response was empty")
	}
	heading := result.Track
	if result.Artist != "" {
		heading = result.Artist + " — " + result.Track
	}
	return heading + "\n\n" + strings.Join(lines, "\n"), nil
}
