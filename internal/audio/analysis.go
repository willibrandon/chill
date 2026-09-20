// Package audio analyzes interleaved stereo PCM independently of playback and UI.
package audio

import (
	"encoding/binary"
	"math"
	"math/cmplx"
	"sync"
	"time"
)

const (
	// SampleRate is the stereo PCM sampling frequency in Hz.
	SampleRate = 48000
	// WindowSize is the sample count per channel used for the FFT.
	WindowSize = 2048
	// Bands is the number of logarithmic spectrum bands.
	Bands = 64
	// WaveSize is the contiguous sample count in each waveform trace.
	WaveSize = 256
)

// Frame is an immutable, bounded snapshot. Levels are linear full-scale values;
// spectrum bands are logarithmically spaced from 30 Hz to 20 kHz. The tap is
// pre-volume: these are source levels, not an estimate of speaker loudness.
type Frame struct {
	// Sequence identifies the latest PCM block in this snapshot.
	Sequence uint64 `json:"sequence"`
	// At records when the latest PCM block arrived.
	At time.Time `json:"at"`
	// Spectrum holds linear full-scale amplitudes for frequency bands.
	Spectrum [Bands]float64 `json:"spectrum"`
	// Wave holds left and right waveform samples.
	Wave [2][WaveSize]float64 `json:"wave"`
	// Peak holds independent left and right sample-peak amplitudes.
	Peak [2]float64 `json:"peak"`
	// RMS holds independent left and right root-mean-square amplitudes.
	RMS [2]float64 `json:"rms"`
}

// Buffer retains only the latest window. Push does no FFT, I/O, or allocation;
// a slow subscriber cannot accumulate audio or block the playback pipe on FFT.
type Buffer struct {
	analysisMu sync.Mutex // serializes subscribers, never acquired by Push
	cached     Frame
	analyzedAt time.Time
	mu         sync.Mutex
	samples    [2][WindowSize]float64
	pos, count int
	sequence   uint64
	at         time.Time
}

// Push accepts complete stereo f32le frames. The decoder pump handles framing.
func (b *Buffer) Push(pcm []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(pcm) >= 8 {
		for ch := range 2 {
			x := float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[ch*4:])))
			if math.IsNaN(x) || math.IsInf(x, 0) {
				x = 0
			}
			b.samples[ch][b.pos] = x
		}
		b.pos = (b.pos + 1) % WindowSize
		b.count = min(b.count+1, WindowSize)
		pcm = pcm[8:]
	}
	b.sequence++
	b.at = time.Now()
}

// Snapshot copies under a short lock and computes outside the playback path.
func (b *Buffer) Snapshot() Frame {
	b.analysisMu.Lock()
	defer b.analysisMu.Unlock()
	if time.Since(b.analyzedAt) < time.Second/30 {
		return b.cached
	}
	b.mu.Lock()
	samples, pos, count, sequence, at := b.samples, b.pos, b.count, b.sequence, b.at
	b.mu.Unlock()
	var ordered [2][WindowSize]float64
	for ch := range 2 {
		for i := range count {
			ordered[ch][WindowSize-count+i] = samples[ch][(pos-count+i+WindowSize)%WindowSize]
		}
	}
	f := Analyze(ordered)
	f.Sequence, f.At = sequence, at
	b.cached, b.analyzedAt = f, time.Now()
	return f
}

// Analyze uses a Hann window and combines stereo power after the FFT, so
// opposite-phase channels cannot cancel each other out in the spectrum.
func Analyze(samples [2][WindowSize]float64) Frame {
	var f Frame
	var power [WindowSize/2 + 1]float64
	for ch := range 2 {
		var bins [WindowSize]complex128
		for i, x := range samples[ch] {
			f.Peak[ch] = max(f.Peak[ch], math.Abs(x))
			f.RMS[ch] += x * x
			window := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(WindowSize-1))
			bins[i] = complex(x*window, 0)
		}
		f.RMS[ch] = math.Sqrt(f.RMS[ch] / WindowSize)
		fft(bins[:])
		for i := range power {
			a := cmplx.Abs(bins[i]) * 4 / WindowSize
			power[i] += a * a / 2
		}
		// A contiguous 5.3 ms trace preserves phase and waveform shape.
		copy(f.Wave[ch][:], samples[ch][WindowSize-WaveSize:])
	}
	for band := range Bands {
		lo := 30 * math.Pow(20000.0/30, float64(band)/Bands)
		hi := 30 * math.Pow(20000.0/30, float64(band+1)/Bands)
		first := max(1, int(math.Round(lo*WindowSize/SampleRate)))
		last := min(len(power)-1, max(first, int(math.Round(hi*WindowSize/SampleRate))-1))
		for i := first; i <= last; i++ {
			f.Spectrum[band] = max(f.Spectrum[band], math.Sqrt(power[i]))
		}
	}
	return f
}

// fft is an in-place radix-2 transform of our fixed power-of-two window.
func fft(a []complex128) {
	for i, j := 1, 0; i < len(a); i++ {
		bit := len(a) >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= len(a); size <<= 1 {
		step := cmplx.Rect(1, -2*math.Pi/float64(size))
		for base := 0; base < len(a); base += size {
			w := complex(1, 0)
			for j := range size / 2 {
				u, v := a[base+j], a[base+j+size/2]*w
				a[base+j], a[base+j+size/2] = u+v, u-v
				w *= step
			}
		}
	}
}
