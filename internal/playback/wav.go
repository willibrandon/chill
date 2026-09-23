package playback

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/gopxl/beep/v2"
)

// waveDecoder handles integer and floating-point PCM without truncating the
// source precision. Metadata chunks are skipped with bounded scratch storage.
type waveDecoder struct {
	source           io.ReadCloser
	reader           *bufio.Reader
	format           beep.Format
	kind             uint16
	width, align     int
	dataStart        int64
	length, position int
	err              error
}

func decodeWave(source io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) {
	d := &waveDecoder{source: source}
	var head [12]byte
	if _, err := io.ReadFull(source, head[:]); err != nil {
		return nil, beep.Format{}, err
	}
	if string(head[:4]) != "RIFF" || string(head[8:]) != "WAVE" {
		return nil, beep.Format{}, fmt.Errorf("unsupported WAV container")
	}
	remaining := int64(binary.LittleEndian.Uint32(head[4:])) - 4
	position := int64(12)
	for remaining >= 8 {
		var h [8]byte
		if _, err := io.ReadFull(source, h[:]); err != nil {
			return nil, beep.Format{}, err
		}
		remaining -= 8
		position += 8
		size := int64(binary.LittleEndian.Uint32(h[4:]))
		if size > remaining {
			return nil, beep.Format{}, fmt.Errorf("WAV chunk exceeds container")
		}
		switch string(h[:4]) {
		case "fmt ":
			if size < 16 || size > 65536 {
				return nil, beep.Format{}, fmt.Errorf("invalid WAV format size")
			}
			body := make([]byte, int(size))
			if _, err := io.ReadFull(source, body); err != nil {
				return nil, beep.Format{}, err
			}
			d.kind = binary.LittleEndian.Uint16(body)
			channels := int(binary.LittleEndian.Uint16(body[2:]))
			rate := int(binary.LittleEndian.Uint32(body[4:]))
			d.align = int(binary.LittleEndian.Uint16(body[12:]))
			bits := int(binary.LittleEndian.Uint16(body[14:]))
			if d.kind == 0xfffe && len(body) >= 40 {
				if !bytes.Equal(body[26:40], []byte{0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xaa, 0, 0x38, 0x9b, 0x71}) {
					return nil, beep.Format{}, fmt.Errorf("unsupported WAV subtype")
				}
				d.kind = binary.LittleEndian.Uint16(body[24:])
			}
			if channels < 1 || channels > 2 || rate < 1 || rate > 768000 || bits%8 != 0 {
				return nil, beep.Format{}, fmt.Errorf("unsupported WAV channel count or sample format")
			}
			d.width = bits / 8
			if d.align != channels*d.width || !(d.kind == 1 && (bits == 8 || bits == 16 || bits == 24 || bits == 32) || d.kind == 3 && (bits == 32 || bits == 64)) {
				return nil, beep.Format{}, fmt.Errorf("unsupported WAV encoding")
			}
			d.format = beep.Format{SampleRate: beep.SampleRate(rate), NumChannels: channels, Precision: d.width}
		case "data":
			if d.align == 0 || size%int64(d.align) != 0 {
				return nil, beep.Format{}, fmt.Errorf("invalid WAV sample data")
			}
			d.dataStart = position
			d.length = int(size / int64(d.align))
			d.reader = bufio.NewReader(source)
			return d, d.format, nil
		default:
			if _, err := io.CopyN(io.Discard, source, size); err != nil {
				return nil, beep.Format{}, err
			}
		}
		if size%2 != 0 {
			var pad [1]byte
			if _, err := io.ReadFull(source, pad[:]); err != nil {
				return nil, beep.Format{}, err
			}
		}
		position += size + size%2
		remaining -= size + size%2
	}
	return nil, beep.Format{}, fmt.Errorf("WAV has no sample data")
}

// Stream decodes complete frames and preserves a truncated-input error.
func (d *waveDecoder) Stream(dst [][2]float64) (int, bool) {
	if d.err != nil {
		return 0, false
	}
	n := min(len(dst), d.length-d.position)
	var frame [16]byte
	for i := 0; i < n; i++ {
		if _, err := io.ReadFull(d.reader, frame[:d.align]); err != nil {
			d.err = io.ErrUnexpectedEOF
			return i, i > 0
		}
		for ch := 0; ch < d.format.NumChannels; ch++ {
			data := frame[ch*d.width:]
			var v float64
			if d.kind == 3 {
				if d.width == 4 {
					v = float64(math.Float32frombits(binary.LittleEndian.Uint32(data)))
				} else {
					v = math.Float64frombits(binary.LittleEndian.Uint64(data))
				}
			} else {
				switch d.width {
				case 1:
					v = float64(int(data[0])-128) / 128
				case 2:
					v = float64(int16(binary.LittleEndian.Uint16(data))) / 32768
				case 3:
					v = float64(int32(uint32(data[0])<<8|uint32(data[1])<<16|uint32(data[2])<<24)>>8) / 8388608
				case 4:
					v = float64(int32(binary.LittleEndian.Uint32(data))) / 2147483648
				}
			}
			if math.IsNaN(v) || math.IsInf(v, 0) {
				v = 0
			}
			dst[i][ch] = v
		}
		if d.format.NumChannels == 1 {
			dst[i][1] = dst[i][0]
		}
		d.position++
	}
	return n, n > 0
}

// Err reports malformed or truncated sample data.
func (d *waveDecoder) Err() error { return d.err }

// Len returns the number of source frames.
func (d *waveDecoder) Len() int { return d.length }

// Position returns the source frame cursor.
func (d *waveDecoder) Position() int { return d.position }

// Seek repositions a seekable WAV source and resets buffered input.
func (d *waveDecoder) Seek(frame int) error {
	seeker, ok := d.source.(io.Seeker)
	if !ok {
		return fmt.Errorf("WAV source is not seekable")
	}
	if frame < 0 || frame > d.length {
		return fmt.Errorf("WAV seek is outside the track")
	}
	if _, err := seeker.Seek(d.dataStart+int64(frame*d.align), io.SeekStart); err != nil {
		return err
	}
	d.reader.Reset(d.source)
	d.position = frame
	d.err = nil
	return nil
}

// Close releases the encoded source.
func (d *waveDecoder) Close() error { return d.source.Close() }
