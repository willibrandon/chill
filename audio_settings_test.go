package main

import (
	"context"
	"encoding/json/v2"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAudioProfilesAndCustomSettings checks named profiles and advanced overrides.
func TestAudioProfilesAndCustomSettings(t *testing.T) {
	settings := defaultAudioSettings()
	lossless, err := updateAudioSettings(settings, "profile Lossless")
	if err != nil || lossless.Profile != audioProfileLossless || lossless.SampleRate != 96000 || lossless.ResampleQuality != 4 {
		t.Fatalf("lossless = %+v, %v", lossless, err)
	}
	custom, err := updateAudioSettings(lossless, "sample-rate 192000")
	if err != nil || custom.Profile != audioProfileCustom || custom.SampleRate != 192000 {
		t.Fatalf("custom = %+v, %v", custom, err)
	}
	custom, err = updateAudioSettings(custom, "mono on")
	if err != nil || !custom.Mono || custom.Channels != "mono" {
		t.Fatalf("mono = %+v, %v", custom, err)
	}
	output := outputSettings(custom)
	if output.SampleRate != 192000 || output.BufferMS != custom.BufferMS || output.Device != custom.Device {
		t.Fatalf("output settings = %+v", output)
	}
}

// TestAudioPersistenceFailureRestoresActiveFormat keeps saved and active settings consistent.
func TestAudioPersistenceFailureRestoresActiveFormat(t *testing.T) {
	withConfigDir(t)
	previous := defaultAudioSettings()
	d := &Daemon{library: emptyLibrary(), audio: previous, activeAudioDevice: previous.Device}
	t.Cleanup(d.kill)
	var started []AudioSettings
	d.newAudioPlayer = func(_ int, _, _ bool, _ time.Duration, _ bool, settings AudioSettings) (player, error) {
		started = append(started, settings)
		return &fakePlayer{event: make(chan playerEvent, 2)}, nil
	}
	if result := d.playMediaItem(testTrack(t.TempDir()+"/song.flac", "Song"), false, false); !commandSucceeded(result) {
		t.Fatal(result)
	}
	if err := os.MkdirAll(volumePath(), 0700); err != nil {
		t.Fatal(err)
	}
	next, err := updateAudioSettings(previous, "sample-rate 96000")
	if err != nil {
		t.Fatal(err)
	}
	if result := d.applyAudioSettings(next); commandSucceeded(result) || !strings.Contains(result, "unchanged") {
		t.Fatal(result)
	}
	if len(started) != 3 || started[1] != next || started[2] != previous || d.audio != previous {
		t.Fatalf("rollback: %+v, settings %+v", started, d.audio)
	}
}

// TestAudioCommandAcceptsTrailingJSON checks command-local machine output.
func TestAudioCommandAcceptsTrailingJSON(t *testing.T) {
	withConfigDir(t)
	output, err := runAudioCommand(context.Background(), []string{"profile", "Lossless", "--json"}, false)
	if err != nil {
		t.Fatal(err)
	}
	var status AudioStatus
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		t.Fatal(err)
	}
	if status.Profile != audioProfileLossless || status.ActiveSampleRate != 96000 {
		t.Fatalf("status = %+v", status)
	}
	if _, err := runAudioCommand(context.Background(), []string{"list", "extra"}, false); err == nil {
		t.Fatal("audio list accepted an extra argument")
	}
}

// TestAudioSettingsRejectInvalidValues checks the supported format boundaries.
func TestAudioSettingsRejectInvalidValues(t *testing.T) {
	settings := defaultAudioSettings()
	for _, command := range []string{"profile Fast", "sample-rate 12345", "buffer 10", "resample-quality 8", "channels surround", "exclusive maybe"} {
		if _, err := updateAudioSettings(settings, command); err == nil {
			t.Errorf("accepted %q", command)
		}
	}
}
