package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/willibrandon/chill/internal/audio"
)

type fakePlayer struct {
	commands [][]any
	event    chan playerEvent
	eq       audio.EqualizerBands
	closed   bool
	err      error
}

func (p *fakePlayer) command(args ...any) error {
	p.commands = append(p.commands, args)
	return p.err
}
func (p *fakePlayer) load(v string) error                     { return p.command("loadfile", v, "replace") }
func (p *fakePlayer) setPaused(v bool) error                  { return p.command("set_property", "pause", v) }
func (p *fakePlayer) setVolume(v int) error                   { return p.command("set_property", "volume", v) }
func (p *fakePlayer) setMuted(v bool) error                   { return p.command("set_property", "mute", v) }
func (p *fakePlayer) setSpeed(v float64) error                { return p.command("set_property", "speed", v) }
func (p *fakePlayer) setDevice(v string) error                { return p.command("set_property", "audio-device", v) }
func (p *fakePlayer) setEqualizer(bands audio.EqualizerBands) { p.eq = bands }
func (p *fakePlayer) events() <-chan playerEvent              { return p.event }
func (p *fakePlayer) close()                                  { p.closed = true }

func fakeDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := &Daemon{volume: 55}
	d.newPlayer = func(int, bool, bool) (player, error) {
		return &fakePlayer{event: make(chan playerEvent, 8)}, nil
	}
	t.Cleanup(func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.cancelSleep()
		d.kill()
	})
	return d
}

