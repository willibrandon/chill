// daemon.go implements the background daemon that manages mpv playback.
// The daemon listens on a Unix socket and accepts commands from clients.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/willibrandon/chill/internal/audio"
	"github.com/willibrandon/chill/internal/episode"
	"github.com/willibrandon/chill/internal/media"
	"github.com/willibrandon/chill/internal/notify"
	"github.com/willibrandon/chill/internal/podcast"
	"github.com/willibrandon/chill/internal/streammeta"
	"github.com/willibrandon/chill/internal/tracklog"
)

// defaultVolume is used until a volume has been saved.
const defaultVolume = 70

const maxRetryDelay = 30 * time.Second

const daemonProtocol = 5

// Daemon manages the mpv subprocess and handles client commands.
// It maintains playback state and communicates over a Unix socket.
type Daemon struct {
	resolveCancel                  context.CancelFunc
	mu                             sync.Mutex // protects all fields
	player                         player
	newPlayer                      func(int, bool, bool) (player, error) // nil uses mpv
	watchDone                      chan struct{}
	station                        *Station // currently playing station
	episode                        *podcast.Episode
	episodeCache                   *episode.Cache
	episodeOffset, episodeDuration time.Duration
	podcasts                       *podcastLibrary
	progressTimer                  *time.Timer
	storageError                   string
	trackStore                     *tracklog.Store
	nowPlaying                     *streammeta.NowPlaying
	media                          *media.Service
	episodeQueue                   []podcast.Episode
	episodeHistory                 []podcast.Episode
	stationHistory                 []Station
	stationForward                 []Station
	notifications                  bool
	speed                          float64
	paused                         bool // whether playback is paused
	muted                          bool
	volume                         int    // 0-100, applied to mpv whenever it changes
	eqPreset                       string // built-in preset name or Custom
	eqCustom                       audio.EqualizerBands
	state                          string // idle, loading, reconnecting, playing, paused, failed
	lastError                      string
	retries                        int
	retryTimer                     *time.Timer
	retryAt                        time.Time
	loadTimer                      *time.Timer
	generation                     uint64 // invalidates callbacks after switching or stopping
	sleepTimer                     *time.Timer
	sleepUntil                     time.Time
	sleepGeneration                uint64
	startedAt                      time.Time    // when current station started
	listener                       net.Listener // Unix socket listener
}

// Status represents the current playback state, serialized as JSON for clients.
type Status struct {
	// NowPlaying is the current live-radio title metadata.
	NowPlaying *streammeta.NowPlaying `json:"now_playing,omitempty"`
	// Episode describes the selected podcast episode, if any.
	Episode *podcast.Episode `json:"episode,omitempty"`
	// Position is the episode playhead in seconds, excluding pauses.
	Position float64 `json:"position,omitempty"`
	// Duration is the episode length in seconds, or zero when unknown.
	Duration float64 `json:"duration,omitempty"`
	// Seekable reports whether podcast seek commands are available.
	Seekable bool `json:"seekable,omitempty"`
	// StorageError reports a failure to persist listening progress.
	StorageError string `json:"storage_error,omitempty"`
	// Queued counts episodes waiting after the current item.
	Queued int `json:"queued,omitempty"`
	// Speed is the podcast playback rate, with 1 meaning normal speed.
	Speed float64 `json:"speed,omitempty"`
	// Version identifies the running daemon binary.
	Version string `json:"version"`
	// Protocol is the daemon's IPC compatibility version.
	Protocol int    `json:"protocol"`
	Playing  bool   `json:"playing"`           // true if actively playing
	Paused   bool   `json:"paused"`            // true if paused
	Station  string `json:"station,omitempty"` // station name
	// URL is the selected station's source address.
	URL    string `json:"url,omitempty"`
	Desc   string `json:"desc,omitempty"`   // station description
	Uptime string `json:"uptime,omitempty"` // how long current station has been playing
	Volume int    `json:"volume"`           // 0-100
	// Muted reports whether mpv output is silenced.
	Muted bool `json:"muted"`
	// EQPreset is the active built-in preset name or Custom.
	EQPreset string `json:"eq_preset"`
	// EQBands contains the ten currently audible gains in decibels.
	EQBands audio.EqualizerBands `json:"eq_bands"`
	// State is idle, loading, reconnecting, playing, paused, ended, or failed.
	State string `json:"state"`
	// Error describes the latest playback failure.
	Error string `json:"error,omitempty"`
	// Retries counts attempts in the current reconnect backoff.
	Retries int `json:"retries,omitempty"`
	// RetryAt is the scheduled reconnect time.
	RetryAt time.Time `json:"retry_at,omitzero"`
	// Sleep is a human-readable remaining sleep timer duration.
	Sleep string `json:"sleep,omitempty"`
	// SleepUntil is the absolute playback stop deadline.
	SleepUntil time.Time `json:"sleep_until,omitzero"`
}

