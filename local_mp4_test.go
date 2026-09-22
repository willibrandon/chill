package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func mp4TestAtom(name string, payload []byte) []byte {
	atom := binary.BigEndian.AppendUint32(nil, uint32(8+len(payload)))
	atom = append(atom, name...)
	return append(atom, payload...)
}

func mp4TestNumberAtom(name string, number uint16) []byte {
	data := make([]byte, 8) // implicit data type and locale
	data = binary.BigEndian.AppendUint16(data, 0)
	data = binary.BigEndian.AppendUint16(data, number)
	data = binary.BigEndian.AppendUint16(data, 65535)
	data = binary.BigEndian.AppendUint16(data, 0)
	return mp4TestAtom(name, mp4TestAtom("data", data))
}

func mp4TestMetadata(track, disc uint16) []byte {
	items := append(mp4TestNumberAtom("trkn", track), mp4TestNumberAtom("disk", disc)...)
	meta := mp4TestAtom("meta", append(make([]byte, 4), mp4TestAtom("ilst", items)...))
	return mp4TestAtom("moov", mp4TestAtom("udta", meta))
}

// TestMP4NumberBoundaries checks all 16 bits are retained without helper executables.
func TestMP4NumberBoundaries(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	for _, value := range []uint16{0, 1, 255, 256, 257, 300, 65535} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			data := mp4TestAtom("ftyp", []byte("M4A \x00\x00\x00\x00M4A isom"))
			data = append(data, mp4TestMetadata(value, value)...)
			path := filepath.Join(t.TempDir(), "metadata.m4a")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			item, err := probeLocalMedia(t.Context(), path)
			if err != nil || item.TrackNumber != int(value) || item.DiscNumber != int(value) {
				t.Fatalf("MP4 number %d: track=%d disc=%d error=%v", value, item.TrackNumber, item.DiscNumber, err)
			}
		})
	}
}

// TestMP4AtomBoundaries checks malformed lengths, extended sizes, and opaque media payloads.
func TestMP4AtomBoundaries(t *testing.T) {
	metadata := mp4TestMetadata(257, 258)
	extended := binary.BigEndian.AppendUint32(nil, 1)
	extended = append(extended, "moov"...)
	extended = binary.BigEndian.AppendUint64(extended, uint64(len(metadata)+8))
	extended = append(extended, metadata[8:]...)
	zeroSize := bytes.Clone(metadata)
	clear(zeroSize[:4])
	media := mp4TestAtom("mdat", mp4TestNumberAtom("trkn", 5))
	for _, data := range [][]byte{metadata, extended, zeroSize, append(media, metadata...)} {
		track, disc, err := readMP4Numbers(bytes.NewReader(data), int64(len(data)))
		if err != nil || track != 257 || disc != 258 {
			t.Fatalf("MP4 container: track=%d disc=%d error=%v", track, disc, err)
		}
	}
	oversized := bytes.Clone(metadata)
	binary.BigEndian.PutUint32(oversized[8:12], uint32(len(metadata))) // udta exceeds moov
	huge := bytes.Clone(extended)
	binary.BigEndian.PutUint64(huge[8:16], ^uint64(0))
	shortData := mp4TestAtom("moov", mp4TestAtom("meta", append(make([]byte, 4), mp4TestAtom("ilst", mp4TestAtom("trkn", mp4TestAtom("data", make([]byte, 11))))...)))
	for name, data := range map[string][]byte{
		"truncated": metadata[:len(metadata)-1], "parent bounds": oversized,
		"overflow": huge, "short header": {0, 0, 0, 8},
		"short payload": shortData, "zero progress": {0, 0, 0, 7, 'm', 'o', 'o', 'v'},
		"atom limit": bytes.Repeat(mp4TestAtom("free", nil), 10001),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := readMP4Numbers(bytes.NewReader(data), int64(len(data))); err == nil {
				t.Fatal("malformed MP4 metadata accepted")
			}
		})
	}
}

// TestMP4FullWidthNumbers checks generated M4A metadata with and without external probing.
func TestMP4FullWidthNumbers(t *testing.T) {
	withConfigDir(t)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("requires FFmpeg to generate M4A fixtures")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "track.m4a")
	args := []string{"-v", "error", "-i", "testdata/audio/tone.wav", "-metadata", "track=257/300", "-metadata", "disc=258/300", path}
	if output, err := exec.CommandContext(t.Context(), ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("generate MP4 tags: %v %s", err, output)
	}
	for _, helpers := range []bool{true, false} {
		t.Run(map[bool]string{true: "with helpers", false: "without helpers"}[helpers], func(t *testing.T) {
			if !helpers {
				t.Setenv("PATH", t.TempDir())
			}
			item, err := probeLocalMedia(t.Context(), path)
			if err != nil || item.TrackNumber != 257 || item.DiscNumber != 258 {
				t.Fatalf("full-width MP4 numbers lost: track=%d disc=%d error=%v", item.TrackNumber, item.DiscNumber, err)
			}
		})
	}
	// A high-numbered file must stay after track 2 when constructing and
	// persisting a folder queue, regardless of its earlier filename.
	second := filepath.Join(dir, "z-second.m4a")
	args = []string{"-v", "error", "-i", path, "-c", "copy", "-metadata", "track=2/300", second}
	if output, err := exec.CommandContext(t.Context(), ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("generate second track: %v %s", err, output)
	}
	t.Setenv("PATH", t.TempDir())
	items, err := loadMediaInputs(t.Context(), []string{dir})
	if err != nil || len(items) != 2 || items[0].Source != second || items[1].TrackNumber != 257 {
		t.Fatalf("MP4 folder order: %+v %v", items, err)
	}
	library := emptyLibrary()
	library.Queue = items
	if err := library.commit(); err != nil {
		t.Fatal(err)
	}
	saved, err := loadLibrary()
	if err != nil || len(saved.Queue) != 2 || saved.Queue[1].TrackNumber != 257 || saved.Queue[1].DiscNumber != 258 {
		t.Fatalf("persisted MP4 metadata: %+v %v", saved, err)
	}
}
