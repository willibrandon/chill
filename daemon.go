// daemon.go implements the background daemon that manages mpv playback.
// The daemon listens on a Unix socket and accepts commands from clients.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultVolume is used until a volume has been saved.
const defaultVolume = 70

const maxRetryDelay = 30 * time.Second

const daemonProtocol = 1

// Daemon manages the mpv subprocess and handles client commands.
// It maintains playback state and communicates over a Unix socket.
type Daemon struct {
	mu              sync.Mutex // protects all fields
	player          player
	newPlayer       func(int, bool, bool) (player, error) // nil uses mpv
	watchDone       chan struct{}
	station         *Station // currently playing station
	paused          bool     // whether playback is paused
	muted           bool
	volume          int    // 0-100, applied to mpv whenever it changes
	state           string // idle, loading, reconnecting, playing, paused, failed
	lastError       string
	retries         int
	retryTimer      *time.Timer
	retryAt         time.Time
	loadTimer       *time.Timer
	generation      uint64 // invalidates callbacks after switching or stopping
	sleepTimer      *time.Timer
	sleepUntil      time.Time
	sleepGeneration uint64
	startedAt       time.Time    // when current station started
	listener        net.Listener // Unix socket listener
}

// Status represents the current playback state, serialized as JSON for clients.
type Status struct {
	Version    string    `json:"version"`
	Protocol   int       `json:"protocol"`
	Playing    bool      `json:"playing"`           // true if actively playing
	Paused     bool      `json:"paused"`            // true if paused
	Station    string    `json:"station,omitempty"` // station name
	URL        string    `json:"url,omitempty"`
	Desc       string    `json:"desc,omitempty"`   // station description
	Uptime     string    `json:"uptime,omitempty"` // how long current station has been playing
	Volume     int       `json:"volume"`           // 0-100
	Muted      bool      `json:"muted"`
	State      string    `json:"state"`
	Error      string    `json:"error,omitempty"`
	Retries    int       `json:"retries,omitempty"`
	RetryAt    time.Time `json:"retry_at,omitzero"`
	Sleep      string    `json:"sleep,omitempty"`
	SleepUntil time.Time `json:"sleep_until,omitzero"`
}

// reply is what the daemon writes back for a command. Clients read the JSON
// tag to tell a failure from a message worth showing as success; the daemon
// itself is versioned with the clients, so the old tagless protocol is gone.
type reply struct {
	OK  bool   `json:"ok"`
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

	switch action {
	case "play":
		return d.play(arg)
	case "pause":
		return d.pause()
	case "resume":
		return d.resume()
	case "toggle":
		if d.state == "idle" || d.state == "failed" || d.station == nil {
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

	d.kill()
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
		start = startPlayer
	}
	p, err := start(d.volume, d.muted, d.paused)
	if err != nil {
		return err
	}
	d.player = p
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
	if err := p.command("loadfile", d.station.URL, "replace"); err != nil {
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

func (d *Daemon) playbackFailed(reason string) {
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
	if d.generation != generation || d.station == nil {
		return
	}
	d.retryTimer = nil
	d.retryAt = time.Time{}
	if err := d.startPlayback(); err != nil {
		d.playbackFailed(err.Error())
	}
}

func (d *Daemon) pause() string {
	if d.station == nil || d.state == "failed" {
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
	if d.state == "playing" {
		d.state = "paused"
	}
	return ok("paused")
}

func (d *Daemon) resume() string {
	if d.station == nil || d.state == "failed" {
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
	if err := writeJSON(volumePath(), playbackSettings{Volume: n}); err != nil {
		return fail("volume changed, but could not save it: " + err.Error())
	}
	return ok(fmt.Sprintf("volume: %d", d.volume))
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
	d.generation++
	if d.retryTimer != nil {
		d.retryTimer.Stop()
		d.retryTimer = nil
	}
	d.closePlayer()
	d.station = nil
	d.paused = false
	d.state, d.lastError = "idle", ""
	d.startedAt = time.Time{}
	d.retries = 0
	d.retryAt = time.Time{}
}

func (d *Daemon) status() string {
	s := Status{
		Version:    buildVersion(),
		Protocol:   daemonProtocol,
		Playing:    d.state == "playing",
		Paused:     d.paused,
		Volume:     d.volume,
		Muted:      d.muted,
		State:      d.state,
		Error:      d.lastError,
		Retries:    d.retries,
		RetryAt:    d.retryAt,
		SleepUntil: d.sleepUntil,
	}
	if s.State == "" {
		s.State = "idle"
	}
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
	d := &Daemon{volume: rememberedVolume(), state: "idle"}
	if err := d.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(dim + "chill daemon started" + reset)
	fmt.Println(dim + "socket: " + socketPath() + reset)

	// keep running
	select {}
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
