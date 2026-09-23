package playback

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/mp3"
)

type mp3Trim struct{ start, length int }

func mp3Trimming(r *bufio.Reader) (mp3Trim, error) {
	trim := mp3Trim{length: -1}
	for skipped := 0; ; {
		header, err := r.Peek(10)
		if err != nil {
			return trim, err
		}
		if string(header[:3]) != "ID3" {
			break
		}
		if header[6]|header[7]|header[8]|header[9] >= 128 {
			return trim, fmt.Errorf("invalid ID3 size")
		}
		size := int(header[6])<<21 | int(header[7])<<14 | int(header[8])<<7 | int(header[9])
		size += 10
		if header[3] == 4 && header[5]&0x10 != 0 {
			size += 10
		}
		skipped += size
		if skipped > 16<<20 {
			return trim, fmt.Errorf("MP3 tags exceed metadata limit")
		}
		if _, err := io.CopyN(io.Discard, r, int64(size)); err != nil {
			return trim, err
		}
	}
	header, err := r.Peek(4)
	if err != nil {
		return trim, err
	}
	h := binary.BigEndian.Uint32(header)
	version, layer, rateIndex, bitRateIndex := (h>>19)&3, (h>>17)&3, (h>>10)&3, (h>>12)&15
	if h>>21 != 0x7ff || version == 1 || layer != 1 || rateIndex == 3 || bitRateIndex == 0 || bitRateIndex == 15 {
		return trim, nil
	}
	rate := []int{44100, 48000, 32000}[rateIndex]
	bitrate := []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}[bitRateIndex]
	samples, coefficient, side := 1152, 144000, 32
	mono := (h>>6)&3 == 3
	if mono {
		side = 17
	}
	if version != 3 {
		rate /= 2
		if version == 0 {
			rate /= 2
		}
		bitrate = []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}[bitRateIndex]
		samples, coefficient, side = 576, 72000, 17
		if mono {
			side = 9
		}
	}
	size := coefficient*bitrate/rate + int((h>>9)&1)
	frame, err := r.Peek(size)
	if err != nil {
		return trim, err
	}
	at := 4 + side
	if len(frame) < at+8 || string(frame[at:at+4]) != "Xing" && string(frame[at:at+4]) != "Info" {
		return trim, nil
	}
	trim.start = samples // The Xing/Info frame is metadata, not decoded music.
	flags := binary.BigEndian.Uint32(frame[at+4:])
	at += 8
	frames := 0
	for _, field := range []struct {
		flag uint32
		size int
	}{{1, 4}, {2, 4}, {4, 100}, {8, 4}} {
		if flags&field.flag == 0 {
			continue
		}
		if at+field.size > len(frame) {
			return trim, fmt.Errorf("truncated MP3 info tag")
		}
		if field.flag == 1 {
			frames = int(binary.BigEndian.Uint32(frame[at:]))
		}
		at += field.size
	}
	if frames > 0 {
		trim.length = frames * samples
	}
	if at+24 > len(frame) {
		return trim, nil
	}
	encoder := string(frame[at : at+4])
	if !strings.HasPrefix(encoder, "LAME") && encoder != "Lavc" && encoder != "Lavf" {
		return trim, nil
	}
	delay := int(frame[at+21])<<4 | int(frame[at+22]>>4)
	padding := int(frame[at+22]&15)<<8 | int(frame[at+23])
	// Layer III synthesis adds 529 samples beyond the encoder's own delay.
	// The end trim compensates for that same delay, preserving the music length.
	if frames > 0 && padding >= 529 && delay+padding < trim.length {
		trim.start += delay + 529
		trim.length -= delay + padding
	}
	return trim, nil
}

type bufferedMP3 struct {
	*bufio.Reader
	source io.Closer
}

// Close releases a non-seekable MP3 source.
func (r bufferedMP3) Close() error { return r.source.Close() }

func decodeMP3(source io.ReadCloser) (beep.StreamSeekCloser, beep.Format, error) {
	reader := bufio.NewReaderSize(source, 4096)
	trim, err := mp3Trimming(reader)
	if err != nil {
		return nil, beep.Format{}, err
	}
	var encoded io.ReadCloser = bufferedMP3{reader, source}
	if seeker, ok := source.(io.Seeker); ok {
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return nil, beep.Format{}, err
		}
		encoded = source
	}
	decoder, format, err := mp3.Decode(encoded)
	if err != nil {
		return nil, beep.Format{}, err
	}
	if trim.length < 0 && decoder.Len() > 0 {
		trim.length = max(0, decoder.Len()-trim.start)
	}
	d := &trimmedMP3{source: decoder, start: trim.start, length: trim.length}
	var scratch [1024][2]float64
	for left := trim.start; left > 0; {
		n, ok := decoder.Stream(scratch[:min(left, len(scratch))])
		left -= n
		if !ok || n == 0 {
			decoder.Close()
			return nil, beep.Format{}, fmt.Errorf("truncated MP3 encoder delay")
		}
	}
	return d, format, nil
}

type trimmedMP3 struct {
	source                  beep.StreamSeekCloser
	start, length, position int
}

// Stream excludes the encoder delay, metadata frame, and final padding.
func (d *trimmedMP3) Stream(dst [][2]float64) (int, bool) {
	if d.length >= 0 {
		dst = dst[:min(len(dst), max(0, d.length-d.position))]
	}
	if len(dst) == 0 {
		return 0, false
	}
	n, ok := d.source.Stream(dst)
	d.position += n
	return n, ok
}

// Len reports only the music frames, excluding encoder padding.
func (d *trimmedMP3) Len() int { return max(0, d.length) }

// Position reports a position relative to the music, not the encoded padding.
func (d *trimmedMP3) Position() int { return d.position }

// Err reports underlying MP3 decoding errors.
func (d *trimmedMP3) Err() error { return d.source.Err() }

// Seek maps a music-frame position to the underlying decoder's padded position.
func (d *trimmedMP3) Seek(frame int) error {
	if frame < 0 || d.length >= 0 && frame > d.length {
		return fmt.Errorf("MP3 seek outside track")
	}
	if frame != d.length {
		if err := d.source.Seek(frame + d.start); err != nil {
			return err
		}
	}
	d.position = frame
	return nil
}

// Close releases the encoded source.
func (d *trimmedMP3) Close() error { return d.source.Close() }
