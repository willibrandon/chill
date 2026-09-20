// client.go implements the client that communicates with the daemon.
// It sends commands over a Unix socket and returns the text to show the user,
// so the CLI can print it and the REPL can add it to its transcript.

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// sendCommand sends a command to the daemon and returns the response.
func sendCommand(cmd string) (string, error) {
	conn, err := dialSocket()
	if err != nil {
		return "", err
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	_, err = conn.Write([]byte(cmd + "\n"))
	if err != nil {
		return "", err
	}

	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(response), nil
}

// ensureDaemon starts the daemon if it's not already running.
// It waits up to 2 seconds for the daemon to become ready.
func ensureDaemon() error {
	if isDaemonRunning() {
		return nil
	}

	// start daemon in background
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "--daemon")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return err
	}

	// wait for daemon to be ready
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		if isDaemonRunning() {
			return nil
		}
	}

	return fmt.Errorf("daemon failed to start")
}

// clientPlay starts playing the specified station via the daemon.
func clientPlay(station string) (string, error) {
	if err := checkRequirements(); err != nil {
		return "", err
	}
	if err := ensureDaemon(); err != nil {
		return "", err
	}

	cmd := "play"
	if station != "" {
		cmd += " " + station
	}

	resp, err := sendCommand(cmd)
	if err != nil {
		return "", err
	}

	return pink + "♪ " + resp + reset, nil
}

// fetchStatus asks the daemon for its playback state. It returns nil if the
// daemon isn't running.
func fetchStatus() (*Status, error) {
	if !isDaemonRunning() {
		return nil, nil
	}

	resp, err := sendCommand("status")
	if err != nil {
		return nil, err
	}

	var s Status
	if err := json.Unmarshal([]byte(resp), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// clientStatus describes the current playback status.
func clientStatus() (string, error) {
	s, err := fetchStatus()
	if err != nil {
		return "", err
	}

	if s == nil {
		return dim + "not running" + reset, nil
	}

	if !s.Playing && !s.Paused {
		return dim + "idle" + reset, nil
	}

	state := purple + "▶" + reset
	if s.Paused {
		state = dim + "⏸" + reset
	}

	return fmt.Sprintf("%s %s%s%s\n  %s%s │ %s%s", state, pink, s.Desc, reset, dim, s.Station, s.Uptime, reset), nil
}

// clientToggle pauses if playing, resumes if paused, or starts playing if stopped.
func clientToggle() (string, error) {
	if !isDaemonRunning() {
		return clientPlay("lofi-girl")
	}

	resp, err := sendCommand("toggle")
	if err != nil {
		return "", err
	}

	if resp == "paused" {
		return dim + "⏸ paused" + reset, nil
	}
	return purple + "▶ resumed" + reset, nil
}

// clientPause pauses playback.
func clientPause() (string, error) {
	if !isDaemonRunning() {
		return dim + "not running" + reset, nil
	}

	resp, err := sendCommand("pause")
	if err != nil {
		return "", err
	}

	return dim + "⏸ " + resp + reset, nil
}

// clientResume resumes paused playback.
func clientResume() (string, error) {
	if !isDaemonRunning() {
		return dim + "not running" + reset, nil
	}

	resp, err := sendCommand("resume")
	if err != nil {
		return "", err
	}

	return purple + "▶ " + resp + reset, nil
}

// clientSkip skips to a random different station.
func clientSkip() (string, error) {
	if err := checkRequirements(); err != nil {
		return "", err
	}
	if err := ensureDaemon(); err != nil {
		return "", err
	}

	resp, err := sendCommand("skip")
	if err != nil {
		return "", err
	}

	return pink + "♪ " + resp + reset, nil
}

// clientStop stops playback and terminates the daemon.
func clientStop() (string, error) {
	if !isDaemonRunning() {
		return dim + "not running" + reset, nil
	}

	_, err := sendCommand("stop")
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		// daemon is still there but never answered, so nothing was stopped
		return "", err
	}
	// any other error means the daemon exited, that's fine

	return dim + "~ stay chill ~" + reset, nil
}
