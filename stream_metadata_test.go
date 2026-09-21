package main

import (
	"testing"
	"time"
)

// TestMetadataDiagnosticsRecognizesAndClearsTitles checks decoder metadata parsing.
func TestMetadataDiagnosticsRecognizesAndClearsTitles(t *testing.T) {
	tail := &tailBuffer{}
	updates := make(chan string, 4)
	diagnostics := newMetadataDiagnostics(tail, func(value string) { updates <- value })
	_, _ = diagnostics.Write([]byte("StreamTitle='Artist - Song'\nStreamTitle=''\n"))
	for _, want := range []string{"Artist - Song", ""} {
		select {
		case got := <-updates:
			if got != want {
				t.Fatalf("metadata = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing metadata %q", want)
		}
	}
}

// TestMetadataDiagnosticsCombinesArtistAndTitle avoids transient partial tracks.
func TestMetadataDiagnosticsCombinesArtistAndTitle(t *testing.T) {
	updates := make(chan string, 2)
	diagnostics := newMetadataDiagnostics(&tailBuffer{}, func(value string) { updates <- value })
	_, _ = diagnostics.Write([]byte("title : Song\nartist : Artist\n"))
	select {
	case got := <-updates:
		if got != "Artist - Song" {
			t.Fatalf("metadata = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("missing combined metadata")
	}
	select {
	case extra := <-updates:
		t.Fatalf("unexpected partial metadata %q", extra)
	case <-time.After(150 * time.Millisecond):
	}
}
