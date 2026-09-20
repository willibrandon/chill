package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"sync"
	"testing"
)

func equalizerSine(frames int, frequency, leftAmplitude, rightAmplitude float64) []byte {
	pcm := make([]byte, frames*8)
	for i := range frames {
		phase := 2 * math.Pi * frequency * float64(i) / SampleRate
		left := float32(leftAmplitude * math.Sin(phase))
		right := float32(rightAmplitude * math.Sin(phase))
		binary.LittleEndian.PutUint32(pcm[i*8:], math.Float32bits(left))
		binary.LittleEndian.PutUint32(pcm[i*8+4:], math.Float32bits(right))
	}
	return pcm
}

func equalizerRMS(pcm []byte, channel, skipFrames int) float64 {
	var sum float64
	frames := len(pcm) / 8
	for i := skipFrames; i < frames; i++ {
		x := float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[i*8+channel*4:])))
		sum += x * x
	}
	return math.Sqrt(sum / float64(frames-skipFrames))
}

// TestEqualizerFlatIsBitExact checks bypass preserves every PCM bit.
func TestEqualizerFlatIsBitExact(t *testing.T) {
	pcm := equalizerSine(4096, 997, 0.73, 0.29)
	want := append([]byte(nil), pcm...)
	NewEqualizer(SampleRate).Process(pcm)
	if !bytes.Equal(pcm, want) {
		t.Fatal("flat equalizer changed PCM bytes")
	}
}

// TestEqualizerCenterFrequencyGain checks peaking filters reach their requested gain.
func TestEqualizerCenterFrequencyGain(t *testing.T) {
	for _, gain := range []float64{-12, 12} {
		t.Run(map[float64]string{-12: "cut", 12: "boost"}[gain], func(t *testing.T) {
			pcm := equalizerSine(SampleRate, 1000, 0.05, 0.05)
			eq := NewEqualizer(SampleRate)
			var bands EqualizerBands
			bands[4] = gain
			eq.SetBands(bands)
			eq.Process(pcm)
			got := equalizerRMS(pcm, 0, SampleRate/2)
			want := 0.05 / math.Sqrt2 * math.Pow(10, gain/20)
			if math.Abs(got-want) > want*0.03 {
				t.Fatalf("RMS at center = %.6f, want %.6f", got, want)
			}
		})
	}
}

// TestEqualizerStereoIsolation checks filter state is independent per channel.
func TestEqualizerStereoIsolation(t *testing.T) {
	pcm := equalizerSine(8192, 3000, 0.1, 0)
	eq := NewEqualizer(SampleRate)
	var bands EqualizerBands
	bands[5] = 12
	eq.SetBands(bands)
	eq.Process(pcm)
	if got := equalizerRMS(pcm, 1, 0); got != 0 {
		t.Fatalf("silent right channel leaked: RMS %.9f", got)
	}
}

// TestEqualizerChunkBoundariesDoNotChangeOutput checks state spans decoder reads.
func TestEqualizerChunkBoundariesDoNotChangeOutput(t *testing.T) {
	original := equalizerSine(10000, 600, 0.04, 0.08)
	whole, chunked := append([]byte(nil), original...), append([]byte(nil), original...)
	var bands EqualizerBands
	bands[3], bands[7] = 7, -5
	one, two := NewEqualizer(SampleRate), NewEqualizer(SampleRate)
	one.SetBands(bands)
	two.SetBands(bands)
	one.Process(whole)
	for offset := 0; offset < len(chunked); {
		size := min(len(chunked)-offset, 137*8)
		two.Process(chunked[offset : offset+size])
		offset += size
	}
	if !bytes.Equal(whole, chunked) {
		t.Fatal("PCM output depends on decoder block boundaries")
	}
}

// TestEqualizerLiveChangesStayFinite checks smoothed coefficient transitions are safe.
func TestEqualizerLiveChangesStayFinite(t *testing.T) {
	eq := NewEqualizer(SampleRate)
	pcm := equalizerSine(4096, 70, 0.1, 0.1)
	eq.Process(pcm[:1024*8])
	var bands EqualizerBands
	bands[0] = 12
	eq.SetBands(bands)
	eq.Process(pcm[1024*8 : 3072*8])
	bands[0] = 0
	eq.SetBands(bands)
	eq.Process(pcm[3072*8:])
	for offset := 0; offset < len(pcm); offset += 4 {
		x := float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[offset:])))
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) > 1 {
			t.Fatalf("invalid live-transition sample at byte %d: %v", offset, x)
		}
	}
}

// TestEqualizerConcurrentControl checks SetBands can race the playback loop safely.
func TestEqualizerConcurrentControl(t *testing.T) {
	eq := NewEqualizer(SampleRate)
	pcm := equalizerSine(512, 1000, 0.01, 0.01)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 1000 {
			var bands EqualizerBands
			bands[i%EqualizerBandCount] = float64(i%25 - 12)
			eq.SetBands(bands)
		}
	}()
	for range 1000 {
		block := append([]byte(nil), pcm...)
		eq.Process(block)
	}
	wg.Wait()
}

// TestClampEqualizerGain checks public gain sanitation.
func TestClampEqualizerGain(t *testing.T) {
	for input, want := range map[float64]float64{-20: -12, -3: -3, 20: 12, math.Inf(1): 0} {
		if got := ClampEqualizerGain(input); got != want {
			t.Errorf("ClampEqualizerGain(%v) = %v, want %v", input, got, want)
		}
	}
	if got := ClampEqualizerGain(math.NaN()); got != 0 {
		t.Errorf("ClampEqualizerGain(NaN) = %v, want 0", got)
	}
}

// BenchmarkEqualizerProcess measures a ten-band stereo playback block.
func BenchmarkEqualizerProcess(b *testing.B) {
	pcm := equalizerSine(512, 1000, 0.05, 0.05)
	eq := NewEqualizer(SampleRate)
	var bands EqualizerBands
	for i := range bands {
		bands[i] = float64(i%7 - 3)
	}
	eq.SetBands(bands)
	b.ResetTimer()
	for range b.N {
		eq.Process(pcm)
	}
}
