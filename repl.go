// repl.go implements the commands, suggestions and history behind the
// interactive REPL. The fullscreen UI that drives them lives in tui.go.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/willibrandon/chill/internal/visualizer"
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
	{"vol", "[level]", "set volume (0-100, +5, -10, up, down)"},
	{"mute", "", "toggle mute"},
	{"skip", "", "skip to random station"},
	{"pause", "", "pause playback"},
	{"resume", "", "resume playback"},
	{"toggle", "", "toggle play/pause"},
	{"status", "", "show current status"},
	{"viz", "[mode|off|list]", "show or select a REPL visualizer (F2 to focus)"},
	{"podcasts", "[command|feed-url]", "browse podcasts (F3; --help for commands)"},
	{"seek", "<seconds>", "jump within an episode (-30, +30, 2m)"},
	{"speed", "[0.5-3]", "set podcast playback speed"},
	{"next", "", "play the next queued episode"},
	{"prev", "", "restart an episode or play the previous one"},
	{"doctor", "[options]", "check setup and streams (--help for options)"},
	{"cancel", "", "cancel diagnostics and discard queued commands"},
	{"list", "", "list all stations"},
	{"add", "<name> <url> [desc]", "save a station to the config"},
	{"remove", "<name>", "remove a custom station or override"},
	{"default", "<station>", "set the default station"},
	{"sleep", "<duration|off>", "set or cancel a sleep timer (45m, 1h)"},
	{"reload", "", "reload stations from the config"},
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

	case (strings.EqualFold(words[0], "play") || strings.EqualFold(words[0], "remove") || strings.EqualFold(words[0], "default")) && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		candidates = stationSuggestions()

	case strings.EqualFold(words[0], "vol") && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		candidates = []suggestion{
			{text: "up", desc: "a notch louder"},
			{text: "down", desc: "a notch quieter"},
		}

	case strings.EqualFold(words[0], "viz") && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		for _, name := range visualizer.Modes {
			candidates = append(candidates, suggestion{text: name, desc: "visualizer"})
		}
		for _, name := range []string{"off", "on", "next", "prev", "list", "fullscreen"} {
			candidates = append(candidates, suggestion{text: name, desc: "visualizer control"})
		}

	case (strings.EqualFold(words[0], "podcasts") || strings.EqualFold(words[0], "podcast")) && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		for _, name := range []string{"top", "search", "categories", "category", "episodes", "subscribe", "unsubscribe", "subscriptions", "country", "play", "latest", "queue", "clear", "--help"} {
			candidates = append(candidates, suggestion{text: name, desc: "podcast command", takes: name == "search" || name == "play" || name == "episodes" || name == "subscribe" || name == "unsubscribe" || name == "category"})
		}

	case strings.EqualFold(words[0], "doctor"):
		args := words[1:]
		if !typingNewWord {
			prefix = args[len(args)-1]
			args = args[:len(args)-1]
		}
		candidates = doctorSuggestions(args)
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

// doctorSuggestions follows completed options so values are suggested in the
// right place and mutually exclusive stream selectors aren't offered together.
func doctorSuggestions(args []string) []suggestion {
	used := make(map[string]bool)
	value := ""
	for _, arg := range args {
		if value != "" {
			value = ""
			continue
		}
		name, _, hasValue := strings.Cut(arg, "=")
		if !strings.HasPrefix(name, "-") {
			return nil
		}
		name = strings.TrimLeft(name, "-")
		switch name {
		case "stream", "timeout":
			if !hasValue {
				value = name
			}
		case "stations", "logs":
		case "help", "h":
			return nil
		default:
			return nil
		}
		used[name] = true
	}
	if value == "stream" {
		return stationSuggestions()
	}
	if value == "timeout" {
		return []suggestion{
			{text: "30s", desc: "30 seconds per stream"},
			{text: "45s", desc: "45 seconds per stream (default)"},
			{text: "1m", desc: "one minute per stream"},
		}
	}
	var options []suggestion
	for _, option := range []suggestion{
		{text: "--stations", desc: "check all configured streams"},
		{text: "--stream", desc: "check a station name or URL", takes: true},
		{text: "--timeout", desc: "set the timeout per stream", takes: true},
		{text: "--logs", desc: "show the latest daemon startup log"},
		{text: "--help", desc: "show diagnostic options and examples"},
	} {
		name := strings.TrimPrefix(option.text, "--")
		if used[name] || name == "stream" && used["stations"] || name == "stations" && used["stream"] {
			continue
		}
		options = append(options, option)
	}
	return options
}