// reply is what the daemon writes back for a command. Clients read the JSON
// tag to tell a failure from a message worth showing as success; the daemon
// itself is versioned with the clients, so the old tagless protocol is gone.
type reply struct {
	// OK reports whether a command succeeded.
	OK bool `json:"ok"`
	// Msg contains the result text or failure reason.
	Msg string `json:"msg"`
}

// ok and fail build the responses to a command.
func ok(msg string) string   { return marshal(reply{true, msg}) }
func fail(msg string) string { return marshal(reply{false, msg}) }

// marshal serializes a reply, which cannot fail for this shape.
func marshal(r reply) string {
	b, _ := json.Marshal(r)
	return string(b)
}

// Start initializes the daemon and begins listening for client connections.
func (d *Daemon) Start() error {
	ln, err := listenSocket()
	if err != nil {
		return err
	}
	d.listener = ln

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.handle(conn)
		}
	}()

	return nil
}

func (d *Daemon) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		cmd := strings.TrimSpace(line)
		if cmd == "visualize" {
			d.serveVisualizer(conn)
			return
		}
		parts := strings.SplitN(cmd, " ", 2)
		action := parts[0]
		arg := ""
		if len(parts) > 1 {
			arg = parts[1]
		}

		response := d.execute(action, arg)
		conn.Write([]byte(response + "\n"))

		if action == "stop" || action == "quit" {
			d.listener.Close()
			cleanupSocket()
			os.Exit(0)
		}
	}
}

func (d *Daemon) execute(action, arg string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.updateMedia()

	switch action {
	case "podcasts", "podcast-country", "podcast-subscribe", "podcast-unsubscribe":
		return d.podcastSettings(action, arg)
	case "episode":
		return d.playEpisode(arg)
	case "seek":
		return d.seekEpisode(arg)
	case "speed":
		return d.episodeSpeed(arg)
	case "podcast-queue", "queue", "queue-clear", "next", "prev":
		return d.podcastQueueCommand(action, arg)
	case "radio-play":
		return d.playRadio(arg)
	case "play":
		return d.play(arg)
	case "pause":
		return d.pause()
	case "resume":
		return d.resume()
	case "toggle":
		if d.episode != nil && (d.state == "ended" || d.state == "failed") {
			return d.resume()
		}
		if d.state == "idle" || d.state == "failed" || d.station == nil && d.episode == nil {
			return d.play("")
		}
		if d.paused {
			return d.resume()
		}
		return d.pause()
	case "stop", "quit":
		d.cancelSleep()
		d.kill()
		return ok("stopped")
	case "skip":
		return d.skip()
	case "status":
		return d.status()
	case "list":
		return d.listStations()
	case "vol":
		return d.volumeCmd(arg)
	case "mute":
		return d.mute()
	case "eq":
		return d.equalizerCmd(arg)
	case "eq-state":
		return d.equalizerStateCmd(arg)
	case "notifications":
		return d.notificationsCmd(arg)
	case "reload":
		return d.reload()
	case "sleep":
		return d.sleep(arg)
	case "restore":
		return d.restore(arg)
	default:
		return fail("unknown command")
	}
}

func (d *Daemon) play(name string) string {
	if name == "" {
		name = defaultStation()
	}

	station := findStation(name)
	if station == nil {
		return fail("unknown station: " + name)
	}

	return d.playStation(station)
}

func (d *Daemon) playRadio(arg string) string {
	var station Station
	if err := json.Unmarshal([]byte(arg), &station); err != nil {
		return fail("invalid radio station")
	}
	station.Name = strings.TrimSpace(station.Name)
	station.URL = strings.TrimSpace(station.URL)
	if station.Name == "" || !podcast.ValidURL(station.URL) {
		return fail("radio station needs a name and HTTP(S) stream URL")
	}
	station.Desc = strings.TrimSpace(station.Desc)
	if station.Desc == "" {
		station.Desc = station.Name
	}
	return d.playStation(&station)
}

