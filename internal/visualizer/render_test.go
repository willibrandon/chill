package visualizer

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/audio"
)

func TestModesFitAndRenderWithoutMutation(t *testing.T) {
	var r Renderer
	var f audio.Frame
	f.Peak, f.RMS = [2]float64{0.9, 0.3}, [2]float64{0.6, 0.2}
	for i := range f.Spectrum {
		f.Spectrum[i] = float64(i+1) / audio.Bands
	}
	for i := range audio.WaveSize {
		f.Wave[0][i] = math.Sin(float64(i) / 12)
		f.Wave[1][i] = math.Cos(float64(i)/9) / 2
	}
	for i := range 120 {
		r.Advance(f, time.Unix(0, int64(i)*int64(time.Second/30)))
	}
	seen := map[string]bool{}
	for _, mode := range Modes {
		if seen[mode] {
			t.Fatal("duplicate mode", mode)
		}
		seen[mode] = true
		for _, size := range [][2]int{{0, 0}, {1, 1}, {8, 2}, {40, 8}, {120, 30}, {240, 60}} {
			before := r
			out := r.Render(mode, size[0], size[1])
			if r != before {
				t.Fatal("render mutated animation", mode)
			}
			if size[0] == 0 {
				if out != "" {
					t.Fatal("nonempty zero canvas")
				}
				continue
			}
			lines := strings.Split(out, "\n")
			if len(lines) != size[1] {
				t.Fatalf("%s: height %d, want %d", mode, len(lines), size[1])
			}
			for _, line := range lines {
				if ansi.StringWidth(line) != size[0] {
					t.Fatalf("%s: width %d, want %d", mode, ansi.StringWidth(line), size[0])
				}
			}
		}
	}
	if len(seen) != 31 {
		t.Fatalf("mode count = %d", len(seen))
	}
}

func TestSilenceDecayAndIndependentPeakHold(t *testing.T) {
	var r Renderer
	now := time.Now()
	r.Advance(audio.Frame{Peak: [2]float64{1, 0.1}}, now)
	if r.hold[0] <= r.hold[1] {
		t.Fatal("meter channels are not independent")
	}
	r.Advance(audio.Frame{}, now.Add(time.Second/2))
	if r.hold[0] != 1 {
		t.Fatal("peak hold expired early")
	}
	for i := range 200 {
		r.Advance(audio.Frame{}, now.Add(time.Duration(i+1)*time.Second/30))
	}
	if r.hold != [2]float64{} {
		t.Fatal("peak hold did not release")
	}
	for _, mode := range []string{"matrix", "flame", "particles", "spectrum", "pulse"} {
		if strings.TrimSpace(ansi.Strip(r.Render(mode, 60, 10))) != "" {
			t.Fatal("silence fabricated animation in", mode)
		}
	}
}
