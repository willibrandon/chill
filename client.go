// client.go implements the client that communicates with the daemon.
// It sends commands over a Unix socket and returns the text to show the user,
// so the CLI can print it and the REPL can add it to its transcript.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// sendCommand sends a command to the daemon and returns the response.
func sendCommand(cmd string) (string, error) {
	unlock, err := lockDaemon()
	if err != nil {
		return "", err
	}
	defer unlock()
	if cmd != "stop" && cmd != "quit" {
		if err := upgradeDaemon(); err != nil {
			return "", err
		}
	}
	return sendRawCommand(cmd)
}

// sendRawCommand is used during the upgrade handshake, already under the lock.
func sendRawCommand(cmd string) (string, error) {
	return sendRawCommandContext(context.Background(), cmd)
}

func sendRawCommandContext(ctx context.Context, cmd string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	conn, err := dialSocket()
	if err != nil {
		return "", err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	deadline := 5 * time.Second
	if cmd == "stop" || cmd == "quit" {
		deadline = 20 * time.Second
	}
	conn.SetDeadline(time.Now().Add(deadline))

	_, err = conn.Write([]byte(cmd + "\n"))
	if err != nil {
		return "", err
	}

	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	if cmd == "stop" || cmd == "quit" {
		// Wait for process exit, not just the acknowledgement. Otherwise the
		// old daemon can unlink the replacement's socket during its cleanup.
		if _, err := io.Copy(io.Discard, reader); err != nil {
			// Windows can reset TCP connections when os.Exit closes them.
			// After an acknowledgement and removal of the discovery file,
			// that reset also confirms the old daemon finished its cleanup.
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return "", err
			}
			if _, statErr := os.Stat(socketPath()); !os.IsNotExist(statErr) {
				return "", err
			}
		}
	}

	return strings.TrimSpace(response), nil
}

// ask sends a command and unwraps the reply: the message for success,
// an error carrying the daemon's reason otherwise.
func ask(cmd string) (string, error) {
	raw, err := sendCommand(cmd)
	return unwrapReply(raw, err)
}

func unwrapReply(raw string, err error) (string, error) {
	if err != nil {
		return "", err
	}

	var r reply
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return "", fmt.Errorf("daemon is an older chill, restart it: %w", err)
	}
	if !r.OK {
		return "", errors.New(r.Msg)
	}
	return r.Msg, nil
}

// ensureDaemon starts or upgrades the daemon under the same lock used by
// commands, so simultaneous clients cannot both replace it.
func ensureDaemon() error {
	unlock, err := lockDaemon()
	if err != nil {
		return err
	}
	defer unlock()
	if isDaemonRunning() {
		return upgradeDaemon()
	}
	return startDaemon()
}

// startDaemon waits up to two seconds for the new daemon to become ready.
func startDaemon() error {
	// start daemon in background
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "--daemon")
	log, err := openDaemonLog()
	if err != nil {
		return fmt.Errorf("opening daemon log: %w", err)
	}
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// wait for daemon to be ready
	for i := 0; i < 20; i++ {
		select {
		case err := <-exited:
			return daemonStartError(fmt.Sprintf("process exited (%v)", err))
		case <-time.After(100 * time.Millisecond):
		}
		if isDaemonRunning() {
			return nil
		}
	}

	cmd.Process.Kill()
	<-exited
	return daemonStartError("timed out waiting for the control socket")
}

// clientPlay starts playing the specified station via the daemon.
func clientPlay(station string) (string, error) {
	palette := currentCLIPalette()
	selected := findStation(station)
	if station == "" {
		selected = findStation(defaultStation())
	}
	if selected != nil {
		if err := checkMediaRequirements([]MediaItem{itemFromStation(*selected)}); err != nil {
			return "", err
		}
	}
	if err := ensureDaemon(); err != nil {
		return "", err
	}

	cmd := "play"
	if station != "" {
		cmd += " " + station
	}

	resp, err := ask(cmd)
	if err != nil {
		return "", err
	}

	return palette.pink + "♪ " + resp + palette.reset, nil
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
	palette := currentCLIPalette()
	s, err := fetchStatus()
	if err != nil {
		return "", err
	}

	if s == nil {
		return palette.dim + "not running" + palette.reset, nil
	}

	return strings.Join(statusFacts(s), " │ "), nil
}

