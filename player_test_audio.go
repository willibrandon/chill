//go:build chill_test_audio

package main

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/willibrandon/chill/internal/playback"
)

// This backend exists only in explicitly tagged integration-test binaries.
// A production release never honors CHILL_TEST_AUDIO or falls back to silence.
type pacedTestDevice struct {
	settings   playback.Settings
	render     func([]byte)
	stop, done chan struct{}
	once       sync.Once
}

// Start consumes samples at the configured rate for CLI integration tests.
func (d *pacedTestDevice) Start() error {
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

// Close waits until the test callback has stopped.
func (d *pacedTestDevice) Close() { d.once.Do(func() { close(d.stop); <-d.done }) }

// Info reports the explicit test output.
func (d *pacedTestDevice) Info() playback.DeviceInfo {
	return playback.DeviceInfo{ID: d.settings.Device, Name: "Integration test output", Backend: "test", SampleRate: d.settings.SampleRate}
}

func init() {
	if os.Getenv("CHILL_TEST_AUDIO") != "null" {
		return
	}
	openAudioOutput = func(s playback.Settings, v int, m, p bool) (*playback.Output, error) {
		return playback.NewOutput(s, func(s playback.Settings, render func([]byte)) (playback.Device, error) {
			return &pacedTestDevice{settings: s, render: render, stop: make(chan struct{}), done: make(chan struct{})}, nil
		}, v, m, p)
	}
	nativeAudioDevices = func(context.Context) ([]playback.DeviceInfo, error) {
		return []playback.DeviceInfo{{ID: "auto", Name: "Integration test output", Default: true}}, nil
	}
}
