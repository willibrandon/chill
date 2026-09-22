package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	audiotag "github.com/dhowden/tag"
	"github.com/willibrandon/chill/internal/playback"
)

func probeLocalMedia(ctx context.Context, path string) (MediaItem, error) {
	if err := ctx.Err(); err != nil {
		return MediaItem{}, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return MediaItem{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return MediaItem{}, err
	}
	if !info.Mode().IsRegular() {
		return MediaItem{}, fmt.Errorf("not a regular audio file: %s", path)
	}
	f, err := os.Open(abs)
	if err != nil {
		return MediaItem{}, err
	}
	defer f.Close()
	stop := context.AfterFunc(ctx, func() { f.Close() })
	defer stop()
	item := MediaItem{Kind: MediaTrack, Source: filepath.Clean(abs), Title: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), AddedAt: time.Now().UTC()}
	item.ID = mediaID(item.Kind, item.Source)
	metadata, tagErr := readMediaTags(f)
	if tagErr == nil {
		item.Title = firstNonempty(metadata.Title(), item.Title)
		item.Artist = firstNonempty(metadata.Artist(), metadata.AlbumArtist())
		item.Album = metadata.Album()
		item.AlbumArtist = cmp.Or(metadata.AlbumArtist(), nativeTagValue(metadata, "albumartist", "album_artist", "album artist"))
		item.Compilation = tagBoolean(nativeTagValue(metadata, "compilation", "cpil", "TCMP"))
		if metadata.Format() != audiotag.MP4 {
			item.DiscNumber, _ = metadata.Disc()
			item.TrackNumber, _ = metadata.Track()
		}
		// Some Vorbis writers store n/total, which the tag reader's numeric
		// accessors don't parse. Retain the number from the original comment.
		if item.DiscNumber <= 0 {
			item.DiscNumber = nativeTagNumber(metadata, "discnumber", "disc")
		}
		if item.TrackNumber <= 0 {
			item.TrackNumber = nativeTagNumber(metadata, "tracknumber", "track")
		}
		item.Genre = metadata.Genre()
		item.EmbeddedLyrics = metadata.Lyrics()
		if picture := metadata.Picture(); picture != nil && len(picture.Data) > 0 && len(picture.Data) <= 16<<20 {
			ext := strings.ToLower(picture.Ext)
			if ext == "jpeg" {
				ext = "jpg"
			}
			if ext == "jpg" || ext == "png" || ext == "gif" || ext == "webp" {
				if base := cachedArtworkPath(item); base != "" {
					cached := strings.TrimSuffix(base, ".jpg") + "." + ext
					if err := os.WriteFile(cached, picture.Data, 0600); err == nil {
						item.Artwork = cached
					}
				}
			}
		}
	}
	f.Seek(0, io.SeekStart)
	var header [512]byte
	n, _ := f.Read(header[:])
	if n >= 8 && string(header[4:8]) == "ftyp" {
		// The pinned tag reader truncates MP4 track/disc fields to eight
		// bits, including its raw values. Read the original 16-bit fields
		// even when another atom prevented it from returning metadata.
		if track, disc, err := readMP4Numbers(f, info.Size()); err == nil {
			item.TrackNumber, item.DiscNumber = track, disc
		}
	}
	kind := playback.Format(header[:n])
	f.Seek(0, io.SeekStart)
	if kind == "wav" {
		readWAVMetadata(f, &item)
		f.Seek(0, io.SeekStart)
	}
	if kind != "" {
		decoder, format, err := playback.Decode(kind, f)
		if err == nil {
			if frames := decoder.Len(); frames > 0 && format.SampleRate > 0 {
				item.Duration = float64(frames) / float64(format.SampleRate)
			}
			decoder.Close()
			return item, ctx.Err()
		}
	}
	// Unknown codecs remain in the library even when probing isn't installed.
	external, err := probeLocalMediaExternal(ctx, path)
	if err != nil {
		return item, err
	}
	if external.Title != strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) {
		item.Title = external.Title
	}
	item.Artist = firstNonempty(item.Artist, external.Artist)
	item.Album = firstNonempty(item.Album, external.Album)
	item.AlbumArtist = cmp.Or(item.AlbumArtist, external.AlbumArtist)
	item.Compilation = item.Compilation || external.Compilation
	if item.DiscNumber == 0 {
		item.DiscNumber = external.DiscNumber
	}
	if item.TrackNumber == 0 {
		item.TrackNumber = external.TrackNumber
	}
	item.Genre = firstNonempty(item.Genre, external.Genre)
	item.Artwork = firstNonempty(item.Artwork, external.Artwork)
	item.EmbeddedLyrics = firstNonempty(item.EmbeddedLyrics, external.EmbeddedLyrics)
	item.Duration = external.Duration
	return item, ctx.Err()
}

