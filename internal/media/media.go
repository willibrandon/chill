// Package media publishes playback state to the host operating system and
// translates its media controls into application commands.
package media

import "time"

// CommandKind identifies an action requested through an operating-system
// media control.
type CommandKind uint8

const (
	// Toggle requests a play/pause transition.
	Toggle CommandKind = iota
	// Play requests active playback.
	Play
	// Pause requests paused playback.
	Pause
	// Stop requests that the current item stop.
	Stop
	// Next requests the next item.
	Next
	// Previous requests the previous item.
	Previous
	// Seek requests a relative playhead movement.
	Seek
	// SetPosition requests an absolute playhead position.
	SetPosition
	// SetVolume requests an absolute volume from zero through one.
	SetVolume
)

// Command is an action received from the operating system.
type Command struct {
	// Kind identifies the requested action.
	Kind CommandKind
	// Position carries an absolute or relative time for seek commands.
	Position time.Duration
	// Volume carries a linear zero-through-one level for volume commands.
	Volume float64
}

// PlaybackStatus describes the state exposed to the operating system.
type PlaybackStatus string

const (
	// StatusStopped means no item is actively playing.
	StatusStopped PlaybackStatus = "Stopped"
	// StatusPlaying means an item is actively playing.
	StatusPlaying PlaybackStatus = "Playing"
	// StatusPaused means an item is selected but paused.
	StatusPaused PlaybackStatus = "Paused"
)

// Track contains media metadata presented by system surfaces.
type Track struct {
	// Title is the track or episode title.
	Title string
	// Artist is the performing artist or podcast author.
	Artist string
	// Album is the station or podcast name.
	Album string
	// Genre is an optional genre label.
	Genre string
	// URL identifies the playable item.
	URL string
	// ArtURL identifies artwork using an HTTP(S) or file URL.
	ArtURL string
	// Duration is the known item length.
	Duration time.Duration
}

// State is the complete playback snapshot published to the operating system.
type State struct {
	// Status is stopped, playing, or paused.
	Status PlaybackStatus
	// Track contains the current item's metadata.
	Track Track
	// Volume is a linear level from zero through one.
	Volume float64
	// AudioDevice identifies the active output destination.
	AudioDevice string
	// AudioFormat describes the active PCM format.
	AudioFormat string
	// Position is the current playhead.
	Position time.Duration
	// Seekable reports whether playhead changes are supported.
	Seekable bool
	// CanGoNext reports whether a next item can be selected.
	CanGoNext bool
	// CanGoPrevious reports whether a previous item can be selected.
	CanGoPrevious bool
}
