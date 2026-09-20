package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

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
	Station    *Station  `json:"station,omitempty"`
	Volume     int       `json:"volume"`
	Muted      bool      `json:"muted"`
	Paused     bool      `json:"paused"`
	SleepUntil time.Time `json:"sleep_until,omitzero"`
}

func snapshotPlayback(s Status, now time.Time) (playbackSnapshot, error) {
	snapshot := playbackSnapshot{Volume: s.Volume, Muted: s.Muted, Paused: s.Paused, SleepUntil: s.SleepUntil}
	if snapshot.SleepUntil.IsZero() && s.Sleep != "" {
		remaining, err := time.ParseDuration(s.Sleep)
		if err != nil {
			return snapshot, fmt.Errorf("reading daemon sleep timer: %w", err)
		}
		snapshot.SleepUntil = now.Add(remaining)
	}
	if s.Station != "" && (s.Playing || s.Paused || s.State == "loading") {
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
		if err := checkRequirements(); err != nil {
			return err
		}
	}
	if err := writeJSON(volumePath(), playbackSettings{Volume: snapshot.Volume}); err != nil {
		return fmt.Errorf("saving volume before daemon upgrade: %w", err)
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
	d.cancelSleep()
	d.kill()
	d.volume = max(0, min(100, snapshot.Volume))
	d.muted = snapshot.Muted
	if snapshot.Station == nil || (!snapshot.SleepUntil.IsZero() && !snapshot.SleepUntil.After(time.Now())) {
		return ok("restored idle playback")
	}
	d.station, d.paused = snapshot.Station, snapshot.Paused
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
	return ok("restored playback")
}
