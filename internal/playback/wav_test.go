package playback

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"testing"
)

type seekableBytes struct{ *bytes.Reader }

// Close leaves the in-memory fixture available for assertions.
func (s seekableBytes) Close() error { return nil }

func waveBytes(kind, bits uint16, samples []byte) []byte {
	data := make([]byte, 44+len(samples))
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], kind)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 48000)
	binary.LittleEndian.PutUint32(data[28:], 48000*uint32(bits/8))
	binary.LittleEndian.PutUint16(data[32:], bits/8)
	binary.LittleEndian.PutUint16(data[34:], bits)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(len(samples)))
	copy(data[44:], samples)
	return data
}

// TestWavePrecisionAndSeeking checks every supported PCM width, mono duplication, and native seeking.
func TestWavePrecisionAndSeeking(t *testing.T) {
	for _, sample := range []struct {
		kind, bits uint16
		raw        uint64
		want       float64
	}{
		{1, 8, 129, 1.0 / 128},
		{1, 16, 1, 1.0 / 32768},
		{1, 24, 1, 1.0 / 8388608},
		{1, 32, 1, 1.0 / 2147483648},
		{1, 24, 0x800000, -1},
		{3, 32, uint64(math.Float32bits(.25)), .25},
		{3, 64, math.Float64bits(.25), .25},
		{3, 64, math.Float64bits(math.NaN()), 0},
	} {
		var encoded [8]byte
		binary.LittleEndian.PutUint64(encoded[:], sample.raw)
		data := waveBytes(sample.kind, sample.bits, bytes.Repeat(encoded[:sample.bits/8], 2))
		decoder, format, err := decodeWave(seekableBytes{bytes.NewReader(data)})
		if err != nil {
			t.Fatal(err)
		}
		var pcm [2][2]float64
		n, ok := decoder.Stream(pcm[:])
		if n != 2 || !ok || decoder.Position() != 2 || format.Precision != int(sample.bits/8) {
			t.Fatalf("invalid WAV frames: %d %v %+v", n, ok, format)
		}
		for _, frame := range pcm {
			if frame[0] != sample.want || frame[1] != sample.want {
				t.Fatalf("kind %d bits %d lost precision: %v want %v", sample.kind, sample.bits, frame, sample.want)
			}
		}
		if err := decoder.Seek(1); err != nil {
			t.Fatal(err)
		}
		if n, ok := decoder.Stream(pcm[:]); n != 1 || !ok {
			t.Fatalf("seek returned %d frames", n)
		}
		if n, ok := decoder.Stream(pcm[:]); n != 0 || ok || decoder.Err() != nil {
			t.Fatal("invalid EOF")
		}
		decoder.Close()
	}
}

// TestWaveRejectsTruncationAndOversizedChunks checks malformed input is reported with bounded reads.
func TestWaveRejectsTruncationAndOversizedChunks(t *testing.T) {
	data := waveBytes(1, 32, make([]byte, 8))
	decoder, _, err := decodeWave(io.NopCloser(bytes.NewReader(data[:len(data)-1])))
	if err != nil {
		t.Fatal(err)
	}
	var pcm [2][2]float64
	decoder.Stream(pcm[:])
	if decoder.Err() != io.ErrUnexpectedEOF {
		t.Fatalf("truncation hidden: %v", decoder.Err())
	}
	decoder.Close()
	binary.LittleEndian.PutUint32(data[16:], math.MaxUint32)
	if _, _, err := decodeWave(io.NopCloser(bytes.NewReader(data))); err == nil {
		t.Fatal("oversized chunk accepted")
	}
}

// FuzzWaveDecoder exercises WAV headers and initial sample decoding with bounded inputs.
func FuzzWaveDecoder(f *testing.F) {
	f.Add(waveBytes(1, 24, make([]byte, 12)))
	f.Add(waveBytes(3, 32, make([]byte, 16)))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		decoder, _, err := decodeWave(io.NopCloser(bytes.NewReader(data)))
		if err == nil {
			defer decoder.Close()
			var pcm [16][2]float64
			decoder.Stream(pcm[:])
		}
	})
}
