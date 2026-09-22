package playback

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"testing"
)

func tone(rate, frames int) []byte {
	data := make([]byte, frames*8)
	for i := range frames {
		v := math.Float32bits(float32(.5 * math.Sin(2*math.Pi*440*float64(i)/float64(rate))))
		binary.LittleEndian.PutUint32(data[i*8:], v)
		binary.LittleEndian.PutUint32(data[i*8+4:], v)
	}
	return data
}

// TestTempoChangesAndTinyTails checks live speed changes, source clocks, and finite tails.
func TestTempoChangesAndTinyTails(t *testing.T) {
	for _, frames := range []int{1, 20, 100, 511, 48000} {
		for _, speed := range []float64{.5, 1.5, 3} {
			ratio := speed
			tempo := NewTempo(bytes.NewReader(tone(48000, frames)), 48000, func() float64 { return ratio })
			var block [512 * 8]byte
			var advance float64
			for reads := 0; ; reads++ {
				if frames == 48000 && reads == 20 {
					ratio = 1
				}
				n, step, err := tempo.Read(block[:])
				advance += step
				if n%8 != 0 || step < 0 {
					t.Fatal("invalid PCM frame or source clock")
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if reads > 1000 {
					t.Fatal("tempo failed to finish")
				}
			}
			if math.Abs(advance-float64(frames)) > 1 {
				t.Fatalf("frames %d speed %v advanced %v", frames, speed, advance)
			}
		}
	}
	for _, speed := range []float64{1, 2} {
		tempo := NewTempo(bytes.NewReader(make([]byte, 9)), 48000, func() float64 { return speed })
		var block [512 * 8]byte
		for {
			_, _, err := tempo.Read(block[:])
			if err != nil {
				if err == io.EOF {
					t.Fatal("incomplete frame error lost")
				}
				break
			}
		}
	}
}

// TestTempoPitchDurationAndShortTail checks pitch, duration, and EOF across rates and speeds.
func TestTempoPitchDurationAndShortTail(t *testing.T) {
	for _, rate := range []int{44100, 48000, 96000} {
		for _, speed := range []float64{.5, 1, 1.5, 3} {
			input := tone(rate, rate*2)
			tempo := NewTempo(bytes.NewReader(input), rate, func() float64 { return speed })
			var out []byte
			var block [512 * 8]byte
			for {
				n, _, err := tempo.Read(block[:])
				out = append(out, block[:n]...)
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(out) > len(input)*3 {
					t.Fatal("unbounded tempo output")
				}
			}
			want := float64(len(input)) / speed
			if math.Abs(float64(len(out))-want) > float64(rate*8)*.12 {
				t.Errorf("rate %d speed %v duration frames %d, want %.0f", rate, speed, len(out)/8, want/8)
			}
			crossings := 0
			prev := float32(0)
			for i := 0; i+8 <= len(out); i += 8 {
				v := math.Float32frombits(binary.LittleEndian.Uint32(out[i:]))
				if prev <= 0 && v > 0 {
					crossings++
				}
				prev = v
			}
			hz := float64(crossings) * float64(rate) / float64(len(out)/8)
			if math.Abs(hz-440) > 8 {
				t.Errorf("rate %d speed %v pitch %.1f", rate, speed, hz)
			}
		}
	}
	for _, frames := range []int{1, 20, 100, 511} {
		input := tone(48000, frames)
		tempo := NewTempo(bytes.NewReader(input), 48000, func() float64 { return 1 })
		var block [4096]byte
		n, _, err := tempo.Read(block[:])
		if n != len(input) || err != io.EOF {
			t.Fatalf("tail: %d %v", n, err)
		}
	}
}

// FuzzFormat checks media sniffing against arbitrary input.
func FuzzFormat(f *testing.F) {
	for _, seed := range [][]byte{[]byte("RIFFxxxxWAVE"), []byte("OggS"), []byte("fLaC"), []byte("ID3")} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) { Format(data) })
}
