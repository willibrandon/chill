package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type keyAction struct {
	id          string
	scope       string
	description string
	defaults    []string
}

var interfaceKeyActions = []keyAction{
	{id: "global.help", scope: "global", description: "Open help", defaults: []string{"f1"}},
	{id: "global.visualizer", scope: "global", description: "Open the visualizer", defaults: []string{"f2"}},
	{id: "global.podcasts", scope: "global", description: "Open podcasts", defaults: []string{"f3"}},
	{id: "global.equalizer", scope: "global", description: "Open the equalizer", defaults: []string{"f4"}},
	{id: "global.radio", scope: "global", description: "Open radio", defaults: []string{"f5"}},
	{id: "global.lyrics", scope: "global", description: "Open lyrics", defaults: []string{"f6"}},
	{id: "global.library", scope: "global", description: "Open the library", defaults: []string{"f7"}},
	{id: "global.providers", scope: "global", description: "Open providers", defaults: []string{"f8"}},
	{id: "global.audio", scope: "global", description: "Open audio settings", defaults: []string{"f9"}},
	{id: "global.interface", scope: "global", description: "Open interface settings", defaults: []string{"f10"}},
	{id: "global.keys", scope: "global", description: "Open searchable keybindings", defaults: []string{"ctrl+k"}},
	{id: "global.quit", scope: "global", description: "Quit Chill", defaults: []string{"ctrl+q"}},
	{id: "prompt.submit", scope: "prompt", description: "Run the command", defaults: []string{"enter"}},
	{id: "prompt.cancel", scope: "prompt", description: "Cancel or clear", defaults: []string{"esc"}},
	{id: "prompt.suggestion-next", scope: "prompt", description: "Select the next suggestion", defaults: []string{"down"}},
	{id: "prompt.suggestion-previous", scope: "prompt", description: "Select the previous suggestion", defaults: []string{"up"}},
	{id: "prompt.history-next", scope: "prompt", description: "Show newer command history", defaults: []string{"ctrl+n"}},
	{id: "prompt.history-previous", scope: "prompt", description: "Show older command history", defaults: []string{"ctrl+p"}},
	{id: "prompt.cancel-command", scope: "prompt", description: "Cancel the active command or clear input", defaults: []string{"ctrl+c"}},
	{id: "prompt.complete", scope: "prompt", description: "Accept the selected completion", defaults: []string{"tab"}},
	{id: "prompt.ghost", scope: "prompt", description: "Accept ghost text", defaults: []string{"right"}},
	{id: "prompt.page-up", scope: "prompt", description: "Scroll transcript up", defaults: []string{"pgup"}},
	{id: "prompt.page-down", scope: "prompt", description: "Scroll transcript down", defaults: []string{"pgdown"}},
	{id: "prompt.select-lines", scope: "prompt", description: "Begin line selection", defaults: []string{"shift+up"}},
	{id: "prompt.clear", scope: "prompt", description: "Clear the transcript", defaults: []string{"ctrl+l"}},
	{id: "help.close", scope: "help", description: "Close help", defaults: []string{"esc"}},
	{id: "help.cancel-command", scope: "help", description: "Cancel the active command", defaults: []string{"ctrl+c"}},
	{id: "help.up", scope: "help", description: "Scroll help up", defaults: []string{"up"}},
	{id: "help.down", scope: "help", description: "Scroll help down", defaults: []string{"down"}},
	{id: "help.page-up", scope: "help", description: "Scroll help up one page", defaults: []string{"pgup"}},
	{id: "help.page-down", scope: "help", description: "Scroll help down one page", defaults: []string{"pgdown"}},
	{id: "selection.copy", scope: "selection", description: "Copy the selection", defaults: []string{"y", "enter", "ctrl+c"}},
	{id: "selection.cancel", scope: "selection", description: "Cancel the selection", defaults: []string{"esc"}},
	{id: "selection.up", scope: "selection", description: "Extend selection upward", defaults: []string{"up", "shift+up"}},
	{id: "selection.down", scope: "selection", description: "Extend selection downward", defaults: []string{"down", "shift+down"}},
	{id: "selection.page-up", scope: "selection", description: "Release selection and scroll up", defaults: []string{"pgup"}},
	{id: "selection.page-down", scope: "selection", description: "Release selection and scroll down", defaults: []string{"pgdown"}},
	{id: "editor.submit", scope: "editor", description: "Submit text input", defaults: []string{"enter"}},
	{id: "editor.cancel", scope: "editor", description: "Cancel text input", defaults: []string{"esc", "ctrl+c"}},
	{id: "browser.back", scope: "browser", description: "Go back or close", defaults: []string{"esc", "b"}},
	{id: "browser.up", scope: "browser", description: "Move selection up", defaults: []string{"up", "k"}},
	{id: "browser.down", scope: "browser", description: "Move selection down", defaults: []string{"down", "j"}},
	{id: "browser.page-up", scope: "browser", description: "Move up one page", defaults: []string{"pgup"}},
	{id: "browser.page-down", scope: "browser", description: "Move down one page", defaults: []string{"pgdown"}},
	{id: "browser.home", scope: "browser", description: "Move to the first item", defaults: []string{"home"}},
	{id: "browser.end", scope: "browser", description: "Move to the last item", defaults: []string{"end"}},
	{id: "browser.select", scope: "browser", description: "Open or play the selected item", defaults: []string{"enter"}},
	{id: "browser.search", scope: "browser", description: "Search or filter", defaults: []string{"/"}},
	{id: "browser.refresh", scope: "browser", description: "Refresh content", defaults: []string{"ctrl+r"}},
	{id: "browser.play-next", scope: "browser", description: "Play the selected item next", defaults: []string{"n"}},
	{id: "browser.append", scope: "browser", description: "Append the selected item", defaults: []string{"a"}},
	{id: "browser.favorite", scope: "browser", description: "Toggle favorite", defaults: []string{"f"}},
	{id: "browser.bookmark", scope: "browser", description: "Toggle bookmark", defaults: []string{"B"}},
	{id: "browser.pause", scope: "browser", description: "Pause or resume playback", defaults: []string{"space"}},
	{id: "browser.remove", scope: "browser", description: "Remove the selected item", defaults: []string{"x"}},
	{id: "browser.undo", scope: "browser", description: "Undo the latest queue edit", defaults: []string{"u"}},
	{id: "browser.shuffle", scope: "browser", description: "Toggle shuffle", defaults: []string{"z"}},
	{id: "browser.repeat", scope: "browser", description: "Cycle repeat mode", defaults: []string{"R"}},
	{id: "library.save", scope: "library", description: "Save the queue as a playlist", defaults: []string{"w"}},
	{id: "library.repeat-rename", scope: "library", description: "Cycle repeat or rename a playlist", defaults: []string{"r"}},
	{id: "library.delete", scope: "library", description: "Delete a playlist", defaults: []string{"d"}},
	{id: "library.move-up", scope: "library", description: "Move the selected item up", defaults: []string{"shift+up", "K"}},
	{id: "library.move-down", scope: "library", description: "Move the selected item down", defaults: []string{"shift+down", "J"}},
	{id: "provider.global-search", scope: "providers", description: "Search the selected provider", defaults: []string{"ctrl+f"}},
	{id: "provider.previous-page", scope: "providers", description: "Load the previous page", defaults: []string{"["}},
	{id: "provider.next-page", scope: "providers", description: "Load the next page", defaults: []string{"]"}},
	{id: "provider.setup", scope: "providers", description: "Configure a provider", defaults: []string{"s"}},
	{id: "provider.cancel", scope: "providers", description: "Cancel the current request", defaults: []string{"ctrl+c"}},
	{id: "provider.queue", scope: "providers", description: "Append the selected item", defaults: []string{"q"}},
	{id: "provider.play-next", scope: "providers", description: "Play the selected item next", defaults: []string{"n"}},
	{id: "radio.global-search", scope: "radio", description: "Search radio stations", defaults: []string{"ctrl+f"}},
	{id: "radio.sort", scope: "radio", description: "Cycle station sort", defaults: []string{"o"}},
	{id: "radio.previous-page", scope: "radio", description: "Load the previous page", defaults: []string{"["}},
	{id: "radio.next-page", scope: "radio", description: "Load the next page", defaults: []string{"]"}},
	{id: "radio.lyrics", scope: "radio", description: "Open lyrics", defaults: []string{"l"}},
	{id: "radio.pin", scope: "radio", description: "Toggle a discovery pin", defaults: []string{"p"}},
	{id: "radio.save", scope: "radio", description: "Save the station", defaults: []string{"a"}},
	{id: "radio.queue", scope: "radio", description: "Append the selected station", defaults: []string{"q"}},
	{id: "radio.play-next", scope: "radio", description: "Play the selected station next", defaults: []string{"n"}},
	{id: "radio.cancel", scope: "radio", description: "Cancel the current request", defaults: []string{"ctrl+c"}},
	{id: "radio.nearby-yes", scope: "radio-consent", description: "Allow nearby suggestions", defaults: []string{"y"}},
	{id: "radio.nearby-no", scope: "radio-consent", description: "Disable nearby suggestions", defaults: []string{"n"}},
	{id: "radio.nearby-cancel", scope: "radio-consent", description: "Cancel nearby setup", defaults: []string{"esc", "ctrl+c"}},
	{id: "podcast.played-filter", scope: "podcasts", description: "Cycle the inbox played filter", defaults: []string{"v"}},
	{id: "podcast.global-search", scope: "podcasts", description: "Search the podcast catalog", defaults: []string{"ctrl+f"}},
	{id: "podcast.seek-back", scope: "podcasts", description: "Seek backward", defaults: []string{"shift+left"}},
	{id: "podcast.seek-forward", scope: "podcasts", description: "Seek forward", defaults: []string{"shift+right"}},
	{id: "podcast.speed-down", scope: "podcasts", description: "Lower playback speed", defaults: []string{"["}},
	{id: "podcast.speed-up", scope: "podcasts", description: "Raise playback speed", defaults: []string{"]"}},
	{id: "podcast.queue", scope: "podcasts", description: "Append the selected episode", defaults: []string{"q"}},
	{id: "podcast.restart", scope: "podcasts", description: "Restart the selected episode", defaults: []string{"r"}},
	{id: "podcast.latest", scope: "podcasts", description: "Queue the newest episode", defaults: []string{"l"}},
	{id: "podcast.download", scope: "podcasts", description: "Download the selected episode", defaults: []string{"d"}},
	{id: "podcast.remove-download", scope: "podcasts", description: "Remove the selected download", defaults: []string{"D"}},
	{id: "podcast.pin-download", scope: "podcasts", description: "Pin the selected download", defaults: []string{"P"}},
	{id: "podcast.retry-download", scope: "podcasts", description: "Retry the selected download", defaults: []string{"R"}},
	{id: "podcast.setting-previous", scope: "podcasts", description: "Decrease a download setting", defaults: []string{"left"}},
	{id: "podcast.setting-next", scope: "podcasts", description: "Increase a download setting", defaults: []string{"right"}},
	{id: "podcast.cancel", scope: "podcasts", description: "Cancel the current request", defaults: []string{"ctrl+c"}},
	{id: "audio.adjust-left", scope: "audio", description: "Select the previous value", defaults: []string{"left"}},
	{id: "audio.adjust-right", scope: "audio", description: "Select the next value", defaults: []string{"right", "enter", "space"}},
	{id: "audio.cancel", scope: "audio", description: "Cancel device discovery", defaults: []string{"ctrl+c"}},
	{id: "lyrics.page-down", scope: "lyrics", description: "Scroll down one page", defaults: []string{"space"}},
	{id: "lyrics.home", scope: "lyrics", description: "Jump to the first lyric", defaults: []string{"home", "g"}},
	{id: "lyrics.end", scope: "lyrics", description: "Jump to the last lyric", defaults: []string{"end", "G"}},
	{id: "equalizer.left", scope: "equalizer", description: "Select the previous band", defaults: []string{"left", "h"}},
	{id: "equalizer.right", scope: "equalizer", description: "Select the next band", defaults: []string{"right", "l"}},
	{id: "equalizer.raise", scope: "equalizer", description: "Raise the selected band", defaults: []string{"up", "k"}},
	{id: "equalizer.lower", scope: "equalizer", description: "Lower the selected band", defaults: []string{"down", "j"}},
	{id: "equalizer.zero", scope: "equalizer", description: "Reset the selected band", defaults: []string{"0"}},
	{id: "equalizer.next-preset", scope: "equalizer", description: "Select the next preset", defaults: []string{"e"}},
	{id: "equalizer.previous-preset", scope: "equalizer", description: "Select the previous preset", defaults: []string{"E"}},
	{id: "equalizer.flat", scope: "equalizer", description: "Select Flat", defaults: []string{"r"}},
	{id: "equalizer.custom", scope: "equalizer", description: "Select Custom", defaults: []string{"c"}},
	{id: "equalizer.close", scope: "equalizer", description: "Return to the prompt", defaults: []string{"esc", "enter"}},
	{id: "equalizer.pause", scope: "equalizer", description: "Pause or resume playback", defaults: []string{"space"}},
	{id: "playback.seek-back", scope: "playback", description: "Seek backward", defaults: []string{"left"}},
	{id: "playback.seek-forward", scope: "playback", description: "Seek forward", defaults: []string{"right"}},
	{id: "playback.seek-large-back", scope: "playback", description: "Seek backward by the large increment", defaults: []string{"shift+left"}},
	{id: "playback.seek-large-forward", scope: "playback", description: "Seek forward by the large increment", defaults: []string{"shift+right"}},
	{id: "playback.pause", scope: "playback", description: "Pause or resume playback", defaults: []string{"space"}},
	{id: "playback.mute", scope: "playback", description: "Toggle mute", defaults: []string{"m"}},
	{id: "playback.next", scope: "playback", description: "Play the next item", defaults: []string{">", "."}},
	{id: "playback.previous", scope: "playback", description: "Play the previous item", defaults: []string{"<", ","}},
	{id: "playback.volume-down", scope: "playback", description: "Lower volume", defaults: []string{"9"}},
	{id: "playback.volume-up", scope: "playback", description: "Raise volume", defaults: []string{"0"}},
	{id: "playback.lyrics", scope: "playback", description: "Open lyrics", defaults: []string{"y"}},
	{id: "playback.favorite", scope: "playback", description: "Toggle favorite", defaults: []string{"f"}},
	{id: "playback.bookmark", scope: "playback", description: "Toggle bookmark", defaults: []string{"B"}},
	{id: "playback.shuffle", scope: "playback", description: "Toggle shuffle", defaults: []string{"z"}},
	{id: "playback.repeat", scope: "playback", description: "Cycle repeat mode", defaults: []string{"R"}},
	{id: "playback.speed-down", scope: "playback", description: "Lower playback speed", defaults: []string{"["}},
	{id: "playback.speed-up", scope: "playback", description: "Raise playback speed", defaults: []string{"]"}},
	{id: "playback.eq-left", scope: "playback", description: "Select the previous EQ band", defaults: []string{"h"}},
	{id: "playback.eq-right", scope: "playback", description: "Select the next EQ band", defaults: []string{"l"}},
	{id: "playback.eq-lower", scope: "playback", description: "Lower the selected EQ band", defaults: []string{"j"}},
	{id: "playback.eq-raise", scope: "playback", description: "Raise the selected EQ band", defaults: []string{"k"}},
	{id: "playback.eq-zero", scope: "playback", description: "Reset the selected EQ band", defaults: []string{"x"}},
	{id: "playback.eq-next", scope: "playback", description: "Select the next EQ preset", defaults: []string{"e"}},
	{id: "playback.eq-previous", scope: "playback", description: "Select the previous EQ preset", defaults: []string{"E"}},
	{id: "playback.eq-flat", scope: "playback", description: "Select Flat EQ", defaults: []string{"r"}},
	{id: "playback.eq-custom", scope: "playback", description: "Select Custom EQ", defaults: []string{"c"}},
	{id: "playback.quit", scope: "playback", description: "Stop foreground playback", defaults: []string{"q", "ctrl+c"}},
	{id: "foreground-lyrics.close", scope: "foreground-lyrics", description: "Close foreground lyrics", defaults: []string{"y", "esc"}},
	{id: "foreground-lyrics.quit", scope: "foreground-lyrics", description: "Stop foreground playback", defaults: []string{"q", "ctrl+c"}},
	{id: "foreground-lyrics.up", scope: "foreground-lyrics", description: "Scroll lyrics up", defaults: []string{"up", "k"}},
	{id: "foreground-lyrics.down", scope: "foreground-lyrics", description: "Scroll lyrics down", defaults: []string{"down", "j"}},
	{id: "foreground-lyrics.page-up", scope: "foreground-lyrics", description: "Scroll lyrics up one page", defaults: []string{"pgup"}},
	{id: "foreground-lyrics.page-down", scope: "foreground-lyrics", description: "Scroll lyrics down one page", defaults: []string{"pgdown", "space"}},
	{id: "foreground-lyrics.refresh", scope: "foreground-lyrics", description: "Refresh lyrics", defaults: []string{"r"}},
	{id: "visualizer.next", scope: "visualizer", description: "Select the next visualizer", defaults: []string{"v", "right"}},
	{id: "visualizer.previous", scope: "visualizer", description: "Select the previous visualizer", defaults: []string{"left"}},
	{id: "visualizer.fullscreen", scope: "visualizer", description: "Toggle fullscreen", defaults: []string{"V", "shift+v"}},
	{id: "visualizer.disable", scope: "visualizer", description: "Disable the visualizer", defaults: []string{"o"}},
	{id: "visualizer.back", scope: "visualizer", description: "Return to the prompt", defaults: []string{"esc", "enter"}},
	{id: "visualizer.cancel", scope: "visualizer", description: "Cancel the active command or return", defaults: []string{"ctrl+c"}},
	{id: "visualizer.pause", scope: "visualizer", description: "Pause or resume playback", defaults: []string{"space"}},
	{id: "overlay.close", scope: "overlay", description: "Close the overlay", defaults: []string{"esc"}},
	{id: "overlay.search", scope: "overlay", description: "Search bindings", defaults: []string{"/"}},
	{id: "overlay.up", scope: "overlay", description: "Scroll up", defaults: []string{"up", "k"}},
	{id: "overlay.down", scope: "overlay", description: "Scroll down", defaults: []string{"down", "j"}},
	{id: "overlay.page-up", scope: "overlay", description: "Scroll up one page", defaults: []string{"pgup"}},
	{id: "overlay.page-down", scope: "overlay", description: "Scroll down one page", defaults: []string{"pgdown"}},
	{id: "interface.cancel", scope: "interface", description: "Cancel the preview", defaults: []string{"esc"}},
	{id: "interface.up", scope: "interface", description: "Select the previous setting", defaults: []string{"up", "k"}},
	{id: "interface.down", scope: "interface", description: "Select the next setting", defaults: []string{"down", "j"}},
	{id: "interface.previous", scope: "interface", description: "Select the previous value", defaults: []string{"left", "h"}},
	{id: "interface.next", scope: "interface", description: "Select the next value", defaults: []string{"right", "l", "enter", "space"}},
	{id: "interface.save", scope: "interface", description: "Save the preview", defaults: []string{"s"}},
	{id: "interface.reset", scope: "interface", description: "Preview defaults", defaults: []string{"r"}},
	{id: "setup-picker.cancel", scope: "setup-picker", description: "Cancel provider setup", defaults: []string{"ctrl+c", "q", "esc"}},
	{id: "setup-picker.up", scope: "setup-picker", description: "Select the previous provider", defaults: []string{"up", "k"}},
	{id: "setup-picker.down", scope: "setup-picker", description: "Select the next provider", defaults: []string{"down", "j"}},
	{id: "setup-picker.select", scope: "setup-picker", description: "Configure the selected provider", defaults: []string{"enter"}},
	{id: "setup-picker.disable", scope: "setup-picker", description: "Disable the selected provider", defaults: []string{"d", "delete"}},
	{id: "setup-form.cancel", scope: "setup-form", description: "Cancel provider setup", defaults: []string{"ctrl+c", "esc"}},
	{id: "setup-form.next", scope: "setup-form", description: "Focus the next field", defaults: []string{"tab", "down"}},
	{id: "setup-form.previous", scope: "setup-form", description: "Focus the previous field", defaults: []string{"shift+tab", "up"}},
	{id: "setup-form.enter", scope: "setup-form", description: "Advance or validate the form", defaults: []string{"enter"}},
	{id: "setup-form.save", scope: "setup-form", description: "Validate and save provider settings", defaults: []string{"ctrl+s"}},
}

