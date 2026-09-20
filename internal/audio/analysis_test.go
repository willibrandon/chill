package audio

import (
	"encoding/binary"
	"math"
	"sync"
	"testing"
)

func TestStereoTone(t *testing.T) {
	var samples [2][WindowSize]float64
	// Bin-centered tone makes peak amplitude and frequency independently known.
	frequency := 48.0 * SampleRate / WindowSize
	for i := range WindowSize {
		samples[0][i] = 0.8 * math.Sin(2*math.Pi*48*float64(i)/WindowSize)
		samples[1][i] = -samples[0][i] / 4
	}
	f := Analyze(samples)
	for ch, amplitude := range []float64{0.8, 0.2} {
		if math.Abs(f.Peak[ch]-amplitude) > 0.001 || math.Abs(f.RMS[ch]-amplitude/math.Sqrt2) > 0.001 {
			t.Fatalf("channel %d peak/RMS = %f/%f", ch, f.Peak[ch], f.RMS[ch])
		}
	}
	loudest := 0
	for i, v := range f.Spectrum {
		if v > f.Spectrum[loudest] {
			loudest = i
		}
	}
	lo := 30 * math.Pow(20000.0/30, float64(loudest)/Bands)
	hi := 30 * math.Pow(20000.0/30, float64(loudest+1)/Bands)
	if frequency < lo || frequency > hi {
		t.Fatalf("tone %f Hz appeared in %f–%f Hz", frequency, lo, hi)
	}
	if f.Spectrum[loudest] < 0.5 {
		t.Fatal("stereo spectrum cancelled opposing channel phase")
	}
	if f.Wave[0][10] != samples[0][WindowSize-WaveSize+10] {
		t.Fatal("scope lost contiguous samples")
	}
}

func TestSilenceAndOppositePhase(t *testing.T) {
	if f := Analyze([2][WindowSize]float64{}); f != (Frame{}) {
		t.Fatal("silence produced energy")
	}
	var samples [2][WindowSize]float64
	for i := range WindowSize {
		samples[0][i] = math.Sin(2 * math.Pi * 32 * float64(i) / WindowSize)
		samples[1][i] = -samples[0][i]
	}
	f := Analyze(samples)
	peak := 0.0
	for _, v := range f.Spectrum {
		peak = max(peak, v)
	}
	if peak < 0.95 {
		t.Fatalf("opposite-phase stereo lost spectrum: %f", peak)
	}
}

func TestBufferWrapSanitizationAndConcurrentSnapshots(t *testing.T) {
	var b Buffer
	pcm := make([]byte, WindowSize*3*8)
	for i := 0; i < len(pcm); i += 8 {
		binary.LittleEndian.PutUint32(pcm[i:], math.Float32bits(0.5))
		binary.LittleEndian.PutUint32(pcm[i+4:], math.Float32bits(float32(math.NaN())))
	}
	b.Push(pcm)
	f := b.Snapshot()
	if f.Peak != [2]float64{0.5, 0} || f.Sequence != 1 || f.At.IsZero() {
		t.Fatalf("bad buffer snapshot: %+v", f)
	}
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			for range 50 {
				if i == 0 {
					b.Push(pcm[:512*8])
				} else {
					b.Snapshot()
				}
			}
		})
	}
	wg.Wait()
}
