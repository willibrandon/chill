package main

import (
	"context"
	"fmt"
	"github.com/willibrandon/chill/internal/playback"
	"sync"
	"testing"
	"time"
)

type testAudioDevice struct {
	settings   playback.Settings
	render     func([]byte)
	stop, done chan struct{}
	once       sync.Once
}

// Start begins consuming PCM samples.
func (d *testAudioDevice) Start() error {
	go func() {
		defer close(d.done)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		buf := make([]byte, d.settings.SampleRate/100*8)
		for {
			select {
			case <-d.stop:
				return
			case <-tick.C:
				d.render(buf)
			}
		}
	}()
	return nil
}

// Close releases resources and waits for owned workers to stop.
func (d *testAudioDevice) Close() { d.once.Do(func() { close(d.stop); <-d.done }) }

// Info returns the effective output configuration.
func (d *testAudioDevice) Info() playback.DeviceInfo {
	return playback.DeviceInfo{ID: d.settings.Device, Backend: "test", SampleRate: d.settings.SampleRate, Exclusive: d.settings.Exclusive}
}
func testDeviceFactory(settings playback.Settings, render func([]byte)) (playback.Device, error) {
	return &testAudioDevice{settings: settings, render: render, stop: make(chan struct{}), done: make(chan struct{})}, nil
}
func useTestAudio(t *testing.T) {
	t.Helper()
	previous, devices := openAudioOutput, nativeAudioDevices
	openAudioOutput = func(s playback.Settings, v int, m, p bool) (*playback.Output, error) {
		return playback.NewOutput(s, testDeviceFactory, v, m, p)
	}
	nativeAudioDevices = func(context.Context) ([]playback.DeviceInfo, error) {
		return []playback.DeviceInfo{{ID: "auto", Name: "Test output", Default: true}}, nil
	}
	t.Cleanup(func() { openAudioOutput = previous; nativeAudioDevices = devices })
}
func testPlayerCommand(p player, args ...any) error {
	if len(args) != 3 {
		return fmt.Errorf("invalid test control")
	}
	switch args[1] {
	case "pause":
		return p.setPaused(args[2].(bool))
	case "mute":
		return p.setMuted(args[2].(bool))
	case "volume":
		return p.setVolume(args[2].(int))
	case "speed":
		return p.setSpeed(args[2].(float64))
	}
	return fmt.Errorf("unknown test control")
}

// TestNativePlaybackWithoutExternalTools verifies native decoding, output, and completion without executables.
func TestNativePlaybackWithoutExternalTools(t *testing.T) {
	withConfigDir(t)
	useTestAudio(t)
	t.Setenv("PATH", t.TempDir())
	p, err := newPCMPlayer(55, false, false, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if err := p.load(stereoFixture(t, 1)); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"loaded", "ended"} {
		select {
		case e := <-p.events():
			if kind == "loaded" && !e.loaded || kind == "ended" && !e.ended {
				t.Fatalf("wanted %s, got %+v", kind, e)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("waiting for %s", kind)
		}
	}
	if pos := p.(*pcmPlayer).position(); pos < 990*time.Millisecond || pos > 1010*time.Millisecond {
		t.Fatalf("consumed position %s", pos)
	}
}
