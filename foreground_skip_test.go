package main

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
	"time"

	"github.com/willibrandon/chill/internal/playback"
)

func constantTrackFixture(t *testing.T, value int16) MediaItem {
	t.Helper()
	path := stereoFixture(t, 3)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for at := 44; at+2 <= len(data); at += 2 {
		binary.LittleEndian.PutUint16(data[at:], uint16(value))
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return testTrack(path, "Skip fixture")
}

// TestForegroundSkipCancelsPreviousAudio checks an early skip, queued samples, and delayed decoder events.
func TestForegroundSkipCancelsPreviousAudio(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "playing", true: "paused"}[paused], func(t *testing.T) {
			withConfigDir(t)
			useTestAudio(t)
			var render func([]byte)
			openAudioOutput = func(s playback.Settings, volume int, muted, paused bool) (*playback.Output, error) {
				return playback.NewOutput(s, func(s playback.Settings, callback func([]byte)) (playback.Device, error) {
					render = callback
					return &policyAudioDevice{s}, nil
				}, volume, muted, paused)
			}
			items := []MediaItem{constantTrackFixture(t, 16384), constantTrackFixture(t, -8192), constantTrackFixture(t, 8192)}
			m := &foregroundMediaModel{items: items, repeat: "off", paused: paused, settings: defaultPlaybackSettings(), library: emptyLibrary(), played: map[int]bool{0: true}}
			m.settings.Volume = 100
			m.start(0)
			if m.player == nil {
				t.Fatal(m.err)
			}
			p := m.player
			defer p.close()
			p.decoderMu.Lock()
			old, next := p.active, p.prepared
			p.decoderMu.Unlock()
			for _, decoder := range []*preparedDecoder{old, next} {
				select {
				case <-decoder.ready:
				case <-time.After(3 * time.Second):
					t.Fatal("decoder not ready")
				}
			}
			deadline := time.Now().Add(3 * time.Second)
			for p.output.Written() < 512 {
				if time.Now().After(deadline) {
					t.Fatal("first track never queued audio")
				}
				time.Sleep(time.Millisecond)
			}
			m.next()
			if m.index != 1 || m.player != p || old.ctx.Err() == nil {
				t.Fatalf("skip did not cancel previous decoder: index=%d, cancelled=%v", m.index, old.ctx.Err())
			}
			// An event may already be in the TUI mailbox when the user skips.
			for _, event := range []playerEvent{{ended: true}, {loaded: true}, {err: "old failure"}, {handoff: old.id}} {
				event.decoder = old.id
				m.Update(foregroundPlayerMsg{generation: m.generation, open: true, event: event})
				if m.index != 1 || m.state != "loading" || m.err != "" {
					t.Fatalf("stale event changed selected track: %+v, index=%d state=%s error=%s", event, m.index, m.state, m.err)
				}
			}
			p.setPaused(false)
			buffer := make([]byte, 512*8)
			heard := 0
			deadline = time.Now().Add(3 * time.Second)
			for heard < 4096 {
				render(buffer)
				for at := 0; at < len(buffer); at += 4 {
					sample := math.Float32frombits(binary.LittleEndian.Uint32(buffer[at:]))
					if sample == 0 {
						continue
					}
					if math.Abs(float64(sample)+0.25) > 0.001 {
						t.Fatalf("previous track remained audible after skip: sample=%g", sample)
					}
					heard++
				}
				if time.Now().After(deadline) {
					t.Fatal("next track never became audible")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
