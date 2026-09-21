package main

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/willibrandon/chill/internal/audio"
)

func stereoFixture(t *testing.T, seconds int) string {
	t.Helper()
	const rate = 48000
	wav := make([]byte, 44+seconds*rate*4)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 2)
	binary.LittleEndian.PutUint32(wav[24:], rate)
	binary.LittleEndian.PutUint32(wav[28:], rate*4)
	binary.LittleEndian.PutUint16(wav[32:], 4)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(len(wav)-44))
	for i := range seconds * rate {
		left := int16(24000 * math.Sin(2*math.Pi*1000*float64(i)/rate))
		right := int16(6000 * math.Sin(2*math.Pi*3000*float64(i)/rate))
		binary.LittleEndian.PutUint16(wav[44+i*4:], uint16(left))
		binary.LittleEndian.PutUint16(wav[46+i*4:], uint16(right))
	}
	path := filepath.Join(t.TempDir(), "stereo.wav")
	if err := os.WriteFile(path, wav, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPCMIntegration exercises real decoding, stereo capture, pause, and cleanup.
func TestPCMIntegration(t *testing.T) {
	for _, name := range []string{"mpv", "ffmpeg"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("playback tests require %s: %v; install with %s", name, err, strings.Join(installCommands([]string{name}), "; "))
		}
	}
	dir := t.TempDir()
	t.Setenv("MPV_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "mpv.conf"), []byte("ao=null\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{true: "start-paused", false: "playing"}[paused], func(t *testing.T) {
			p, err := startPCMPlayer(55, false, paused)
			if err != nil {
				t.Fatal(err)
			}
			defer p.close()
			if err := p.command("loadfile", stereoFixture(t, 8), "replace"); err != nil {
				t.Fatal(err)
			}
			select {
			case e := <-p.events():
				if !e.loaded {
					t.Fatalf("PCM failed to load: %+v", e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("PCM output did not load")
			}
			pcm := p.(*pcmPlayer)
			deadline := time.Now().Add(3 * time.Second)
			for {
				frame := pcm.audioFrame()
				if frame.Peak[0] > 0.7 && frame.Peak[1] > 0.17 && frame.Peak[1] < 0.2 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("stereo PCM tap failed: peak=%v", frame.Peak)
				}
				time.Sleep(20 * time.Millisecond)
			}
			for _, cmd := range [][]any{{"set_property", "pause", true}, {"set_property", "volume", 32}, {"set_property", "mute", true}, {"set_property", "pause", false}, {"set_property", "pause", true}} {
				if err := p.command(cmd...); err != nil {
					t.Fatal(err)
				}
			}
			// Closing a paused pipeline must unblock both sides of the PCM pipe.
			done := make(chan struct{})
			go func() { p.close(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("paused PCM cleanup hung")
			}
		})
	}
	t.Run("decoder-failure", func(t *testing.T) {
		p, err := startPCMPlayer(55, false, false)
		if err != nil {
			t.Fatal(err)
		}
		defer p.close()
		if err := p.command("loadfile", filepath.Join(dir, "missing.wav"), "replace"); err != nil {
			t.Fatal(err)
		}
		select {
		case e := <-p.events():
			if !strings.Contains(e.err, "audio decoder") || !strings.Contains(e.err, "missing.wav") {
				t.Fatalf("decoder diagnostics lost: %+v", e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("decoder failure was not reported")
		}
	})
	t.Run("post-equalizer-tap", func(t *testing.T) {
		p, err := startPCMPlayer(55, false, false)
		if err != nil {
			t.Fatal(err)
		}
		defer p.close()
		var bands audio.EqualizerBands
		bands[4] = -12
		p.setEqualizer(bands)
		if err := p.command("loadfile", stereoFixture(t, 4), "replace"); err != nil {
			t.Fatal(err)
		}
		select {
		case event := <-p.events():
			if !event.loaded {
				t.Fatalf("PCM failed to load: %+v", event)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("equalized PCM output did not load")
		}
		pcm := p.(*pcmPlayer)
		deadline := time.Now().Add(3 * time.Second)
		for {
			frame := pcm.audioFrame()
			if frame.Sequence > 0 && frame.Peak[0] < 0.25 && frame.Peak[1] > 0.16 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("tap did not receive equalized PCM: peak=%v", frame.Peak)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

// TestResolveDirectAudioAndCancellation checks direct sources and cancelled extraction.
func TestResolveDirectAudioAndCancellation(t *testing.T) {
	for _, source := range []string{"/music/100% chill.wav", `C:\Music\a track.wav`, "https://radio.example/stream"} {
		got, err := resolveAudio(context.Background(), source)
		if err != nil || got.URL != source {
			t.Fatalf("direct source %q: %+v, %v", source, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolveAudio(ctx, "https://youtube.com/watch?v=test"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled resolution dispatched", err)
	}
}

// TestExtractorAudioFormatAvoidsMixcloudDASH guards FFmpeg-compatible resolution.
func TestExtractorAudioFormatAvoidsMixcloudDASH(t *testing.T) {
	mixcloud := extractorAudioFormat("https://www.mixcloud.com/listener/show/")
	if !strings.HasPrefix(mixcloud, "bestaudio[protocol=https]") {
		t.Fatalf("Mixcloud format selector = %q", mixcloud)
	}
	if format := extractorAudioFormat("https://www.youtube.com/watch?v=test"); format != "bestaudio/best" {
		t.Fatalf("YouTube format selector = %q", format)
	}
}

// TestPCMLocalTransitionKeepsOutputAndUsesPreload checks the gapless local path.
func TestPCMLocalTransitionKeepsOutputAndUsesPreload(t *testing.T) {
	for _, name := range []string{"mpv", "ffmpeg"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("playback tests require %s: %v", name, err)
		}
	}
	config := t.TempDir()
	t.Setenv("MPV_HOME", config)
	if err := os.WriteFile(filepath.Join(config, "mpv.conf"), []byte("ao=null\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first, second := stereoFixture(t, 1), stereoFixture(t, 1)
	raw, err := newPCMPlayer(55, false, false, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	p := raw.(*pcmPlayer)
	defer p.close()
	if err := p.command("loadfile", first, "replace"); err != nil {
		t.Fatal(err)
	}
	output := p.output
	p.preload(second, 0, true)
	wait := func(kind string) {
		t.Helper()
		select {
		case event := <-p.events():
			if kind == "loaded" && !event.loaded || kind == "ended" && !event.ended {
				t.Fatalf("wanted %s event, got %+v", kind, event)
			}
		case <-time.After(4 * time.Second):
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
	wait("loaded")
	wait("ended")
	started := time.Now()
	if !p.transition(second, 0, true) {
		t.Fatal("preloaded transition was not available")
	}
	wait("loaded")
	if p.output != output {
		t.Fatal("local transition replaced the mpv PCM output")
	}
	if delay := time.Since(started); delay > 250*time.Millisecond {
		t.Fatalf("preloaded transition took %s", delay)
	}
	wait("ended")
}
