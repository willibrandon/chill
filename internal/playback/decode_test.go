package playback

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/gopxl/beep/v2"
)

type memoryAudio struct{ *bytes.Reader }

// Close implements an in-memory seekable audio source.
func (memoryAudio) Close() error { return nil }

func audioFixture(t testing.TB, extension string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../testdata/audio/tone." + extension)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeFrames(t testing.TB, stream beep.Streamer) [][2]float64 {
	t.Helper()
	var frames [][2]float64
	var buffer [733][2]float64
	for {
		n, ok := stream.Stream(buffer[:])
		frames = append(frames, buffer[:n]...)
		if !ok || n == 0 {
			break
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	return frames
}

// TestMP3GaplessTrimAndSeek verifies music length and seeking after encoder-delay removal.
func TestMP3GaplessTrimAndSeek(t *testing.T) {
	data := audioFixture(t, "mp3")
	var reference [][2]float64
	for _, seekable := range []bool{true, false} {
		var source io.ReadCloser = io.NopCloser(bytes.NewReader(data))
		if seekable {
			source = memoryAudio{bytes.NewReader(data)}
		}
		d, format, err := Decode("mp3", source)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if format.SampleRate != 44100 || d.Len() != 11025 {
			t.Fatalf("music duration: %v %d", format, d.Len())
		}
		frames := decodeFrames(t, d)
		if len(frames) != 11025 || d.Position() != 11025 {
			t.Fatalf("music frames: %d, position: %d", len(frames), d.Position())
		}
		if seekable {
			reference = frames
			for _, position := range []int{0, 5000, 11025} {
				if err := d.Seek(position); err != nil {
					t.Fatal(err)
				}
				got := decodeFrames(t, d)
				if len(got) != len(reference)-position {
					t.Fatalf("seek %d: %d frames", position, len(got))
				}
				// Layer III seeks rebuild the bit reservoir; compare after its warmup.
				if len(got) > 3000 && !slices.Equal(got[3000:], reference[position+3000:]) {
					t.Fatalf("seek %d changed music", position)
				}
			}
		} else if !slices.Equal(frames, reference) {
			t.Fatal("streaming trim differs from local trim")
		}
	}
}

// TestChainedVorbisLengthSeekAndTags checks both logical streams over seekable and live input.
func TestChainedVorbisLengthSeekAndTags(t *testing.T) {
	data := audioFixture(t, "ogg")
	chain := append(bytes.Clone(data), data...)
	for _, seekable := range []bool{true, false} {
		var source io.ReadCloser = io.NopCloser(bytes.NewReader(chain))
		if seekable {
			source = memoryAudio{bytes.NewReader(chain)}
		}
		var titles []string
		d, format, err := DecodeWithMetadata("vorbis", source, func(title string) { titles = append(titles, title) })
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		frames := decodeFrames(t, d)
		if format.SampleRate != 44100 || len(frames) != 22144 {
			t.Fatalf("chained music: %v %d frames", format, len(frames))
		}
		if !slices.Equal(titles, []string{"Chill - Native fixture", "Chill - Native fixture"}) {
			t.Fatalf("chain tags: %q", titles)
		}
		if !slices.Equal(frames[:11072], frames[11072:]) {
			t.Fatal("second chain lost or changed audio")
		}
		if seekable {
			if d.Len() != len(frames) {
				t.Fatalf("chain duration: %d", d.Len())
			}
			if err := d.Seek(12000); err != nil {
				t.Fatal(err)
			}
			if got := decodeFrames(t, d); !slices.Equal(got, frames[12000:]) {
				t.Fatal("seek into second chain changed audio")
			}
		}
	}
}

type panicDecoder struct{}

// Stream simulates a lazy codec panic.
func (d panicDecoder) Stream([][2]float64) (int, bool) { panic("stream") }

// Err simulates a decoder diagnostics panic.
func (d panicDecoder) Err() error { panic("error") }

// Len simulates a malformed index panic.
func (d panicDecoder) Len() int { panic("length") }

// Position simulates a decoder position panic.
func (d panicDecoder) Position() int { panic("position") }

// Seek simulates a decoder seek panic.
func (d panicDecoder) Seek(int) error { panic("seek") }

// Close simulates a decoder cleanup panic.
func (d panicDecoder) Close() error { panic("close") }

// TestNativeDecoderContainsPanics covers initialization, streaming, metadata, seek and cleanup.
func TestNativeDecoderContainsPanics(t *testing.T) {
	malformed := make([]byte, 40)
	copy(malformed, "OggS")
	copy(malformed[27:], "\x01vorbis")
	if Format(malformed) != "vorbis" {
		t.Fatal("reproducer no longer reaches native decoder")
	}
	if d, _, err := Decode("vorbis", io.NopCloser(bytes.NewReader(malformed))); err == nil {
		d.Close()
		t.Fatal("accepted malformed Ogg page")
	}
	for _, operation := range []string{"stream", "error", "length", "position", "seek", "close"} {
		d := &guardedDecoder{source: panicDecoder{}}
		var err error
		switch operation {
		case "stream":
			d.Stream(make([][2]float64, 10))
		case "length":
			d.Len()
		case "position":
			d.Position()
		case "seek":
			err = d.Seek(1)
		case "close":
			err = d.Close()
		}
		if err == nil {
			err = d.Err()
		}
		if err == nil || !strings.Contains(err.Error(), operation) {
			t.Fatalf("%s panic not contained: %v", operation, err)
		}
	}
}

type sampleStream struct{ frames [][2]float64 }

// Stream consumes generated signal frames.
func (s *sampleStream) Stream(dst [][2]float64) (int, bool) {
	n := copy(dst, s.frames)
	s.frames = s.frames[n:]
	return n, n > 0
}

// Err returns no error for generated signal input.
func (*sampleStream) Err() error { return nil }

// TestBandlimitedResamplingRejectsAliases checks stopband attenuation, passband gain and length.
func TestBandlimitedResamplingRejectsAliases(t *testing.T) {
	for _, rates := range [][2]int{{96000, 48000}, {192000, 48000}, {96000, 44100}, {44100, 48000}} {
		for _, frequency := range []float64{0, 1000, 30000} {
			if frequency >= float64(rates[0])/2 {
				continue
			}
			t.Run(fmt.Sprintf("%d_%d_%g", rates[0], rates[1], frequency), func(t *testing.T) {
				frames := make([][2]float64, rates[0]/4)
				for i := range frames {
					sample := .5
					if frequency > 0 {
						sample *= math.Sin(2 * math.Pi * frequency * float64(i) / float64(rates[0]))
					}
					frames[i] = [2]float64{sample, sample}
				}
				r := newBandlimited(&sampleStream{frames}, rates[0], rates[1], 3)
				got := decodeFrames(t, r)
				if len(got) != (len(frames)*rates[1]+rates[0]-1)/rates[0] {
					t.Fatalf("length %d", len(got))
				}
				var power float64
				for _, frame := range got[200 : len(got)-200] {
					power += frame[0] * frame[0]
				}
				rms := math.Sqrt(power / float64(len(got)-400))
				if frequency > float64(rates[1])/2 {
					if rms > .000015 {
						t.Fatalf("alias RMS %.8f", rms)
					}
				} else {
					want := .5
					if frequency > 0 {
						want /= math.Sqrt2
					}
					if math.Abs(rms-want) > .001 {
						t.Fatalf("passband RMS %.8f, want %.8f", rms, want)
					}
				}
			})
		}
	}
}

// TestOutputPreservesSavedVolumeCurve checks the previous cubic gain at restored levels.
func TestOutputPreservesSavedVolumeCurve(t *testing.T) {
	o, render := newManualOutput(t)
	for _, level := range []int{0, 10, 25, 50, 75, 100} {
		o.SetVolume(level)
		if err := o.Write(t.Context(), testPCM(1), 1, 0, 1); err != nil {
			t.Fatal(err)
		}
		var data [8]byte
		render(data[:])
		got := float64(math.Float32frombits(binary.LittleEndian.Uint32(data[:])))
		want := .8 * math.Pow(float64(level)/100, 3)
		if math.Abs(got-want) > 1e-7 {
			t.Fatalf("volume %d: gain %g, want %g", level, got, want)
		}
	}
}

// FuzzNativeDecoders checks malformed codec inputs cannot panic during initialization or playback.
func FuzzNativeDecoders(f *testing.F) {
	for _, ext := range []string{"wav", "mp3", "ogg", "flac"} {
		f.Add(audioFixture(f, ext))
	}
	malformed := make([]byte, 40)
	copy(malformed, "OggS")
	copy(malformed[27:], "\x01vorbis")
	f.Add(malformed)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		kind := Format(data)
		if kind == "" {
			return
		}
		d, _, err := Decode(kind, io.NopCloser(bytes.NewReader(data)))
		if err != nil {
			return
		}
		defer d.Close()
		var buffer [128][2]float64
		for range 32 {
			if n, ok := d.Stream(buffer[:]); n == 0 || !ok {
				break
			}
		}
		d.Err()
	})
}
