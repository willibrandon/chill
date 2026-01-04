// client.go implements the CLI client that communicates with the daemon.
// It sends commands over a Unix socket and displays responses to the user.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
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
func clientPlay(station string) {
	if err := ensureDaemon(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	cmd := "play"
	if station != "" {
		cmd += " " + station
	}

	resp, err := sendCommand(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%s♪ %s%s\n", pink, resp, reset)
}

// clientStatus displays the current playback status.
func clientStatus() {
	if !isDaemonRunning() {
		fmt.Println(dim + "not running" + reset)
		return
	}

	resp, err := sendCommand("status")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var s Status
	if err := json.Unmarshal([]byte(resp), &s); err != nil {
		fmt.Println(resp)
		return
	}

	if !s.Playing && !s.Paused {
		fmt.Println(dim + "idle" + reset)
		return
	}

	state := purple + "▶" + reset
	if s.Paused {
		state = dim + "⏸" + reset
	}

	fmt.Printf("%s %s%s%s\n", state, pink, s.Desc, reset)
	fmt.Printf("  %s%s │ %s%s\n", dim, s.Station, s.Uptime, reset)
}

// clientToggle pauses if playing, resumes if paused, or starts playing if stopped.
func clientToggle() {
	if !isDaemonRunning() {
		clientPlay("lofi-girl")
		return
	}

	resp, err := sendCommand("toggle")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if resp == "paused" {
		fmt.Printf("%s⏸ paused%s\n", dim, reset)
	} else {
		fmt.Printf("%s▶ resumed%s\n", purple, reset)
	}
}

// clientSkip skips to a random different station.
func clientSkip() {
	if err := ensureDaemon(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	resp, err := sendCommand("skip")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%s♪ %s%s\n", pink, resp, reset)
}

// clientStop stops playback and terminates the daemon.
func clientStop() {
	if !isDaemonRunning() {
		fmt.Println(dim + "not running" + reset)
		return
	}

	_, err := sendCommand("stop")
	if err != nil {
		// daemon exited, that's fine
	}

	fmt.Println(dim + "~ stay chill ~" + reset)
}