func nativeTagValue(metadata audiotag.Metadata, names ...string) string {
	for _, name := range names {
		for key, value := range metadata.Raw() {
			if strings.EqualFold(key, name) {
				return fmt.Sprint(value)
			}
			if strings.HasPrefix(key, "TXX") {
				if text, ok := value.(*audiotag.Comm); ok && text != nil && strings.EqualFold(text.Description, name) {
					return strings.TrimRight(text.Text, "\x00")
				}
			}
		}
	}
	return ""
}

func nativeTagNumber(metadata audiotag.Metadata, names ...string) int {
	for _, name := range names {
		for key, value := range metadata.Raw() {
			if strings.EqualFold(key, name) {
				if n := tagNumber(fmt.Sprint(value)); n > 0 {
					return n
				}
			}
		}
	}
	return 0
}

func readMediaTags(source io.ReadSeeker) (metadata audiotag.Metadata, err error) {
	defer func() {
		if failure := recover(); failure != nil {
			metadata = nil
			err = fmt.Errorf("invalid audio metadata: %v", failure)
		}
	}()
	return audiotag.ReadFrom(source)
}

func nativeStreamTitle(data []byte) string {
	metadata, err := readMediaTags(bytes.NewReader(data))
	if err != nil || metadata.Title() == "" {
		return ""
	}
	title := metadata.Title()
	if artist := firstNonempty(metadata.Artist(), metadata.AlbumArtist()); artist != "" {
		title = artist + " - " + title
	}
	return title
}

// RIFF INFO is intentionally parsed without loading the audio payload. Chunks
// are bounded by the file size and metadata reads by a separate small limit.
func readWAVMetadata(f *os.File, item *MediaItem) {
	stat, err := f.Stat()
	if err != nil {
		return
	}
	var header [8]byte
	for at := int64(12); at+8 <= stat.Size(); {
		if _, err = f.ReadAt(header[:], at); err != nil {
			return
		}
		size := int64(binary.LittleEndian.Uint32(header[4:]))
		at += 8
		if size > stat.Size()-at {
			return
		}
		if string(header[:4]) == "LIST" && size >= 4 && size <= 1<<20 {
			data := make([]byte, int(size))
			if _, err = f.ReadAt(data, at); err != nil {
				return
			}
			if string(data[:4]) == "INFO" {
				for p := 4; p+8 <= len(data); {
					id := string(data[p : p+4])
					n := int(binary.LittleEndian.Uint32(data[p+4:]))
					p += 8
					if n > len(data)-p {
						break
					}
					value := strings.TrimSpace(strings.TrimRight(string(data[p:p+n]), "\x00"))
					switch id {
					case "INAM":
						item.Title = firstNonempty(value, item.Title)
					case "IART":
						item.Artist = value
					case "IPRD":
						item.Album = value
					case "ITRK", "IPRT":
						item.TrackNumber = tagNumber(value)
					case "IGNR":
						item.Genre = value
					case "ILYR":
						item.EmbeddedLyrics = value
					}
					p += n + n%2
				}
			}
		}
		at += size + size%2
	}
}
