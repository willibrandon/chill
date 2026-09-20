package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// wantReply decodes a daemon response and checks the tag.
func wantReply(t *testing.T, raw string, ok bool, contains string) {
	t.Helper()
	var r reply
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("response %q is not JSON: %v", raw, err)
	}
	if r.OK != ok {
		t.Errorf("reply ok = %v, want %v (msg %q)", r.OK, ok, r.Msg)
	}
	if contains != "" && !strings.Contains(r.Msg, contains) {
		t.Errorf("reply msg = %q, want it to contain %q", r.Msg, contains)
	}
}

func TestVolumeCommand(t *testing.T) {
	withConfigDir(t)
	d := &Daemon{volume: defaultVolume}

	tests := []struct {
		arg  string
		want int
		ok   bool
	}{
		{"", defaultVolume, true}, // report only
		{"up", defaultVolume + 5, true},
		{"down", defaultVolume, true},
		{"+10", defaultVolume + 10, true},
		{"-20", defaultVolume - 10, true},
		{"60", 60, true},
		{"0", 0, true},
		{"999", 100, true}, // clamped
		{"-999", 0, true},  // clamped
		{"loud", 0, false}, // not a number
		{"", 0, true},      // still 0 afterwards
	}
	for _, tt := range tests {
		raw := d.volumeCmd(tt.arg)
		if tt.ok {
			wantReply(t, raw, true, "")
		} else {
			wantReply(t, raw, false, "bad volume")
			continue
		}
		if d.volume != tt.want {
			t.Errorf("vol %q left volume %d, want %d", tt.arg, d.volume, tt.want)
		}
	}
}

func TestMuteRoundTrip(t *testing.T) {
	d := &Daemon{volume: 55}

	wantReply(t, d.mute(), true, "muted")
	if d.volume != 55 || !d.muted {
		t.Errorf("mute: volume %d, muted %v", d.volume, d.muted)
	}

	wantReply(t, d.mute(), true, "")
	if d.volume != 55 || d.muted {
		t.Errorf("unmute: volume %d, muted %v", d.volume, d.muted)
	}
}

func TestPauseWithoutPlayback(t *testing.T) {
	d := &Daemon{}
	wantReply(t, d.pause(), false, "nothing playing")
	wantReply(t, d.resume(), false, "nothing playing")
}

func TestUnknownStation(t *testing.T) {
	d := &Daemon{volume: defaultVolume}
	wantReply(t, d.play("bogus"), false, "unknown station")
}

func TestUnknownCommand(t *testing.T) {
	d := &Daemon{}
	wantReply(t, d.execute("bogus", ""), false, "unknown command")
}

func TestStatusJSON(t *testing.T) {
	d := &Daemon{volume: 42}
	var s Status
	if err := json.Unmarshal([]byte(d.status()), &s); err != nil {
		t.Fatalf("status: %v", err)
	}
	if s.Volume != 42 {
		t.Errorf("volume = %d, want 42", s.Volume)
	}
	if s.Playing || s.Paused || s.Station != "" {
		t.Errorf("status = %+v, want idle", s)
	}
}