func keyActionByID(id string) (keyAction, bool) {
	for _, action := range interfaceKeyActions {
		if action.id == id {
			return action, true
		}
	}
	return keyAction{}, false
}

func normalizeKeyName(value string) string {
	if value == " " {
		return "space"
	}
	value = strings.TrimSpace(value)
	if len(value) == 1 && value >= "A" && value <= "Z" {
		return value
	}
	lower := strings.ToLower(value)
	lower = strings.ReplaceAll(lower, "control+", "ctrl+")
	lower = strings.ReplaceAll(lower, "pageup", "pgup")
	lower = strings.ReplaceAll(lower, "pagedown", "pgdown")
	lower = strings.ReplaceAll(lower, "page up", "pgup")
	lower = strings.ReplaceAll(lower, "page down", "pgdown")
	parts := strings.Split(lower, "+")
	if len(parts) > 1 {
		counts := map[string]int{}
		for _, modifier := range parts[:len(parts)-1] {
			if !slices.Contains([]string{"ctrl", "alt", "shift"}, modifier) {
				return lower
			}
			counts[modifier]++
		}
		ordered := make([]string, 0, len(parts)-1)
		for _, modifier := range []string{"ctrl", "alt", "shift"} {
			for range counts[modifier] {
				ordered = append(ordered, modifier)
			}
		}
		base := parts[len(parts)-1]
		if len(ordered) == 1 && ordered[0] == "shift" && len(base) == 1 && base[0] >= 'a' && base[0] <= 'z' {
			return strings.ToUpper(base)
		}
		return strings.Join(append(ordered, base), "+")
	}
	return lower
}

