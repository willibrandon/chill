package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingProgressProvider struct {
	states chan string
	delay  time.Duration
}

type providerProgressCall struct {
	position time.Duration
	state    string
}

type reorderingProgressProvider struct {
	mu    sync.Mutex
	calls []providerProgressCall
}

type orderedForegroundPlayer struct {
	closed bool
}

func (player *orderedForegroundPlayer) position() time.Duration { return 42 * time.Second }
func (player *orderedForegroundPlayer) close()                  { player.closed = true }

func (provider *recordingProgressProvider) key() string                    { return "test" }
func (provider *recordingProgressProvider) name() string                   { return "Test" }
func (provider *recordingProgressProvider) capabilities() []string         { return []string{"progress"} }
func (provider *recordingProgressProvider) validate(context.Context) error { return nil }
func (provider *recordingProgressProvider) reportProgress(ctx context.Context, _ MediaItem, _, _ time.Duration, state string) error {
	if provider.delay > 0 {
		timer := time.NewTimer(provider.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case provider.states <- state:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (provider *reorderingProgressProvider) key() string                    { return "test" }
func (provider *reorderingProgressProvider) name() string                   { return "Test" }
func (provider *reorderingProgressProvider) capabilities() []string         { return []string{"progress"} }
func (provider *reorderingProgressProvider) validate(context.Context) error { return nil }
func (provider *reorderingProgressProvider) reportProgress(ctx context.Context, _ MediaItem, position, _ time.Duration, state string) error {
	if state == "playing" {
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls = append(provider.calls, providerProgressCall{position: position, state: state})
	return nil
}

// TestAudiobookshelfMultipartProgressUsesBookTimeline keeps chapter progress absolute.
func TestAudiobookshelfMultipartProgressUsesBookTimeline(t *testing.T) {
	var mu sync.Mutex
	var progressPath string
	var progress map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/items/book":
			_, _ = response.Write([]byte(`{"id":"book","media":{"duration":7200,"metadata":{"title":"Novel","authorName":"Author"},"tracks":[{"ino":"1","title":"Part One","startOffset":0,"duration":3600},{"ino":"2","title":"Part Two","startOffset":3600,"duration":3600}]}}`))
		case request.Method == http.MethodPatch:
			mu.Lock()
			defer mu.Unlock()
			progressPath = request.URL.Path
			_ = json.NewDecoder(request.Body).Decode(&progress)
		default:
			http.Error(response, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()
	provider := newHTTPMediaProvider("books", providerConfig{Type: "audiobookshelf", URL: server.URL}, providerSecret{Token: "token"})
	items, err := provider.audiobookshelfTracks(t.Context(), map[string]any{"id": "book"})
	if err != nil || len(items) != 2 {
		t.Fatalf("tracks = %+v, %v", items, err)
	}
	if items[1].ProviderMeta["progress_offset"] != "3600" || items[1].ProviderMeta["progress_duration"] != "7200" {
		t.Fatalf("second-part progress metadata = %+v", items[1].ProviderMeta)
	}
	if err := provider.reportProgress(t.Context(), items[1], 120*time.Second, time.Hour, "playing"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if progressPath != "/api/me/progress/book" || progress["currentTime"] != float64(3720) || progress["duration"] != float64(7200) || progress["isFinished"] != false {
		t.Fatalf("progress %s = %#v", progressPath, progress)
	}
	mu.Unlock()
	if err := provider.reportProgress(t.Context(), items[0], time.Hour, time.Hour, "finished"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if progress["isFinished"] != false {
		t.Fatalf("first part marked the book finished: %#v", progress)
	}
	mu.Unlock()
	if err := provider.reportProgress(t.Context(), items[1], time.Hour, time.Hour, "finished"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if progress["isFinished"] != true {
		t.Fatalf("book end was not marked finished: %#v", progress)
	}
	mu.Unlock()
	if err := provider.reportProgress(t.Context(), items[1], time.Hour, time.Hour, "stopped"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if progress["currentTime"] != float64(7200) || progress["duration"] != float64(7200) || progress["isFinished"] != true {
		t.Fatalf("stopping at the book end cleared completion: %#v", progress)
	}
}

// TestAudiobookshelfPodcastProgressUsesEpisodeEndpoint keeps episode records distinct.
func TestAudiobookshelfPodcastProgressUsesEpisodeEndpoint(t *testing.T) {
	path := ""
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) { path = request.URL.Path }))
	defer server.Close()
	provider := newHTTPMediaProvider("books", providerConfig{Type: "audiobookshelf", URL: server.URL}, providerSecret{Token: "token"})
	item := providerMediaItem("books", "show:episode", "Episode", "", "", "", "", 60)
	item.ProviderMeta["item_id"], item.ProviderMeta["episode_id"] = "show", "episode"
	if err := provider.reportProgress(t.Context(), item, 10*time.Second, time.Minute, "playing"); err != nil {
		t.Fatal(err)
	}
	if path != "/api/me/progress/show/episode" {
		t.Fatalf("progress path = %q", path)
	}
}

// TestProviderRedirectStripsCredentialsAcrossOrigins prevents cross-origin token disclosure.
func TestProviderRedirectStripsCredentialsAcrossOrigins(t *testing.T) {
	tests := []struct {
		kind    string
		headers []string
	}{
		{kind: "jellyfin", headers: []string{"X-Emby-Token"}},
		{kind: "plex", headers: []string{"X-Plex-Token"}},
		{kind: "qobuz", headers: []string{"X-App-Id", "X-User-Auth-Token"}},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			var mu sync.Mutex
			leaked := http.Header{}
			var target *httptest.Server
			target = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/first" {
					http.Redirect(response, request, target.URL+"/final", http.StatusFound)
					return
				}
				mu.Lock()
				leaked = request.Header.Clone()
				mu.Unlock()
				_, _ = response.Write([]byte(`{}`))
			}))
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				http.Redirect(response, request, target.URL+"/first", http.StatusFound)
			}))
			defer origin.Close()
			config := providerConfig{Type: test.kind, URL: origin.URL, ClientID: "application"}
			provider := newHTTPMediaProvider(test.kind, config, providerSecret{Token: "private"})
			var output map[string]any
			if err := provider.getJSON(t.Context(), "/System/Info", nil, &output); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, header := range test.headers {
				if value := leaked.Get(header); value != "" {
					t.Errorf("%s leaked on second foreign redirect: %q", header, value)
				}
			}
		})
	}
}