func (d *Daemon) playStation(station *Station) string {
	history := d.stationHistory
	if d.station != nil && d.station.URL != station.URL {
		history = append(history, *d.station)
		if len(history) > 100 {
			history = history[len(history)-100:]
		}
	}
	d.kill()
	d.stationHistory, d.stationForward = history, nil
	d.station = station
	if err := d.startPlayback(); err != nil {
		d.state, d.lastError = "failed", err.Error()
		return fail("failed to start: " + err.Error())
	}
	return ok("loading: " + station.Desc)
}

// startPlayback is called under mu, both for a new station and a reconnect.
func (d *Daemon) startPlayback() error {
	d.state = "loading"
	if d.retries > 0 {
		d.state = "reconnecting"
	}
	start := d.newPlayer
	if start == nil {
		start = startPCMPlayer
		if d.episode != nil {
			start = func(v int, m, p bool) (player, error) { return startPCMPlayerAt(v, m, p, d.episodeOffset) }
		}
	}
	p, err := start(d.volume, d.muted, d.paused)
	if err != nil {
		return err
	}
	d.player = p
	p.setEqualizer(d.equalizer().activeBands())
	if d.episode != nil && d.speed != 0 && d.speed != 1 {
		if err := p.command("set_property", "speed", d.speed); err != nil {
			p.close()
			d.player = nil
			return err
		}
	}
	d.watchDone = make(chan struct{})
	done := d.watchDone
	go func() {
		for {
			select {
			case e, open := <-p.events():
				if !open {
					e = playerEvent{err: "mpv event stream closed"}
				}
				d.mu.Lock()
				if d.player == p {
					d.playerEvent(e)
				}
				d.mu.Unlock()
				if !open {
					return
				}
			case <-done:
				return
			}
		}
	}()
	source := ""
	if d.episode != nil {
		source = d.episodeCache.URL()
	} else {
		source = d.station.URL
	}
	if err := p.command("loadfile", source, "replace"); err != nil {
		d.closePlayer()
		return err
	}
	d.loadTimer = time.AfterFunc(45*time.Second, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.player == p && (d.state == "loading" || d.state == "reconnecting") {
			d.playbackFailed("stream took too long to load")
		}
	})
	return nil
}

func (d *Daemon) playerEvent(e playerEvent) {
	defer d.updateMedia()
	if e.nowPlaying != nil && d.station != nil {
		if e.nowPlaying.Raw == "" {
			d.nowPlaying = nil
			return
		}
		d.updateNowPlaying(*e.nowPlaying)
		return
	}
	if e.ended {
		if d.episode != nil {
			d.finishEpisode()
		} else {
			d.playbackFailed("stream ended")
		}
		return
	}
	if e.err != "" {
		d.playbackFailed(e.err)
		return
	}
	if e.loaded {
		if d.loadTimer != nil {
			d.loadTimer.Stop()
			d.loadTimer = nil
		}
		d.state, d.lastError = "playing", ""
		if d.paused {
			d.state = "paused"
		}
		d.startedAt = time.Now()
	}
}

func (d *Daemon) updateNowPlaying(now streammeta.NowPlaying) {
	if now.Raw == "" || d.nowPlaying != nil && d.nowPlaying.Raw == now.Raw {
		return
	}
	now.ChangedAt = time.Now().UTC()
	now.Artwork = d.station.Artwork
	d.nowPlaying = &now
	if d.trackStore == nil {
		d.trackStore = tracklog.DefaultStore()
	}
	err := d.trackStore.Record(tracklog.Entry{
		PlayedAt: now.ChangedAt, Station: d.station.Name, StationURL: d.station.URL,
		Artist: now.Artist, Title: now.Title, Raw: now.Raw, Artwork: now.Artwork,
	})
	if err != nil {
		d.storageError = "could not save track history: " + err.Error()
	} else if strings.HasPrefix(d.storageError, "could not save track history:") {
		d.storageError = ""
	}
	if d.notifications {
		title, body, artwork := now.Title, now.Artist, now.Artwork
		if title == "" {
			title = now.Raw
		}
		if body == "" {
			body = d.station.Name
		} else {
			body += " · " + d.station.Name
		}
		go func() { _ = notify.Show(title, body, artwork) }()
	}
}