func validKeyName(value string) bool {
	value = normalizeKeyName(value)
	validBase := func(base string) bool {
		if slices.Contains([]string{"up", "down", "left", "right", "enter", "esc", "space", "tab", "pgup", "pgdown", "home", "end", "delete", "backspace", "insert"}, base) {
			return true
		}
		if number, ok := strings.CutPrefix(base, "f"); ok {
			parsed, err := strconv.Atoi(number)
			return err == nil && parsed >= 1 && parsed <= 24
		}
		if utf8.RuneCountInString(base) != 1 {
			return false
		}
		r, _ := utf8.DecodeRuneInString(base)
		return r != utf8.RuneError && !unicode.IsControl(r)
	}
	if utf8.RuneCountInString(value) == 1 {
		return validBase(value)
	}
	parts := strings.Split(value, "+")
	if len(parts) == 1 {
		return validBase(parts[0])
	}
	if len(parts) > 4 {
		return false
	}
	seen := map[string]bool{}
	for _, modifier := range parts[:len(parts)-1] {
		if !slices.Contains([]string{"ctrl", "alt", "shift"}, modifier) || seen[modifier] {
			return false
		}
		seen[modifier] = true
	}
	return validBase(parts[len(parts)-1])
}

func validGlobalKeyName(value string) bool {
	value = normalizeKeyName(value)
	if strings.Contains(value, "+") {
		return validKeyName(value)
	}
	number, ok := strings.CutPrefix(value, "f")
	parsed, err := strconv.Atoi(number)
	return ok && err == nil && parsed >= 1 && parsed <= 24
}

