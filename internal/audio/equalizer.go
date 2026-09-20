package audio

import (
	"encoding/binary"
	"math"
	"sync/atomic"
)

const (
	// EqualizerBandCount is the number of independently adjustable filters.
	EqualizerBandCount = 10
	// EqualizerMinGain is the minimum supported gain in decibels.
	EqualizerMinGain = -12.0
	// EqualizerMaxGain is the maximum supported gain in decibels.
	EqualizerMaxGain = 12.0
	equalizerQ       = 1.4
	equalizerRampMS  = 10
)

var equalizerFrequencies = [EqualizerBandCount]float64{70, 180, 320, 600, 1000, 3000, 6000, 12000, 14000, 16000}

// EqualizerBands is one complete ten-band gain curve in decibels.
type EqualizerBands [EqualizerBandCount]float64

// ClampEqualizerGain restricts a finite gain to the supported range. Non-finite
// input becomes zero so malformed control data can never poison the audio path.
func ClampEqualizerGain(gain float64) float64 {
	if math.IsNaN(gain) || math.IsInf(gain, 0) {
		return 0
	}
	return max(EqualizerMinGain, min(EqualizerMaxGain, gain))
}

type biquadCoefficients struct {
	b0, b1, b2 float64
	a1, a2     float64
}

var identityCoefficients = biquadCoefficients{b0: 1}

// peakingBiquad is a transposed-direct-form-II peaking filter. Only the decoder
// goroutine touches its coefficients and delay state.
type peakingBiquad struct {
	freq, rate float64
	targetGain float64
	coeff      biquadCoefficients
	step       biquadCoefficients
	remaining  int
	z1, z2     [2]float64
}

func (b *peakingBiquad) coefficients(gain float64) biquadCoefficients {
	if gain == 0 {
		return identityCoefficients
	}
	a := math.Pow(10, gain/40)
	w0 := 2 * math.Pi * b.freq / b.rate
	alpha := math.Sin(w0) / (2 * equalizerQ)
	cosW0 := math.Cos(w0)
	b0 := 1 + alpha*a
	b1 := -2 * cosW0
	b2 := 1 - alpha*a
	a0 := 1 + alpha/a
	a1 := -2 * cosW0
	a2 := 1 - alpha/a
	return biquadCoefficients{
		b0: b0 / a0,
		b1: b1 / a0,
		b2: b2 / a0,
		a1: a1 / a0,
		a2: a2 / a0,
	}
}

func coefficientStep(from, to biquadCoefficients, samples int) biquadCoefficients {
	n := float64(samples)
	return biquadCoefficients{
		b0: (to.b0 - from.b0) / n,
		b1: (to.b1 - from.b1) / n,
		b2: (to.b2 - from.b2) / n,
		a1: (to.a1 - from.a1) / n,
		a2: (to.a2 - from.a2) / n,
	}
}

func (b *peakingBiquad) setInitialGain(gain float64) {
	b.targetGain = gain
	b.coeff = b.coefficients(gain)
	b.step = biquadCoefficients{}
	b.remaining = 0
}

func (b *peakingBiquad) beginRamp(gain float64, samples int) {
	b.targetGain = gain
	b.step = coefficientStep(b.coeff, b.coefficients(gain), samples)
	b.remaining = samples
}

func (b *peakingBiquad) advanceRamp() {
	if b.remaining <= 0 {
		return
	}
	b.coeff.b0 += b.step.b0
	b.coeff.b1 += b.step.b1
	b.coeff.b2 += b.step.b2
	b.coeff.a1 += b.step.a1
	b.coeff.a2 += b.step.a2
	b.remaining--
	if b.remaining == 0 {
		b.coeff = b.coefficients(b.targetGain)
		b.step = biquadCoefficients{}
		if b.targetGain == 0 {
			b.z1, b.z2 = [2]float64{}, [2]float64{}
		}
	}
}

func (b *peakingBiquad) process(x float64, channel int) float64 {
	y := b.coeff.b0*x + b.z1[channel]
	b.z1[channel] = b.coeff.b1*x - b.coeff.a1*y + b.z2[channel]
	b.z2[channel] = b.coeff.b2*x - b.coeff.a2*y
	return y
}

func (b *peakingBiquad) bypassed() bool {
	return b.targetGain == 0 && b.remaining == 0
}

// Equalizer processes interleaved stereo f32le PCM. SetBands is safe from a
// control goroutine while Process runs on the decoder goroutine.
type Equalizer struct {
	rate        int
	target      atomic.Pointer[EqualizerBands]
	filters     [EqualizerBandCount]peakingBiquad
	initialized bool
}

// NewEqualizer constructs an equalizer for a fixed output sample rate.
func NewEqualizer(sampleRate int) *Equalizer {
	if sampleRate <= 0 {
		sampleRate = SampleRate
	}
	eq := &Equalizer{rate: sampleRate}
	flat := EqualizerBands{}
	eq.target.Store(&flat)
	for i, freq := range equalizerFrequencies {
		eq.filters[i] = peakingBiquad{
			freq:  freq,
			rate:  float64(sampleRate),
			coeff: identityCoefficients,
		}
	}
	return eq
}

// SetBands changes the target curve. Processing transitions to it over a short
// ramp so repeated interactive adjustments do not click.
func (eq *Equalizer) SetBands(bands EqualizerBands) {
	for i, gain := range bands {
		bands[i] = ClampEqualizerGain(gain)
	}
	eq.target.Store(&bands)
}

// Process applies the current curve to complete stereo f32le frames in place.
func (eq *Equalizer) Process(pcm []byte) {
	frames := len(pcm) / 8
	if frames == 0 {
		return
	}
	target := eq.target.Load()
	if !eq.initialized {
		for i := range eq.filters {
			eq.filters[i].setInitialGain((*target)[i])
		}
		eq.initialized = true
	}

	rampSamples := max(1, eq.rate*equalizerRampMS/1000)
	bypassed := true
	for i := range eq.filters {
		gain := (*target)[i]
		if gain != eq.filters[i].targetGain {
			eq.filters[i].beginRamp(gain, rampSamples)
		}
		bypassed = bypassed && eq.filters[i].bypassed()
	}
	if bypassed {
		return
	}

	for frame := range frames {
		for i := range eq.filters {
			eq.filters[i].advanceRamp()
		}
		base := frame * 8
		for channel := range 2 {
			offset := base + channel*4
			x := float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[offset:])))
			if math.IsNaN(x) || math.IsInf(x, 0) {
				x = 0
			}
			for i := range eq.filters {
				if !eq.filters[i].bypassed() {
					x = eq.filters[i].process(x, channel)
				}
			}
			binary.LittleEndian.PutUint32(pcm[offset:], math.Float32bits(float32(x)))
		}
	}
}