// TestProviderRedirectRejectsHTTPSDowngrade prevents clear-text redirect disclosure.
func TestProviderRedirectRejectsHTTPSDowngrade(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("downgrade target was contacted") }))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	client := origin.Client()
	client.CheckRedirect = providerHTTPClient().CheckRedirect
	provider := newHTTPMediaProvider("video", providerConfig{Type: "jellyfin", URL: origin.URL}, providerSecret{Token: "private"})
	provider.client = client
	var output map[string]any
	err := provider.getJSON(t.Context(), "/System/Info", nil, &output)
	if err == nil || !strings.Contains(err.Error(), "insecure destination") {
		t.Fatalf("downgrade error = %v", err)
	}
}

// TestHostedCatalogsExposeOnlyExplicitPreviews keeps catalog metadata honest.
func TestHostedCatalogsExposeOnlyExplicitPreviews(t *testing.T) {
	for _, kind := range []string{"spotify", "tidal", "qobuz"} {
		provider := newHTTPMediaProvider(kind, providerConfig{Type: kind, ClientID: "app"}, providerSecret{Token: "token"})
		capabilities := provider.capabilities()
		if slices.Contains(capabilities, "playback") || !slices.Contains(capabilities, "previews") {
			t.Fatalf("%s capabilities = %v", kind, capabilities)
		}
	}
	spotify := newHTTPMediaProvider("spotify", providerConfig{Type: "spotify"}, providerSecret{Token: "token"})
	item := spotify.spotifyItem(map[string]any{"id": "track", "name": "Song", "duration_ms": float64(240000), "preview_url": "https://audio.example/preview.mp3"})
	if item.Duration != 30 || item.ProviderMeta["playback"] != "preview" || item.ProviderMeta["catalog_duration"] != "240" {
		t.Fatalf("preview item = %+v", item)
	}
	if !strings.HasSuffix(item.display(), "[preview]") {
		t.Fatalf("preview label = %q", item.display())
	}
	stream, err := spotify.resolve(t.Context(), item)
	if err != nil || stream.URL != "https://audio.example/preview.mp3" {
		t.Fatalf("preview stream = %+v, %v", stream, err)
	}
	full := providerMediaItem("spotify", "track", "Song", "", "", "", "https://open.spotify.com/track/track", 240)
	spotify.setPreview(&full, "")
	if !strings.HasSuffix(full.display(), "[catalog]") || providerPlaybackAvailable(full) == nil {
		t.Fatalf("catalog-only label = %q", full.display())
	}
	if _, err := spotify.resolve(t.Context(), full); err == nil {
		t.Fatal("catalog item was presented as full-track playback")
	}
}

