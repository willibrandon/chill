package main

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
)

type toolConfigError struct{ message string }

// Error describes an explicit tool setting that needs correction.
func (e *toolConfigError) Error() string { return e.message }

func invalidToolConfig(format string, args ...any) error {
	return &toolConfigError{message: fmt.Sprintf(format, args...)}
}

// ToolSettings is the optional tools.json document shared by playback, search,
// and diagnostics. Empty paths use discovery; arbitrary yt-dlp config is ignored.
type ToolSettings struct {
	// FFmpeg overrides the decoder executable path.
	FFmpeg string `json:"ffmpeg,omitempty"`
	// FFprobe overrides the optional metadata probe executable path.
	FFprobe string `json:"ffprobe,omitempty"`
	// YTDLP overrides the website extractor executable path.
	YTDLP string `json:"yt_dlp,omitempty"`
	// JSRuntime selects auto, deno, node, or quickjs.
	JSRuntime string `json:"js_runtime,omitempty"`
	// JSRuntimePath overrides the selected JavaScript runtime executable.
	JSRuntimePath string `json:"js_runtime_path,omitempty"`
}

func toolsPath() string {
	if path := configPath(); path != "" {
		return filepath.Join(filepath.Dir(path), "tools.json")
	}
	return ""
}
func loadToolSettings() (ToolSettings, error) {
	s := ToolSettings{JSRuntime: "auto"}
	path := toolsPath()
	if path == "" {
		return s, invalidToolConfig("no user config directory for tools.json")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, invalidToolConfig("read tools.json: %v", err)
	}
	if err = json.Unmarshal(data, &s, json.RejectUnknownMembers(true)); err != nil {
		return s, invalidToolConfig("read tools.json: %v", err)
	}
	if s.JSRuntime == "" {
		s.JSRuntime = "auto"
	}
	if !slices.Contains([]string{"auto", "deno", "node", "quickjs"}, s.JSRuntime) {
		return s, invalidToolConfig("tools.json: js_runtime must be auto, deno, node, or quickjs")
	}
	if s.JSRuntimePath != "" && s.JSRuntime == "auto" {
		return s, invalidToolConfig("tools.json: js_runtime_path requires an explicit js_runtime")
	}
	return s, nil
}

func toolPath(name string) (string, error) {
	s, err := loadToolSettings()
	if err != nil {
		return "", err
	}
	override := map[string]string{"ffmpeg": s.FFmpeg, "ffprobe": s.FFprobe, "yt-dlp": s.YTDLP}[name]
	if override != "" {
		path, err := exec.LookPath(override)
		if err != nil {
			return "", invalidToolConfig("configured %s executable: %v", name, err)
		}
		return path, nil
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	if executable, err := os.Executable(); err == nil {
		if path, err := exec.LookPath(filepath.Join(filepath.Dir(executable), name)); err == nil {
			return path, nil
		}
	}
	if name == "yt-dlp" {
		if path := legacyExtractorPath(); path != "" {
			return path, nil
		}
	}
	return "", &requirementsError{missing: []string{name}}
}

func legacyExtractorPath() string {
	var dirs []string
	if dir := os.Getenv("MPV_HOME"); dir != "" {
		dirs = append(dirs, dir)
	}
	if dir, err := os.UserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(dir, "mpv"))
	}
	if dir, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(dir, ".config", "mpv"))
	}
	if executable, err := exec.LookPath("mpv"); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(executable), "portable_config"))
	}
	for _, dir := range dirs {
		for _, candidate := range []string{"yt-dlp", "yt-dlp_x86"} {
			if path, err := exec.LookPath(filepath.Join(dir, candidate)); err == nil {
				return path
			}
		}
	}
	return ""
}

func selectedJSRuntime() (string, string, error) {
	s, err := loadToolSettings()
	if err != nil {
		return "", "", err
	}
	names := []string{s.JSRuntime}
	if s.JSRuntime == "auto" {
		names = []string{"deno", "node", "quickjs"}
	}
	for _, name := range names {
		binary := name
		if name == "quickjs" {
			binary = "qjs"
		}
		if s.JSRuntimePath != "" {
			binary = s.JSRuntimePath
		}
		if path, err := toolPath(binary); err == nil {
			return name, path, nil
		}
	}
	if s.JSRuntime != "auto" {
		return "", "", invalidToolConfig("configured JavaScript runtime %s was not found", s.JSRuntime)
	}
	return "", "", nil
}

func extractorArgs() ([]string, error) {
	args := []string{"--ignore-config", "--no-playlist", "--no-progress", "--socket-timeout", "10", "--retries", "0"}
	name, path, err := selectedJSRuntime()
	if err != nil {
		return nil, err
	}
	if path != "" {
		args = append(args, "--no-js-runtimes", "--js-runtimes", name+":"+path)
	}
	return args, nil
}
