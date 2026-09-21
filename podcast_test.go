package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/willibrandon/chill/internal/podcast"
)

// TestPodcastLibraryIdentityResumeAndCorruption checks saved progress and unreadable files.
func TestPodcastLibraryIdentityResumeAndCorruption(t *testing.T) {
	withConfigDir(t)
	l, err := loadPodcastLibrary()
	if err != nil {
		t.Fatal(err)
	}
	e := podcast.Episode{FeedURL: "https://example.com/feed", GUID: "one", Title: "One", URL: "https://example.com/audio"}
	if err := l.record(e, 100, 600, false); err != nil {
		t.Fatal(err)
	}
	e.URL += "?new-token=1"
	if got := l.resume(e); got != 95*time.Second {
		t.Fatal("resume lost episode", got)
	}
	if err := l.record(e, 550, 600, false); err != nil {
		t.Fatal(err)
	}
	if l.resume(e) != 0 {
		t.Fatal("finished episode resumed near end")
	}
	if err := os.WriteFile(podcastPath(), []byte(`{"version":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPodcastLibrary(); err == nil {
		t.Fatal("accepted newer storage format")
	}
	d := &Daemon{}
	wantReply(t, d.execute("podcast-country", "gb"), false, "unsupported")
	data, _ := os.ReadFile(podcastPath())
	if string(data) != `{"version":99}` {
		t.Fatal("overwrote unreadable storage")
	}
}

// TestPodcastPlaybackIntegration checks real episode playback, seeking, completion, and replay.
func TestPodcastPlaybackIntegration(t *testing.T) {
	if err := checkPodcastRequirements(); err != nil {
		t.Fatal(err)
	}
	tmp := os.TempDir()
	withConfigDir(t)
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, tmp)
	}
	config := t.TempDir()
	t.Setenv("MPV_HOME", config)
	if err := os.WriteFile(filepath.Join(config, "mpv.conf"), []byte("ao=null\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wav := stereoFixture(t, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, wav) }))
	defer server.Close()
	d := &Daemon{volume: 55}
	defer func() { d.mu.Lock(); defer d.mu.Unlock(); d.kill() }()
	e := podcast.Episode{FeedURL: server.URL + "/feed", GUID: "tone", Show: "Test Show", Title: "Tone", URL: server.URL + "/audio", Duration: 8}
	b, _ := json.Marshal(episodeRequest{Episode: e})
	wantReply(t, d.execute("episode", string(b)), true, "loading")
	waitState(t, d, "playing", 5*time.Second)
	wantReply(t, d.execute("pause", ""), true, "paused")
	wantReply(t, d.execute("seek", "4"), true, "seeking")
	s := waitState(t, d, "paused", 5*time.Second)
	if s.Position < 4 || s.Episode == nil || !s.Seekable {
		t.Fatalf("seek lost playback: %+v", s)
	}
	wantReply(t, d.execute("speed", "2"), true, "2.00x")
	wantReply(t, d.execute("resume", ""), true, "resumed")
	s = waitState(t, d, "ended", 6*time.Second)
	if s.Retries != 0 || s.Error != "" {
		t.Fatalf("episode EOF triggered reconnect: %+v", s)
	}
	l, err := loadPodcastLibrary()
	if err != nil || !l.Progress[e.Key()].Played {
		t.Fatalf("completion not saved: %+v %v", l, err)
	}
	wantReply(t, d.execute("resume", ""), true, "seeking")
	s = waitState(t, d, "playing", 5*time.Second)
	if s.Position > 2 {
		t.Fatalf("replay did not start over: %+v", s)
	}
}

// TestPodcastQueueFinishesInOrder checks automatic playback and final played marks.
func TestPodcastQueueFinishesInOrder(t *testing.T) {
	tmp := os.TempDir()
	withConfigDir(t)
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, tmp)
	}
	config := t.TempDir()
	t.Setenv("MPV_HOME", config)
	if err := os.WriteFile(filepath.Join(config, "mpv.conf"), []byte("ao=null\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wav := stereoFixture(t, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, wav) }))
	defer server.Close()
	d := &Daemon{volume: 55}
	defer func() { d.mu.Lock(); defer d.mu.Unlock(); d.kill() }()
	first := podcast.Episode{FeedURL: server.URL + "/feed", URL: server.URL + "/audio", GUID: "first", Title: "First", Show: "Show", Duration: 2}
	second := first
	second.GUID, second.Title = "second", "Second"
	b, _ := json.Marshal(episodeRequest{Episode: first})
	wantReply(t, d.execute("episode", string(b)), true, "loading")
	waitState(t, d, "playing", 5*time.Second)
	wantReply(t, d.execute("pause", ""), true, "paused")
	b, _ = json.Marshal(second)
	wantReply(t, d.execute("podcast-queue", string(b)), true, "queued")
	wantReply(t, d.execute("speed", "2"), true, "2.00x")
	wantReply(t, d.execute("resume", ""), true, "resumed")
	s := waitState(t, d, "ended", 8*time.Second)
	if s.Episode.GUID != "second" || s.Queued != 0 || s.Speed != 2 || s.Retries != 0 {
		t.Fatalf("bad queue completion: %+v", s)
	}
	l, err := loadPodcastLibrary()
	if err != nil {
		t.Fatal(err)
	}
	if !l.Progress[first.Key()].Played || !l.Progress[second.Key()].Played {
		t.Fatal("queue did not preserve completed episodes")
	}
}

// TestPodcastControlsRejectInvalidInput checks seek, speed, and dependency errors.
func TestPodcastControlsRejectInvalidInput(t *testing.T) {
	withConfigDir(t)
	d := &Daemon{}
	wantReply(t, d.execute("seek", "30"), false, "finite media")
	for _, arg := range []string{"NaN", "Inf", "-2", "5", "oops"} {
		wantReply(t, d.execute("speed", arg), false, "speed")
	}
	for _, arg := range []string{"NaN", "Inf", "oops"} {
		if _, err := seekDelta(arg); err == nil {
			t.Fatal("invalid seek", arg)
		}
	}
	if out := strings.Join(installCommands([]string{"ffprobe"}), " "); strings.Contains(out, "install ffprobe") {
		t.Fatal("ffprobe install guidance is incorrect", out)
	}
}

// TestPodcastM4ASeek verifies seeking a container whose index is at the end.
func TestPodcastM4ASeek(t *testing.T) {
	if err := checkPodcastRequirements(); err != nil {
		t.Fatal(err)
	}
	tmp := os.TempDir()
	withConfigDir(t)
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, tmp)
	}
	config := t.TempDir()
	t.Setenv("MPV_HOME", config)
	if err := os.WriteFile(filepath.Join(config, "mpv.conf"), []byte("ao=null\n"), 0600); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(t.TempDir(), "episode.m4a")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-i", stereoFixture(t, 8), "-c:a", "aac", media).CombinedOutput(); err != nil {
		t.Fatalf("creating AAC fixture: %v %s", err, out)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, media) }))
	defer server.Close()
	d := &Daemon{volume: 55}
	defer func() { d.mu.Lock(); defer d.mu.Unlock(); d.kill() }()
	e := podcast.Episode{FeedURL: server.URL + "/feed", GUID: "aac", URL: server.URL + "/media", Show: "Show", Title: "AAC"}
	b, _ := json.Marshal(episodeRequest{Episode: e})
	wantReply(t, d.execute("episode", string(b)), true, "loading")
	waitState(t, d, "playing", 5*time.Second)
	wantReply(t, d.execute("pause", ""), true, "paused")
	wantReply(t, d.execute("seek", "5"), true, "seeking")
	s := waitState(t, d, "paused", 5*time.Second)
	if s.Position < 5 {
		t.Fatalf("AAC seek position: %+v", s)
	}
	wantReply(t, d.execute("resume", ""), true, "resumed")
	waitState(t, d, "ended", 6*time.Second)
}