// TestQobuzNumericTrackIdentity preserves numeric catalog identifiers.
func TestQobuzNumericTrackIdentity(t *testing.T) {
	provider := newHTTPMediaProvider("qobuz", providerConfig{Type: "qobuz"}, providerSecret{})
	item, ok := provider.openCatalogItem(map[string]any{"id": float64(5966783), "title": "Song"})
	if !ok || item.ProviderID != "5966783" {
		t.Fatalf("numeric Qobuz item = %+v, %t", item, ok)
	}
}

// TestPlexPaginationUsesReportedTotal keeps browsing past the first page.
func TestPlexPaginationUsesReportedTotal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/library/sections":
			_, _ = response.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","type":"artist","title":"Music"}]}}`))
		case "/library/sections/1/all":
			if request.URL.Query().Get("X-Plex-Container-Size") != "3" {
				t.Errorf("container size = %q", request.URL.Query().Get("X-Plex-Container-Size"))
			}
			_, _ = response.Write([]byte(`{"MediaContainer":{"totalSize":3,"Metadata":[{"ratingKey":"1","title":"A"},{"ratingKey":"2","title":"B"}]}}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	provider := newHTTPMediaProvider("plex", providerConfig{Type: "plex", URL: server.URL}, providerSecret{Token: "token"})
	page, err := provider.browsePlex(t.Context(), providerBrowseRequest{Kind: "albums", Limit: 2})
	if err != nil || len(page.Entries) != 2 || page.Next != 2 {
		t.Fatalf("Plex page = %+v, %v", page, err)
	}
}

// TestProviderCookiesReachPlaybackExtractor carries configured browser sessions.
func TestProviderCookiesReachPlaybackExtractor(t *testing.T) {
	previousFinder, previousRunner := findExtractor, runDiagnosticCommand
	t.Cleanup(func() { findExtractor, runDiagnosticCommand = previousFinder, previousRunner })
	findExtractor = func(string) string { return "yt-dlp" }
	var arguments []string
	runDiagnosticCommand = func(_ context.Context, _ string, _ time.Duration, args ...string) (string, string, error) {
		arguments = slices.Clone(args)
		return `{"url":"https://audio.example/song"}`, "", nil
	}
	resolved, err := resolveAudioWithCookies(t.Context(), "https://www.youtube.com/watch?v=private", "firefox")
	if err != nil || resolved.URL == "" {
		t.Fatalf("resolved = %+v, %v", resolved, err)
	}
	index := slices.Index(arguments, "--cookies-from-browser")
	if index < 0 || index+1 >= len(arguments) || arguments[index+1] != "firefox" {
		t.Fatalf("extractor arguments = %q", arguments)
	}
}

// TestProviderResumeRestoresDaemonOffset resumes finite provider media.
func TestProviderResumeRestoresDaemonOffset(t *testing.T) {
	withConfigDir(t)
	item := providerMediaItem("books", "chapter", "Chapter", "Author", "Book", "", "", 3600)
	library := emptyLibrary()
	library.Resume[item.ID] = resumePoint{Position: 1200, Duration: 3600}
	d := &Daemon{library: library, audio: defaultPlaybackSettings().Audio, activeAudioDevice: "auto"}
	d.newPlayer = func(int, bool, bool) (player, error) { return &fakePlayer{event: make(chan playerEvent, 1)}, nil }
	if result := d.playMediaItem(item, false, false); !commandSucceeded(result) {
		t.Fatalf("play result = %s", result)
	}
	defer d.kill()
	if d.episodeOffset != 1195*time.Second {
		t.Fatalf("resume offset = %s", d.episodeOffset)
	}
}

// TestForegroundProviderMetadataIsSeeded preserves saved playlist details.
func TestForegroundProviderMetadataIsSeeded(t *testing.T) {
	registry := &providerRegistry{providers: map[string]mediaProvider{}, recent: map[string]MediaItem{}}
	providerRegistryState.Lock()
	previous := providerRegistryState.registry
	providerRegistryState.registry = registry
	providerRegistryState.Unlock()
	t.Cleanup(func() {
		providerRegistryState.Lock()
		providerRegistryState.registry = previous
		providerRegistryState.Unlock()
	})
	item := providerMediaItem("soundcloud", "song", "Song", "Artist", "", "", "https://soundcloud.com/artist/song", 180)
	rememberForegroundProviderItem(item)
	saved, ok := registry.item("soundcloud", "song")
	if !ok || saved.ProviderMeta["url"] != item.ProviderMeta["url"] || saved.Title != "Song" {
		t.Fatalf("remembered item = %+v, %t", saved, ok)
	}
}

// TestDaemonStopReportsStoppedProviderLifecycle closes remote sessions cleanly.
func TestDaemonStopReportsStoppedProviderLifecycle(t *testing.T) {
	withConfigDir(t)
	recorder := &recordingProgressProvider{states: make(chan string, 1)}
	registry := &providerRegistry{providers: map[string]mediaProvider{"test": recorder}, order: []string{"test"}, recent: map[string]MediaItem{}}
	providerRegistryState.Lock()
	previous := providerRegistryState.registry
	providerRegistryState.registry = registry
	providerRegistryState.Unlock()
	t.Cleanup(func() {
		providerRegistryState.Lock()
		providerRegistryState.registry = previous
		providerRegistryState.Unlock()
	})
	item := providerMediaItem("test", "track", "Track", "Artist", "", "", "", 120)
	d := &Daemon{current: &item, library: emptyLibrary(), player: &fakePlayer{event: make(chan playerEvent, 1)}, watchDone: make(chan struct{}), state: "playing", episodeDuration: 2 * time.Minute}
	d.kill()
	select {
	case state := <-recorder.states:
		if state != "stopped" {
			t.Fatalf("lifecycle state = %q", state)
		}
	case <-time.After(time.Second):
		t.Fatal("provider did not receive stopped lifecycle")
	}
	d.providerProgress.wait()
}

// TestDaemonShutdownDrainsProviderProgress waits for the final remote lifecycle update.
func TestDaemonShutdownDrainsProviderProgress(t *testing.T) {
	withConfigDir(t)
	recorder := &recordingProgressProvider{states: make(chan string, 1), delay: 100 * time.Millisecond}
	registry := &providerRegistry{providers: map[string]mediaProvider{"test": recorder}, order: []string{"test"}, recent: map[string]MediaItem{}}
	providerRegistryState.Lock()
	previous := providerRegistryState.registry
	providerRegistryState.registry = registry
	providerRegistryState.Unlock()
	t.Cleanup(func() {
		providerRegistryState.Lock()
		providerRegistryState.registry = previous
		providerRegistryState.Unlock()
	})
	item := providerMediaItem("test", "track", "Track", "Artist", "", "", "", 120)
	d := &Daemon{current: &item, library: emptyLibrary(), player: &fakePlayer{event: make(chan playerEvent, 1)}, watchDone: make(chan struct{}), state: "playing", episodeDuration: 2 * time.Minute}
	d.kill()
	d.providerProgress.wait()
	select {
	case state := <-recorder.states:
		if state != "stopped" {
			t.Fatalf("lifecycle state = %q", state)
		}
	default:
		t.Fatal("provider synchronization was not drained")
	}
}

// TestDaemonProviderProgressIsOrdered keeps older updates ahead of terminal state.
func TestDaemonProviderProgressIsOrdered(t *testing.T) {
	provider := &reorderingProgressProvider{}
	registry := &providerRegistry{providers: map[string]mediaProvider{"test": provider}, order: []string{"test"}, recent: map[string]MediaItem{}}
	providerRegistryState.Lock()
	previous := providerRegistryState.registry
	providerRegistryState.registry = registry
	providerRegistryState.Unlock()
	t.Cleanup(func() {
		providerRegistryState.Lock()
		providerRegistryState.registry = previous
		providerRegistryState.Unlock()
	})
	item := providerMediaItem("test", "book", "Book", "Author", "", "", "", 7200)
	d := &Daemon{}
	d.mu.Lock()
	d.syncProviderProgress(item, 7100*time.Second, 7200*time.Second, "playing")
	d.providerCompleted = true
	d.syncProviderProgress(item, 7200*time.Second, 7200*time.Second, "stopped")
	d.mu.Unlock()
	d.providerProgress.wait()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.calls) != 2 || provider.calls[0].state != "playing" || provider.calls[0].position != 7100*time.Second || provider.calls[1].state != "stopped" || provider.calls[1].position != 7200*time.Second {
		t.Fatalf("provider calls = %+v", provider.calls)
	}
}

// TestCompletedAudiobookStopRemainsFinished retains confirmed completion state.
func TestCompletedAudiobookStopRemainsFinished(t *testing.T) {
	withConfigDir(t)
	var mu sync.Mutex
	var payloads []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		payloads = append(payloads, payload)
		mu.Unlock()
	}))
	defer server.Close()
	provider := newHTTPMediaProvider("books", providerConfig{Type: "audiobookshelf", URL: server.URL}, providerSecret{Token: "token"})
	registry := &providerRegistry{providers: map[string]mediaProvider{"books": provider}, order: []string{"books"}, recent: map[string]MediaItem{}}
	providerRegistryState.Lock()
	previous := providerRegistryState.registry
	providerRegistryState.registry = registry
	providerRegistryState.Unlock()
	t.Cleanup(func() {
		providerRegistryState.Lock()
		providerRegistryState.registry = previous
		providerRegistryState.Unlock()
	})
	item := providerMediaItem("books", "part-two", "Part Two", "Author", "Book", "", "", 3600)
	item.ProviderMeta["item_id"] = "book"
	item.ProviderMeta["progress_offset"] = "3600"
	item.ProviderMeta["progress_duration"] = "7200"
	item.ProviderMeta["progress_final"] = "true"
	d := &Daemon{current: &item, library: emptyLibrary(), episodeOffset: 3598 * time.Second, episodeDuration: time.Hour, state: "playing"}
	d.mu.Lock()
	d.finishFiniteItem()
	d.kill()
	d.mu.Unlock()
	d.providerProgress.wait()
	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 2 {
		t.Fatalf("progress updates = %#v", payloads)
	}
	for _, payload := range payloads {
		if payload["currentTime"] != float64(7198) || payload["duration"] != float64(7200) || payload["isFinished"] != true {
			t.Fatalf("completed progress was downgraded: %#v", payloads)
		}
	}
}

// TestSubsonicCompletionScrobblesOnce separates the play event from retained completion.
func TestSubsonicCompletionScrobblesOnce(t *testing.T) {
	for _, kind := range []string{"navidrome", "subsonic"} {
		for _, mode := range []string{"daemon", "foreground"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				var mu sync.Mutex
				var submissions []string
				server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
					if request.URL.Path != "/rest/scrobble.view" {
						http.NotFound(response, request)
						return
					}
					mu.Lock()
					submissions = append(submissions, request.URL.Query().Get("submission"))
					mu.Unlock()
					_, _ = response.Write([]byte(`{"subsonic-response":{"status":"ok","version":"1.16.1"}}`))
				}))
				defer server.Close()
				provider := newHTTPMediaProvider(kind, providerConfig{Type: kind, URL: server.URL, Username: "listener"}, providerSecret{Password: "password"})
				registry := &providerRegistry{providers: map[string]mediaProvider{kind: provider}, order: []string{kind}, recent: map[string]MediaItem{}}
				providerRegistryState.Lock()
				previous := providerRegistryState.registry
				providerRegistryState.registry = registry
				providerRegistryState.Unlock()
				t.Cleanup(func() {
					providerRegistryState.Lock()
					providerRegistryState.registry = previous
					providerRegistryState.Unlock()
				})
				item := providerMediaItem(kind, "track", "Track", "Artist", "", "", "", 180)
				if mode == "daemon" {
					d := &Daemon{providerCompleted: true}
					d.mu.Lock()
					d.syncProviderProgress(item, 0, 180*time.Second, "started")
					d.syncProviderProgress(item, 180*time.Second, 180*time.Second, "finished")
					d.syncProviderProgress(item, 180*time.Second, 180*time.Second, "finished")
					d.syncProviderProgress(item, 180*time.Second, 180*time.Second, "stopped")
					d.mu.Unlock()
					d.providerProgress.wait()
				} else {
					model := &foregroundMediaModel{items: []MediaItem{item}, providerDone: true}
					_ = model.syncProviderProgress("started")
					_ = model.syncProviderProgress("finished")
					_ = model.syncProviderProgress("finished")
					_ = model.syncProviderProgress("stopped")
					model.providerSync.wait()
				}
				mu.Lock()
				defer mu.Unlock()
				if !slices.Equal(submissions, []string{"false", "true"}) {
					t.Fatalf("scrobble submissions = %v", submissions)
				}
			})
		}
	}
}

// TestAsynchronousAudioFailureFallsBackToDefault covers post-load device failures.
func TestAsynchronousAudioFailureFallsBackToDefault(t *testing.T) {
	withConfigDir(t)
	item := testTrack(t.TempDir()+"/song.flac", "Song")
	settings := defaultPlaybackSettings().Audio
	settings.Device = "coreaudio/missing"
	var started []AudioSettings
	d := &Daemon{library: emptyLibrary(), audio: settings, activeAudioDevice: settings.Device}
	d.newAudioPlayer = func(_ int, _, _ bool, _ time.Duration, _ bool, audioSettings AudioSettings) (player, error) {
		started = append(started, audioSettings)
		return &fakePlayer{event: make(chan playerEvent, 2)}, nil
	}
	if result := d.playMediaItem(item, false, false); !commandSucceeded(result) {
		t.Fatalf("play result = %s", result)
	}
	d.playerEvent(playerEvent{err: "audio initialization failed", output: true})
	defer d.kill()
	if len(started) != 2 || started[0].Device != settings.Device || started[1].Device != "auto" || d.activeAudioDevice != "auto" {
		t.Fatalf("starts = %+v, active = %q", started, d.activeAudioDevice)
	}
}

// TestIdleDeviceSelectionAppliesToNextPlayback keeps preference and runtime state distinct.
func TestIdleDeviceSelectionAppliesToNextPlayback(t *testing.T) {
	withConfigDir(t)
	settings := defaultPlaybackSettings().Audio
	d := &Daemon{library: emptyLibrary(), audio: settings, activeAudioDevice: "auto", state: "idle"}
	selected := settings
	selected.Device = "coreaudio/selected"
	if result := d.applyAudioSettings(selected); !commandSucceeded(result) {
		t.Fatalf("device selection = %s", result)
	}
	if d.activeAudioDevice != selected.Device || d.audioDeviceFallback {
		t.Fatalf("idle routing = %q, fallback = %t", d.activeAudioDevice, d.audioDeviceFallback)
	}
	var started AudioSettings
	d.newAudioPlayer = func(_ int, _, _ bool, _ time.Duration, _ bool, audioSettings AudioSettings) (player, error) {
		started = audioSettings
		return &fakePlayer{event: make(chan playerEvent, 1)}, nil
	}
	item := testTrack(t.TempDir()+"/song.flac", "Song")
	if result := d.playMediaItem(item, false, false); !commandSucceeded(result) {
		t.Fatalf("play result = %s", result)
	}
	defer d.kill()
	if started.Device != selected.Device {
		t.Fatalf("playback device = %q, want %q", started.Device, selected.Device)
	}
}

// TestRemoteJobCapacityEvictsCompletedWork reserves capacity for active work.
func TestRemoteJobCapacityEvictsCompletedWork(t *testing.T) {
	service := newRemoteService(&Daemon{})
	now := time.Now().UTC()
	for index := range remoteJobLimit {
		id := string(rune(index + 1))
		service.jobs[id] = &remoteJobEntry{job: remoteJob{ID: id, State: "succeeded", FinishedAt: now.Add(time.Duration(index) * time.Second)}}
	}
	if _, err := service.submit("playback.pause", nil); err != nil {
		t.Fatalf("submit after completed jobs: %v", err)
	}
}

// TestRemoteAudioPatchMergesAfterDeviceDiscovery prevents concurrent lost updates.
func TestRemoteAudioPatchMergesAfterDeviceDiscovery(t *testing.T) {
	withConfigDir(t)
	previous := enumerateAudioDevices
	t.Cleanup(func() { enumerateAudioDevices = previous })
	started, release := make(chan struct{}), make(chan struct{})
	enumerateAudioDevices = func(context.Context, string) ([]AudioDevice, error) {
		close(started)
		<-release
		return []AudioDevice{{ID: "auto", Name: "Automatic"}, {ID: "device", Name: "Device"}}, nil
	}
	d := &Daemon{audio: defaultPlaybackSettings().Audio, activeAudioDevice: "auto"}
	done := make(chan error, 1)
	go func() {
		_, _, err := d.remoteAudioSettings(t.Context(), map[string]any{"device": "device"})
		done <- err
	}()
	<-started
	if _, _, err := d.remoteAudioSettings(t.Context(), map[string]any{"sample_rate": 96000}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d.audio.Device != "device" || d.audio.SampleRate != 96000 {
		t.Fatalf("merged audio settings = %+v", d.audio)
	}
}

// TestRemoteObservationIsIdleWithoutSubscribersAndCachesDurableState prevents idle churn.
func TestRemoteObservationIsIdleWithoutSubscribersAndCachesDurableState(t *testing.T) {
	withConfigDir(t)
	resetProviders()
	t.Cleanup(resetProviders)
	d := &Daemon{library: emptyLibrary(), audio: defaultPlaybackSettings().Audio, activeAudioDevice: "auto"}
	service := newRemoteService(d)
	if allocations := testing.AllocsPerRun(100, service.observe); allocations != 0 {
		t.Fatalf("idle observation allocations = %.1f", allocations)
	}
	service.observe()
	if service.libraryCache != nil || service.podcastCache != nil {
		t.Fatal("idle observation built a durable snapshot")
	}
	service.subscribers["test"] = &remoteSubscription{topics: map[string]bool{"runtime.playback": true}, events: make(chan remoteEvent, 8), overflow: make(chan struct{}, 1)}
	service.observe()
	firstLibrary, firstPodcasts := service.libraryCache, service.podcastCache
	if firstLibrary == nil || firstPodcasts == nil {
		t.Fatal("subscribed observation did not build a snapshot")
	}
	service.observe()
	if service.libraryCache != firstLibrary || service.podcastCache != firstPodcasts {
		t.Fatal("unchanged durable state was rebuilt")
	}
}

// TestProviderSetupPreservesPasswordWhitespace retains exact credentials.
func TestProviderSetupPreservesPasswordWhitespace(t *testing.T) {
	if got := normalizedProviderSetupValue("password", "  secret  "); got != "  secret  " {
		t.Fatalf("password = %q", got)
	}
	if got := normalizedProviderSetupValue("username", "  listener  "); got != "listener" {
		t.Fatalf("username = %q", got)
	}
}

// TestArtworkWriteFailureDoesNotPublishCacheFile rejects partial cache entries.
func TestArtworkWriteFailureDoesNotPublishCacheFile(t *testing.T) {
	withConfigDir(t)
	previous := writeProviderArtwork
	t.Cleanup(func() { writeProviderArtwork = previous })
	writeProviderArtwork = func(_ *os.File, data []byte) (int, error) {
		return len(data) / 2, errors.New("disk full")
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "image/png")
		_, _ = response.Write([]byte("\x89PNG\r\n\x1a\nfixture"))
	}))
	defer server.Close()
	provider := newHTTPMediaProvider("jellyfin", providerConfig{Type: "jellyfin", URL: server.URL}, providerSecret{Token: "token"})
	item := providerMediaItem("jellyfin", "song", "Song", "", "", "", "", 60)
	item.ProviderMeta["image_id"] = "song"
	if _, err := provider.loadArtwork(t.Context(), item); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("artwork error = %v", err)
	}
	if _, err := os.Stat(providerArtworkPath(item, ".png")); !os.IsNotExist(err) {
		t.Fatalf("partial artwork was published: %v", err)
	}
}

// TestProviderEmbeddedLyricsAreAvailableToCLI includes provider metadata in lookup.
func TestProviderEmbeddedLyricsAreAvailableToCLI(t *testing.T) {
	item := providerMediaItem("music", "track", "Song", "Artist", "Album", "", "", 180)
	item.EmbeddedLyrics = "line one\nline two"
	result, err := lyricsFromStatus(t.Context(), &Status{Item: &item})
	if err != nil || result.Plain != item.EmbeddedLyrics || result.Track != "Song" {
		t.Fatalf("lyrics = %+v, %v", result, err)
	}
}

// TestForegroundPlaybackClosesBeforeProviderSync prevents audio from lingering on exit.
func TestForegroundPlaybackClosesBeforeProviderSync(t *testing.T) {
	player := &orderedForegroundPlayer{}
	finalizeForegroundPlayback(player, func(position time.Duration) {
		if !player.closed {
			t.Fatal("provider synchronization started before audio stopped")
		}
		if position != 42*time.Second {
			t.Fatalf("captured position = %s", position)
		}
	})
}

// TestCurrentAudioStatusUsesDaemonActiveDevice keeps JSON output truthful after fallback.
func TestCurrentAudioStatusUsesDaemonActiveDevice(t *testing.T) {
	runtimeDir, err := os.MkdirTemp("", "ca-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	withConfigDir(t)
	t.Setenv("TMPDIR", runtimeDir)
	t.Setenv("TMP", runtimeDir)
	t.Setenv("TEMP", runtimeDir)
	listener, err := listenSocket()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		cleanupSocket()
	})
	settings := defaultPlaybackSettings().Audio
	settings.Device = "coreaudio/missing"
	want := settings.status("auto")
	data, err := json.Marshal(Status{Version: buildVersion(), BuildID: buildIdentity(), Protocol: daemonProtocol, Audio: want, State: "idle", Queue: []MediaItem{}, PlayNext: []MediaItem{}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer connection.Close()
				command, _ := bufio.NewReader(connection).ReadString('\n')
				if command != "" {
					_, _ = fmt.Fprintln(connection, string(data))
				}
			}()
		}
	}()
	got, err := currentAudioStatus()
	if err != nil {
		t.Fatal(err)
	}
	if got.Device != settings.Device || got.ActiveDevice != "auto" {
		t.Fatalf("audio status = %+v", got)
	}
}
