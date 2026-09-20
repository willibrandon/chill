// requirements.go checks that the programs chill plays through are installed.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// requirementsError reports programs chill needs that couldn't be found.
type requirementsError struct {
	missing []string
}

func (e *requirementsError) Error() string {
	return strings.Join(e.missing, " and ") + " not found"
}

// chill puts it the laid-back way, with how to fix it.
func (e *requirementsError) chill() string {
	them := "it"
	if len(e.missing) > 1 {
		them = "them"
	}

	lines := []string{
		purple + "  ~ no sound system yet ~" + reset,
		"",
		dim + "  chill uses mpv, FFmpeg and yt-dlp, and couldn't find " + strings.Join(e.missing, " or ") + "." + reset,
		dim + "  no rush. grab " + them + " and come back, the beats will wait:" + reset,
		"",
	}
	for _, cmd := range installCommands(e.missing) {
		lines = append(lines, "    "+cyan+cmd+reset)
	}
	return strings.Join(lines, "\n")
}

// installCommands returns how to install the given programs on this platform,
// the same way the readme does.
func installCommands(programs []string) []string {
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

// checkRequirements returns the programs required by the daemon's PCM pipeline.
func checkRequirements() error {
	var missing []string

	mpv, err := exec.LookPath("mpv")
	if err != nil {
		missing = append(missing, "mpv")
	}
	if !hasYtdl(mpv) {
		missing = append(missing, "yt-dlp")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		missing = append(missing, "ffmpeg")
	}

	if len(missing) == 0 {
		return nil
	}
	return &requirementsError{missing}
}

// hasYtdl reports whether mpv will find something to fetch YouTube streams
// with. mpv doesn't only look in PATH, a copy in its config directory or
// beside mpv itself works too.
func hasYtdl(mpv string) bool {
	return findYtdl(mpv) != ""
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
