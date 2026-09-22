package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/willibrandon/chill/internal/playback"
)

// TestNativeCodecsWithoutHelpers checks local playback and metadata with no
// decoder processes available. Fixtures contain only a generated sine wave.
func TestNativeCodecsWithoutHelpers(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	for _, ext := range []string{"wav", "mp3", "flac", "ogg"} {
		t.Run(ext, func(t *testing.T) {
			path := filepath.Join("testdata", "audio", "tone."+ext)
			r, _, err := openPCM(t.Context(), path, 0, true, defaultAudioSettings(), func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			data, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if len(data) < 48000*8/5 {
				t.Fatalf("short PCM %d", len(data))
			}
			item, err := probeLocalMedia(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			if item.Duration <= 0 {
				t.Fatalf("duration unavailable: %+v", item)
			}
			if ext != "wav" && (item.Title != "Native fixture" || item.Artist != "Chill") {
				t.Fatalf("metadata: %+v", item)
			}
		})
	}
}

// TestDoctorExplicitConfigurationAndOutputErrors checks requested capabilities fail clearly.
func TestDoctorExplicitConfigurationAndOutputErrors(t *testing.T) {
	withConfigDir(t)
	useTestAudio(t)
	t.Setenv("PATH", t.TempDir())
	if err := writeJSON(toolsPath(), ToolSettings{FFmpeg: filepath.Join(t.TempDir(), "missing")}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runDoctor(nil, &out); err == nil || !strings.Contains(out.String(), "[FAIL] ffmpeg: configured") {
		t.Fatalf("invalid override: %v %s", err, out.String())
	}
	if err := os.Remove(toolsPath()); err != nil {
		t.Fatal(err)
	}
	openAudioOutput = func(playback.Settings, int, bool, bool) (*playback.Output, error) {
		return nil, fmt.Errorf("device unavailable")
	}
	out.Reset()
	if err := runDoctor([]string{"--audio"}, &out); err == nil || !strings.Contains(out.String(), "[FAIL] audio output: device unavailable") {
		t.Fatalf("explicit output: %v %s", err, out.String())
	}
}

// TestNativeHTTPSeekAndRangeFallback checks finite seeks with and without valid server ranges.
func TestNativeHTTPSeekAndRangeFallback(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	data, err := os.ReadFile("testdata/audio/tone.wav")
	if err != nil {
		t.Fatal(err)
	}
	local, _, err := openPCM(t.Context(), "testdata/audio/tone.wav", 125*time.Millisecond, true, defaultAudioSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := io.ReadAll(local)
	local.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, ignoreRange := range []bool{false, true} {
		var ranges atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Accept-Ranges", "bytes")
			if r.Header.Get("Range") != "" {
				ranges.Add(1)
				if ignoreRange {
					r.Header.Del("Range")
				}
			}
			http.ServeContent(w, r, "audio", time.Time{}, bytes.NewReader(data))
		}))
		r, _, err := openPCM(t.Context(), server.URL, 125*time.Millisecond, true, defaultAudioSettings(), nil)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		pcm, err := io.ReadAll(r)
		r.Close()
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(pcm, want) || ranges.Load() == 0 {
			t.Fatalf("seek returned %d bytes with %d ranges", len(pcm), ranges.Load())
		}
	}
}

// TestPCMProcessHelper supplies controlled external-process behavior to the lifecycle tests.
func TestPCMProcessHelper(t *testing.T) {
	switch os.Getenv("CHILL_PCM_PROCESS_HELPER") {
	case "data":
		os.Stdout.Write(bytes.Repeat([]byte{0x42}, 256<<10))
		os.Exit(0)
	case "failure":
		fmt.Fprintln(os.Stderr, "EJS component unavailable: fixture diagnostic")
		os.Exit(7)
	case "stall":
		os.Stdout.Write([]byte{1})
		time.Sleep(time.Hour)
		os.Exit(0)
	}
}

// TestPCMProcessLifecycle checks full stdout delivery, useful errors, and cancellation under backpressure.
func TestPCMProcessLifecycle(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"data", "failure", "stall"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cmd := exec.Command(exe, "-test.run=^TestPCMProcessHelper$")
			cmd.Env = append(os.Environ(), "CHILL_PCM_PROCESS_HELPER="+mode)
			var diagnostics tailBuffer
			cmd.Stderr = &diagnostics
			reader, err := startPCMProcess(ctx, cmd, nil, &diagnostics)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if mode == "stall" {
				var ready [1]byte
				if _, err := io.ReadFull(reader, ready[:]); err != nil {
					t.Fatal(err)
				}
				cancel()
			}
			done := make(chan struct{})
			var data []byte
			var readErr error
			go func() { data, readErr = io.ReadAll(reader); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("process cleanup hung")
			}
			switch mode {
			case "data":
				if readErr != nil || len(data) != 256<<10 {
					t.Fatalf("truncated stdout %d: %v", len(data), readErr)
				}
			case "failure":
				if readErr == nil || !strings.Contains(readErr.Error(), "EJS component unavailable") {
					t.Fatalf("lost diagnostics: %v", readErr)
				}
			case "stall":
				if readErr == nil {
					t.Fatal("cancellation was hidden")
				}
			}
			if cmd.ProcessState == nil {
				t.Fatal("child process not reaped")
			}
		})
	}
}