func (settings interfaceSettings) keysFor(action keyAction) []string {
	if keys, ok := settings.Bindings[action.id]; ok {
		return keys
	}
	return action.defaults
}

func displayKeyName(key string) string {
	key = normalizeKeyName(key)
	special := map[string]string{
		"up": "↑", "down": "↓", "left": "←", "right": "→", "space": "Space",
		"enter": "Enter", "esc": "Esc", "tab": "Tab", "pgup": "PgUp", "pgdown": "PgDn",
		"home": "Home", "end": "End", "delete": "Delete",
	}
	if label, ok := special[key]; ok {
		return label
	}
	parts := strings.Split(key, "+")
	for i, part := range parts {
		if label, ok := special[part]; ok {
			parts[i] = label
		} else if part == "ctrl" {
			parts[i] = "Ctrl"
		} else if part == "shift" {
			parts[i] = "Shift"
		} else if strings.HasPrefix(part, "f") {
			parts[i] = strings.ToUpper(part)
		} else if len(part) == 1 && i > 0 {
			parts[i] = strings.ToUpper(part)
		}
	}
	return strings.Join(parts, "+")
}

func (settings interfaceSettings) bindingLabel(id string) string {
	action, ok := keyActionByID(id)
	if !ok {
		return id
	}
	keys := settings.keysFor(action)
	labels := make([]string, len(keys))
	for i, key := range keys {
		labels[i] = displayKeyName(key)
	}
	return strings.Join(labels, "/")
}

