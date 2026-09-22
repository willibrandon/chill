package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/willibrandon/chill/internal/playback"
)

// TestMalformedNativeAudioReturnsPlaybackError checks remote/local Ogg failures leave playback usable.
func TestMalformedNativeAudioReturnsPlaybackError(t *testing.T) {
	withConfigDir(t)
	useTestAudio(t)
	t.Setenv("PATH", t.TempDir())
	data := make([]byte, 40)
	copy(data, "OggS")
	copy(data[27:], "\x01vorbis")
	path := filepath.Join(t.TempDir(), "malformed.ogg")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer server.Close()
	for _, source := range []string{path, server.URL, "testdata/audio/tone.wav"} {
		p, err := newPCMPlayer(50, false, false, 0, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.load(source); err != nil {
			p.close()
			t.Fatal(err)
		}
		select {
		case event := <-p.events():
			p.close()
			if source == "testdata/audio/tone.wav" && !event.loaded || source != "testdata/audio/tone.wav" && event.err == "" {
				t.Fatalf("unexpected playback result: %+v", event)
			}
		case <-time.After(3 * time.Second):
			p.close()
			t.Fatal("malformed decoder hung")
		}
	}
	var out bytes.Buffer
	if err := runDoctor([]string{"--stream", path}, &out); err == nil {
		t.Fatalf("doctor accepted malformed audio: %s", out.String())
	}
}

// TestNativeChainedHTTPRateChanges checks song boundaries with changing sample rates and tags.
func TestNativeChainedHTTPRateChanges(t *testing.T) {
	withConfigDir(t)
	first, err := os.ReadFile("testdata/audio/tone.ogg")
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile("testdata/audio/tone-48000.ogg")
	if err != nil {
		t.Fatal(err)
	}
	chain := append(first, second...)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write(chain) }))
	defer server.Close()
	t.Setenv("PATH", t.TempDir())
	var titles []string
	r, _, err := openPCM(t.Context(), server.URL, 0, false, defaultAudioSettings(), func(title string) { titles = append(titles, title) })
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	// The granule lengths are 11072 at 44.1 kHz and 12032 at 48 kHz.
	// The second link is normalized to the first link's rate before output.
	if len(data)/8 != 24084 || requests.Load() != 1 {
		t.Fatalf("chained stream frames %d, connections %d", len(data)/8, requests.Load())
	}
	if !slices.Equal(titles, []string{"Chill - Native fixture", "Chill - Second song"}) {
		t.Fatalf("chain titles %q", titles)
	}
}

// TestNativeMP3EncoderVariants checks VBR, CBR, MPEG-2 and missing gapless tags against FFmpeg lengths.
func TestNativeMP3EncoderVariants(t *testing.T) {
	withConfigDir(t)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("requires FFmpeg to generate encoder variants")
	}
	for _, variant := range []struct {
		name string
		rate int
		args []string
	}{
		{"vbr", 48000, []string{"-q:a", "4"}},
		{"mpeg2", 22050, []string{"-b:a", "64k"}},
		{"without-xing", 44100, []string{"-write_xing", "0"}},
	} {
		t.Run(variant.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "variant.mp3")
			args := []string{"-v", "error", "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:sample_rate=%d:duration=0.25", variant.rate), "-c:a", "libmp3lame"}
			args = append(args, variant.args...)
			args = append(args, path)
			if output, err := exec.CommandContext(t.Context(), ffmpeg, args...).CombinedOutput(); err != nil {
				t.Fatalf("generate MP3: %v %s", err, output)
			}
			want, err := exec.CommandContext(t.Context(), ffmpeg, "-v", "error", "-i", path, "-ac", "2", "-ar", "48000", "-f", "f32le", "-").Output()
			if err != nil {
				t.Fatal(err)
			}
			r, _, err := openPCM(t.Context(), path, 0, true, defaultAudioSettings(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			got, err := io.ReadAll(r)
			if err != nil || math.Abs(float64(len(got)-len(want))) > 8 {
				t.Fatalf("MP3 %s: %d frames, want %d: %v", variant.name, len(got)/8, len(want)/8, err)
			}
		})
	}
}

type panicTagSource struct{}

// Read reproduces a tag reader failure at untrusted metadata input.
func (panicTagSource) Read([]byte) (int, error) { panic("metadata") }

// Seek reproduces a malformed metadata seek failure.
func (panicTagSource) Seek(int64, int) (int64, error) { panic("metadata") }

// TestMetadataPanicIsContained checks metadata scanning cannot terminate the daemon either.
func TestMetadataPanicIsContained(t *testing.T) {
	if _, err := readMediaTags(panicTagSource{}); err == nil {
		t.Fatal("metadata panic escaped containment")
	}
}

