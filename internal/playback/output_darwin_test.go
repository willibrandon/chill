//go:build darwin && cgo && audio_integration

package playback

import (
	"context"
	"encoding/binary"
	"encoding/json/v2"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestMacOSOutputContainsAudio measures the production device through a separate
// CoreAudio process tap. An application callback alone cannot pass this check.
func TestMacOSOutputContainsAudio(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "output-meter")
	cmd := exec.CommandContext(t.Context(), "clang", "-fobjc-arc", "-framework", "Foundation", "-framework", "CoreAudio", "testdata/output_meter.m", "-o", probe)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build OS output meter: %v\n%s", err, data)
	}
	output, err := NewOutput(Settings{Device: "auto", SampleRate: 48000, BufferMS: 100}, 70, false, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var data [512 * 8]byte
		var frame uint64
		for {
			for i := range 512 {
				sample := float32(.15 * math.Sin(2*math.Pi*440*float64(frame)/48000))
				binary.LittleEndian.PutUint32(data[i*8:], math.Float32bits(sample))
				binary.LittleEndian.PutUint32(data[i*8+4:], math.Float32bits(sample))
				frame++
			}
			if output.Write(ctx, data[:], 1, 0, float64(time.Second)/48000) != nil {
				return
			}
		}
	}()
	defer func() { cancel(); output.Close(); <-done }()
	time.Sleep(200 * time.Millisecond)
	data, err := exec.CommandContext(t.Context(), probe, strconv.Itoa(os.Getpid())).CombinedOutput()
	if err != nil {
		t.Fatalf("cannot verify macOS output: %v\n%s", err, data)
	}
	var result struct {
		Samples, Nonzero uint64
		Peak, RMS        float64
		Error            int
	}
	if err := json.Unmarshal(data, &result, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatalf("read output measurement: %v\n%s", err, data)
	}
	ratio := float64(result.Nonzero) / float64(result.Samples)
	t.Logf("CoreAudio measured %d/%d nonzero samples (%.2f%%), peak %.5f, RMS %.5f", result.Nonzero, result.Samples, 100*ratio, result.Peak, result.RMS)
	if result.Error != 0 || result.Samples < 48000 || ratio < .90 || result.RMS < .005 {
		t.Fatalf("native output lost the test audio: %.2f%% nonzero, RMS %.5f; require at least 90%% and RMS 0.005", 100*ratio, result.RMS)
	}
}