func (settings interfaceSettings) bindingHint(id, label string) string {
	return settings.bindingLabel(id) + " " + label
}

func (settings interfaceSettings) bindingPairHint(first, second, label string) string {
	return settings.bindingLabel(first) + "/" + settings.bindingLabel(second) + " " + label
}

func (settings interfaceSettings) actionFor(scope, pressed string) (keyAction, bool) {
	pressed = normalizeKeyName(pressed)
	for _, candidateScope := range []string{scope, parentKeyScope(scope)} {
		if candidateScope == "" {
			continue
		}
		for _, action := range interfaceKeyActions {
			if action.scope != candidateScope || !keyActionApplies(action, scope) {
				continue
			}
			for _, key := range settings.keysFor(action) {
				if normalizeKeyName(key) == pressed {
					return action, true
				}
			}
		}
	}
	return keyAction{}, false
}

func parentKeyScope(scope string) string {
	if slices.Contains([]string{"library", "providers", "radio", "podcasts", "audio", "lyrics"}, scope) {
		return "browser"
	}
	return ""
}

var inheritedBrowserActions = map[string][]string{
	"library": {
		"browser.back", "browser.up", "browser.down", "browser.page-up", "browser.page-down", "browser.select", "browser.search",
		"browser.play-next", "browser.append", "browser.favorite", "browser.bookmark", "browser.remove", "browser.undo", "browser.shuffle", "browser.repeat",
	},
	"providers": {"browser.back", "browser.up", "browser.down", "browser.page-up", "browser.page-down", "browser.home", "browser.end", "browser.select", "browser.search", "browser.refresh", "browser.favorite", "browser.bookmark"},
	"radio":     {"browser.back", "browser.up", "browser.down", "browser.page-up", "browser.page-down", "browser.home", "browser.end", "browser.select", "browser.search", "browser.refresh", "browser.favorite", "browser.pause"},
	"podcasts":  {"browser.back", "browser.up", "browser.down", "browser.page-up", "browser.page-down", "browser.home", "browser.end", "browser.select", "browser.search", "browser.refresh", "browser.favorite", "browser.pause"},
	"audio":     {"browser.back", "browser.up", "browser.down", "browser.page-up", "browser.page-down", "browser.home", "browser.end", "browser.refresh"},
	"lyrics":    {"browser.back", "browser.up", "browser.down", "browser.page-up", "browser.page-down", "browser.home", "browser.end", "browser.refresh"},
}

