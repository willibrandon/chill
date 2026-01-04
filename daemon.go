package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Daemon struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	station   *Station
	paused    bool
	startedAt time.Time
	listener  net.Listener
}

type Status struct {
	Playing   bool   `json:"playing"`
	Paused    bool   `json:"paused"`
	Station   string `json:"station,omitempty"`
	Desc      string `json:"desc,omitempty"`
	Uptime    string `json:"uptime,omitempty"`
}

func socketPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("chill-%d.sock", os.Getuid()))
}

func (d *Daemon) Start() error {
	sock := socketPath()
	os.Remove(sock) // clean up old socket

	ln, err := net.Listen("unix", sock)
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
			os.Remove(socketPath())
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
		return "stopped"
	case "skip":
		return d.skip()
	case "status":
		return d.status()
	case "list":
		return d.listStations()
	default:
		return "unknown command"
	}
}

func (d *Daemon) play(name string) string {
	if name == "" {
		name = "lofi-girl"
	}

	station := findStation(name)
	if station == nil {
		return "unknown station: " + name
	}

	d.kill()
	d.station = station
	d.paused = false
	d.startedAt = time.Now()

	d.cmd = exec.Command("mpv",
		"--no-video",
		"--really-quiet",
		station.URL,
	)
	d.cmd.Stdout = io.Discard
	d.cmd.Stderr = io.Discard

	if err := d.cmd.Start(); err != nil {
		return "failed to start: " + err.Error()
	}

	go func() {
		d.cmd.Wait()
	}()

	return "playing: " + station.Desc
}

func (d *Daemon) pause() string {
	if d.cmd == nil || d.cmd.Process == nil {
		return "nothing playing"
	}
	d.cmd.Process.Signal(syscall.SIGSTOP)
	d.paused = true
	return "paused"
}

func (d *Daemon) resume() string {
	if d.cmd == nil || d.cmd.Process == nil {
		return "nothing playing"
	}
	d.cmd.Process.Signal(syscall.SIGCONT)
	d.paused = false
	return "resumed"
}

func (d *Daemon) skip() string {
	if len(stations) == 0 {
		return "no stations"
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

func (d *Daemon) kill() {
	if d.cmd != nil && d.cmd.Process != nil {
		d.cmd.Process.Kill()
		d.cmd.Wait()
	}
	d.cmd = nil
	d.station = nil
}

func (d *Daemon) status() string {
	s := Status{
		Playing: d.cmd != nil && d.cmd.Process != nil && !d.paused,
		Paused:  d.paused,
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

func runDaemon() {
	d := &Daemon{}
	if err := d.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(dim + "chill daemon started" + reset)
	fmt.Println(dim + "socket: " + socketPath() + reset)

	// keep running
	select {}
}

func isDaemonRunning() bool {
	conn, err := net.Dial("unix", socketPath())
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
