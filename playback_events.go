package main

import (
	"github.com/willibrandon/chill/internal/audio"
	"github.com/willibrandon/chill/internal/streammeta"
)

type playerEvent struct {
	decoder    uint64
	handoff    uint64
	loaded     bool
	ended      bool
	err        string
	output     bool
	permanent  bool
	nowPlaying *streammeta.NowPlaying
}

// player is the playback controller used by the foreground UI and daemon.
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