func keyActionApplies(action keyAction, scope string) bool {
	if action.scope == scope {
		return true
	}
	if action.scope != "browser" || parentKeyScope(scope) != "browser" {
		return false
	}
	return slices.Contains(inheritedBrowserActions[scope], action.id)
}

// mapKey translates a user binding back to the existing handler's canonical key.
func (settings interfaceSettings) mapKey(scope, pressed string) string {
	pressed = normalizeKeyName(pressed)
	if action, ok := settings.actionFor(scope, pressed); ok {
		return action.defaults[0]
	}
	// Once an action is overridden, its old defaults must stop activating it.
	for _, action := range interfaceKeyActions {
		if !keyActionApplies(action, scope) {
			continue
		}
		if _, overridden := settings.Bindings[action.id]; overridden {
			for _, key := range action.defaults {
				if normalizeKeyName(key) == pressed {
					return "unbound:" + pressed
				}
			}
		}
	}
	return pressed
}

func keyBindingConflicts(settings interfaceSettings) []string {
	type owner struct{ id, scope string }
	owners := map[string]owner{}
	var conflicts []string
	for _, action := range interfaceKeyActions {
		for _, key := range settings.keysFor(action) {
			key = normalizeKeyName(key)
			identity := action.scope + "\x00" + key
			if previous, ok := owners[identity]; ok && previous.id != action.id {
				conflicts = append(conflicts, fmt.Sprintf("%s conflicts with %s on %s in %s", action.id, previous.id, key, action.scope))
			} else if !ok {
				owners[identity] = owner{action.id, action.scope}
			}
			if action.scope != "global" && globalKeyAppliesToScope(action.scope) {
				globalIdentity := "global\x00" + key
				if previous, ok := owners[globalIdentity]; ok {
					conflicts = append(conflicts, fmt.Sprintf("%s conflicts with %s on global key %s", action.id, previous.id, key))
				}
			} else if action.scope == "global" {
				for identity, previous := range owners {
					parts := strings.SplitN(identity, "\x00", 2)
					if len(parts) == 2 && parts[0] != "global" && globalKeyAppliesToScope(parts[0]) && parts[1] == key {
						conflicts = append(conflicts, fmt.Sprintf("%s conflicts with %s on global key %s", action.id, previous.id, key))
					}
				}
			}
		}
	}
	for _, scope := range []string{"library", "providers", "radio", "podcasts", "audio", "lyrics"} {
		effective := map[string]keyAction{}
		for _, action := range interfaceKeyActions {
			if !keyActionApplies(action, scope) {
				continue
			}
			for _, key := range settings.keysFor(action) {
				key = normalizeKeyName(key)
				if previous, ok := effective[key]; ok && previous.id != action.id && (settings.Bindings[action.id] != nil || settings.Bindings[previous.id] != nil) {
					conflicts = append(conflicts, fmt.Sprintf("%s conflicts with %s on %s in %s", action.id, previous.id, key, scope))
				} else if !ok {
					effective[key] = action
				}
			}
		}
	}
	slices.Sort(conflicts)
	return slices.Compact(conflicts)
}

