package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/willibrandon/chill/internal/podcast"
)

// TestResumableDownloadAndIntegrity checks byte ranges and SHA-256 validation.
func TestResumableDownloadAndIntegrity(t *testing.T) {
	payload := []byte("abcdef")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=3-" {
			t.Errorf("Range = %q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", "bytes 3-5/6")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[3:])
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "episode.part")
	if err := os.WriteFile(path, payload[:3], 0600); err != nil {
		t.Fatal(err)
	}
	written, total, err := resumeDownload(context.Background(), server.URL, path, 1024, func(int64, int64) {})
	if err != nil || written != 6 || total != 6 {
		t.Fatalf("resume = %d/%d, %v", written, total, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(payload) {
		t.Fatalf("payload = %q, %v", got, err)
	}

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", os.Getenv("XDG_CACHE_HOME"))
	episode := podcast.Episode{FeedURL: server.URL + "/feed", URL: server.URL + "/audio", GUID: "one"}
	managed := episodeDownloadPath(episode)
	if err := os.MkdirAll(filepath.Dir(managed), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, payload, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	record := episodeDownload{Episode: episode, State: "ready", Path: managed, Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	if valid, err := validEpisodeDownload(record); err != nil || !valid {
		t.Fatalf("valid download = %v, %v", valid, err)
	}
	if err := os.WriteFile(managed, []byte("abcdeg"), 0600); err != nil {
		t.Fatal(err)
	}
	if valid, err := validEpisodeDownload(record); err != nil || valid {
		t.Fatalf("modified download = %v, %v", valid, err)
	}
	record.Path = filepath.Join(t.TempDir(), "outside.audio")
	if valid, err := validEpisodeDownload(record); err == nil || valid {
		t.Fatalf("outside download = %v, %v", valid, err)
	}
}

// TestResumeDownloadRejectsWrongRangeAndQuota checks bounded safe resumption.
func TestResumeDownloadRejectsWrongRangeAndQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-5/6")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("abcdef"))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "episode.part")
	if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resumeDownload(context.Background(), server.URL, path, 1024, func(int64, int64) {}); err == nil {
		t.Fatal("invalid content range was accepted")
	}
	if _, _, err := resumeDownload(context.Background(), server.URL, filepath.Join(t.TempDir(), "new.part"), 3, func(int64, int64) {}); err == nil {
		t.Fatal("quota overflow was accepted")
	}
}
