package main

import (
	"github.com/willibrandon/chill/internal/audio"
	"github.com/willibrandon/chill/internal/streammeta"
)

type playerEvent struct {
	handoff    uint64
	loaded     bool
	ended      bool
	err        string
	output     bool
	permanent  bool
	nowPlaying *streammeta.NowPlaying
}

// player keeps audio implementation separate from daemon state, and lets tests
// exercise reconnects without a network stream or an audio device.
type player interface {
	load(string) error
	setPaused(bool) error
	setVolume(int) error
	setMuted(bool) error
	setSpeed(float64) error
	setDevice(string) error
	setEqualizer(audio.EqualizerBands)
	events() <-chan playerEvent
	close()
}
