package main

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func taggedFolderFixture(t *testing.T, path, disc, track string, extraTags ...string) {
	t.Helper()
	data, err := os.ReadFile("testdata/audio/tone.flac")
	if err != nil {
		t.Fatal(err)
	}
	// Replace the fixture's Vorbis comments, retaining its native audio frames.
	for at := 4; at+4 <= len(data); {
		size := int(data[at+1])<<16 | int(data[at+2])<<8 | int(data[at+3])
		if data[at]&0x7f == 4 {
			tags := []string{"TITLE=Folder fixture", "ALBUM=Album", "DISCNUMBER=" + disc, "TRACKNUMBER=" + track}
			tags = append(tags, extraTags...)
			comments := binary.LittleEndian.AppendUint32(nil, 0)
			comments = binary.LittleEndian.AppendUint32(comments, uint32(len(tags)))
			for _, tag := range tags {
				comments = binary.LittleEndian.AppendUint32(comments, uint32(len(tag)))
				comments = append(comments, tag...)
			}
			out := slices.Clone(data[:at])
			out = append(out, data[at], byte(len(comments)>>16), byte(len(comments)>>8), byte(len(comments)))
			out = append(out, comments...)
			out = append(out, data[at+4+size:]...)
			if err := os.WriteFile(path, out, 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
		at += 4 + size
	}
	t.Fatal("FLAC fixture has no comments")
}

// TestFolderKeepsAlbumIdentitiesTogether checks same-title releases and compilation albums.
func TestFolderKeepsAlbumIdentitiesTogether(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	for _, test := range []struct {
		name string
		tags [][]string
		want []string
	}{
		{"album artists", [][]string{
			{"ARTIST=Guest One", "ALBUMARTIST=Artist A"}, {"ARTIST=Guest Two", "ALBUMARTIST=Artist A"},
			{"ARTIST=Guest Three", "ALBUMARTIST=Artist B"}, {"ARTIST=Guest Four", "ALBUMARTIST=Artist B"},
		}, []string{"A1.flac", "A2.flac", "B1.flac", "B2.flac"}},
		{"artist fallback", [][]string{
			{"ARTIST=Artist A"}, {"ARTIST=Artist A"}, {"ARTIST=Artist B"}, {"ARTIST=Artist B"},
		}, []string{"A1.flac", "A2.flac", "B1.flac", "B2.flac"}},
		{"album artist alias", [][]string{
			{"ARTIST=Guest", "ALBUM_ARTIST=Artist A"}, {"ARTIST=Guest", "ALBUM_ARTIST=Artist A"},
			{"ARTIST=Guest", "ALBUM_ARTIST=Artist B"}, {"ARTIST=Guest", "ALBUM_ARTIST=Artist B"},
		}, []string{"A1.flac", "A2.flac", "B1.flac", "B2.flac"}},
		{"naturally equal album artists", [][]string{
			{"ALBUMARTIST=Artist 01"}, {"ALBUMARTIST=Artist 01"}, {"ALBUMARTIST=Artist 1"}, {"ALBUMARTIST=Artist 1"},
		}, []string{"A1.flac", "A2.flac", "B1.flac", "B2.flac"}},
		{"compilation album artist", [][]string{
			{"ARTIST=Artist A", "ALBUMARTIST=Various Artists"}, {"ARTIST=Artist A", "ALBUMARTIST=Various Artists"},
			{"ARTIST=Artist B", "ALBUMARTIST=Various Artists"}, {"ARTIST=Artist B", "ALBUMARTIST=Various Artists"},
		}, []string{"A1.flac", "B1.flac", "A2.flac", "B2.flac"}},
		{"compilation flag", [][]string{
			{"ARTIST=Artist A", "COMPILATION=1"}, {"ARTIST=Artist A", "COMPILATION=1"},
			{"ARTIST=Artist B", "COMPILATION=1"}, {"ARTIST=Artist B", "COMPILATION=1"},
		}, []string{"A1.flac", "B1.flac", "A2.flac", "B2.flac"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			for i, name := range []string{"A1.flac", "A2.flac", "B1.flac", "B2.flac"} {
				tags := append([]string{"ALBUM=Greatest Hits"}, test.tags[i]...)
				track := []string{"1", "2", "1", "2"}[i]
				taggedFolderFixture(t, filepath.Join(dir, name), "1", track, tags...)
			}
			items, err := loadMediaInputs(t.Context(), []string{dir})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, item := range items {
				got = append(got, filepath.Base(item.Source))
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("album order = %v, want %v", got, test.want)
			}
			library := emptyLibrary()
			library.Queue = items
			if err := library.commit(); err != nil {
				t.Fatal(err)
			}
			saved, err := loadLibrary()
			if err != nil || len(saved.Queue) != len(items) {
				t.Fatalf("load folder queue: %v", err)
			}
			for i, item := range saved.Queue {
				if item.Source != items[i].Source || item.AlbumArtist != items[i].AlbumArtist || item.Compilation != items[i].Compilation {
					t.Fatalf("saved album identity changed: %+v", item)
				}
			}
		})
	}
}

// TestFolderKeepsNaturallyEqualDirectoriesTogether checks path ties precede track metadata.
func TestFolderKeepsNaturallyEqualDirectoriesTogether(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	want := []string{filepath.Join("Disc 01", "1.flac"), filepath.Join("Disc 01", "2.flac"), filepath.Join("Disc 1", "1.flac"), filepath.Join("Disc 1", "2.flac")}
	for _, name := range want {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		taggedFolderFixture(t, path, "1", strings.TrimSuffix(filepath.Base(path), ".flac"))
	}
	items, err := loadMediaInputs(t.Context(), []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range items {
		path, _ := filepath.Rel(dir, item.Source)
		got = append(got, path)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("directory order = %v, want %v", got, want)
	}
}

// TestLocalTrackNumbersAcrossCodecs checks native and FFprobe disc/track metadata parsing.
func TestLocalTrackNumbersAcrossCodecs(t *testing.T) {
	withConfigDir(t)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("requires FFmpeg to generate tagged fixtures")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("requires FFprobe to verify fallback metadata")
	}
	for _, extension := range []string{"mp3", "flac", "ogg", "m4a", "wav"} {
		t.Run(extension, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tagged."+extension)
			args := []string{"-v", "error", "-i", "testdata/audio/tone.wav", "-metadata", "track=2/12", "-metadata", "disc=2/3", "-metadata", "album_artist=Various Artists", "-metadata", "compilation=1", path}
			if output, err := exec.CommandContext(t.Context(), ffmpeg, args...).CombinedOutput(); err != nil {
				t.Fatalf("generate tags: %v %s", err, output)
			}
			for _, probe := range []func(context.Context, string) (MediaItem, error){probeLocalMedia, probeLocalMediaExternal} {
				item, err := probe(t.Context(), path)
				// RIFF INFO retains a track number, but has no disc-number field.
				if err != nil || item.TrackNumber != 2 || extension != "wav" && item.DiscNumber != 2 {
					t.Fatalf("disc/track metadata missing: %+v %v", item, err)
				}
				if extension != "wav" && (item.AlbumArtist != "Various Artists" || !item.Compilation) {
					t.Fatalf("album identity missing: %+v", item)
				}
			}
		})
	}
}

// TestFolderTrackOrderAndExplicitOrder checks native disc/track tags without reordering explicit inputs.
func TestFolderTrackOrderAndExplicitOrder(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	first, second, third := filepath.Join(dir, "z.flac"), filepath.Join(dir, "m.flac"), filepath.Join(dir, "a.flac")
	taggedFolderFixture(t, first, "1/2", "2/12")
	taggedFolderFixture(t, second, "1/2", "10/12")
	taggedFolderFixture(t, third, "2/2", "1/12")
	for _, test := range []struct {
		name   string
		inputs []string
		want   []string
	}{
		{"folder", []string{dir}, []string{first, second, third}},
		{"explicit", []string{third, second, first}, []string{third, second, first}},
		{"mixed", []string{third, dir, first}, []string{third, first, second, third, first}},
	} {
		t.Run(test.name, func(t *testing.T) {
			items, err := loadMediaInputs(t.Context(), test.inputs)
			if err != nil {
				t.Fatal(err)
			}
			var sources []string
			for _, item := range items {
				sources = append(sources, item.Source)
				if item.DiscNumber == 0 || item.TrackNumber == 0 || item.Duration == 0 {
					t.Fatalf("missing native metadata: %+v", item)
				}
			}
			if !slices.Equal(sources, test.want) {
				t.Fatalf("order = %v, want %v", sources, test.want)
			}
		})
	}
	playlist := filepath.Join(t.TempDir(), "order.m3u8")
	if err := os.WriteFile(playlist, []byte(third+"\n"+first+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := loadMediaInputs(t.Context(), []string{playlist})
	if err != nil || len(items) != 2 || items[0].Source != third || items[1].Source != first {
		t.Fatalf("playlist order changed: %+v %v", items, err)
	}
}

// TestFolderNaturalOrderAndCancellation checks untagged names, recursive discovery, and cancellation.
func TestFolderNaturalOrderAndCancellation(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	data, err := os.ReadFile("testdata/audio/tone.wav")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2.wav", "10.wav", filepath.Join("Disc 2", "2.wav"), filepath.Join("Disc 10", "1.wav")}
	for _, name := range want {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	items, err := loadMediaInputs(t.Context(), []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range items {
		name, _ := filepath.Rel(dir, item.Source)
		got = append(got, name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("folder order = %v", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := loadMediaInputs(ctx, []string{dir}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled folder: %v", err)
	}
	if _, err := loadMediaInputs(t.Context(), []string{t.TempDir()}); err == nil {
		t.Fatal("empty folder succeeded")
	}
}

// TestFolderSkipsUnreadableFiles checks permission errors are local to a folder entry.
func TestFolderSkipsUnreadableFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions")
	}
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	bad := filepath.Join(dir, "unreadable.flac")
	taggedFolderFixture(t, bad, "1", "1")
	if err := os.Chmod(bad, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(bad, 0600) })
	if f, err := os.Open(bad); err == nil {
		f.Close()
		t.Skip("process can bypass file permissions")
	}
	if _, err := loadMediaInputs(t.Context(), []string{dir}); err == nil || !strings.Contains(err.Error(), "no readable audio files") {
		t.Fatalf("unreadable folder result: %v", err)
	}
	good := filepath.Join(dir, "good.flac")
	taggedFolderFixture(t, good, "1", "2")
	items, err := loadMediaInputs(t.Context(), []string{dir})
	if err != nil || len(items) != 1 || items[0].Source != good {
		t.Fatalf("unreadable file aborted folder: %+v %v", items, err)
	}
	if _, err := loadMediaInputs(t.Context(), []string{good, bad}); err == nil {
		t.Fatal("explicit unreadable file was silently omitted")
	}
}
