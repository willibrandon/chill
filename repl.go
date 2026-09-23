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
	{"play", "[station|path|url]", "play a station, local file, folder, playlist, or URL"},
	{"open", "[path]", "browse the library or play a local path"},
	{"queue", "[command]", "show or edit the universal queue"},
	{"playlist", "[command]", "manage saved playlists"},
	{"library", "[recent|favorites|bookmarks]", "browse listening collections"},
	{"search", "<words>", "search every enabled music provider"},
	{"browse", "<provider>", "browse a provider catalog"},
	{"providers", "[--check]", "list provider capabilities and connections"},
	{"setup", "[provider]", "configure a provider securely"},
	{"link", "[open|register|unregister|status]", "use secure chill:// links"},
	{"completion", "<shell>", "generate shell completion definitions"},
	{"theme", "[list|set|preview|validate]", "inspect or change terminal themes (F10)"},
	{"keys", "[list|search|set|reset]", "inspect or remap terminal keys (Ctrl+K)"},
	{"interface", "[show|set|panel|reset]", "configure layouts and accessibility"},
	{"shuffle", "[on|off|toggle]", "control queue shuffle"},
	{"repeat", "[off|all|one|cycle]", "control queue repeat"},
	{"favorite", "", "favorite or unfavorite the current item"},
	{"bookmark", "", "bookmark or unbookmark the current item"},
	{"vol", "[level]", "set volume (0-100, +5, -10, up, down)"},
	{"audio", "[setting]", "show or change audio output settings"},
	{"device", "[list|set|default]", "list or switch audio output devices"},
	{"mono", "[on|off|toggle]", "control mono downmix"},
	{"mute", "", "toggle mute"},
	{"skip", "", "skip to random station"},
	{"pause", "", "pause playback"},
	{"resume", "", "resume playback"},
	{"toggle", "", "toggle play/pause"},
	{"status", "", "show current status"},
	{"remote", "[state|capabilities|call|events|job|cancel]", "use the versioned automation API"},
	{"eq", "[preset|--band N dB|list]", "show or edit the 10-band equalizer (F4)"},
	{"viz", "[mode|off|list]", "show or select a REPL visualizer (F2 to focus)"},
	{"podcasts", "[command|feed-url]", "browse podcasts (F3; --help for commands)"},
	{"radio", "[command]", "discover and favorite stations (F5; --help for commands)"},
	{"history", "[--limit N|clear]", "show recently heard radio tracks"},
	{"lyrics", "", "show lyrics for the current track (F6)"},
	{"notifications", "[on|off]", "control track-change notifications"},
	{"seek", "<seconds>", "jump within finite media (-30, +30, 2m)"},
	{"speed", "[0.5-3]", "set finite-media playback speed"},
	{"next", "", "play the next queued item"},
	{"prev", "", "restart finite media or play the previous item"},
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
	replace string // complete replacement for suggestions containing spaces
}

