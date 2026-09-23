//go:build darwin && cgo

package playback

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/gopxl/beep/v2"
)

// TestStreamReaderCloseStopsDecoding exercises the production reader against a
// real WAV decoder, without opening a device or producing sound.
func TestStreamReaderCloseStopsDecoding(t *testing.T) {
	file, err := os.Open("../../testdata/audio/tone.wav")
	if err != nil {
		t.Fatal(err)
	}
	decoder, _, err := Decode("wav", file)
	if err != nil {
		file.Close()
		t.Fatal(err)
	}
	reader := &streamReader{source: beep.Loop(-1, decoder)}
	firstRead := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		defer decoder.Close()
		var block [512 * 8]byte
		first := true
		for {
			n, err := reader.Read(block[:])
			if first {
				close(firstRead)
				first = false
			}
			if err != nil {
				finished <- err
				return
			}
			if n != len(block) {
				finished <- io.ErrUnexpectedEOF
				return
			}
			if t.Context().Err() != nil {
				return
			}
		}
	}()
	<-firstRead
	reader.close()
	select {
	case err := <-finished:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("retired reader: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader continued decoding after close")
	}
	// Oto can issue another read after Player.Close. It must not reach the
	// decoder, which has now been closed by the worker above.
	var block [512 * 8]byte
	for range 10 {
		if n, err := reader.Read(block[:]); n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("read after close = %d, %v; want 0, EOF", n, err)
		}
	}
}
