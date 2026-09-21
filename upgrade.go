package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/willibrandon/chill/internal/audio"
	"github.com/willibrandon/chill/internal/podcast"

	"golang.org/x/mod/semver"
)

// A kernel-backed lock is released even if a client crashes. The file stays in
// place so waiting clients always lock the same inode/Windows file object.
func lockDaemon() (func(), error) {
	return lockFile(socketPath() + ".lock")
}

func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		locked, err := tryDaemonLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if locked {
			return func() { f.Close() }, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("timed out waiting for another chill command (%s)", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func daemonNeedsUpgrade(s Status, clientVersion string) (bool, error) {
	if s.Protocol > daemonProtocol {
		return false, fmt.Errorf("daemon %s requires a newer chill client", s.Version)
	}
	if s.Protocol < daemonProtocol || s.Version == "" {
		return true, nil // releases before the version handshake
	}
	// Never downgrade a newer daemon, or replace a matching development build
	// on every REPL status poll.
	if !semver.IsValid(clientVersion) {
		return false, nil
	}
	return !semver.IsValid(s.Version) || semver.Compare(s.Version, clientVersion) < 0, nil
}

type playbackSnapshot struct {
	// Queue preserves regular pending media during daemon upgrades.
	Queue []MediaItem `json:"queue,omitempty"`
	// PlayNext preserves priority media during daemon upgrades.
	PlayNext []MediaItem `json:"play_next,omitempty"`
	// Item preserves the source-neutral current item.
	Item *MediaItem `json:"item,omitempty"`
	// Shuffle preserves randomized queue selection.
	Shuffle bool `json:"shuffle"`
	// Repeat preserves off, all, or one mode.
	Repeat string `json:"repeat,omitempty"`
	// Episode preserves podcast metadata during a daemon upgrade.
	Episode *podcast.Episode `json:"episode,omitempty"`
	// Position is the finite-media playhead in seconds.
	Position float64 `json:"position,omitempty"`
	// Speed preserves the finite-media playback rate.
	Speed float64 `json:"speed,omitempty"`
	// Station preserves the complete radio definition, including overrides.
	Station *Station `json:"station,omitempty"`
	// Volume is the restored output level from 0 to 100.
	Volume int `json:"volume"`
	// EQPreset preserves the active equalizer preset.
	EQPreset string `json:"eq_preset,omitempty"`
	// EQBands preserves the audible curve when Custom is active.
	EQBands audio.EqualizerBands `json:"eq_bands"`
	// Muted preserves output mute during handoff.
	Muted bool `json:"muted"`
	// Paused prevents resumed playback from briefly becoming audible.
	Paused bool `json:"paused"`
	// SleepUntil preserves the absolute stop deadline.
	SleepUntil time.Time `json:"sleep_until,omitzero"`
}

func snapshotPlayback(s Status, now time.Time) (playbackSnapshot, error) {
	snapshot := playbackSnapshot{Queue: cloneItems(s.Queue), PlayNext: cloneItems(s.PlayNext), Item: s.Item, Shuffle: s.Shuffle, Repeat: s.Repeat, Volume: s.Volume, EQPreset: s.EQPreset, EQBands: s.EQBands, Muted: s.Muted, Paused: s.Paused, SleepUntil: s.SleepUntil}
	if snapshot.Item != nil {
		if s.State == "ended" || s.State == "idle" {
			snapshot.Item = nil
		} else {
			snapshot.Position, snapshot.Speed = s.Position, s.Speed
		}
	}
	if snapshot.Item == nil && s.Episode != nil && s.State != "ended" && s.State != "idle" {
		snapshot.Episode, snapshot.Position, snapshot.Speed = s.Episode, s.Position, s.Speed
	}
	if snapshot.SleepUntil.IsZero() && s.Sleep != "" {
		remaining, err := time.ParseDuration(s.Sleep)
		if err != nil {
			return snapshot, fmt.Errorf("reading daemon sleep timer: %w", err)
		}
		snapshot.SleepUntil = now.Add(remaining)
	}
	if snapshot.Item == nil && s.Station != "" && (s.Playing || s.Paused || s.State == "loading" || s.State == "reconnecting") {
		if s.URL != "" {
			snapshot.Station = &Station{Name: s.Station, URL: s.URL, Desc: s.Desc}
		} else {
			snapshot.Station = findStation(s.Station)
		}
		if snapshot.Station == nil {
			return snapshot, fmt.Errorf("cannot restore station %q during daemon upgrade: add it back to the config first", s.Station)
		}
	}
	return snapshot, nil
}

// upgradeDaemon runs before commands (including the REPL's first status poll).
// Installing a new executable doesn't replace an already-running process.
func upgradeDaemon() error {
	if !isDaemonRunning() {
		return nil
	}
	raw, err := sendRawCommand("status")
	if err != nil {
		return err
	}
	var s Status
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return fmt.Errorf("reading daemon version: %w", err)
	}
	upgrade, err := daemonNeedsUpgrade(s, buildVersion())
	if err != nil || !upgrade {
		return err
	}
	snapshot, err := snapshotPlayback(s, time.Now())
	if err != nil {
		return err
	}
	if snapshot.Station != nil {
		if err := checkMediaRequirements([]MediaItem{itemFromStation(*snapshot.Station)}); err != nil {
			return err
		}
	}
	if snapshot.Episode != nil {
		if err := checkPodcastRequirements(); err != nil {
			return err
		}
	}
	if snapshot.Item != nil {
		if err := checkMediaRequirements([]MediaItem{*snapshot.Item}); err != nil {
			return err
		}
	}
	settings, err := loadPlaybackSettings()
	if err != nil {
		return fmt.Errorf("reading playback settings before daemon upgrade: %w", err)
	}
	settings.Volume = snapshot.Volume
	if snapshot.EQPreset != "" {
		eq := settings.equalizer()
		eq.Preset = snapshot.EQPreset
		if strings.EqualFold(snapshot.EQPreset, customEqualizerPreset) {
			eq.Custom = snapshot.EQBands
		}
		settings.setEqualizer(eq)
	}
	if err := savePlaybackSettings(settings); err != nil {
		return fmt.Errorf("saving playback settings before daemon upgrade: %w", err)
	}
	if _, err := sendRawCommand("stop"); err != nil {
		return fmt.Errorf("stopping outdated daemon: %w", err)
	}
	if err := startDaemon(); err != nil {
		return fmt.Errorf("starting updated daemon: %w", err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = unwrapReply(sendRawCommand("restore " + string(data)))
	return err
}

// restore applies the whole snapshot before starting mpv, so a muted or paused
// stream is never briefly audible during handoff.
func (d *Daemon) restore(arg string) string {
	var snapshot playbackSnapshot
	if err := json.Unmarshal([]byte(arg), &snapshot); err != nil {
		return fail("bad playback snapshot: " + err.Error())
	}
	selected := 0
	if snapshot.Station != nil {
		selected++
	}
	if snapshot.Episode != nil {
		selected++
	}
	if snapshot.Item != nil {
		selected++
	}
	if selected > 1 || snapshot.Position < 0 || snapshot.Position > 365*24*3600 || math.IsNaN(snapshot.Position) || math.IsInf(snapshot.Position, 0) || len(snapshot.Queue)+len(snapshot.PlayNext) > 5000 {
		return fail("invalid playback snapshot")
	}
	for i := range snapshot.Queue {
		item, err := snapshot.Queue[i].normalized()
		if err != nil {
			return fail("invalid playback snapshot: " + err.Error())
		}
		snapshot.Queue[i] = item
	}
	for i := range snapshot.PlayNext {
		item, err := snapshot.PlayNext[i].normalized()
		if err != nil {
			return fail("invalid playback snapshot: " + err.Error())
		}
		snapshot.PlayNext[i] = item
	}
	d.cancelSleep()
	d.kill()
	d.volume = max(0, min(100, snapshot.Volume))
	d.muted = snapshot.Muted
	if snapshot.EQPreset != "" {
		eq := d.equalizer()
		eq.Preset = snapshot.EQPreset
		if strings.EqualFold(snapshot.EQPreset, customEqualizerPreset) {
			eq.Custom = snapshot.EQBands
		}
		eq = normalizeEqualizerConfig(eq)
		d.eqPreset, d.eqCustom = eq.Preset, eq.Custom
	}
	if d.library == nil {
		d.library = emptyLibrary()
	}
	d.library.Queue, d.library.PlayNext, d.library.Shuffle = cloneItems(snapshot.Queue), cloneItems(snapshot.PlayNext), snapshot.Shuffle
	if snapshot.Repeat == "off" || snapshot.Repeat == "all" || snapshot.Repeat == "one" {
		d.library.Repeat = snapshot.Repeat
	}
	_ = d.library.commit()
	if snapshot.Station == nil && snapshot.Episode == nil && snapshot.Item == nil || (!snapshot.SleepUntil.IsZero() && !snapshot.SleepUntil.After(time.Now())) {
		return ok("restored idle playback")
	}
	d.station, d.paused = snapshot.Station, snapshot.Paused
	if snapshot.Item != nil {
		item, err := snapshot.Item.normalized()
		if err != nil {
			return fail("invalid restored media: " + err.Error())
		}
		d.current, d.station, d.episode = &item, item.Station, item.Episode
		d.episodeOffset = time.Duration(max(0, snapshot.Position) * float64(time.Second))
		d.episodeDuration = time.Duration(item.Duration * float64(time.Second))
		if snapshot.Speed >= 0.5 && snapshot.Speed <= 3 {
			d.rate = snapshot.Speed
			if item.Kind == MediaPodcast {
				d.speed = snapshot.Speed
			}
		}
		if d.episode != nil {
			if err := d.startEpisodeCache(); err != nil {
				return fail(err.Error())
			}
		}
	}
	if snapshot.Episode != nil {
		if !podcast.ValidURL(snapshot.Episode.URL) || !podcast.ValidURL(snapshot.Episode.FeedURL) {
			return fail("invalid restored episode")
		}
		if _, err := d.podcastLibrary(); err != nil {
			return fail(err.Error())
		}
		d.episode = snapshot.Episode
		item := itemFromEpisode(*snapshot.Episode)
		d.current = &item
		d.episodeOffset = time.Duration(max(0, snapshot.Position) * float64(time.Second))
		d.episodeDuration = time.Duration(snapshot.Episode.Duration * float64(time.Second))
		if snapshot.Speed >= 0.5 && snapshot.Speed <= 3 {
			d.speed, d.rate = snapshot.Speed, snapshot.Speed
		}
		if err := d.startEpisodeCache(); err != nil {
			return fail(err.Error())
		}
	}
	if snapshot.Station != nil {
		item := itemFromStation(*snapshot.Station)
		d.current = &item
	}
	if err := d.startPlayback(); err != nil {
		d.state, d.lastError = "failed", err.Error()
		return fail("restoring playback: " + err.Error())
	}
	if !snapshot.SleepUntil.IsZero() {
		remaining := time.Until(snapshot.SleepUntil)
		if remaining <= 0 {
			d.kill()
		} else {
			d.sleep(remaining.String())
		}
	}
	if d.current != nil && d.current.finite() {
		d.scheduleProgress()
	}
	return ok("restored playback")
}