func (d *Daemon) playbackFailed(reason string) {
	if d.episode != nil {
		d.saveEpisode(false)
		d.episodeOffset = d.episodePosition()
		d.closeEpisodeCache()
	}
	d.closePlayer()
	d.lastError = reason
	// A minute of successful playback resets the backoff. Short-lived loads
	// keep their backoff so an unavailable stream never creates a tight loop.
	if !d.startedAt.IsZero() && time.Since(d.startedAt) >= time.Minute {
		d.retries = 0
	}
	d.startedAt = time.Time{}
	delay := retryDelay(d.retries)
	d.retries++
	d.state = "reconnecting"
	d.retryAt = time.Now().Add(delay)
	generation := d.generation
	d.retryTimer = time.AfterFunc(delay, func() {
		d.retryPlayback(generation)
	})
}

func retryDelay(retries int) time.Duration {
	return min(time.Second<<min(max(retries, 0), 5), maxRetryDelay)
}

func (d *Daemon) retryPlayback(generation uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.generation != generation || d.station == nil && d.episode == nil {
		return
	}
	d.retryTimer = nil
	d.retryAt = time.Time{}
	if d.episode != nil {
		// Signed media URLs can expire. Refresh without blocking controls.
		old := *d.episode
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		d.resolveCancel = cancel
		d.mu.Unlock()
		feed, err := podcast.NewClient().Feed(ctx, old.FeedURL)
		cancel()
		d.mu.Lock()
		if d.generation != generation || d.episode == nil {
			return
		}
		d.resolveCancel = nil
		if err == nil {
			for _, e := range feed.Episodes {
				if e.Key() == old.Key() {
					d.episode.URL = e.URL
					break
				}
			}
		}
		if err := d.startEpisodeCache(); err != nil {
			d.playbackFailed(err.Error())
			return
		}
	}
	if err := d.startPlayback(); err != nil {
		d.playbackFailed(err.Error())
	}
	if d.episode != nil {
		d.scheduleProgress()
	}
}

func (d *Daemon) pause() string {
	if d.station == nil && d.episode == nil || d.state == "failed" || d.state == "ended" {
		return fail("nothing playing")
	}
	if d.paused {
		return ok("paused")
	}
	if d.player != nil {
		if err := d.player.command("set_property", "pause", true); err != nil {
			return fail(err.Error())
		}
	}
	d.paused = true
	d.saveEpisode(false)
	if d.state == "playing" {
		d.state = "paused"
	}
	return ok("paused")
}

func (d *Daemon) resume() string {
	if d.episode != nil && (d.state == "ended" || d.state == "failed") {
		if d.state == "ended" {
			d.episodeOffset = 0
		}
		d.paused = false
		return d.seekEpisode("0")
	}
	if d.station == nil && d.episode == nil || d.state == "failed" {
		return fail("nothing playing")
	}
	if d.player != nil {
		if err := d.player.command("set_property", "pause", false); err != nil {
			return fail(err.Error())
		}
	}
	d.paused = false
	if d.state == "paused" {
		d.state = "playing"
	}
	return ok("resumed")
}

func (d *Daemon) skip() string {
	stations := stationSnapshot()
	if len(stations) == 0 {
		return fail("no stations")
	}

	// pick a different station
	var next *Station
	for {
		next = &stations[randInt(len(stations))]
		if d.station == nil || next.Name != d.station.Name {
			break
		}
		if len(stations) == 1 {
			break
		}
	}

	return d.play(next.Name)
}

// volumeCmd changes the volume. The argument is a number ("70"), a step
// ("+5", "-10"), "up"/"down", or empty to just report the current level.
func (d *Daemon) volumeCmd(arg string) string {
	arg = strings.TrimSpace(arg)
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "":
		return ok(fmt.Sprintf("volume: %d", d.volume))
	case "up":
		return d.setVolume(d.volume + 5)
	case "down":
		return d.setVolume(d.volume - 5)
	}

	if strings.HasPrefix(arg, "+") || strings.HasPrefix(arg, "-") {
		n, err := strconv.Atoi(arg)
		if err != nil {
			return fail("bad volume: " + arg)
		}
		return d.setVolume(d.volume + n)
	}

	n, err := strconv.Atoi(arg)
	if err != nil {
		return fail("bad volume: " + arg)
	}
	return d.setVolume(n)
}

