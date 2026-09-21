// Package streammeta extracts now-playing data from live audio streams.
package streammeta

import (
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// NowPlaying is a normalized live-stream metadata update.
type NowPlaying struct {
	// Raw is the unmodified stream title.
	Raw string `json:"raw"`
	// Artist is populated when the title follows an artist-title form.
	Artist string `json:"artist,omitempty"`
	// Title is the song, program, or complete stream title.
	Title string `json:"title"`
	// Program is a station-supplied show or programme name when available.
	Program string `json:"program,omitempty"`
	// Artwork is the current station or track artwork URL.
	Artwork string `json:"artwork,omitempty"`
	// ChangedAt is when this title became current.
	ChangedAt time.Time `json:"changed_at,omitzero"`
}

// Parse converts a raw stream title into structured fields.
func Parse(raw string) NowPlaying {
	raw = clean(raw)
	result := NowPlaying{Raw: raw, Title: raw}
	for _, separator := range []string{" — ", " – ", " - "} {
		if artist, title, ok := strings.Cut(raw, separator); ok {
			artist, title = clean(artist), clean(title)
			if artist != "" && title != "" {
				result.Artist, result.Title = artist, title
				break
			}
		}
	}
	return result
}

func clean(value string) string {
	if !utf8.ValidString(value) {
		if decoded, err := charmap.Windows1252.NewDecoder().String(value); err == nil {
			value = decoded
		}
	}
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	value = strings.ReplaceAll(value, "\x00", "")
	return value
}

// Title extracts StreamTitle from one ICY metadata block.
func Title(block string) string {
	title, _ := metadataTitle(block)
	return title
}

func metadataTitle(block string) (string, bool) {
	for len(block) > 0 {
		key, rest, found := strings.Cut(block, "=")
		if !found {
			return "", false
		}
		key = strings.TrimSpace(key)
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			return "", false
		}
		quote := byte(0)
		if rest[0] == '\'' || rest[0] == '"' {
			quote, rest = rest[0], rest[1:]
		}
		var value string
		if quote != 0 {
			if end := strings.IndexByte(rest, quote); end >= 0 {
				value, block = rest[:end], strings.TrimLeft(rest[end+1:], "; \t")
			} else {
				value, block = rest, ""
			}
		} else if end := strings.IndexByte(rest, ';'); end >= 0 {
			value, block = rest[:end], strings.TrimLeft(rest[end+1:], " \t")
		} else {
			value, block = rest, ""
		}
		if strings.EqualFold(key, "StreamTitle") {
			return clean(value), true
		}
	}
	return "", false
}

// Reader removes interleaved metadata while passing audio to a decoder.
type Reader struct {
	source    io.ReadCloser
	interval  int
	remaining int
	onTitle   func(string)
}

// NewReader wraps an ICY response body with its advertised metadata interval.
func NewReader(source io.ReadCloser, interval int, onTitle func(string)) *Reader {
	return &Reader{source: source, interval: interval, remaining: interval, onTitle: onTitle}
}

// Read returns audio bytes without crossing into a metadata block.
func (r *Reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		if err := r.consume(); err != nil {
			return 0, err
		}
		r.remaining = r.interval
	}
	p = p[:min(len(p), r.remaining)]
	n, err := r.source.Read(p)
	r.remaining -= n
	return n, err
}

func (r *Reader) consume() error {
	var length [1]byte
	if _, err := io.ReadFull(r.source, length[:]); err != nil {
		return err
	}
	size := int(length[0]) * 16
	if size == 0 {
		return nil
	}
	block := make([]byte, size)
	if _, err := io.ReadFull(r.source, block); err != nil {
		return err
	}
	if title, found := metadataTitle(strings.TrimRight(string(block), "\x00")); found && r.onTitle != nil {
		r.onTitle(title)
	}
	return nil
}

// Close closes the underlying live response body.
func (r *Reader) Close() error { return r.source.Close() }