// splitCommandLine gives the REPL predictable quoting on every platform.
// Quotes group spaces and backslash escapes whitespace or quotes outside
// single quotes. Other backslashes stay intact for Windows paths.
func splitCommandLine(input string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	flush := func() {
		if started {
			words = append(words, word.String())
			word.Reset()
			started = false
		}
	}
	for _, r := range input {
		if escaped {
			if r != '\'' && r != '"' && r != ' ' && r != '\t' && r != '\r' && r != '\n' {
				word.WriteByte('\\')
			}
			word.WriteRune(r)
			escaped, started = false, true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped, started = true, true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			started = true
			continue
		}
		if r == '\'' || r == '"' {
			quote, started = r, true
			continue
		}
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			flush()
			continue
		}
		word.WriteRune(r)
		started = true
	}
	if escaped {
		word.WriteByte('\\')
		started = true
	}
	if quote != 0 {
		return nil, fmt.Errorf("unfinished quoted argument")
	}
	flush()
	return words, nil
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
			// Keep the long-standing `pl` completion unambiguous; playlist
			// appears as soon as enough of its name has been typed.
			if c.name == "playlist" && len(prefix) < 5 {
				continue
			}
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

	case strings.EqualFold(words[0], "notifications") && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		candidates = []suggestion{{text: "on", desc: "show track changes"}, {text: "off", desc: "hide track changes"}}

	case strings.EqualFold(words[0], "audio") && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		candidates = append(candidates, []suggestion{
			{text: "profile", desc: "select a quality profile", takes: true},
			{text: "device", desc: "select an output device", takes: true},
			{text: "sample-rate", desc: "set PCM sample rate", takes: true},
			{text: "buffer", desc: "set output buffer milliseconds", takes: true},
			{text: "resample-quality", desc: "set resampling quality", takes: true},
			{text: "mono", desc: "control mono downmix", takes: true},
			{text: "channels", desc: "select mono or stereo", takes: true},
			{text: "exclusive", desc: "control exclusive device access", takes: true},
			{text: "list", desc: "list output devices"},
		}...)

	case strings.EqualFold(words[0], "eq"):
		arg := strings.TrimSpace(input[len(words[0]):])
		bandFields := strings.Fields(arg)
		if len(bandFields) > 0 && strings.EqualFold(bandFields[0], "--band") {
			if len(bandFields) == 1 || len(bandFields) == 2 && !typingNewWord {
				if len(bandFields) == 2 {
					prefix = bandFields[1]
				}
				for i, label := range equalizerBandLabels {
					candidates = append(candidates, suggestion{
						text: label, desc: fmt.Sprintf("band %d · %sHz", i, label), takes: true,
						replace: "eq --band " + label,
					})
				}
			}
		} else {
			prefix = arg
			for _, preset := range equalizerPresets {
				candidates = append(candidates, suggestion{text: preset.Name, desc: "EQ preset", replace: "eq " + preset.Name})
			}
			candidates = append(candidates,
				suggestion{text: customEqualizerPreset, desc: "saved custom curve", replace: "eq " + customEqualizerPreset},
				suggestion{text: "list", desc: "list EQ presets", replace: "eq list"},
				suggestion{text: "next", desc: "next EQ preset", replace: "eq next"},
				suggestion{text: "prev", desc: "previous EQ preset", replace: "eq prev"},
				suggestion{text: "--band", desc: "edit one EQ band", takes: true, replace: "eq --band"},
			)
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
		for _, name := range []string{"top", "search", "categories", "category", "episodes", "subscribe", "unsubscribe", "subscriptions", "inbox", "sync", "download", "downloads", "auto", "country", "play", "latest", "queue", "clear", "--help"} {
			candidates = append(candidates, suggestion{text: name, desc: "podcast command", takes: name == "search" || name == "play" || name == "episodes" || name == "subscribe" || name == "unsubscribe" || name == "category"})
		}

	case strings.EqualFold(words[0], "radio") && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		candidates = append(candidates, []suggestion{
			{text: "top", desc: "most-voted stations"}, {text: "popular", desc: "most-listened stations"},
			{text: "trending", desc: "stations gaining listeners"}, {text: "random", desc: "random stations"},
			{text: "search", desc: "search station names", takes: true}, {text: "country", desc: "browse a country", takes: true},
			{text: "tag", desc: "browse a genre or tag", takes: true}, {text: "countries", desc: "list countries"},
			{text: "tags", desc: "list genres and tags"}, {text: "favorites", desc: "favorite stations"},
			{text: "nearby", desc: "local country suggestions", takes: true}, {text: "--help", desc: "radio command help"},
		}...)

	case strings.EqualFold(words[0], "remote") && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		candidates = append(candidates, []suggestion{
			{text: "state", desc: "runtime snapshot"},
			{text: "capabilities", desc: "available operations"},
			{text: "call", desc: "submit an operation", takes: true},
			{text: "events", desc: "stream event topics", takes: true},
			{text: "job", desc: "read a job", takes: true},
			{text: "cancel", desc: "cancel a job", takes: true},
		}...)

	case strings.EqualFold(words[0], "queue") && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		candidates = append(candidates, []suggestion{
			{text: "list", desc: "show pending items"}, {text: "add", desc: "append files, folders, playlists, or URLs", takes: true},
			{text: "next", desc: "add to play-next", takes: true}, {text: "replace", desc: "replace pending items", takes: true},
			{text: "play", desc: "play a numbered item", takes: true}, {text: "remove", desc: "remove a numbered item", takes: true},
			{text: "move", desc: "reorder two positions", takes: true}, {text: "search", desc: "filter pending items", takes: true},
			{text: "undo", desc: "undo the last queue edit"}, {text: "clear", desc: "clear pending items"},
		}...)

	case strings.EqualFold(words[0], "playlist") && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		for _, name := range []string{"list", "show", "play", "create", "add", "save", "remove", "move", "dedupe", "rename", "delete", "import", "export"} {
			candidates = append(candidates, suggestion{text: name, desc: "playlist command", takes: name != "list"})
		}

	case strings.EqualFold(words[0], "library") && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		for _, name := range []string{"recent", "favorites", "bookmarks", "play", "add", "next"} {
			candidates = append(candidates, suggestion{text: name, desc: "library collection", takes: name == "play" || name == "add" || name == "next"})
		}
	case (strings.EqualFold(words[0], "shuffle") || strings.EqualFold(words[0], "repeat")) && (len(words) == 1 || len(words) == 2 && !typingNewWord):
		if len(words) == 2 {
			prefix = words[1]
		}
		values := []string{"on", "off", "toggle"}
		if strings.EqualFold(words[0], "repeat") {
			values = []string{"off", "all", "one", "cycle"}
		}
		for _, value := range values {
			candidates = append(candidates, suggestion{text: value, desc: words[0] + " mode"})
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
		if strings.HasPrefix(strings.ToLower(c.text), strings.ToLower(prefix)) {
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
	if s.replace != "" {
		if s.takes {
			return strings.TrimRight(s.replace, " ") + " "
		}
		return s.replace
	}
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
	parts, parseErr := splitCommandLine(input)
	if parseErr != nil {
		return "", parseErr
	}
	if len(parts) == 0 {
		return "", nil
	}

	// Parsed arguments keep their original case and may contain quoted spaces.
	cmd := strings.ToLower(parts[0])
	arg := strings.Join(parts[1:], " ")

	var out string
	var err error

	switch cmd {
	case "lyrics":
		return runLyricsCommand(context.Background(), parts[1:], true)
	case "history":
		return runHistoryCommand(parts[1:], true)
	case "radio":
		return runRadioCommand(context.Background(), parts[1:], true)
	case "podcasts", "podcast":
		return runPodcast(context.Background(), parts[1:], true)
	case "seek":
		return clientSeek(arg)
	case "speed", "next", "prev":
		return clientPodcastControl(cmd, arg)
	case "queue":
		return runQueueCommand(context.Background(), parts[1:])
	case "playlist":
		return runPlaylistCommand(context.Background(), parts[1:])
	case "library":
		return runLibraryCommand(parts[1:])
	case "search":
		return runSearchCommand(context.Background(), parts[1:], false, false)
	case "browse":
		return runBrowseCommand(context.Background(), parts[1:], false, false)
	case "providers":
		return runProvidersCommand(context.Background(), parts[1:], false)
	case "setup":
		if helpRequested(parts[1:]) {
			return "Usage: chill setup [provider]", nil
		}
		return "", fmt.Errorf("provider setup uses a protected terminal form: run chill setup%s", func() string {
			if arg != "" {
				return " " + arg
			}
			return ""
		}())
	case "link":
		return runLinkCommand(context.Background(), parts[1:], false)
	case "completion":
		return runCompletionCommand(parts[1:])
	case "theme":
		return runThemeCommand(parts[1:], false)
	case "keys":
		return runKeysCommand(parts[1:], false)
	case "interface":
		return runInterfaceCommand(parts[1:], false)
	case "shuffle", "repeat":
		if err := ensureDaemon(); err != nil {
			return "", err
		}
		return ask(strings.TrimSpace(cmd + " " + arg))
	case "favorite", "bookmark":
		if arg != "" {
			return "", fmt.Errorf("usage: %s", cmd)
		}
		if !isDaemonRunning() {
			return "", fmt.Errorf("nothing playing")
		}
		return ask(cmd)
	case "open":
		if len(parts) == 1 {
			return "", fmt.Errorf("press F7 to browse local audio")
		}
		items, loadErr := loadMediaInputs(context.Background(), parts[1:])
		if loadErr != nil {
			return "", loadErr
		}
		return playMediaItems(items)
	case "play":
		if len(parts) == 1 {
			return clientPlay(defaultStation())
		}
		if len(parts) == 2 && findStation(parts[1]) != nil {
			out, err = clientPlay(parts[1])
			break
		}
		items, loadErr := loadMediaInputs(context.Background(), parts[1:])
		if loadErr != nil {
			return "", loadErr
		}
		return playMediaItems(items)

	case "vol", "volume":
		out, err = clientVolume(arg)

	case "audio":
		return runAudioCommand(context.Background(), parts[1:], false)

	case "device":
		if helpRequested(parts[1:]) {
			return deviceCommandHelp, nil
		}
		deviceArgs := parts[1:]
		if len(deviceArgs) == 0 {
			deviceArgs = []string{"list"}
		} else if deviceArgs[0] == "set" {
			deviceArgs = append([]string{"device"}, deviceArgs[1:]...)
		} else if deviceArgs[0] == "default" {
			deviceArgs = []string{"device", "auto"}
		} else if deviceArgs[0] != "list" {
			deviceArgs = append([]string{"device"}, deviceArgs...)
		}
		return runAudioCommand(context.Background(), deviceArgs, false)

	case "mono":
		return runAudioCommand(context.Background(), append([]string{"mono"}, parts[1:]...), false)

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

	case "remote":
		if len(parts) > 1 && strings.EqualFold(parts[1], "events") {
			return "", fmt.Errorf("event streams run in a terminal: chill remote events <topic...>")
		}
		return runRemoteCommand(context.Background(), parts[1:])

	case "eq":
		out, err = clientEqualizer(arg)

	case "notifications":
		out, err = clientNotifications(arg)

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
		if findStation(cmd) != nil {
			out, err = clientPlay(cmd)
			break
		}
		if len(parts) == 1 {
			value := parts[0]
			if _, statErr := os.Stat(value); statErr == nil || strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
				items, loadErr := loadMediaInputs(context.Background(), []string{value})
				if loadErr != nil {
					return "", loadErr
				}
				return playMediaItems(items)
			}
		}
		return "", fmt.Errorf("unknown command %s, try help", cmd)
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
func replHelp() string { return renderCLITranscript(replHelpLines()) }

func replHelpLines() []transcriptLine {
	lines := make([]transcriptLine, 0, len(replCommands)+1)
	names := helpNames()
	for index, command := range replCommands {
		lines = append(lines, transcriptLine{
			{transcriptBright, fmt.Sprintf("%-*s", helpWidth(), names[index])},
			{transcriptLiteral, "  "}, {transcriptDim, command.desc},
		})
	}
	return append(lines, transcriptLine{
		{transcriptBright, fmt.Sprintf("%-*s", helpWidth(), "<station>")},
		{transcriptLiteral, "  "}, {transcriptDim, "same as play <station>"},
	})
}

// stationList lists the stations, marking the one that is loaded.
func stationList() string { return renderCLITranscript(stationListLines()) }

func stationListLines() []transcriptLine {
	current := ""
	if status, _ := fetchStatus(); status != nil {
		current = status.Station
	}
	var lines []transcriptLine
	for _, station := range stationSnapshot() {
		marker := transcriptSpan{transcriptLiteral, " "}
		if station.Name == current {
			marker = transcriptSpan{transcriptPrompt, "♪"}
		}
		lines = append(lines, transcriptLine{
			marker, {transcriptLiteral, " "}, {transcriptBright, fmt.Sprintf("%-16s", station.Name)},
			{transcriptLiteral, "  "}, {transcriptDim, station.Desc},
		})
	}
	return lines
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

	dir, err := chillConfigDir()
	if err != nil {
		return h
	}
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