// TestNativeStreamTagsAndMP3Duration checks codec tags and trimmed duration without helpers.
func TestNativeStreamTagsAndMP3Duration(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	for _, ext := range []string{"ogg", "mp3", "flac"} {
		t.Run(ext, func(t *testing.T) {
			data, err := os.ReadFile("testdata/audio/tone." + ext)
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write(data) }))
			defer server.Close()
			var titles []string
			r, _, err := openPCM(t.Context(), server.URL, 0, false, defaultAudioSettings(), func(title string) { titles = append(titles, title) })
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			pcm, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(titles, []string{"Chill - Native fixture"}) || requests.Load() != 1 {
				t.Fatalf("native tags %q, requests %d", titles, requests.Load())
			}
			if ext == "mp3" {
				if len(pcm)/8 != 12000 {
					t.Fatalf("MP3 output has %d frames", len(pcm)/8)
				}
				item, err := probeLocalMedia(t.Context(), "testdata/audio/tone.mp3")
				if err != nil || item.Duration != .25 {
					t.Fatalf("MP3 duration %g: %v", item.Duration, err)
				}
			}
		})
	}
}

// TestMP3NativeAlignmentMatchesFFmpeg compares trimmed samples against the previous decoder.
func TestMP3NativeAlignmentMatchesFFmpeg(t *testing.T) {
	withConfigDir(t)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("requires FFmpeg reference decoder")
	}
	want, err := exec.CommandContext(t.Context(), ffmpeg, "-v", "error", "-i", "testdata/audio/tone.mp3", "-ac", "2", "-ar", "48000", "-f", "f32le", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := openPCM(t.Context(), "testdata/audio/tone.mp3", 0, true, defaultAudioSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || len(got) != 12000*8 {
		t.Fatalf("MP3 length %d, reference %d", len(got)/8, len(want)/8)
	}
	var power float64
	for at := 256 * 8; at < len(got)-256*8; at += 4 {
		a := math.Float32frombits(binary.LittleEndian.Uint32(got[at:]))
		b := math.Float32frombits(binary.LittleEndian.Uint32(want[at:]))
		power += float64(a-b) * float64(a-b)
	}
	if rms := math.Sqrt(power / float64(len(got)/4-1024)); rms > .0002 {
		t.Fatalf("native MP3 alignment differs: error RMS %g", rms)
	}
}

// TestNativeMetadataDeliveredAfterLoaded checks early codec tags survive player initialization.
func TestNativeMetadataDeliveredAfterLoaded(t *testing.T) {
	withConfigDir(t)
	useTestAudio(t)
	t.Setenv("PATH", t.TempDir())
	raw, err := newPCMPlayer(50, false, false, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.close()
	if err := raw.load("testdata/audio/tone.ogg"); err != nil {
		t.Fatal(err)
	}
	loaded := false
	for {
		select {
		case event := <-raw.events():
			if event.err != "" || event.ended {
				t.Fatalf("ended before metadata: %+v", event)
			}
			if event.loaded {
				loaded = true
			}
			if event.nowPlaying != nil {
				if !loaded || event.nowPlaying.Raw != "Chill - Native fixture" {
					t.Fatalf("metadata ordering: %+v", event)
				}
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatal("native metadata missing")
		}
	}
}

type policyAudioDevice struct{ settings playback.Settings }

// Start leaves rendering under the test's control.
func (*policyAudioDevice) Start() error { return nil }

// Close releases the manual test device.
func (*policyAudioDevice) Close() {}

// Info reproduces the native backend's drain latency.
func (d *policyAudioDevice) Info() playback.DeviceInfo {
	return playback.DeviceInfo{SampleRate: d.settings.SampleRate, Latency: 100 * time.Millisecond}
}

// TestPreloadPolicyBeforeAudibleHandoff reproduces queue edits before and after EOF authorization.
func TestPreloadPolicyBeforeAudibleHandoff(t *testing.T) {
	for _, action := range []string{"queue-clear", "shuffle", "repeat", "queue-remove"} {
		for _, authorize := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/authorized=%t", action, authorize), func(t *testing.T) {
				withConfigDir(t)
				useTestAudio(t)
				var render func([]byte)
				openAudioOutput = func(s playback.Settings, v int, m, paused bool) (*playback.Output, error) {
					return playback.NewOutput(s, func(s playback.Settings, callback func([]byte)) (playback.Device, error) {
						render = callback
						return &policyAudioDevice{s}, nil
					}, v, m, paused)
				}
				raw, err := newPCMPlayer(50, false, false, 0, true)
				if err != nil {
					t.Fatal(err)
				}
				p := raw.(*pcmPlayer)
				defer p.close()
				first, err := (MediaItem{Kind: MediaTrack, Source: "testdata/audio/tone.wav"}).normalized()
				if err != nil {
					t.Fatal(err)
				}
				second, err := (MediaItem{Kind: MediaTrack, Source: stereoFixture(t, 1)}).normalized()
				if err != nil {
					t.Fatal(err)
				}
				library, err := loadLibrary()
				if err != nil {
					t.Fatal(err)
				}
				library.Queue = []MediaItem{second}
				d := &Daemon{player: p, current: &first, library: library}
				if err := p.load(first.Source); err != nil {
					t.Fatal(err)
				}
				d.preloadNextLocal()
				p.decoderMu.Lock()
				preload, active := p.prepared, p.active.id
				p.decoderMu.Unlock()
				select {
				case <-preload.ready:
				case <-time.After(3 * time.Second):
					t.Fatal("preload not ready")
				}
				var handoff playerEvent
				deadline := time.After(3 * time.Second)
				buffer := make([]byte, 480*8)
				for handoff.handoff == 0 {
					select {
					case event := <-p.events():
						if event.err != "" || event.ended {
							t.Fatalf("handoff missing: %+v", event)
						}
						if event.handoff != 0 {
							handoff = event
						}
					case <-time.After(time.Millisecond):
						render(buffer)
					case <-deadline:
						t.Fatal("handoff timed out")
					}
				}
				preload.mu.Lock()
				started := preload.started
				preload.mu.Unlock()
				if started {
					t.Fatal("preload started without controller authorization")
				}
				if authorize {
					d.playerEvent(handoff)
				}
				arg := map[string]string{"shuffle": "on", "repeat": "one", "queue-remove": "1"}[action]
				if result := d.queueCommand(action, arg); strings.Contains(result, `"ok":false`) {
					t.Fatal(result)
				}
				if preload.ctx.Err() == nil {
					t.Fatal("queue edit did not invalidate preload")
				}
				d.playerEvent(handoff) // A queued EOF notification must recheck current policy.
				for {
					select {
					case event := <-p.events():
						if event.err != "" {
							t.Fatal(event.err)
						}
						if event.ended {
							return
						}
					case <-time.After(time.Millisecond):
						render(buffer)
						if id, _ := p.output.Position(); id != 0 && id != active {
							t.Fatalf("stale preload %d became audible", id)
						}
					case <-deadline:
						t.Fatal("completion timed out")
					}
				}
			})
		}
	}
}

