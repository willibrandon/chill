//go:build darwin && cgo && audio_integration

package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// This executes the built application's real daemon, decoder, network stream,
// effects, and physical output. A separate CoreAudio process tap must measure
// continuous audio. No device, player, decoder, or station response is replaced.
func TestMacOSStationPlayback(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "chill")
	build := exec.CommandContext(t.Context(), "go", "build", "-race", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	check := exec.CommandContext(t.Context(), "python3", "scripts/check_macos_playback.py", "--binary", binary)
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("real playback failed: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
