package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakePlayer struct {
	commands [][]any
	event    chan playerEvent
	closed   bool
	err      error
}

func (p *fakePlayer) command(args ...any) error {
	p.commands = append(p.commands, args)
	return p.err
}
func (p *fakePlayer) events() <-chan playerEvent { return p.event }
func (p *fakePlayer) close()                     { p.closed = true }

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

func TestReconnectBudgetAndFailureStatus(t *testing.T) {
	d := fakeDaemon(t)
	starts := 0
	d.newPlayer = func(int, bool, bool) (player, error) {
		starts++ // called only under the daemon lock
		p := &fakePlayer{event: make(chan playerEvent, 8)}
		p.event <- playerEvent{err: "stream unavailable"}
		return p, nil
	}
	wantReply(t, d.execute("play", "lofi-girl"), true, "loading")
	s := waitState(t, d, "failed", 10*time.Second)
	if s.Playing || s.Paused || s.Error != "stream unavailable" || s.Retries != maxRetries {
		t.Fatalf("failure status: %+v", s)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if starts != 1+maxRetries || d.player != nil {
		t.Fatalf("starts=%d player=%v", starts, d.player)
	}
}

func TestStopCancelsReconnectAndStaleEvents(t *testing.T) {
	d := fakeDaemon(t)
	wantReply(t, d.execute("play", "lofi-girl"), true, "loading")
	d.mu.Lock()
	p := d.player.(*fakePlayer)
	d.playbackFailed("disconnected")
	if d.retryTimer == nil {
		t.Fatal("retry was not scheduled")
	}
	d.mu.Unlock()
	wantReply(t, d.execute("stop", ""), true, "stopped")
	p.event <- playerEvent{loaded: true}
	time.Sleep(1100 * time.Millisecond)
	if s := daemonStatus(t, d); s.State != "idle" || s.Station != "" || s.Error != "" {
		t.Fatalf("stale retry/event revived playback: %+v", s)
	}
}

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

func TestStatusDisplaysLoadingFailureMuteAndSleep(t *testing.T) {
	out := strings.Join(statusFacts(&Status{State: "loading", Station: "lofi-girl", Error: "unavailable", Retries: 2, Muted: true, Sleep: "45m0s"}), " ")
	for _, want := range []string{"loading", "retry 2/3", "unavailable", "muted", "sleep 45m0s"} {
		if !strings.Contains(out, want) {
			t.Errorf("status %q missing %q", out, want)
		}
	}
}