// setVolume updates the live player without disturbing the stream or pause.
func (d *Daemon) setVolume(n int) string {
	n = max(0, min(100, n))
	if d.player != nil {
		if err := d.player.command("set_property", "volume", n); err != nil {
			return fail(err.Error())
		}
	}
	d.volume = n
	if err := savePlaybackSettings(d.playbackSettings()); err != nil {
		return fail("volume changed, but could not save it: " + err.Error())
	}
	return ok(fmt.Sprintf("volume: %d", d.volume))
}

func (d *Daemon) equalizer() equalizerConfig {
	return normalizeEqualizerConfig(equalizerConfig{Preset: d.eqPreset, Custom: d.eqCustom})
}

func (d *Daemon) playbackSettings() playbackSettings {
	settings := playbackSettings{Volume: d.volume, Notifications: d.notifications}
	settings.setEqualizer(d.equalizer())
	return settings
}

func (d *Daemon) notificationsCmd(arg string) string {
	value := strings.ToLower(strings.TrimSpace(arg))
	if value == "" {
		if d.notifications {
			return ok("notifications: on")
		}
		return ok("notifications: off")
	}
	var enabled bool
	switch value {
	case "on", "true", "1":
		enabled = true
	case "off", "false", "0":
		enabled = false
	default:
		return fail("notifications must be on or off")
	}
	previous := d.notifications
	d.notifications = enabled
	if err := savePlaybackSettings(d.playbackSettings()); err != nil {
		d.notifications = previous
		return fail("notifications unchanged: " + err.Error())
	}
	if enabled {
		return ok("notifications: on")
	}
	return ok("notifications: off")
}

func (d *Daemon) setEqualizer(next equalizerConfig) string {
	next = normalizeEqualizerConfig(next)
	settings := d.playbackSettings()
	settings.setEqualizer(next)
	if err := savePlaybackSettings(settings); err != nil {
		return fail("EQ unchanged because it could not be saved: " + err.Error())
	}
	d.eqPreset, d.eqCustom = next.Preset, next.Custom
	if d.player != nil {
		d.player.setEqualizer(next.activeBands())
	}
	return ok(formatEqualizer(next))
}

func (d *Daemon) equalizerCmd(arg string) string {
	if strings.EqualFold(strings.TrimSpace(arg), "list") {
		return ok(equalizerPresetList())
	}
	next, changed, err := updateEqualizerConfig(d.equalizer(), arg)
	if err != nil {
		return fail(err.Error())
	}
	if !changed {
		return ok(formatEqualizer(next))
	}
	return d.setEqualizer(next)
}

func (d *Daemon) equalizerStateCmd(arg string) string {
	var state equalizerWireState
	if err := json.Unmarshal([]byte(arg), &state); err != nil {
		return fail("invalid EQ state: " + err.Error())
	}
	if strings.TrimSpace(state.Preset) == "" {
		return fail("invalid EQ state: missing preset")
	}
	if len(state.Custom) != audio.EqualizerBandCount {
		return fail(fmt.Sprintf("invalid EQ state: expected %d bands", audio.EqualizerBandCount))
	}
	var custom audio.EqualizerBands
	for i, gain := range state.Custom {
		if math.IsNaN(gain) || math.IsInf(gain, 0) || gain < audio.EqualizerMinGain || gain > audio.EqualizerMaxGain {
			return fail("invalid EQ state: band gain out of range")
		}
		custom[i] = gain
	}
	presetName := customEqualizerPreset
	if !strings.EqualFold(strings.TrimSpace(state.Preset), customEqualizerPreset) {
		preset, ok := equalizerPresetByName(state.Preset)
		if !ok {
			return fail("invalid EQ state: unknown preset")
		}
		presetName = preset.Name
	}
	next := equalizerConfig{Preset: presetName, Custom: custom}
	if next == d.equalizer() {
		return ok(formatEqualizer(next))
	}
	return d.setEqualizer(next)
}