// statusFacts is shared by the CLI and the REPL's continuously updated bar.
func statusFacts(s *Status) []string {
	if s == nil {
		return []string{"daemon off"}
	}
	state := s.State
	if state == "" { // also display status from a daemon started before an upgrade
		state = "idle"
		if s.Playing {
			state = "playing"
		} else if s.Paused {
			state = "paused"
		}
	}
	facts := []string{state}
	if s.Episode != nil {
		duration := "?"
		if s.Duration > 0 {
			duration = clock(s.Duration)
		}
		facts = append(facts, clock(s.Position)+" / "+duration)
		if s.Speed != 0 && s.Speed != 1 {
			facts = append(facts, fmt.Sprintf("%.2fx", s.Speed))
		}
		facts = append(facts, s.Episode.Show, s.Episode.Title)
	} else if s.Item != nil && s.Item.Kind != MediaStation {
		duration := "?"
		if s.Duration > 0 {
			duration = clock(s.Duration)
		}
		facts = append(facts, clock(s.Position)+" / "+duration)
		if s.Speed != 0 && s.Speed != 1 {
			facts = append(facts, fmt.Sprintf("%.2fx", s.Speed))
		}
		facts = append(facts, s.Item.display())
	}
	if s.Queued > 0 {
		facts = append(facts, fmt.Sprintf("queued %d", s.Queued))
	}
	if s.Shuffle {
		facts = append(facts, "shuffle")
	}
	if s.Repeat != "" && s.Repeat != "off" {
		facts = append(facts, "repeat "+s.Repeat)
	}
	if s.StorageError != "" {
		facts = append(facts, s.StorageError)
	}
	if s.Paused && state != "paused" {
		facts = append(facts, "paused")
	}
	if s.Station != "" {
		facts = append(facts, s.Station)
		if s.NowPlaying != nil && s.NowPlaying.Raw != "" {
			facts = append(facts, s.NowPlaying.Raw)
		}
	}
	if s.Uptime != "" {
		facts = append(facts, s.Uptime)
	}
	facts = append(facts, fmt.Sprintf("vol %d", s.Volume))
	if s.Muted {
		facts = append(facts, "muted")
	}
	if s.EQPreset != "" {
		facts = append(facts, "eq "+s.EQPreset)
	}
	if s.Sleep != "" {
		facts = append(facts, "sleep "+s.Sleep)
	}
	if s.Error != "" {
		if s.State == "loading" || s.State == "reconnecting" {
			retry := fmt.Sprintf("retry %d", s.Retries)
			if !s.RetryAt.IsZero() {
				retry += " in " + max(time.Duration(0), time.Until(s.RetryAt)).Round(time.Second).String()
			}
			facts = append(facts, retry)
		}
		facts = append(facts, s.Error)
	}
	return facts
}

// clientToggle pauses if playing, resumes if paused, or starts playing if stopped.
func clientToggle() (string, error) {
	palette := currentCLIPalette()
	if !isDaemonRunning() {
		return clientPlay("")
	}

	resp, err := ask("toggle")
	if err != nil {
		return "", err
	}

	if resp == "paused" {
		return palette.dim + "⏸ paused" + palette.reset, nil
	}
	return palette.purple + "▶ " + resp + palette.reset, nil
}

// clientPause pauses playback.
func clientPause() (string, error) {
	palette := currentCLIPalette()
	if !isDaemonRunning() {
		return palette.dim + "not running" + palette.reset, nil
	}

	resp, err := ask("pause")
	if err != nil {
		return "", err
	}

	return palette.dim + "⏸ " + resp + palette.reset, nil
}

// clientResume resumes paused playback.
func clientResume() (string, error) {
	palette := currentCLIPalette()
	if !isDaemonRunning() {
		return palette.dim + "not running" + palette.reset, nil
	}

	resp, err := ask("resume")
	if err != nil {
		return "", err
	}

	return palette.purple + "▶ " + resp + palette.reset, nil
}

// clientSkip skips to a random different station.
func clientSkip() (string, error) {
	palette := currentCLIPalette()
	stations := stationSnapshot()
	items := make([]MediaItem, len(stations))
	for i, station := range stations {
		items[i] = itemFromStation(station)
	}
	if err := checkMediaRequirements(items); err != nil {
		return "", err
	}
	if err := ensureDaemon(); err != nil {
		return "", err
	}

	resp, err := ask("skip")
	if err != nil {
		return "", err
	}

	return palette.pink + "♪ " + resp + palette.reset, nil
}

