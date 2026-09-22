// requirements.go checks the optional tools needed by a selected source.

package main

import (
	"errors"
	"net/url"
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
		palette.purple + "  ~ this source needs an extra tool ~" + palette.reset,
		"",
		palette.dim + "  chill couldn't find " + strings.Join(e.missing, " or ") + "." + palette.reset,
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
			cmds = append(cmds, "pipx install 'yt-dlp[default]'")
		} else if p == "deno" {
			cmds = append(cmds, "curl -fsSL https://deno.land/install.sh | sh")
		} else {
			apt = append(apt, p)
		}
	}
	if len(apt) > 0 {
		cmds = append([]string{"sudo apt install " + strings.Join(apt, " ")}, cmds...)
	}
	return cmds
}

// checkMediaRequirements checks the first item before starting playback. Later
// queue items report missing capabilities when selected, without blocking native
// tracks earlier in the queue. Codec requirements are determined from content.
func checkMediaRequirements(items []MediaItem) error {
	if _, err := loadToolSettings(); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	item := items[0]
	providerNeedsExtractor := item.Kind == MediaProvider && slices.Contains([]string{"youtube", "ytmusic", "soundcloud", "mixcloud"}, item.Provider)
	if !sourceNeedsYtdl(item.Source) && !providerNeedsExtractor {
		return nil
	}
	var missing []string
	for _, name := range []string{"ffmpeg", "yt-dlp"} {
		if _, err := toolPath(name); err != nil {
			if _, absent := errors.AsType[*requirementsError](err); !absent {
				return err
			}
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return &requirementsError{missing}
	}
	_, _, err := selectedJSRuntime()
	return err
}

// findYtdl retains its argument for portable-install compatibility.
func findYtdl(_ string) string { result, _ := toolPath("yt-dlp"); return result }
