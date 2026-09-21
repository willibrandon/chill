package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/willibrandon/chill/internal/audio"
	"github.com/willibrandon/chill/internal/podcast"
	"github.com/willibrandon/chill/internal/radio"
	"github.com/willibrandon/chill/internal/streammeta"
	"github.com/willibrandon/chill/internal/tracklog"
)

// TestNotificationPreferenceSurvivesEqualizerChanges guards shared playback settings.
func TestNotificationPreferenceSurvivesEqualizerChanges(t *testing.T) {
	withConfigDir(t)
	d := &Daemon{volume: 64, notifications: true, eqPreset: "Flat", eqCustom: audio.EqualizerBands{}}
	wantReply(t, d.setEqualizer(equalizerConfig{Preset: "Rock"}), true, "Rock")
	settings, err := loadPlaybackSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Notifications || settings.EQPreset != "Rock" || settings.Volume != 64 {
		t.Fatalf("saved settings = %+v", settings)
	}
}

// TestNowPlayingHistoryCoalescesAndFeedsMedia verifies one metadata transition path.
func TestNowPlayingHistoryCoalescesAndFeedsMedia(t *testing.T) {
	store := &tracklog.Store{Path: filepath.Join(t.TempDir(), "tracks.json")}
	d := &Daemon{
		station:    &Station{Name: "Example FM", URL: "https://radio.example/live", Desc: "Example", Artwork: "https://radio.example/art.png"},
		trackStore: store,
		state:      "playing",
		volume:     70,
	}
	now := streammeta.Parse("Artist - Song")
	d.updateNowPlaying(now)
	d.updateNowPlaying(now)
	entries, err := store.Load(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Artist != "Artist" || entries[0].Title != "Song" {
		t.Fatalf("history = %+v", entries)
	}
	state := d.mediaState()
	if state.Track.Title != "Song" || state.Track.Artist != "Artist" || state.Track.ArtURL != d.station.Artwork || state.AudioDevice != "auto" || state.AudioFormat != "f32le/stereo/48000Hz" {
		t.Fatalf("media state = %+v", state)
	}
}

// TestNewCommandHelpStaysSideEffectFree checks that discovery never starts a daemon or TUI.
func TestNewCommandHelpStaysSideEffectFree(t *testing.T) {
	ctx := context.Background()
	checks := []struct {
		name string
		run  func() (string, error)
	}{
		{"remote", func() (string, error) { return runRemoteCommand(ctx, []string{"--help"}) }},
		{"audio", func() (string, error) { return runAudioCommand(ctx, []string{"--help"}, false) }},
		{"providers", func() (string, error) { return runProvidersCommand(ctx, []string{"--help"}, false) }},
		{"search", func() (string, error) { return runSearchCommand(ctx, []string{"--help"}, false, false) }},
		{"browse", func() (string, error) { return runBrowseCommand(ctx, []string{"--help"}, false, false) }},
		{"link", func() (string, error) { return runLinkCommand(ctx, []string{"--help"}, false) }},
		{"completion", func() (string, error) { return runCompletionCommand([]string{"--help"}) }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			output, err := check.run()
			if err != nil || !strings.HasPrefix(output, "Usage: chill ") {
				t.Fatalf("output = %q, err = %v", output, err)
			}
		})
	}
}

// TestStationMediaHistoryNavigatesBackAndForward checks native previous and next behavior.
func TestStationMediaHistoryNavigatesBackAndForward(t *testing.T) {
	d := &Daemon{volume: 70, state: "idle", eqPreset: "Flat"}
	d.newPlayer = func(int, bool, bool) (player, error) {
		return &fakePlayer{event: make(chan playerEvent, 8)}, nil
	}
	first := &Station{Name: "one", URL: "https://example.com/one", Desc: "One"}
	second := &Station{Name: "two", URL: "https://example.com/two", Desc: "Two"}
	wantReply(t, d.playStation(first), true, "One")
	wantReply(t, d.playStation(second), true, "Two")
	if len(d.stationHistory) != 1 || d.stationHistory[0].Name != "one" {
		t.Fatalf("history = %+v", d.stationHistory)
	}
	wantReply(t, d.previousStation(), true, "One")
	if d.station.Name != "one" || len(d.stationForward) != 1 || d.stationForward[0].Name != "two" {
		t.Fatalf("previous state: station=%+v forward=%+v", d.station, d.stationForward)
	}
	wantReply(t, d.nextStation(), true, "Two")
	if d.station.Name != "two" || len(d.stationHistory) != 1 {
		t.Fatalf("next state: station=%+v history=%+v", d.station, d.stationHistory)
	}
	d.kill()
}

// TestPodcastMediaStateIncludesPublisherMetadata checks lock-screen podcast details.
func TestPodcastMediaStateIncludesPublisherMetadata(t *testing.T) {
	episode := &podcast.Episode{
		Title: "Episode", Show: "Show", Author: "Host", URL: "https://example.com/audio",
		Artwork: "https://example.com/art.png", Duration: 120,
	}
	d := &Daemon{episode: episode, episodeDuration: 2 * time.Minute, state: "playing", volume: 70}
	state := d.mediaState()
	if state.Track.Artist != "Host" || state.Track.Album != "Show" || state.Track.ArtURL != episode.Artwork || !state.Seekable {
		t.Fatalf("media state = %+v", state)
	}
}

// TestRadioOptionsAndForegroundSelection covers scriptable and browser foreground routing.
func TestRadioOptionsAndForegroundSelection(t *testing.T) {
	words, query, jsonOutput, play, favorite, foreground, err := parseRadioOptions([]string{
		"search", "jazz", "--language", "english", "--limit", "25", "--play", "2", "--favorite", "1", "--fg", "--json",
	})
	if err != nil || len(words) != 2 || words[1] != "jazz" || query.Language != "english" || query.Limit != 25 || play != 1 || favorite != 0 || !foreground || !jsonOutput {
		t.Fatalf("options = %v %+v %v %d %d %v, %v", words, query, jsonOutput, play, favorite, foreground, err)
	}
	if _, err := runRadioCommand(context.Background(), []string{"--json"}, false); err == nil {
		t.Fatal("JSON radio invocation without a command was accepted")
	}
	model := &tui{radioFG: true, radio: radioBrowser{page: radioPage{kind: "stations", stations: []radio.Station{{ID: "id", Name: "Station", URL: "https://example.com/live"}}}}}
	command := model.radioSelect(0)
	if command == nil {
		t.Fatal("foreground station selection did not exit the browser")
	}
	if _, ok := command().(tea.QuitMsg); !ok || model.radioChoice == nil || model.radioChoice.CatalogID != "id" {
		t.Fatalf("foreground choice = %+v", model.radioChoice)
	}
}
