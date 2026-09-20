package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestDaemonUpgradeCompatibility(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  Status
		client  string
		upgrade bool
		fail    bool
	}{
		{"legacy daemon", Status{}, "v0.4.1", true, false},
		{"same release", Status{Protocol: daemonProtocol, Version: "v0.4.1"}, "v0.4.1", false, false},
		{"older release", Status{Protocol: daemonProtocol, Version: "v0.4.1"}, "v0.4.2", true, false},
		{"newer daemon", Status{Protocol: daemonProtocol, Version: "v0.5.0"}, "v0.4.1", false, false},
		{"development build", Status{Protocol: daemonProtocol, Version: "dev"}, "dev", false, false},
		{"release replaces dev", Status{Protocol: daemonProtocol, Version: "dev"}, "v0.4.1", true, false},
		{"newer protocol", Status{Protocol: daemonProtocol + 1, Version: "v0.5.0"}, "v0.4.1", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upgrade, err := daemonNeedsUpgrade(tt.status, tt.client)
			if upgrade != tt.upgrade || (err != nil) != tt.fail {
				t.Fatalf("upgrade=%v error=%v", upgrade, err)
			}
		})
	}
}

func TestLegacyPlaybackSnapshot(t *testing.T) {
	now := time.Now()
	snapshot, err := snapshotPlayback(Status{Station: "lofi-girl", Playing: true, Volume: 90}, now)
	if err != nil || snapshot.Station == nil || snapshot.Station.URL != findStation("lofi-girl").URL || snapshot.Volume != 90 {
		t.Fatalf("v0.3 snapshot: %+v, %v", snapshot, err)
	}
	snapshot, err = snapshotPlayback(Status{Station: "sleep", Paused: true, Volume: 31, Muted: true, Sleep: "45m0s"}, now)
	if err != nil || !snapshot.Paused || !snapshot.Muted || !snapshot.SleepUntil.Equal(now.Add(45*time.Minute)) {
		t.Fatalf("v0.4 snapshot: %+v, %v", snapshot, err)
	}
	if _, err := snapshotPlayback(Status{Station: "deleted-custom", Playing: true}, now); err == nil {
		t.Fatal("must not stop the old player when its station cannot be restored")
	}
	snapshot, err = snapshotPlayback(Status{Station: "deleted-custom", URL: "https://example.com", State: "loading", SleepUntil: now.Add(time.Minute)}, now)
	if err != nil || snapshot.Station == nil || snapshot.Station.URL != "https://example.com" || !snapshot.SleepUntil.Equal(now.Add(time.Minute)) {
		t.Fatalf("snapshot with removed station URL: %+v, %v", snapshot, err)
	}
	for _, paused := range []bool{false, true} {
		snapshot, err = snapshotPlayback(Status{Station: "sleep", State: "reconnecting", Paused: paused, Volume: 32, Muted: true}, now)
		if err != nil || snapshot.Station == nil || snapshot.Paused != paused || snapshot.Volume != 32 || !snapshot.Muted {
			t.Fatalf("reconnecting snapshot: %+v, %v", snapshot, err)
		}
	}
}

func TestRestoreKeepsPauseMuteAndSleepDeadline(t *testing.T) {
	d := fakeDaemon(t)
	starts := 0
	d.newPlayer = func(volume int, muted, paused bool) (player, error) {
		starts++
		if volume != 31 || !muted || !paused {
			t.Errorf("mpv started with volume=%d muted=%v paused=%v", volume, muted, paused)
		}
		return &fakePlayer{event: make(chan playerEvent, 8)}, nil
	}
	deadline := time.Now().Add(time.Hour)
	snapshot := playbackSnapshot{Station: findStation("sleep"), Volume: 31, Muted: true, Paused: true, SleepUntil: deadline}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	wantReply(t, d.execute("restore", string(data)), true, "restored")
	d.mu.Lock()
	defer d.mu.Unlock()
	if starts != 1 || !d.paused || !d.muted || d.station.Name != "sleep" || d.sleepUntil.Sub(deadline).Abs() > 10*time.Millisecond {
		t.Fatalf("playback state not preserved: starts=%d paused=%v muted=%v station=%v sleep=%v", starts, d.paused, d.muted, d.station, d.sleepUntil)
	}
}

func TestRestoreDoesNotRestartExpiredSleepTimer(t *testing.T) {
	d := fakeDaemon(t)
	d.newPlayer = func(int, bool, bool) (player, error) {
		t.Fatal("expired timer must not restart music")
		return nil, nil
	}
	data, err := json.Marshal(playbackSnapshot{Station: findStation("sleep"), Volume: 31, SleepUntil: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	wantReply(t, d.execute("restore", string(data)), true, "idle")
	if s := daemonStatus(t, d); s.State != "idle" || s.Volume != 31 || s.Sleep != "" {
		t.Fatalf("expired snapshot: %+v", s)
	}
}

func TestDaemonLockReleasedOnClose(t *testing.T) {
	withConfigDir(t)
	unlock, err := lockDaemon()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	other, err := os.OpenFile(socketPath()+".lock", os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if locked, err := tryDaemonLock(other); locked || err != nil {
		t.Fatalf("second client acquired upgrade lock: locked=%v err=%v", locked, err)
	}
	unlock()
	if locked, err := tryDaemonLock(other); !locked || err != nil {
		t.Fatalf("upgrade lock not released: locked=%v err=%v", locked, err)
	}
}
