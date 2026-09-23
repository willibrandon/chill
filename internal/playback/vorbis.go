package playback

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gopxl/beep/v2"
	"github.com/jfreymuth/oggvorbis"
)

// readOggPage bounds every allocation by the Ogg page format. In particular,
// zero-segment pages never reach the pinned decoder's unchecked lacing index.
func readOggPage(source io.Reader) ([]byte, error) {
	var header [27]byte
	if _, err := io.ReadFull(source, header[:]); err != nil {
		return nil, err
	}
	if string(header[:4]) != "OggS" || header[4] != 0 {
		return nil, fmt.Errorf("invalid Ogg page header")
	}
	segments := int(header[26])
	page := make([]byte, 27+segments, 27+segments+255*segments)
	copy(page, header[:])
	if _, err := io.ReadFull(source, page[27:]); err != nil {
		return nil, err
	}
	size := 0
	for _, length := range page[27:] {
		size += int(length)
	}
	page = page[:27+segments+size]
	if _, err := io.ReadFull(source, page[27+segments:]); err != nil {
		return nil, err
	}
	return page, nil
}

type logicalOgg struct {
	source         io.Reader
	page           []byte
	serial         uint32
	packetBytes    int
	started, ended bool
}

// Read exposes exactly one logical stream, never reading into the next chain.
func (r *logicalOgg) Read(dst []byte) (int, error) {
	for len(r.page) == 0 {
		if r.ended {
			return 0, io.EOF
		}
		page, err := readOggPage(r.source)
		if err != nil {
			if err == io.EOF && r.started {
				err = io.ErrUnexpectedEOF
			}
			return 0, err
		}
		serial := binary.LittleEndian.Uint32(page[14:])
		if !r.started {
			if page[5]&2 == 0 {
				return 0, fmt.Errorf("Ogg stream has no beginning page")
			}
			r.serial, r.started = serial, true
		} else if serial != r.serial || page[5]&2 != 0 {
			return 0, fmt.Errorf("Ogg stream changed before EOS")
		}
		for _, size := range page[27 : 27+int(page[26])] {
			r.packetBytes += int(size)
			if r.packetBytes > 1<<20 {
				return 0, fmt.Errorf("Ogg packet exceeds decoding limit")
			}
			if size < 255 {
				r.packetBytes = 0
			}
		}
		r.ended = page[5]&4 != 0
		if page[26] == 0 {
			continue
		}
		r.page = page
	}
	n := copy(dst, r.page)
	r.page = r.page[n:]
	return n, nil
}

type vorbisLink struct {
	reader  *oggvorbis.Reader
	scratch [2048]float32
	err     error
	ended   bool
}

// Stream adapts one logical Vorbis stream to stereo sample frames.
func (v *vorbisLink) Stream(dst [][2]float64) (int, bool) {
	if v.ended {
		return 0, false
	}
	channels := v.reader.Channels()
	n, err := v.reader.Read(v.scratch[:min(len(dst), len(v.scratch)/channels)*channels])
	for i := 0; i < n/channels; i++ {
		left := float64(v.scratch[i*channels])
		right := left
		if channels == 2 {
			right = float64(v.scratch[i*channels+1])
		}
		dst[i] = [2]float64{left, right}
	}
	if err != nil {
		v.ended = true
		if !errors.Is(err, io.EOF) {
			v.err = err
		}
	}
	return n / channels, n > 0
}

// Err reports the logical stream's decoding error.
func (v *vorbisLink) Err() error { return v.err }

type chainedVorbis struct {
	source                 io.ReadCloser
	input                  *bufio.Reader
	stream                 beep.Streamer
	rate, length, position int
	onTitle                func(string)
	err                    error
}