func daemonStatus(t *testing.T, d *Daemon) Status {
	t.Helper()
	var s Status
	if err := json.Unmarshal([]byte(d.execute("status", "")), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func waitState(t *testing.T, d *Daemon, state string, timeout time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		s := daemonStatus(t, d)
		if s.State == state {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("wanted %s, got %+v", state, s)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestLiveControlsPreservePlayerAndPause checks controls do not replace the player.
func TestLiveControlsPreservePlayerAndPause(t *testing.T) {
	withConfigDir(t)
	d := fakeDaemon(t)
	wantReply(t, d.execute("play", "lofi-girl"), true, "loading")
	d.mu.Lock()
	p := d.player.(*fakePlayer)
	d.mu.Unlock()
	if s := daemonStatus(t, d); s.Playing || s.State != "loading" {
		t.Fatalf("startup prematurely reported playing: %+v", s)
	}
	p.event <- playerEvent{loaded: true}
	waitState(t, d, "playing", time.Second)
	wantReply(t, d.execute("pause", ""), true, "paused")
	wantReply(t, d.execute("vol", "32"), true, "32")
	wantReply(t, d.execute("mute", ""), true, "muted")
	wantReply(t, d.execute("mute", ""), true, "unmuted")
	if s := daemonStatus(t, d); !s.Paused || s.Playing || s.Volume != 32 || s.Muted {
		t.Fatalf("controls changed playback state: %+v", s)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.player != p || p.closed {
		t.Fatal("volume/mute restarted the player")
	}
	want := [][]any{
		{"loadfile", findStation("lofi-girl").URL, "replace"},
		{"set_property", "pause", true},
		{"set_property", "volume", 32},
		{"set_property", "mute", true},
		{"set_property", "mute", false},
	}
	if !reflect.DeepEqual(p.commands, want) {
		t.Fatalf("commands = %#v", p.commands)
	}
	p.err = fmt.Errorf("control socket gone")
	wantReply(t, d.resume(), false, "control socket gone")
	if !d.paused {
		t.Fatal("failed resume changed pause state")
	}
}

// advanceReconnect fires a scheduled retry without waiting for real backoff.
func advanceReconnect(t *testing.T, d *Daemon) {
	t.Helper()
	d.mu.Lock()
	if d.retryTimer == nil || !d.retryTimer.Stop() {
		d.mu.Unlock()
		t.Fatal("no pending reconnect")
	}
	generation := d.generation
	d.mu.Unlock()
	d.retryPlayback(generation)
}

// TestProlongedOutageRecoversWithPlaybackSettings checks reconnect backoff and restored controls.
func TestProlongedOutageRecoversWithPlaybackSettings(t *testing.T) {
	withConfigDir(t)
	d := fakeDaemon(t)
	wantReply(t, d.execute("play", "lofi-girl"), true, "loading")
	wantReply(t, d.execute("pause", ""), true, "paused")
	wantReply(t, d.execute("mute", ""), true, "muted")
	wantReply(t, d.execute("sleep", "1h"), true, "")
	sleepUntil := daemonStatus(t, d).SleepUntil
	d.mu.Lock()
	d.playbackFailed("network offline")
	d.newPlayer = func(volume int, muted, paused bool) (player, error) {
		if volume != 32 || !muted || !paused {
			t.Errorf("reconnect lost settings: %d %v %v", volume, muted, paused)
		}
		return nil, fmt.Errorf("network offline")
	}
	d.mu.Unlock()
	// Controls work even while there is no player.
	wantReply(t, d.execute("vol", "32"), true, "32")
	for i := 0; i < 12; i++ {
		s := daemonStatus(t, d)
		if s.State != "reconnecting" || s.Playing || !s.Paused || !s.Muted || s.Error != "network offline" || s.Retries != i+1 || !s.SleepUntil.Equal(sleepUntil) {
			t.Fatalf("outage status: %+v", s)
		}
		delay := time.Until(s.RetryAt)
		if delay <= 0 || delay > maxRetryDelay || i > 5 && delay < 29*time.Second {
			t.Fatalf("unbounded or missing backoff: %s", delay)
		}
		advanceReconnect(t, d)
	}
	d.mu.Lock()
	d.newPlayer = func(volume int, muted, paused bool) (player, error) {
		if volume != 32 || !muted || !paused {
			t.Errorf("recovery lost settings: %d %v %v", volume, muted, paused)
		}
		return &fakePlayer{event: make(chan playerEvent, 8)}, nil
	}
	d.mu.Unlock()
	advanceReconnect(t, d)
	d.mu.Lock()
	p := d.player.(*fakePlayer)
	d.mu.Unlock()
	p.event <- playerEvent{loaded: true}
	s := waitState(t, d, "paused", time.Second)
	if s.Error != "" || !s.RetryAt.IsZero() || s.Volume != 32 || !s.Muted || !s.SleepUntil.Equal(sleepUntil) {
		t.Fatalf("recovery status: %+v", s)
	}
	wantReply(t, d.execute("resume", ""), true, "resumed")
	waitState(t, d, "playing", time.Second)
	// A stable minute (including time spent asleep) restores the fast retry.
	d.mu.Lock()
	d.startedAt = time.Now().Add(-time.Minute)
	d.playbackFailed("disconnected after wake")
	d.mu.Unlock()
	if s := daemonStatus(t, d); s.Retries != 1 || time.Until(s.RetryAt) > time.Second {
		t.Fatalf("backoff did not reset after stable playback: %+v", s)
	}
}

// TestStopCancelsReconnectAndStaleEvents checks stopped playback cannot restart itself.
func TestStopCancelsReconnectAndStaleEvents(t *testing.T) {
	d := fakeDaemon(t)
	wantReply(t, d.execute("play", "lofi-girl"), true, "loading")
	d.mu.Lock()
	p := d.player.(*fakePlayer)
	d.playbackFailed("disconnected")
	if d.retryTimer == nil {
		t.Fatal("retry was not scheduled")
	}
	generation := d.generation
	d.mu.Unlock()
	wantReply(t, d.execute("stop", ""), true, "stopped")
	d.retryPlayback(generation) // a timer callback already queued before stop
	p.event <- playerEvent{loaded: true}
	time.Sleep(1100 * time.Millisecond)
	if s := daemonStatus(t, d); s.State != "idle" || s.Station != "" || s.Error != "" {
		t.Fatalf("stale retry/event revived playback: %+v", s)
	}
}

// TestSwitchStationAndSleepExpiryCancelReconnect checks replacement and timer cancellation.
func TestSwitchStationAndSleepExpiryCancelReconnect(t *testing.T) {
	for _, action := range []string{"switch", "sleep"} {
		t.Run(action, func(t *testing.T) {
			d := fakeDaemon(t)
			wantReply(t, d.execute("play", "lofi-girl"), true, "loading")
			d.mu.Lock()
			old := d.player.(*fakePlayer)
			d.playbackFailed("offline")
			generation := d.generation
			d.mu.Unlock()
			want := "loading"
			if action == "switch" {
				wantReply(t, d.execute("play", "chillhop"), true, "loading")
			} else {
				wantReply(t, d.execute("sleep", "1ms"), true, "")
				want = "idle"
				waitState(t, d, want, time.Second)
			}
			d.retryPlayback(generation)
			old.event <- playerEvent{loaded: true}
			s := daemonStatus(t, d)
			if s.State != want || s.Retries != 0 || !s.RetryAt.IsZero() || action == "switch" && s.Station != "chillhop" {
				t.Fatalf("stale reconnect changed playback: %+v", s)
			}
		})
	}
}

// TestSleepTimerReplaceCancelAndExpire checks sleep timer ownership and deadlines.
func TestSleepTimerReplaceCancelAndExpire(t *testing.T) {
	d := fakeDaemon(t)
	wantReply(t, d.execute("sleep", "-1m"), false, "positive duration")
	wantReply(t, d.execute("sleep", "45m"), false, "nothing playing")
	wantReply(t, d.execute("play", "lofi-girl"), true, "")
	wantReply(t, d.execute("sleep", "20ms"), true, "")
	wantReply(t, d.execute("sleep", "1h"), true, "")
	time.Sleep(40 * time.Millisecond)
	if s := daemonStatus(t, d); s.Station == "" || s.Sleep == "" {
		t.Fatalf("old timer wasn't replaced: %+v", s)
	}
	wantReply(t, d.execute("sleep", "off"), true, "off")
	if s := daemonStatus(t, d); s.Sleep != "" {
		t.Fatal("timer wasn't cancelled")
	}
	wantReply(t, d.execute("sleep", "50ms"), true, "")
	// Switching stations must retain the sleep deadline.
	wantReply(t, d.execute("play", "chillhop"), true, "")
	waitState(t, d, "idle", time.Second)
	if s := daemonStatus(t, d); s.Sleep != "" || s.Station != "" {
		t.Fatalf("timer didn't clear playback: %+v", s)
	}
	// The daemon remains usable after a timer expires.
	wantReply(t, d.execute("toggle", ""), true, "loading")
}

// TestStatusDisplaysLoadingFailureMuteAndSleep checks visible playback state details.
func TestStatusDisplaysLoadingFailureMuteAndSleep(t *testing.T) {
	out := strings.Join(statusFacts(&Status{State: "reconnecting", Station: "lofi-girl", Error: "unavailable", Retries: 2, RetryAt: time.Now().Add(2 * time.Second), Paused: true, Muted: true, Sleep: "45m0s"}), " ")
	for _, want := range []string{"reconnecting", "retry 2 in 2s", "paused", "unavailable", "muted", "sleep 45m0s"} {
		if !strings.Contains(out, want) {
			t.Errorf("status %q missing %q", out, want)
		}
	}
}