func copyTestExecutable(t *testing.T, path string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPortableExtractorDiscovery checks legacy portable_config remains optional and lower priority.
func TestPortableExtractorDiscovery(t *testing.T) {
	withConfigDir(t)
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("MPV_HOME", t.TempDir())
	copyTestExecutable(t, filepath.Join(dir, "mpv"))
	portable := copyTestExecutable(t, filepath.Join(dir, "portable_config", "yt-dlp"))
	if got, err := toolPath("yt-dlp"); err != nil || got != portable {
		t.Fatalf("portable discovery: %q %v", got, err)
	}
	preferred := copyTestExecutable(t, filepath.Join(dir, "yt-dlp"))
	if got, err := toolPath("yt-dlp"); err != nil || got != preferred {
		t.Fatalf("PATH priority: %q %v", got, err)
	}
	mpv := filepath.Join(dir, "mpv")
	if runtime.GOOS == "windows" {
		mpv += ".exe"
	}
	if err := os.Remove(mpv); err != nil {
		t.Fatal(err)
	}
	if got, err := toolPath("yt-dlp"); err != nil || got != preferred {
		t.Fatalf("optional legacy player: %q %v", got, err)
	}
}

// TestWebsiteUsesConfiguredFFmpeg checks both children honor a path containing spaces outside PATH.
func TestWebsiteUsesConfiguredFFmpeg(t *testing.T) {
	withConfigDir(t)
	ffmpeg := copyTestExecutable(t, filepath.Join(t.TempDir(), "tools with spaces", "ffmpeg"))
	ytdlp := copyTestExecutable(t, filepath.Join(t.TempDir(), "yt-dlp"))
	if err := writeJSON(toolsPath(), ToolSettings{FFmpeg: ffmpeg, YTDLP: ytdlp}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CHILL_WEBSITE_HELPER_FFMPEG", ffmpeg)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	r, _, err := openWebsitePCM(ctx, "https://example.com/watch?v=test", defaultAudioSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(data, []byte("configured FFmpeg reached extractor")) {
		t.Fatalf("website tool settings: %q %v", data, err)
	}
}

func init() {
	ffmpeg := os.Getenv("CHILL_WEBSITE_HELPER_FFMPEG")
	if ffmpeg == "" {
		return
	}
	if slices.Contains(os.Args[1:], "--ignore-config") {
		at := slices.Index(os.Args[1:], "--ffmpeg-location") + 1
		if at < 1 || at+1 >= len(os.Args) || os.Args[at+1] != ffmpeg {
			fmt.Fprintln(os.Stderr, "extractor did not receive configured FFmpeg")
			os.Exit(1)
		}
		at = slices.Index(os.Args[1:], "--downloader-args") + 1
		if at < 1 || at+1 >= len(os.Args) || os.Args[at+1] != "ffmpeg_i:-rw_timeout 15000000" {
			fmt.Fprintln(os.Stderr, "extractor downloader has no idle-read timeout")
			os.Exit(1)
		}
		fmt.Fprint(os.Stdout, "configured FFmpeg reached extractor")
		os.Exit(0)
	}
	if slices.Contains(os.Args[1:], "-nostdin") {
		io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	}
}
