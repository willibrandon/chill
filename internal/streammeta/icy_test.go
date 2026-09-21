package streammeta

import (
	"bytes"
	"io"
	"testing"
)

type readCloser struct{ io.Reader }

// Close satisfies io.ReadCloser for the test stream.
func (readCloser) Close() error { return nil }

// TestParse splits common station title forms without damaging other text.
func TestParse(t *testing.T) {
	got := Parse("  Artist – Song  ")
	if got.Artist != "Artist" || got.Title != "Song" || got.Raw != "Artist – Song" {
		t.Fatalf("parsed = %#v", got)
	}
	got = Parse("Morning programme")
	if got.Artist != "" || got.Title != "Morning programme" {
		t.Fatalf("program = %#v", got)
	}
	got = Parse("Beyonc\xe9 - Halo")
	if got.Artist != "Beyoncé" || got.Title != "Halo" {
		t.Fatalf("legacy metadata = %#v", got)
	}
}

// TestTitle accepts quoted and unquoted ICY fields.
func TestTitle(t *testing.T) {
	for input, want := range map[string]string{
		"StreamTitle='Artist - Song';StreamUrl='';": "Artist - Song",
		"StreamUrl=''; StreamTitle=Programme;":      "Programme",
		"Other='x';":                                "",
	} {
		if got := Title(input); got != want {
			t.Errorf("Title(%q) = %q, want %q", input, got, want)
		}
	}
	if title, found := metadataTitle("StreamTitle='';"); title != "" || !found {
		t.Fatalf("empty title = %q, found %v", title, found)
	}
}

// TestReaderStripsMetadata verifies that only audio reaches the decoder.
func TestReaderStripsMetadata(t *testing.T) {
	metadata := append([]byte("StreamTitle='A - B';"), make([]byte, 32-len("StreamTitle='A - B';"))...)
	stream := append([]byte("abcd"), byte(2))
	stream = append(stream, metadata...)
	stream = append(stream, []byte("efgh")...)
	var title string
	r := NewReader(readCloser{bytes.NewReader(stream)}, 4, func(value string) { title = value })
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abcdefgh" || title != "A - B" {
		t.Fatalf("audio = %q, title = %q", data, title)
	}
}