// TestWebsitePipelinePreservesExtractorError checks useful diagnostics through both child processes.
func TestWebsitePipelinePreservesExtractorError(t *testing.T) {
	withConfigDir(t)
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg integration requires ffmpeg")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.Command(exe, "-test.run=^TestPCMProcessHelper$")
	cmd.Env = append(os.Environ(), "CHILL_PCM_PROCESS_HELPER=failure")
	var diagnostics tailBuffer
	cmd.Stderr = &diagnostics
	media, err := startPCMProcess(ctx, cmd, nil, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	pcm, err := openFFmpegPCM(ctx, resolvedAudio{}, media, 0, defaultAudioSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	reader := &websitePCM{Reader: pcm, media: media}
	defer reader.Close()
	if _, err := io.Copy(io.Discard, reader); err == nil || !strings.Contains(err.Error(), "EJS component unavailable") {
		t.Fatalf("extractor diagnostic lost: %v", err)
	}
}

// TestNativeHTTPUsesOneConnection checks extensionless streaming and prefix replay.
func TestNativeHTTPUsesOneConnection(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	data, err := os.ReadFile("testdata/audio/tone.mp3")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(data)
	}))
	defer server.Close()
	r, _, err := openPCM(t.Context(), server.URL+"/live", 0, false, defaultAudioSettings(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if requests.Load() != 1 {
		t.Fatalf("requests %d", requests.Load())
	}
}

// TestDoctorCapabilitiesAndExplicitSources verifies optional warnings, actual
// decoding, and explicit device failures without opening physical hardware.
func TestDoctorCapabilitiesAndExplicitSources(t *testing.T) {
	withConfigDir(t)
	useTestAudio(t)
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{nil, {"--stream", "testdata/audio/tone.flac"}, {"--audio"}} {
		var out bytes.Buffer
		if err := runDoctor(args, &out); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
		if strings.Contains(out.String(), "[FAIL]") {
			t.Fatal(out.String())
		}
	}
	var out bytes.Buffer
	if err := runDoctor([]string{"--stream", "https://youtube.com/watch?v=test"}, &out); err == nil || !strings.Contains(out.String(), "[FAIL] stream") {
		t.Fatalf("missing website helpers: %v %s", err, out.String())
	}
}

// TestToolSettingsValidation checks shared configuration and explicit runtime discovery.
func TestToolSettingsValidation(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(toolsPath(), ToolSettings{JSRuntime: "node", JSRuntimePath: exe, YTDLP: exe}); err != nil {
		t.Fatal(err)
	}
	args, err := extractorArgs()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "node:"+exe) {
		t.Fatalf("runtime args %v", args)
	}
	if got := findYtdl(""); got != exe {
		t.Fatal(got)
	}
	if err := writeJSON(toolsPath(), ToolSettings{JSRuntime: "unknown"}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToolSettings(); err == nil {
		t.Fatal("invalid runtime accepted")
	}
}

// TestSourceCancellationStopsHTTPRead verifies a stalled native source can be stopped.
func TestSourceCancellationStopsHTTPRead(t *testing.T) {
	withConfigDir(t)
	ready := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ID3")
		w.(http.Flusher).Flush()
		close(ready)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		r, _, err := openPCM(ctx, server.URL, 0, false, defaultAudioSettings(), func(string) {})
		if r != nil {
			r.Close()
		}
		done <- err
	}()
	<-ready
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled source succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("source cancellation hung")
	}
}
