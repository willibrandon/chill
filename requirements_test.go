package main

import (
	"errors"
	"os"
	"slices"
	"testing"
)

// TestYouTubeRequirementsIncludeRuntime checks complete install hints and supported runtime alternatives.
func TestYouTubeRequirementsIncludeRuntime(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("MPV_HOME", t.TempDir())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		item     MediaItem
		settings ToolSettings
		missing  []string
	}{
		{"all missing", MediaItem{Source: "https://www.youtube.com/watch?v=test"}, ToolSettings{}, []string{"ffmpeg", "yt-dlp", "deno"}},
		{"runtime missing", MediaItem{Source: "https://youtu.be/test"}, ToolSettings{FFmpeg: exe, YTDLP: exe}, []string{"deno"}},
		{"provider runtime", MediaItem{Kind: MediaProvider, Provider: "ytmusic", Source: "provider:test"}, ToolSettings{FFmpeg: exe, YTDLP: exe}, []string{"deno"}},
		{"deno", MediaItem{Source: "https://youtube.com/watch?v=test"}, ToolSettings{FFmpeg: exe, YTDLP: exe, JSRuntime: "deno", JSRuntimePath: exe}, nil},
		{"node", MediaItem{Source: "https://youtube.com/watch?v=test"}, ToolSettings{FFmpeg: exe, YTDLP: exe, JSRuntime: "node", JSRuntimePath: exe}, nil},
		{"quickjs", MediaItem{Source: "https://youtube.com/watch?v=test"}, ToolSettings{FFmpeg: exe, YTDLP: exe, JSRuntime: "quickjs", JSRuntimePath: exe}, nil},
		{"other website", MediaItem{Source: "https://soundcloud.com/artist/track"}, ToolSettings{FFmpeg: exe, YTDLP: exe}, nil},
		{"direct audio", MediaItem{Source: "https://example.com/audio.wav"}, ToolSettings{}, nil},
		{"local", MediaItem{Kind: MediaTrack, Source: "testdata/audio/tone.flac"}, ToolSettings{}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := writeJSON(toolsPath(), test.settings); err != nil {
				t.Fatal(err)
			}
			err := checkMediaRequirements([]MediaItem{test.item})
			if test.missing == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			missing, ok := errors.AsType[*requirementsError](err)
			if !ok || !slices.Equal(missing.missing, test.missing) {
				t.Fatalf("requirements = %v, want %v", err, test.missing)
			}
		})
	}
}
