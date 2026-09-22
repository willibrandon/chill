// requirements.go checks that the programs chill plays through are installed.

package main

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

func sourceNeedsYtdl(source string) bool {
	u, err := url.Parse(source)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, domain := range []string{"youtube.com", "youtu.be", "soundcloud.com", "mixcloud.com", "bandcamp.com", "bilibili.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// requirementsError reports programs chill needs that couldn't be found.
type requirementsError struct {
	missing []string
}

// Error lists the required programs that could not be found.
func (e *requirementsError) Error() string {
	return strings.Join(e.missing, " and ") + " not found"
}

// chill puts it the laid-back way, with how to fix it.
func (e *requirementsError) chill() string {
	palette := currentCLIPalette()
	them := "it"
	if len(e.missing) > 1 {
		them = "them"
	}

	lines := []string{
		palette.purple + "  ~ no sound system yet ~" + palette.reset,
		"",
		palette.dim + "  chill uses mpv, FFmpeg and yt-dlp, and couldn't find " + strings.Join(e.missing, " or ") + "." + palette.reset,
		palette.dim + "  no rush. grab " + them + " and come back, the beats will wait:" + palette.reset,
		"",
	}
	for _, cmd := range installCommands(e.missing) {
		lines = append(lines, "    "+palette.cyan+cmd+palette.reset)
	}
	return strings.Join(lines, "\n")
}

// installCommands returns how to install the given programs on this platform,
// the same way the readme does.
func installCommands(programs []string) []string {
	normalized := make([]string, 0, len(programs))
	seen := map[string]bool{}
	for _, p := range programs {
		if p == "ffprobe" {
			p = "ffmpeg"
		}
		if !seen[p] {
			normalized = append(normalized, p)
			seen[p] = true
		}
	}
	programs = normalized
	switch runtime.GOOS {
	case "windows":
		return []string{"choco install " + strings.Join(programs, " ")}
	case "darwin":
		return []string{"brew install " + strings.Join(programs, " ")}
	}

	// pip refuses to install system-wide on Debian and Ubuntu, and their own
	// yt-dlp package goes stale, so yt-dlp comes through pipx
	var apt, cmds []string
	for _, p := range programs {
		if p == "yt-dlp" {
			apt = append(apt, "pipx")
			cmds = append(cmds, "pipx install yt-dlp")
		} else {
			apt = append(apt, p)
		}
	}
	return append([]string{"sudo apt install " + strings.Join(apt, " ")}, cmds...)
}

func checkPodcastRequirements() error {
	var missing []string
	for _, name := range []string{"mpv", "ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return &requirementsError{missing}
	}
	return nil
}

// checkLocalMediaRequirements returns the programs needed while discovering
// local tracks. Playback checks mpv separately when it starts.
func checkLocalMediaRequirements() error {
	var missing []string
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return &requirementsError{missing}
	}
	return nil
}

// checkMediaRequirements checks only the tools required by these sources, so
// local files and direct streams do not depend on a video-site extractor.
func checkMediaRequirements(items []MediaItem) error {
	missing, seen := []string{}, map[string]bool{}
	need := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	need("mpv")
	need("ffmpeg")
	mpv, _ := exec.LookPath("mpv")
	hasExtractor := findYtdl(mpv) != ""
	for _, item := range items {
		if item.Kind == MediaPodcast {
			need("ffprobe")
		}
		providerNeedsExtractor := item.Kind == MediaProvider && slices.Contains([]string{"youtube", "ytmusic", "soundcloud", "mixcloud"}, item.Provider)
		if (sourceNeedsYtdl(item.Source) || providerNeedsExtractor) && !hasExtractor && !seen["yt-dlp"] {
			seen["yt-dlp"] = true
			missing = append(missing, "yt-dlp")
		}
	}
	if len(missing) > 0 {
		return &requirementsError{missing}
	}
	return nil
}

// findYtdl uses the same search locations as the requirements check so doctor
// can report and run the discovered extractor, including portable installs.
func findYtdl(mpv string) string {
	var dirs []string
	if dir := os.Getenv("MPV_HOME"); dir != "" {
		dirs = append(dirs, dir)
	}
	if mpv != "" {
		dir := filepath.Dir(mpv)
		dirs = append(dirs, dir, filepath.Join(dir, "portable_config"))
	}
	if dir, err := os.UserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(dir, "mpv"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".config", "mpv"))
	}

	// the names mpv tries, in its order
	for _, name := range []string{"yt-dlp", "yt-dlp_x86", "youtube-dl"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
		for _, dir := range dirs {
			if path, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
				return path
			}
		}
	}
	return ""
}
