// repl.go implements the commands, suggestions and history behind the
// interactive REPL. The fullscreen UI that drives them lives in tui.go.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// replCommand describes a REPL command for help and suggestions.
type replCommand struct {
	name string // what the user types
	args string // argument hint shown in help
	desc string // one-line description
}

// replCommands contains the available REPL commands.
var replCommands = []replCommand{
	{"play", "[station]", "play a station"},
	{"skip", "", "skip to random station"},
	{"pause", "", "pause playback"},
	{"resume", "", "resume playback"},
	{"toggle", "", "toggle play/pause"},
	{"status", "", "show current status"},
	{"list", "", "list all stations"},
	{"stop", "", "stop playback"},
	{"clear", "", "clear the screen"},
	{"help", "", "show this help"},
	{"quit", "", "leave the repl, music keeps playing"},
}

// suggestion is one entry offered while typing.
type suggestion struct {
	text    string // what gets inserted
	desc    string // shown next to it
	station bool   // a station name rather than a command
	takes   bool   // an argument follows, so accepting it adds a space
}

// suggest returns what could complete the word at the end of input. Nothing
// is offered for an empty line, so the list never opens on its own.
func suggest(input string) []suggestion {
	words := strings.Fields(input)
	if len(words) == 0 {
		return nil
	}
	typingNewWord := strings.HasSuffix(input, " ")

	var candidates []suggestion
	prefix := ""

	switch {
	case len(words) == 1 && !typingNewWord:
		// first word: commands, or a station name on its own
		prefix = words[0]
		for _, c := range replCommands {
			candidates = append(candidates, suggestion{text: c.name, desc: c.desc, takes: c.args != ""})
		}
		candidates = append(candidates, stationSuggestions()...)

	case strings.EqualFold(words[0], "play") && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		candidates = stationSuggestions()
	}

	var matches []suggestion
	for _, c := range candidates {
		if strings.HasPrefix(c.text, strings.ToLower(prefix)) {
			matches = append(matches, c)
		}
	}

	// nothing left to complete
	if len(matches) == 1 && strings.EqualFold(matches[0].text, prefix) {
		return nil
	}
	return matches
}

// stationSuggestions returns the station names as suggestions.
func stationSuggestions() []suggestion {
	var s []suggestion
	for _, st := range stations {
		s = append(s, suggestion{text: st.Name, desc: st.Desc, station: true})
	}
	return s
}

// acceptSuggestion replaces the word at the end of input with the suggestion.
func acceptSuggestion(input string, s suggestion) string {
	start := strings.LastIndex(input, " ") + 1
	if s.takes {
		return input[:start] + s.text + " "
	}
	return input[:start] + s.text
}

// execute runs one line of REPL input by parsing the command and delegating
// to the daemon. It returns what to show for it.
func execute(input string) (string, error) {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return "", nil
	}

	cmd := strings.ToLower(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = parts[1]
	}

	var out string
	var err error

	switch cmd {
	case "play":
		if arg == "" {
			arg = "lofi-girl"
		}
		if findStation(arg) == nil {
			return "", fmt.Errorf("unknown station %s, try list", arg)
		}
		out, err = clientPlay(arg)

	case "skip":
		out, err = clientSkip()

	case "pause":
		out, err = clientPause()

	case "resume":
		out, err = clientResume()

	case "toggle":
		out, err = clientToggle()

	case "status":
		out, err = clientStatus()

	case "list":
		out = stationList()

	case "stop":
		out, err = clientStop()

	case "help", "?":
		out = replHelp()

	default:
		// try as station name
		if findStation(cmd) == nil {
			return "", fmt.Errorf("unknown command %s, try help", cmd)
		}
		out, err = clientPlay(cmd)
	}

	return out, err
}

// replHelp lists the REPL commands.
func replHelp() string {
	var b strings.Builder
	for _, c := range replCommands {
		fmt.Fprintf(&b, "%s%-16s%s  %s%s%s\n", cyan, strings.TrimSpace(c.name+" "+c.args), reset, dim, c.desc, reset)
	}
	fmt.Fprintf(&b, "%s%-16s%s  %s%s%s", cyan, "<station>", reset, dim, "same as play <station>", reset)
	return b.String()
}

// stationList lists the stations, marking the one that is loaded.
func stationList() string {
	current := ""
	if s, _ := fetchStatus(); s != nil {
		current = s.Station
	}

	var lines []string
	for _, s := range stations {
		marker := " "
		if s.Name == current {
			marker = pink + "♪" + reset
		}
		lines = append(lines, fmt.Sprintf("%s %s%-16s%s  %s%s%s", marker, cyan, s.Name, reset, dim, s.Desc, reset))
	}
	return strings.Join(lines, "\n")
}

// maxHistory is how many lines of history are kept.
const maxHistory = 500

// history holds submitted lines, oldest first, and saves them to a file.
type history struct {
	path  string // "" keeps history in memory only
	lines []string
}

// loadHistory reads the saved history, if there is any.
func loadHistory() *history {
	h := &history{}

	dir, err := os.UserConfigDir()
	if err != nil {
		return h
	}
	dir = filepath.Join(dir, "chill")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return h
	}
	h.path = filepath.Join(dir, "history")

	data, err := os.ReadFile(h.path)
	if err != nil {
		return h
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			h.lines = append(h.lines, line)
		}
	}
	if len(h.lines) > maxHistory {
		h.lines = h.lines[len(h.lines)-maxHistory:]
	}
	return h
}

// add records a submitted line, skipping repeats of the previous one.
func (h *history) add(line string) {
	if len(h.lines) > 0 && h.lines[len(h.lines)-1] == line {
		return
	}
	h.lines = append(h.lines, line)

	if h.path == "" {
		return
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}
