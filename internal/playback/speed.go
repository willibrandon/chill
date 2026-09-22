package playback

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
)

// Tempo uses waveform-similarity overlap-add. Both stereo channels use the
// same alignment, preserving their phase relationship. Work and buffering are
// bounded independently of track duration. All processing runs off the device
// callback. Speed changes apply at the next synthesis window.
type Tempo struct {
	source                       io.Reader
	speed                        func() float64
	window, overlap, search, hop int
	input, output                [][2]float64
	tail                         [][2]float64
	position                     float64
	valid, eof                   bool
	err                          error
	bytes                        []byte
	consumed                     float64
}

// NewTempo creates a bounded stereo time stretcher at the pipeline sample rate.
func NewTempo(source io.Reader, rate int, speed func() float64) *Tempo {
	o := max(32, rate*12/1000)
	h := max(o, rate*70/1000)
	s := rate * 20 / 1000
	return &Tempo{source: source, speed: speed, window: h + o, overlap: o, hop: h, search: s, input: make([][2]float64, 0, (h+o+s)*5), output: make([][2]float64, 0, h+o), tail: make([][2]float64, o), bytes: make([]byte, 4096*8)}
}

func (t *Tempo) fill(want int) {
	for len(t.input) < want && !t.eof {
		n, err := io.ReadFull(t.source, t.bytes)
		if n%8 != 0 {
			err = errors.New("decoder returned an incomplete PCM frame")
		}
		for i := 0; i+8 <= n; i += 8 {
			t.input = append(t.input, [2]float64{float64(math.Float32frombits(binary.LittleEndian.Uint32(t.bytes[i:]))), float64(math.Float32frombits(binary.LittleEndian.Uint32(t.bytes[i+4:])))})
		}
		if err != nil {
			t.eof = true
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				t.err = err
			}
		}
	}
}

// Read returns PCM and the source-frame advance represented by that PCM.
func (t *Tempo) Read(dst []byte) (int, float64, error) {
	ratio := t.speed()
	if math.IsNaN(ratio) || ratio < 0.5 || ratio > 3 {
		ratio = 1
	}
	if len(t.output) == 0 {
		if ratio == 1 && !t.valid && len(t.input) == 0 {
			if t.eof {
				if t.err != nil {
					return 0, 0, t.err
				}
				return 0, 0, io.EOF
			}
			n, err := io.ReadFull(t.source, dst)
			if n%8 != 0 {
				err = errors.New("decoder returned an incomplete PCM frame")
			} else if err == io.ErrUnexpectedEOF {
				err = io.EOF
			}
			return n, float64(n / 8), err
		}
		if drop := max(0, int(t.position)-t.search); drop > 0 {
			drop = min(drop, len(t.input))
			copy(t.input, t.input[drop:])
			t.input = t.input[:len(t.input)-drop]
			t.position -= float64(drop)
		}
		expected := int(t.position)
		t.fill(expected + max(t.window+t.search, int(math.Ceil(float64(t.hop)*ratio))+t.overlap))
		if expected >= len(t.input) {
			if t.err != nil {
				return 0, 0, t.err
			}
			return 0, 0, io.EOF
		}
		start := expected
		if ratio != 1 && t.valid && len(t.input) >= t.window {
			lo, hi := max(0, expected-t.search), min(len(t.input)-t.window, expected+t.search)
			best := math.Inf(-1)
			// Coarse alignment followed by sample-level refinement bounds CPU.
			score := func(at int) float64 {
				var dot, a, b float64
				for i := 0; i < t.overlap; i += 4 {
					for ch := range 2 {
						x, y := t.tail[i][ch], t.input[at+i][ch]
						dot += x * y
						a += x * x
						b += y * y
					}
				}
				return dot / math.Sqrt(max(1e-20, a*b))
			}
			for at := lo; at <= hi; at += 8 {
				if v := score(at); v > best {
					best = v
					start = at
				}
			}
			center := start
			for at := max(lo, center-7); at <= min(hi, center+7); at++ {
				if v := score(at); v > best {
					best = v
					start = at
				}
			}
		}
		n := min(t.hop, len(t.input)-start)
		if t.eof {
			n = min(n, int(math.Ceil((float64(len(t.input))-t.position)/ratio)))
		}
		t.output = append(t.output[:0], t.input[start:start+n]...)
		if t.valid {
			for i := 0; i < min(t.overlap, n); i++ {
				a := float64(i) / float64(t.overlap)
				for ch := range 2 {
					t.output[i][ch] = t.tail[i][ch]*(1-a) + t.output[i][ch]*a
				}
			}
		}
		available := len(t.input) - start - n
		t.valid = available >= t.overlap
		if t.valid {
			copy(t.tail, t.input[start+n:start+n+t.overlap])
		}
		advance := min(float64(n)*ratio, float64(len(t.input))-t.position)
		t.position += advance
		t.consumed = advance / float64(n)
	}
	n := min(len(dst)/8, len(t.output))
	for i := 0; i < n; i++ {
		for ch := range 2 {
			binary.LittleEndian.PutUint32(dst[i*8+ch*4:], math.Float32bits(float32(t.output[i][ch])))
		}
	}
	copy(t.output, t.output[n:])
	t.output = t.output[:len(t.output)-n]
	return n * 8, float64(n) * t.consumed, nil
}