// stationSuggestions returns the station names as suggestions.
func stationSuggestions() []suggestion {
	var s []suggestion
	for _, st := range stationSnapshot() {
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
	input = strings.TrimSpace(input)
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return "", nil
	}

	// the argument keeps its original case and spacing, for descriptions
	cmd := strings.ToLower(parts[0])
	arg := ""
	arg = strings.TrimSpace(input[len(parts[0]):])

	var out string
	var err error

	switch cmd {
	case "podcasts", "podcast":
		return runPodcast(context.Background(), parts[1:], true)
	case "seek":
		return clientSeek(arg)
	case "speed", "next", "prev":
		return clientPodcastControl(cmd, arg)
	case "play":
		if arg == "" {
			arg = defaultStation()
		}
		if findStation(arg) == nil {
			return "", fmt.Errorf("unknown station %s, try list", arg)
		}
		out, err = clientPlay(arg)

	case "vol", "volume":
		out, err = clientVolume(arg)

	case "mute":
		out, err = clientMute()

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

	case "doctor":
		var report strings.Builder
		err = runDoctor(parts[1:], &report)
		out = strings.TrimSuffix(report.String(), "\n")

	case "list":
		out = stationList()

	case "add":
		if len(parts) < 3 {
			return "", fmt.Errorf("usage: add <name> <url> [description...]")
		}
		var addErr error
		out, addErr = saveStation(parts[1:])
		if addErr != nil {
			return "", addErr
		}
		// this process has its own copy of the station list too
		configErr = loadUserStations()

	case "remove":
		out, err = removeStation(parts[1:])

	case "default":
		out, err = saveDefaultStation(parts[1:])

	case "sleep":
		if arg == "" {
			out, err = clientPlay("sleep") // keep the existing station shorthand
		} else {
			out, err = clientSleep(arg)
		}

	case "reload":
		// the REPL and the daemon each read the config, so both do
		if err := loadUserStations(); err != nil {
			return "", err
		}
		configErr = nil
		if isDaemonRunning() {
			out, err = ask("reload")
		} else {
			out = fmt.Sprintf("reloaded, %d stations", len(stationSnapshot()))
		}

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

// helpNames are the command names with their argument hints, plus the
// station shorthand, in the order help lists them.
func helpNames() []string {
	var names []string
	for _, c := range replCommands {
		names = append(names, strings.TrimSpace(c.name+" "+c.args))
	}
	return append(names, "<station>")
}

// helpWidth is the column the descriptions start on, fitting the longest name.
func helpWidth() int {
	w := 0
	for _, name := range helpNames() {
		w = max(w, len(name))
	}
	return w + 2
}

// replHelp lists the REPL commands.
func replHelp() string {
	var b strings.Builder
	for i, c := range replCommands {
		fmt.Fprintf(&b, "%s%-*s%s  %s%s%s\n", cyan, helpWidth(), helpNames()[i], reset, dim, c.desc, reset)
	}
	fmt.Fprintf(&b, "%s%-*s%s  %s%s%s", cyan, helpWidth(), "<station>", reset, dim, "same as play <station>", reset)
	return b.String()
}

// stationList lists the stations, marking the one that is loaded.
func stationList() string {
	current := ""
	if s, _ := fetchStatus(); s != nil {
		current = s.Station
	}

	var lines []string
	for _, s := range stationSnapshot() {
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
