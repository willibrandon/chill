// daemon.go implements the background daemon that manages mpv playback.
// The daemon listens on a Unix socket and accepts commands from clients.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultVolume is where playback starts, and where volume returns after a restart.
const defaultVolume = 70

// Daemon manages the mpv subprocess and handles client commands.
// It maintains playback state and communicates over a Unix socket.
type Daemon struct {
	mu        sync.Mutex    // protects all fields
	tree      *processTree  // mpv and any processes it spawned
	exited    chan struct{} // closed once mpv has exited
	station   *Station      // currently playing station
	paused    bool          // whether playback is paused
	volume    int           // 0-100, applied to mpv whenever it changes
	startedAt time.Time     // when current station started
	listener  net.Listener  // Unix socket listener
}

// Status represents the current playback state, serialized as JSON for clients.
type Status struct {
	Playing bool   `json:"playing"`           // true if actively playing
	Paused  bool   `json:"paused"`            // true if paused
	Station string `json:"station,omitempty"` // station name
	Desc    string `json:"desc,omitempty"`    // station description
	Uptime  string `json:"uptime,omitempty"`  // how long current station has been playing
	Volume  int    `json:"volume"`            // 0-100
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
		if d.paused {
			return d.resume()
		}
		return d.pause()
	case "stop", "quit":
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
	default:
		return fail("unknown command")
	}
}

func (d *Daemon) play(name string) string {
	if name == "" {
		name = "lofi-girl"
	}

	station := findStation(name)
	if station == nil {
		return fail("unknown station: " + name)
	}

	d.kill()

	// Stdout and stderr stay nil so they go to the null device. An io.Writer
	// would make exec use pipes, and Wait would then block until every
	// process that inherited them has exited.
	cmd := exec.Command("mpv",
		"--no-video",
		"--really-quiet",
		fmt.Sprintf("--volume=%d", d.volume),
		station.URL,
	)

	tree, err := startInTree(cmd)
	if err != nil {
		return fail("failed to start: " + err.Error())
	}

	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()

	d.tree = tree
	d.exited = exited
	d.station = station
	d.paused = false
	d.startedAt = time.Now()

	return ok("playing: " + station.Desc)
}

func (d *Daemon) pause() string {
	if d.tree == nil {
		return fail("nothing playing")
	}
	if d.paused {
		// suspending twice on Windows would take two resumes to undo
		return ok("paused")
	}
	if err := d.tree.pause(); err != nil {
		return fail(err.Error())
	}
	d.paused = true
	return ok("paused")
}

func (d *Daemon) resume() string {
	if d.tree == nil {
		return fail("nothing playing")
	}
	if err := d.tree.resume(); err != nil {
		return fail(err.Error())
	}
	d.paused = false
	return ok("resumed")
}

func (d *Daemon) skip() string {
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

// setVolume clamps the level to 0-100 and restarts playback with it, if
// something is playing. mpv only takes its volume flag at startup, and the
// stream is re-resolved by yt-dlp in a couple of seconds.
func (d *Daemon) setVolume(n int) string {
	d.volume = max(0, min(100, n))
	if d.tree == nil {
		return ok(fmt.Sprintf("volume: %d", d.volume))
	}

	station := d.station
	out := d.play(station.Name)
	if strings.HasPrefix(out, `{"ok":false`) {
		return out
	}
	return ok(fmt.Sprintf("volume: %d │ %s", d.volume, station.Desc))
}

// mute silences playback without losing the level it returns to.
func (d *Daemon) mute() string {
	if d.volume == 0 {
		return d.setVolume(defaultVolume)
	}
	return d.setVolume(0)
}

// reload picks up station edits from the config file.
func (d *Daemon) reload() string {
	if err := loadUserStations(); err != nil {
		return fail(err.Error())
	}
	return ok(fmt.Sprintf("reloaded, %d stations", len(stations)))
}

func (d *Daemon) kill() {
	if d.tree != nil {
		d.tree.kill()
		<-d.exited
	}
	d.tree = nil
	d.station = nil
}

func (d *Daemon) status() string {
	s := Status{
		Playing: d.tree != nil && !d.paused,
		Paused:  d.paused,
		Volume:  d.volume,
	}

	if d.station != nil {
		s.Station = d.station.Name
		s.Desc = d.station.Desc
		s.Uptime = time.Since(d.startedAt).Round(time.Second).String()
	}

	b, _ := json.Marshal(s)
	return string(b)
}

func (d *Daemon) listStations() string {
	var names []string
	for _, s := range stations {
		names = append(names, s.Name)
	}
	return strings.Join(names, " ")
}

// runDaemon starts the daemon process and blocks forever.
func runDaemon() {
	if err := loadUserStations(); err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
	}
	d := &Daemon{volume: defaultVolume}
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