// mute silences playback without losing the level it returns to.
func (d *Daemon) mute() string {
	if d.player != nil {
		if err := d.player.command("set_property", "mute", !d.muted); err != nil {
			return fail(err.Error())
		}
	}
	d.muted = !d.muted
	if d.muted {
		return ok(fmt.Sprintf("muted (volume: %d)", d.volume))
	}
	return ok(fmt.Sprintf("unmuted (volume: %d)", d.volume))
}

// reload picks up station edits from the config file.
func (d *Daemon) reload() string {
	if err := loadUserStations(); err != nil {
		return fail(err.Error())
	}
	return ok(fmt.Sprintf("reloaded, %d stations", len(stationSnapshot())))
}

func (d *Daemon) closePlayer() {
	if d.loadTimer != nil {
		d.loadTimer.Stop()
		d.loadTimer = nil
	}
	if d.player != nil {
		close(d.watchDone)
		d.player.close()
		d.player = nil
	}
}

func (d *Daemon) kill() {
	d.saveEpisode(false)
	if d.resolveCancel != nil {
		d.resolveCancel()
		d.resolveCancel = nil
	}
	d.generation++
	if d.retryTimer != nil {
		d.retryTimer.Stop()
		d.retryTimer = nil
	}
	d.closePlayer()
	d.closeEpisodeCache()
	if d.progressTimer != nil {
		d.progressTimer.Stop()
		d.progressTimer = nil
	}
	d.episode = nil
	d.episodeQueue, d.episodeHistory = nil, nil
	d.stationHistory, d.stationForward = nil, nil
	d.episodeOffset, d.episodeDuration = 0, 0
	d.station = nil
	d.nowPlaying = nil
	d.paused = false
	d.state, d.lastError = "idle", ""
	d.startedAt = time.Time{}
	d.retries = 0
	d.retryAt = time.Time{}
}

func (d *Daemon) mediaState() media.State {
	status := media.StatusStopped
	if d.state == "playing" {
		status = media.StatusPlaying
	} else if d.paused || d.state == "paused" {
		status = media.StatusPaused
	}
	state := media.State{Status: status, Volume: float64(d.volume) / 100}
	if d.muted {
		state.Volume = 0
	}
	if d.episode != nil {
		state.Track = media.Track{
			Title: d.episode.Title, Artist: d.episode.Author, Album: d.episode.Show,
			URL: d.episode.URL, ArtURL: d.episode.Artwork, Duration: d.episodeDuration,
		}
		state.Position, state.Seekable = d.episodePosition(), true
		state.CanGoNext = len(d.episodeQueue) > 0
		state.CanGoPrevious = true
	} else if d.station != nil {
		state.Track = media.Track{Title: d.station.Desc, Album: d.station.Name, Genre: d.station.Tags, URL: d.station.URL, ArtURL: d.station.Artwork}
		if d.nowPlaying != nil {
			state.Track.Title, state.Track.Artist = d.nowPlaying.Title, d.nowPlaying.Artist
			if state.Track.Title == "" {
				state.Track.Title = d.nowPlaying.Raw
			}
		}
		state.CanGoNext = len(stationSnapshot()) > 1 || len(d.stationForward) > 0
		state.CanGoPrevious = len(d.stationHistory) > 0
	}
	return state
}

func (d *Daemon) updateMedia() {
	if d.media != nil {
		d.media.Update(d.mediaState())
	}
}

func (d *Daemon) mediaCommand(command media.Command) {
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.updateMedia()
	switch command.Kind {
	case media.Toggle:
		if d.paused {
			d.resume()
		} else if d.station == nil && d.episode == nil {
			d.play("")
		} else {
			d.pause()
		}
	case media.Play:
		if d.station == nil && d.episode == nil {
			d.play("")
		} else {
			d.resume()
		}
	case media.Pause:
		d.pause()
	case media.Stop:
		d.cancelSleep()
		d.kill()
	case media.Next:
		if d.episode != nil {
			d.podcastQueueCommand("next", "")
		} else {
			d.nextStation()
		}
	case media.Previous:
		if d.episode != nil {
			d.podcastQueueCommand("prev", "")
		} else {
			d.previousStation()
		}
	case media.Seek:
		if d.episode != nil {
			d.seekEpisode(command.Position.String())
		}
	case media.SetPosition:
		if d.episode != nil {
			delta := command.Position - d.episodePosition()
			d.seekEpisode(delta.String())
		}
	case media.SetVolume:
		d.setVolume(int(math.Round(command.Volume * 100)))
	}
}