// clientVolume sets or reports the volume. arg is a number, a step like
// "+5", "up", "down", or empty to report.
func clientVolume(arg string) (string, error) {
	palette := currentCLIPalette()
	if !isDaemonRunning() {
		return palette.dim + "not running" + palette.reset, nil
	}

	cmd := "vol"
	if arg != "" {
		cmd += " " + arg
	}

	resp, err := ask(cmd)
	if err != nil {
		return "", err
	}
	return palette.cyan + "♫ " + resp + palette.reset, nil
}

// clientEqualizer reports or changes the persistent equalizer. Unlike playback
// controls, it also works while the daemon is stopped so a curve can be chosen
// before starting audio.
func clientEqualizer(arg string) (string, error) {
	palette := currentCLIPalette()
	arg = strings.TrimSpace(arg)
	if strings.EqualFold(arg, "list") {
		return equalizerPresetList(), nil
	}
	unlock, err := lockDaemon()
	if err != nil {
		return "", err
	}
	defer unlock()
	if isDaemonRunning() {
		if err := upgradeDaemon(); err != nil {
			return "", err
		}
		command := "eq"
		if arg != "" {
			command += " " + arg
		}
		out, err := unwrapReply(sendRawCommand(command))
		if err != nil {
			return "", err
		}
		return palette.cyan + "♫ " + out + palette.reset, nil
	}

	settings, err := loadPlaybackSettings()
	if err != nil {
		return "", fmt.Errorf("reading playback state: %w", err)
	}
	next, changed, err := updateEqualizerConfig(settings.equalizer(), arg)
	if err != nil {
		return "", err
	}
	if changed {
		settings.setEqualizer(next)
		if err := savePlaybackSettings(settings); err != nil {
			return "", fmt.Errorf("saving EQ: %w", err)
		}
	}
	return palette.cyan + "♫ " + formatEqualizer(next) + palette.reset, nil
}

func clientNotifications(arg string) (string, error) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	if arg != "" && arg != "on" && arg != "off" {
		return "", fmt.Errorf("notifications must be on or off")
	}
	unlock, err := lockDaemon()
	if err != nil {
		return "", err
	}
	defer unlock()
	if isDaemonRunning() {
		if err := upgradeDaemon(); err != nil {
			return "", err
		}
		command := "notifications"
		if arg != "" {
			command += " " + arg
		}
		return unwrapReply(sendRawCommand(command))
	}
	settings, err := loadPlaybackSettings()
	if err != nil {
		return "", fmt.Errorf("reading playback state: %w", err)
	}
	if arg != "" {
		settings.Notifications = arg == "on"
		if err := savePlaybackSettings(settings); err != nil {
			return "", fmt.Errorf("saving notifications: %w", err)
		}
	}
	if settings.Notifications {
		return "notifications: on", nil
	}
	return "notifications: off", nil
}

func clientSetEqualizerState(eq equalizerConfig) error {
	eq = normalizeEqualizerConfig(eq)
	state, err := json.Marshal(equalizerWireState{Preset: eq.Preset, Custom: append([]float64(nil), eq.Custom[:]...)})
	if err != nil {
		return err
	}
	unlock, err := lockDaemon()
	if err != nil {
		return err
	}
	defer unlock()
	if isDaemonRunning() {
		if err := upgradeDaemon(); err != nil {
			return err
		}
		_, err := unwrapReply(sendRawCommand("eq-state " + string(state)))
		return err
	}
	settings, err := loadPlaybackSettings()
	if err != nil {
		return err
	}
	settings.setEqualizer(eq)
	return savePlaybackSettings(settings)
}

// clientMute toggles mute without changing the volume.
func clientMute() (string, error) {
	palette := currentCLIPalette()
	if !isDaemonRunning() {
		return palette.dim + "not running" + palette.reset, nil
	}

	resp, err := ask("mute")
	if err != nil {
		return "", err
	}
	return palette.cyan + "♫ " + resp + palette.reset, nil
}

func clientSleep(arg string) (string, error) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	if arg != "off" && arg != "" {
		if _, err := sleepDuration(arg); err != nil {
			return "", err
		}
	}
	if !isDaemonRunning() {
		return "", fmt.Errorf("nothing playing; start a station or podcast before setting a sleep timer")
	}
	return ask("sleep " + arg)
}

// clientStop stops playback and terminates the daemon.
func clientStop() (string, error) {
	palette := currentCLIPalette()
	if !isDaemonRunning() {
		return palette.dim + "not running" + palette.reset, nil
	}

	_, err := sendCommand("stop")
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		// daemon is still there but never answered, so nothing was stopped
		return "", err
	}
	// any other error means the daemon exited, that's fine

	return palette.dim + "~ stay chill ~" + palette.reset, nil
}
