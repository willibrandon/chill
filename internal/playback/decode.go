package playback

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/flac"
)

// Format recognizes codecs from their bytes, not a URL or filename extension.
func Format(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("fLaC")):
		return "flac"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return "wav"
	case bytes.HasPrefix(data, []byte("OggS")):
		if bytes.Contains(data, []byte("\x01vorbis")) {
			return "vorbis"
		}
		return ""
	case bytes.HasPrefix(data, []byte("ID3")):
		return "mp3"
	case len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0 && data[1]&0x06 != 0:
		return "mp3"
	default:
		return ""
	}
}

// Decode initializes the requested built-in codec.
func Decode(kind string, source io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) {
	return DecodeWithMetadata(kind, source, nil)
}

// DecodeWithMetadata contains codec panics and reports native stream tags.
func DecodeWithMetadata(kind string, source io.ReadCloser, onTitle func(string)) (decoder beep.StreamSeekCloser, format beep.Format, err error) {
	defer func() {
		if failure := recover(); failure != nil {
			decoder, err = nil, fmt.Errorf("invalid %s audio: decoder panic: %v", kind, failure)
		}
		if err == nil && decoder != nil {
			decoder = &guardedDecoder{source: decoder}
		}
	}()
	switch kind {
	case "mp3":
		return decodeMP3(source)
	case "flac":
		return flac.Decode(source)
	case "wav":
		return decodeWave(source)
	case "vorbis":
		return decodeVorbis(source, onTitle)
	}
	return nil, beep.Format{}, fmt.Errorf("format requires FFmpeg")
}

// PCM adapts a native decoder to interleaved float PCM at the pipeline rate.
// Seek is used only for genuinely seekable sources; streaming input is skipped
// incrementally so seeking never requires an unbounded in-memory download.
func PCM(ctx context.Context, decoder beep.StreamSeekCloser, format beep.Format, rate, quality int, offset time.Duration, seekable bool) (io.Reader, error) {
	if format.SampleRate <= 0 || format.SampleRate > 768000 || rate < 8000 || rate > 768000 {
		return nil, fmt.Errorf("invalid audio sample rate")
	}
	frames := format.SampleRate.N(offset)
	if frames > 0 {
		if seekable {
			if n := decoder.Len(); n > 0 {
				frames = min(frames, n)
			}
			if err := decoder.Seek(frames); err != nil {
				return nil, err
			}
		} else {
			var scratch [1024][2]float64
			for frames > 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				n, ok := decoder.Stream(scratch[:min(frames, len(scratch))])
				frames -= n
				if !ok || n == 0 {
					break
				}
			}
		}
	}
	var stream beep.Streamer = decoder
	if int(format.SampleRate) != rate {
		stream = newBandlimited(stream, int(format.SampleRate), rate, quality)
	}
	return &pcmReader{ctx: ctx, stream: stream}, nil
}

type pcmReader struct {
	ctx     context.Context
	stream  beep.Streamer
	samples [1024][2]float64
}

// Read fills the caller's buffer and reports source errors.
func (r *pcmReader) Read(dst []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, ok := r.stream.Stream(r.samples[:min(len(dst)/8, len(r.samples))])
	for i := 0; i < n; i++ {
		for ch := range 2 {
			binary.LittleEndian.PutUint32(dst[i*8+ch*4:], math.Float32bits(float32(r.samples[i][ch])))
		}
	}
	if n > 0 {
		return n * 8, nil
	}
	if err := r.stream.Err(); err != nil {
		return 0, fmt.Errorf("decode audio: %w", err)
	}
	if !ok {
		return 0, io.EOF
	}
	return 0, io.ErrNoProgress
}
