package lyrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetFallsBackToSearchAndCaches verifies lookup behavior.
func TestGetFallsBackToSearchAndCaches(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/get" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"trackName":"Song","artistName":"Artist","plainLyrics":"one\ntwo"}]`))
	}))
	defer server.Close()
	client := NewClient()
	client.BaseURL, client.CacheDir = server.URL, t.TempDir()
	result, err := client.Get(context.Background(), "Artist", "Song")
	if err != nil || len(result.Lines()) != 2 {
		t.Fatalf("result = %#v, %v", result, err)
	}
	if _, err := client.Get(context.Background(), "Artist", "Song"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

// TestLinesStripsLiveTimestamps verifies manual-scroll text.
func TestLinesStripsLiveTimestamps(t *testing.T) {
	lines := (Result{Synced: "[00:01.00] First\n[00:02.00][by:test] Second"}).Lines()
	if len(lines) != 2 || lines[1] != "Second" {
		t.Fatalf("lines = %#v", lines)
	}
}
