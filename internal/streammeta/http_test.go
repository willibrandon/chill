package streammeta

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOpenRequestsAndStripsICYMetadata checks the live single-connection path.
func TestOpenRequestsAndStripsICYMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Icy-MetaData") != "1" || r.Header.Get("X-Test") != "value" {
			t.Errorf("headers = %v", r.Header)
		}
		w.Header().Set("icy-metaint", "4")
		w.Header().Set("icy-name", " Example FM ")
		w.Header().Set("Content-Type", "audio/mpeg; charset=binary")
		metadata := append([]byte("StreamTitle='Artist - Song';"), make([]byte, 32-len("StreamTitle='Artist - Song';"))...)
		_, _ = w.Write(append(append(append([]byte("abcd"), byte(2)), metadata...), []byte("efgh")...))
	}))
	defer server.Close()
	var title string
	source, err := Open(context.Background(), server.URL, map[string]string{"X-Test": "value"}, func(value string) { title = value })
	if err != nil {
		t.Fatal(err)
	}
	defer source.Body.Close()
	audio, err := io.ReadAll(source.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(audio) != "abcdefgh" || title != "Artist - Song" || source.StationName != "Example FM" || source.ContentType != "audio/mpeg" || source.Playlist {
		t.Fatalf("source = %+v, audio = %q, title = %q", source, audio, title)
	}
}

// TestOpenRecognizesPlaylistContent checks the decoder-owned metadata path.
func TestOpenRecognizesPlaylistContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n"))
	}))
	defer server.Close()
	source, err := Open(context.Background(), server.URL+"/live", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Body.Close()
	if !source.Playlist {
		t.Fatal("HLS response was not recognized as a playlist")
	}
}