func (d *Daemon) nextStation() string {
	if len(d.stationForward) == 0 {
		return d.skip()
	}
	target := d.stationForward[len(d.stationForward)-1]
	forward := d.stationForward[:len(d.stationForward)-1]
	history := d.stationHistory
	if d.station != nil {
		history = append(history, *d.station)
	}
	d.kill()
	d.stationHistory, d.stationForward = history, forward
	d.station = &target
	if err := d.startPlayback(); err != nil {
		d.state, d.lastError = "failed", err.Error()
		return fail(err.Error())
	}
	return ok("loading: " + target.Desc)
}

func (d *Daemon) previousStation() string {
	if len(d.stationHistory) == 0 {
		return fail("no previous station")
	}
	target := d.stationHistory[len(d.stationHistory)-1]
	history := d.stationHistory[:len(d.stationHistory)-1]
	forward := d.stationForward
	if d.station != nil {
		forward = append(forward, *d.station)
	}
	d.kill()
	d.stationHistory, d.stationForward = history, forward
	d.station = &target
	if err := d.startPlayback(); err != nil {
		d.state, d.lastError = "failed", err.Error()
		return fail(err.Error())
	}
	return ok("loading: " + target.Desc)
}

func (d *Daemon) status() string {
	s := Status{
		Episode:      d.episode,
		NowPlaying:   d.nowPlaying,
		StorageError: d.storageError,
		Version:      buildVersion(),
		Protocol:     daemonProtocol,
		Playing:      d.state == "playing",
		Paused:       d.paused,
		Volume:       d.volume,
		Muted:        d.muted,
		EQPreset:     d.equalizer().Preset,
		EQBands:      d.equalizer().activeBands(),
		State:        d.state,
		Error:        d.lastError,
		Retries:      d.retries,
		RetryAt:      d.retryAt,
		SleepUntil:   d.sleepUntil,
	}
	if s.State == "" {
		s.State = "idle"
	}
	if d.episode != nil {
		s.Position, s.Duration, s.Seekable = d.episodePosition().Seconds(), d.episodeDuration.Seconds(), true
		s.Speed = d.playbackSpeed()
	}
	s.Queued = len(d.episodeQueue)
	if !d.sleepUntil.IsZero() {
		s.Sleep = max(time.Duration(0), time.Until(d.sleepUntil)).Round(time.Second).String()
	}

	if d.station != nil {
		s.Station = d.station.Name
		s.URL = d.station.URL
		s.Desc = d.station.Desc
		if !d.startedAt.IsZero() {
			s.Uptime = time.Since(d.startedAt).Round(time.Second).String()
		}
	}

	b, _ := json.Marshal(s)
	return string(b)
}

func (d *Daemon) listStations() string {
	var names []string
	for _, s := range stationSnapshot() {
		names = append(names, s.Name)
	}
	return strings.Join(names, " ")
}

// runDaemon starts the daemon process and blocks forever.
func runDaemon() {
	if err := loadUserStations(); err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
	}
	settings, err := loadPlaybackSettings()
	if err != nil {
		fmt.Fprintf(os.Stderr, "playback state: %v\n", err)
		settings = defaultPlaybackSettings()
	}
	d := &Daemon{volume: settings.Volume, eqPreset: settings.EQPreset, eqCustom: settings.EQBands, notifications: settings.Notifications, state: "idle"}
	mediaService, mediaErr := media.New(func(command media.Command) { go d.mediaCommand(command) })
	if mediaErr != nil {
		fmt.Fprintf(os.Stderr, "media controls: %v\n", mediaErr)
	} else {
		d.media = mediaService
		defer mediaService.Close()
	}
	if err := d.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(dim + "chill daemon started" + reset)
	fmt.Println(dim + "socket: " + socketPath() + reset)

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for range ticker.C {
			d.mu.Lock()
			d.updateMedia()
			d.mu.Unlock()
		}
	}()
	if err := media.Run(mediaService, func() error { select {} }); err != nil {
		fmt.Fprintf(os.Stderr, "media controls: %v\n", err)
	}
}

// isDaemonRunning checks if a daemon is already running by attempting
// to connect to the socket.
func isDaemonRunning() bool {
	conn, err := dialSocket()
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
