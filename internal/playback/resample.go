package playback

import (
	"math"

	"github.com/gopxl/beep/v2"
)

// bandlimited is a polyphase windowed-sinc resampler. The cutoff and support
// scale with the conversion ratio, so downsampling rejects frequencies above
// the destination Nyquist frequency. Filter work never runs on the device thread.
type bandlimited struct {
	source                              beep.Streamer
	inRate, outRate, half, phases, taps int
	coefficients                        []float64
	frames                              [][2]float64
	scratch                             [1024][2]float64
	read, position                      int64
	eof                                 bool
	err                                 error
}

func newBandlimited(source beep.Streamer, inRate, outRate, quality int) *bandlimited {
	ratio := math.Min(1, float64(outRate)/float64(inRate))
	half := int(math.Ceil(float64([]int{0, 24, 32, 48, 64}[min(4, max(1, quality))]) / ratio))
	a, b := inRate, outRate
	for b != 0 {
		a, b = b, a%b
	}
	phases := min(1024, outRate/a)
	r := &bandlimited{source: source, inRate: inRate, outRate: outRate, half: half, phases: phases, taps: 2*half + 1}
	r.frames = make([][2]float64, 2*half+2049)
	r.coefficients = make([]float64, (phases+1)*r.taps)
	cutoff := .94 * ratio
	for phase := 0; phase <= phases; phase++ {
		weights := r.coefficients[phase*r.taps : (phase+1)*r.taps]
		var sum float64
		for i := range weights {
			x := float64(i-half) - float64(phase)/float64(phases)
			y := math.Pi * x / float64(half+1)
			window := .35875 + .48829*math.Cos(y) + .14128*math.Cos(2*y) + .01168*math.Cos(3*y)
			value := cutoff
			if x != 0 {
				value = math.Sin(math.Pi*cutoff*x) / (math.Pi * x)
			}
			weights[i] = value * window
			sum += weights[i]
		}
		for i := range weights {
			weights[i] /= sum
		}
	}
	return r
}

// Stream produces filtered frames while keeping only a fixed input window.
func (r *bandlimited) Stream(dst [][2]float64) (n int, ok bool) {
	for n < len(dst) {
		center := r.position / int64(r.outRate)
		for !r.eof && r.read <= center+int64(r.half) {
			count, more := r.source.Stream(r.scratch[:])
			for _, frame := range r.scratch[:count] {
				r.frames[r.read%int64(len(r.frames))] = frame
				r.read++
			}
			if count == 0 || !more {
				r.eof = true
				r.err = r.source.Err()
			}
		}
		if r.eof && r.position >= r.read*int64(r.outRate) {
			break
		}
		phase := float64(r.position%int64(r.outRate)) * float64(r.phases) / float64(r.outRate)
		index := int(phase)
		fraction := phase - float64(index)
		weights := r.coefficients[index*r.taps : (index+1)*r.taps]
		next := r.coefficients[(index+1)*r.taps : (index+2)*r.taps]
		var sample [2]float64
		for i, weight := range weights {
			at := center + int64(i-r.half)
			if at < 0 || at >= r.read {
				continue
			}
			weight += fraction * (next[i] - weight)
			frame := r.frames[at%int64(len(r.frames))]
			sample[0] += frame[0] * weight
			sample[1] += frame[1] * weight
		}
		dst[n] = sample
		n++
		r.position += int64(r.inRate)
	}
	return n, n > 0
}

// Err reports source decoding errors after the filtered tail has drained.
func (r *bandlimited) Err() error { return r.err }