func globalKeyAppliesToScope(scope string) bool {
	return scope != "playback" && scope != "foreground-lyrics"
}

type interfaceKeyEntry struct {
	id          string
	scope       string
	description string
	keys        []string
}

func interfaceKeyEntries(settings interfaceSettings, scope, query string) []interfaceKeyEntry {
	query = strings.ToLower(strings.TrimSpace(query))
	actions := slices.Clone(interfaceKeyActions)
	slices.SortFunc(actions, func(a, b keyAction) int {
		if a.scope == b.scope {
			return strings.Compare(a.id, b.id)
		}
		return strings.Compare(a.scope, b.scope)
	})
	var entries []interfaceKeyEntry
	for _, action := range actions {
		if scope != "" && action.scope != "global" && !keyActionApplies(action, scope) {
			continue
		}
		keys := settings.keysFor(action)
		searchable := strings.ToLower(action.id + " " + action.scope + " " + action.description + " " + strings.Join(keys, " "))
		if query != "" && !strings.Contains(searchable, query) {
			continue
		}
		entries = append(entries, interfaceKeyEntry{id: action.id, scope: action.scope, description: action.description, keys: keys})
	}
	return entries
}

func interfaceKeyRows(settings interfaceSettings, scope, query string) []string {
	entries := interfaceKeyEntries(settings, scope, query)
	rows := make([]string, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, fmt.Sprintf("%-18s  %-24s  %s", strings.Join(entry.keys, ", "), entry.id, entry.description))
	}
	return rows
}

func interfaceKeyValues(settings interfaceSettings, scope, query string) []map[string]any {
	entries := interfaceKeyEntries(settings, scope, query)
	values := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		values = append(values, map[string]any{"id": entry.id, "scope": entry.scope, "description": entry.description, "keys": entry.keys})
	}
	return values
}

func runKeysCommand(args []string, jsonOutput bool) (string, error) {
	args, jsonOutput = consumeLocalJSONFlag(args, jsonOutput)
	if helpRequested(args) {
		return "Usage: chill keys [list|search <text>|set <action> <key>...|reset [action]|conflicts] [--json]", nil
	}
	command := "list"
	if len(args) > 0 {
		command, args = strings.ToLower(args[0]), args[1:]
	}
	if command == "reset" {
		if len(args) > 1 {
			return "", errors.New("usage: chill keys reset [action]")
		}
		action := ""
		if len(args) == 1 {
			action = args[0]
			if _, ok := keyActionByID(action); !ok {
				return "", fmt.Errorf("unknown key action %q", action)
			}
		}
		settings, err := resetInterfaceBindings(action)
		if err != nil {
			return "", err
		}
		if jsonOutput {
			return marshalInterfaceJSON(map[string]any{"bindings": settings.Bindings})
		}
		return "keybindings reset", nil
	}
	settings, err := loadInterfaceSettings()
	if err != nil {
		return "", err
	}
	switch command {
	case "list", "show":
		if jsonOutput {
			return marshalInterfaceJSON(interfaceKeyValues(settings, "", ""))
		}
		return strings.Join(interfaceKeyRows(settings, "", ""), "\n"), nil
	case "search":
		if len(args) == 0 {
			return "", errors.New("usage: chill keys search <text>")
		}
		query := strings.Join(args, " ")
		if jsonOutput {
			return marshalInterfaceJSON(interfaceKeyValues(settings, "", query))
		}
		return strings.Join(interfaceKeyRows(settings, "", query), "\n"), nil
	case "set":
		if len(args) < 2 {
			return "", errors.New("usage: chill keys set <action> <key> [key]")
		}
		action := args[0]
		if _, ok := keyActionByID(action); !ok {
			return "", fmt.Errorf("unknown key action %q", action)
		}
		var keys []string
		for _, value := range args[1:] {
			for key := range strings.SplitSeq(value, ",") {
				if key = normalizeKeyName(key); key != "" && !slices.Contains(keys, key) {
					keys = append(keys, key)
				}
			}
		}
		if len(keys) == 0 {
			return "", errors.New("at least one key is required")
		}
		settings.Bindings[action] = keys
		if err := saveInterfaceSettings(settings); err != nil {
			return "", err
		}
		if jsonOutput {
			return marshalInterfaceJSON(map[string]any{"action": action, "keys": keys})
		}
		return action + ": " + strings.Join(keys, ", "), nil
	case "conflicts":
		conflicts := keyBindingConflicts(settings)
		if jsonOutput {
			if conflicts == nil {
				conflicts = []string{}
			}
			return marshalInterfaceJSON(conflicts)
		}
		if len(conflicts) == 0 {
			return "no keybinding conflicts", nil
		}
		return "", errors.New(strings.Join(conflicts, "\n"))
	default:
		return "", fmt.Errorf("unknown keys command %q", command)
	}
}