func (v *chainedVorbis) next() error {
	if _, err := v.input.Peek(1); err != nil {
		return err
	}
	r, err := oggvorbis.NewReader(&logicalOgg{source: v.input})
	if err != nil {
		return err
	}
	if r.SampleRate() < 1 || r.SampleRate() > 768000 || r.Channels() < 1 || r.Channels() > 2 {
		return fmt.Errorf("unsupported Vorbis sample format")
	}
	if v.rate == 0 {
		v.rate = r.SampleRate()
	}
	v.stream = &vorbisLink{reader: r}
	if r.SampleRate() != v.rate {
		v.stream = newBandlimited(v.stream, r.SampleRate(), v.rate, 3)
	}
	if v.onTitle != nil {
		var title, artist string
		for _, comment := range r.CommentHeader().Comments {
			key, value, ok := strings.Cut(comment, "=")
			if !ok {
				continue
			}
			switch strings.ToUpper(key) {
			case "TITLE":
				title = value
			case "ARTIST":
				artist = value
			}
		}
		if title != "" {
			if artist != "" {
				title = artist + " - " + title
			}
			v.onTitle(title)
		}
	}
	return nil
}

func decodeVorbis(source io.ReadCloser, onTitle func(string)) (beep.StreamSeekCloser, beep.Format, error) {
	v := &chainedVorbis{source: source, onTitle: onTitle}
	if seeker, ok := source.(io.ReadSeeker); ok {
		v.length, _ = vorbisLength(seeker)
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return nil, beep.Format{}, err
		}
	}
	v.input = bufio.NewReader(source)
	if err := v.next(); err != nil {
		return nil, beep.Format{}, err
	}
	return v, beep.Format{SampleRate: beep.SampleRate(v.rate), NumChannels: 2, Precision: 4}, nil
}

func vorbisLength(source io.Reader) (int, error) {
	var length int64
	rate, outputRate := 0, 0
	complete := false
	for {
		page, err := readOggPage(source)
		if err != nil {
			if err == io.EOF && complete {
				return int(length), nil
			}
			return 0, err
		}
		if page[5]&2 != 0 {
			body := page[27+int(page[26]):]
			if len(body) < 16 || string(body[:7]) != "\x01vorbis" {
				return 0, fmt.Errorf("invalid Vorbis identification")
			}
			rate = int(binary.LittleEndian.Uint32(body[12:]))
			if rate < 1 || rate > 768000 {
				return 0, fmt.Errorf("invalid Vorbis sample rate")
			}
			if outputRate == 0 {
				outputRate = rate
			}
			complete = false
		}
		if page[5]&4 != 0 {
			frames := int64(binary.LittleEndian.Uint64(page[6:]))
			if rate == 0 || frames < 0 || frames > 1<<40 {
				return 0, fmt.Errorf("invalid Vorbis granule position")
			}
			length += (frames*int64(outputRate) + int64(rate) - 1) / int64(rate)
			complete = true
		}
	}
}

// Stream continues through EOS into the next logical stream on the same source.
func (v *chainedVorbis) Stream(dst [][2]float64) (n int, ok bool) {
	if v.err != nil {
		return 0, false
	}
	for n < len(dst) {
		count, more := v.stream.Stream(dst[n:])
		n += count
		v.position += count
		if more && count > 0 {
			continue
		}
		if err := v.stream.Err(); err != nil {
			v.err = err
			break
		}
		if err := v.next(); err != nil {
			if !errors.Is(err, io.EOF) {
				v.err = err
			}
			break
		}
	}
	return n, n > 0
}

// Err returns malformed-chain or codec errors.
func (v *chainedVorbis) Err() error { return v.err }

// Len returns the sum of logical stream lengths when the source is seekable.
func (v *chainedVorbis) Len() int { return v.length }

// Position returns the frame cursor across all logical streams.
func (v *chainedVorbis) Position() int { return v.position }

// Seek decodes forward from the beginning with bounded storage across chains.
func (v *chainedVorbis) Seek(frame int) error {
	seeker, ok := v.source.(io.Seeker)
	if !ok || frame < 0 || v.length > 0 && frame > v.length {
		return fmt.Errorf("invalid Vorbis seek")
	}
	if _, err := seeker.Seek(0, io.SeekStart); err != nil {
		return err
	}
	v.input.Reset(v.source)
	v.position, v.err = 0, nil
	if err := v.next(); err != nil {
		return err
	}
	var scratch [1024][2]float64
	for v.position < frame {
		if n, ok := v.Stream(scratch[:min(len(scratch), frame-v.position)]); n == 0 || !ok {
			if v.err != nil {
				return v.err
			}
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}

// Close releases the shared encoded source.
func (v *chainedVorbis) Close() error { return v.source.Close() }
