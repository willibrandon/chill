package main

import (
	"context"
	"encoding/json/v2"
	"slices"
	"strings"
	"testing"
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
	rate, mpv, ffmpeg := audioSettingsArgs(custom)
	if rate != 192000 || !slices.Contains(mpv, "--demuxer-rawaudio-rate=192000") || !strings.Contains(strings.Join(ffmpeg, " "), "pan=stereo") {
		t.Fatalf("audio args = %d %#v %#v", rate, mpv, ffmpeg)
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