func runInterfaceCommand(args []string, jsonOutput bool) (string, error) {
	args, jsonOutput = consumeLocalJSONFlag(args, jsonOutput)
	if helpRequested(args) {
		return "Usage: chill interface [show|set <setting> <value>|panel <name> <on|off>|reset] [--json]", nil
	}
	command := "show"
	if len(args) > 0 {
		command, args = strings.ToLower(args[0]), args[1:]
	}
	if command == "reset" {
		if len(args) != 0 {
			return "", errors.New("usage: chill interface reset")
		}
		settings := defaultInterfaceSettings()
		if err := saveInterfaceSettings(settings); err != nil {
			return "", err
		}
		if jsonOutput {
			return marshalInterfaceJSON(settings)
		}
		return "interface settings saved", nil
	}
	settings, err := loadInterfaceSettings()
	if err != nil {
		return "", err
	}
	switch command {
	case "show":
		data, marshalErr := json.Marshal(&settings, json.Deterministic(true), jsontext.WithIndent("  "))
		if marshalErr != nil {
			return "", marshalErr
		}
		if jsonOutput {
			return string(data), nil
		}
		return fmt.Sprintf("theme %s · colors %s · characters %s · screen %s · visualizer %d · seek %ds/%ds · simplified %t · low power %t",
			settings.Theme, settings.ColorMode, settings.CharacterMode, settings.DefaultScreen, settings.VisualizerHeight, settings.SeekStep, settings.SeekLargeStep, settings.Simplified, settings.LowPower), nil
	case "panel":
		if len(args) != 2 || !slices.Contains(interfacePanelNames(), strings.ToLower(args[0])) {
			return "", errors.New("usage: chill interface panel <source|queue|equalizer|audio|downloads|network|metadata> <on|off>")
		}
		value, parseErr := parseInterfaceBool(args[1])
		if parseErr != nil {
			return "", parseErr
		}
		settings.Panels[strings.ToLower(args[0])] = value
	case "set":
		if len(args) < 2 {
			return "", errors.New("usage: chill interface set <setting> <value>")
		}
		field, value := strings.ToLower(args[0]), strings.Join(args[1:], " ")
		switch field {
		case "theme":
			settings.Theme = value
		case "colors", "color-mode":
			settings.ColorMode = value
		case "characters", "character-mode":
			settings.CharacterMode = value
		case "simplified":
			settings.Simplified, err = parseInterfaceBool(value)
		case "low-power":
			settings.LowPower, err = parseInterfaceBool(value)
		case "show-status":
			settings.ShowStatus, err = parseInterfaceBool(value)
		case "show-help", "help-hints":
			settings.ShowHelp, err = parseInterfaceBool(value)
		case "visualizer-height":
			settings.VisualizerHeight, err = strconv.Atoi(value)
		case "seek-step":
			settings.SeekStep, err = strconv.Atoi(value)
		case "seek-large-step":
			settings.SeekLargeStep, err = strconv.Atoi(value)
		case "initial-directory":
			settings.InitialDirectory = value
		case "default-screen":
			settings.DefaultScreen = value
		case "status-fields":
			settings.StatusFields = nil
			for field := range strings.SplitSeq(value, ",") {
				settings.StatusFields = append(settings.StatusFields, strings.TrimSpace(field))
			}
		default:
			return "", fmt.Errorf("unknown interface setting %q", field)
		}
		if err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unknown interface command %q", command)
	}
	if err := saveInterfaceSettings(settings); err != nil {
		return "", err
	}
	if jsonOutput {
		return marshalInterfaceJSON(settings)
	}
	return "interface settings saved", nil
}

func parseInterfaceBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	default:
		return false, fmt.Errorf("expected on or off, got %q", value)
	}
}
