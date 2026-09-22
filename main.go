// Package main implements chill, a terminal audio, radio, and podcast player.
// It uses a client-server architecture
// where a background daemon manages native audio playback and clients communicate
// over a Unix socket.
//
// Usage:
//
//	chill              # open the interactive REPL
//	chill chillhop     # play specific station
//	chill -i           # interactive mode (repl)
//	chill --vol 60     # set volume (or +5, -10, up, down)
//	chill eq Rock      # select an equalizer preset
//	chill --mute       # toggle mute
//	chill --status     # show what's playing
//	chill --stop       # stop playback
//	chill add n url    # save your own station
//	chill update       # install the latest release
package main

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/willibrandon/chill/internal/notify"
	"github.com/willibrandon/chill/internal/podcast"
)

type cliPalette struct {
	reset, dim, purple, pink, cyan, logo string
}

var activeCLIPalette atomic.Pointer[cliPalette]

func setCLIColors(settings interfaceSettings, theme interfaceTheme) {
	palette := cliPalette{}
	mode := resolvedCLIColorMode(settings.ColorMode)
	if mode != "none" {
		ansiColor := func(value string) string {
			rgb, err := parseHexColor(value)
			if err != nil {
				return ""
			}
			r, g, b := int(math.Round(rgb[0]*255)), int(math.Round(rgb[1]*255)), int(math.Round(rgb[2]*255))
			if mode == "ansi256" {
				index := nearestANSIIndex(r, g, b, 256)
				return fmt.Sprintf("\033[38;5;%dm", index)
			}
			return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
		}
		palette.reset, palette.dim = "\033[0m", "\033[2m"
		if mode == "ansi16" {
			// Standalone output uses the terminal canvas, not the TUI theme background.
			palette.purple, palette.pink, palette.cyan = "\033[36m", "\033[94m", "\033[97m"
		} else {
			palette.purple, palette.pink, palette.cyan = ansiColor(theme.Secondary), ansiColor(theme.Accent), ansiColor(theme.Bright)
		}
	}
	if settings.ascii() {
		palette.logo = "\n" + palette.purple + "        +------------------+" + palette.reset + "\n" +
			palette.pink + "        | " + palette.reset + "  .:# " + palette.cyan + "chill" + palette.reset + " #: .  " + palette.pink + "|" + palette.reset + "\n" +
			palette.purple + "        +------------------+" + palette.reset + "\n"
	} else {
		palette.logo = "\n" + palette.purple + "        ╭──────────────────╮" + palette.reset + "\n" +
			palette.pink + "        │ " + palette.reset + "  ░▒▓ " + palette.cyan + "chill" + palette.reset + " ▓▒░  " + palette.pink + "│" + palette.reset + "\n" +
			palette.purple + "        ╰──────────────────╯" + palette.reset + "\n"
	}
	activeCLIPalette.Store(&palette)
}

func currentCLIPalette() cliPalette {
	if palette := activeCLIPalette.Load(); palette != nil {
		return *palette
	}
	return cliPalette{reset: "\033[0m", dim: "\033[2m", purple: "\033[38;5;183m", pink: "\033[38;5;218m", cyan: "\033[38;5;159m"}
}

// vibes contains random taglines displayed during playback.
var vibes = []string{
	"late night coding session",
	"3am thoughts",
	"rainy day indoors",
	"coffee & code",
	"midnight debugging",
	"sunday morning slow",
	"lost in the sauce",
	"just vibing",
	"in the zone",
	"flow state activated",
	"compiling thoughts",
	"segfault serenity",
	"null pointer nirvana",
	"stack overflow dreams",
	"git push & chill",
}

// Station represents a radio stream with a name, URL, and description.
type Station struct {
	Name string `json:"name"` // short identifier or directory display name
	URL  string `json:"url"`  // video or direct stream URL
	Desc string `json:"desc"` // human-readable description
	// CatalogID is the public directory's stable identifier.
	CatalogID string `json:"catalog_id,omitempty"`
	// Country is the station's display country.
	Country string `json:"country,omitempty"`
	// CountryCode is the station's ISO country code.
	CountryCode string `json:"country_code,omitempty"`
	// Region is the station's optional state or region.
	Region string `json:"region,omitempty"`
	// Tags contains directory genres and descriptors.
	Tags string `json:"tags,omitempty"`
	// Codec is the advertised stream codec.
	Codec string `json:"codec,omitempty"`
	// Bitrate is the advertised stream rate in kilobits per second.
	Bitrate int `json:"bitrate,omitempty"`
	// Homepage is the station's website.
	Homepage string `json:"homepage,omitempty"`
	// Artwork is the station's image URL.
	Artwork string `json:"artwork,omitempty"`
}

// builtinStations is the immutable starting point for every config reload.
var builtinStations = []Station{
	// These channels run multiple broadcasts: /live can select the wrong mix.
	// Keep the intended stream IDs and audit with chill doctor --stations.
	{Name: "lofi-girl", URL: "https://www.youtube.com/watch?v=rFZHOHl-L8A", Desc: "Lofi Girl - beats to relax/study to"},
	{Name: "chillhop", URL: "https://www.youtube.com/watch?v=5yx6BWlEVcY", Desc: "Chillhop Radio - jazzy & lofi hip hop"},
	{Name: "chillout", URL: "https://www.youtube.com/watch?v=9UMxZofMNbA", Desc: "Chillout Lounge - calm & relaxing"},
	{Name: "code-radio", URL: "https://www.youtube.com/watch?v=ByZGu229-yA", Desc: "Code Radio - beats to study & code to"},
	{Name: "sleep", URL: "https://www.youtube.com/watch?v=rPjez8z61rI", Desc: "Lofi - beats to sleep/relax to"},
	{Name: "study", URL: "https://www.youtube.com/watch?v=7NOSDKb0HlU", Desc: "Lofi - beats to study/relax to"},
}

// configErr is why the stations file did not load, reported on the way out.
var configErr error

func init() {
	configErr = loadUserStations()
	setCLIColors(defaultInterfaceSettings(), builtinInterfaceThemes[0])
}

func randInt(n int) int {
	return rand.Intn(n)
}

func main() {
	if handled, err := notify.RunHelper(os.Args); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if runDeepLinkHandlerIfNeeded() {
		return
	}
	// Capture the executable before a later client installation can replace its path.
	_ = buildIdentity()
	promoteTrailingPlaybackFlags()
	interfaceSettings, interfaceErr := loadInterfaceSettings()
	if interfaceErr != nil {
		interfaceSettings = defaultInterfaceSettings()
	}
	setInterfaceSessionOverrides(interfaceOverridesFromArgs(os.Args[1:]))
	_, _, _ = activateInterfaceSettings(interfaceSettings)
	// commands
	daemon := flag.Bool("daemon", false, "run as daemon")
	repl := flag.Bool("i", false, "interactive mode (repl)")
	list := flag.Bool("list", false, "list stations")
	status := flag.Bool("status", false, "show current status")
	jsonOutput := flag.Bool("json", false, "machine-readable output for supported commands")
	toggle := flag.Bool("toggle", false, "pause/resume, or play the default station when stopped")
	skip := flag.Bool("skip", false, "skip to random station")
	stop := flag.Bool("stop", false, "stop playback")
	sleep := flag.String("sleep", "", "stop playback after a duration (45m, 1h, or off)")
	fg := flag.Bool("fg", false, "run in foreground (no daemon)")
	version := flag.Bool("version", false, "show version")
	mute := flag.Bool("mute", false, "toggle mute")
	update := flag.Bool("update", false, "install the latest release")
	upgrade := flag.Bool("upgrade", false, "alias for -update")

	// options
	station := flag.String("station", "", "station to play")
	vol := flag.String("vol", "", "set volume (0-100, +5, -10, up, down)")
	seek := flag.String("seek", "", "jump within finite media (-30, +30, 2m)")
	speed := flag.String("speed", "", "set finite-media playback speed (0.5-3)")
	eqPreset := flag.String("eq", "", "set the 10-band EQ preset")
	shuffle := flag.Bool("shuffle", false, "shuffle queued media")
	repeat := flag.String("repeat", "", "repeat mode: off, all, or one")
	device := flag.String("device", "", "audio output device id or name")
	audioProfile := flag.String("audio-profile", "", "audio profile: Automatic, Lossless, Low Latency, Stable Streaming, or Custom")
	sampleRate := flag.Int("sample-rate", 0, "audio output sample rate")
	bufferMS := flag.Int("buffer", 0, "audio output buffer in milliseconds")
	resampleQuality := flag.Int("resample-quality", 0, "audio resample quality (1-4)")
	mono := flag.Bool("mono", false, "downmix audio to mono")
	uiTheme := flag.String("theme", "", "terminal theme name")
	noColor := flag.Bool("no-color", false, "disable terminal colors")
	simplified := flag.Bool("simplified", false, "use the simplified accessible interface")
	lowPower := flag.Bool("low-power", false, "reduce redraws and background work")

	flag.Usage = printCLIHelp
	flag.Parse()
	if interfaceErr != nil {
		fmt.Fprintln(os.Stderr, "interface:", interfaceErr)
	}
	_, noColorEnvironment := os.LookupEnv("NO_COLOR")
	setInterfaceSessionOverrides(interfaceSessionOverrides{
		Theme: *uiTheme, NoColor: *noColor || noColorEnvironment, Simplified: *simplified, LowPower: *lowPower,
	})
	if _, _, err := activateInterfaceSettings(interfaceSettings); err != nil && *uiTheme != "" {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	enableANSI()
	if *fg {
		if err := saveForegroundQueueModes(*shuffle, *repeat); err != nil {
			printResult("", err)
			return
		}
	} else if *shuffle || *repeat != "" {
		if err := ensureDaemon(); err != nil {
			printResult("", err)
			return
		}
		if *shuffle {
			if _, err := ask("shuffle on"); err != nil {
				printResult("", err)
				return
			}
		}
		if *repeat != "" {
			if _, err := ask("repeat " + *repeat); err != nil {
				printResult("", err)
				return
			}
		}
	}
	applyStartupEqualizer := func() bool {
		if *eqPreset == "" {
			return true
		}
		if _, err := clientEqualizer(*eqPreset); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return false
		}
		return true
	}
	applyStartupAudio := func() bool {
		var commands [][]string
		if *audioProfile != "" {
			commands = append(commands, []string{"profile", *audioProfile})
		}
		if *sampleRate != 0 {
			commands = append(commands, []string{"sample-rate", fmt.Sprint(*sampleRate)})
		}
		if *bufferMS != 0 {
			commands = append(commands, []string{"buffer", fmt.Sprint(*bufferMS)})
		}
		if *resampleQuality != 0 {
			commands = append(commands, []string{"resample-quality", fmt.Sprint(*resampleQuality)})
		}
		if *device != "" {
			commands = append(commands, []string{"device", *device})
		}
		if *mono {
			commands = append(commands, []string{"mono", "on"})
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, command := range commands {
			if _, err := runAudioCommand(ctx, command, false); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return false
			}
		}
		return true
	}
	if !applyStartupAudio() {
		return
	}
	if *jsonOutput && !*status && (flag.NArg() == 0 || flag.Arg(0) != "status" && flag.Arg(0) != "podcasts" && flag.Arg(0) != "podcast" && flag.Arg(0) != "radio" && flag.Arg(0) != "history" && flag.Arg(0) != "lyrics" && flag.Arg(0) != "queue" && flag.Arg(0) != "playlist" && flag.Arg(0) != "library" && flag.Arg(0) != "remote" && flag.Arg(0) != "audio" && flag.Arg(0) != "device" && flag.Arg(0) != "providers" && flag.Arg(0) != "search" && flag.Arg(0) != "browse" && flag.Arg(0) != "theme" && flag.Arg(0) != "keys" && flag.Arg(0) != "interface") {
		printResult("", fmt.Errorf("--json is supported by status, radio, podcast, history, lyrics, queue, playlist, library, remote, audio, device, provider, search, browse, theme, keys, and interface commands"))
		return
	}
	if flag.NArg() > 0 {
		args := flag.Args()
		switch args[0] {
		case "pause", "resume", "toggle", "stop", "mute":
			if len(args) != 1 {
				printResult("", fmt.Errorf("usage: chill %s", args[0]))
				return
			}
			printResult(execute(args[0]))
			return
		case "play":
			if !applyStartupEqualizer() {
				return
			}
			if len(args) == 1 {
				printResult(clientResume())
			} else if len(args) >= 2 {
				if len(args) == 2 && findStation(args[1]) != nil {
					printResult(clientPlay(args[1]))
					return
				}
				ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
				defer cancel()
				items, err := loadMediaInputs(ctx, args[1:])
				if err != nil {
					printResult("", err)
					return
				}
				if *fg {
					printResult("", runForegroundMediaItems(items))
				} else {
					printResult(playMediaItems(items))
				}
			} else {
				printResult("", fmt.Errorf("usage: chill play [station|file|folder|playlist|url]"))
			}
			return
		case "volume":
			if len(args) > 2 {
				printResult("", fmt.Errorf("usage: chill volume [level]"))
				return
			}
			printResult(clientVolume(strings.Join(args[1:], " ")))
			return
		case "eq":
			printResult(clientEqualizer(strings.Join(args[1:], " ")))
			return
		case "notifications":
			if len(args) > 2 {
				printResult("", fmt.Errorf("usage: chill notifications [on|off]"))
				return
			}
			printResult(clientNotifications(strings.Join(args[1:], " ")))
			return
		case "status":
			if len(args) == 2 && args[1] == "--json" || len(args) == 1 && *jsonOutput {
				printStatusJSON()
			} else if len(args) == 1 {
				printResult(clientStatus())
			} else {
				printResult("", fmt.Errorf("usage: chill status [--json]"))
			}
			return
		case "remote":
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			printResult(runRemoteCommand(ctx, args[1:]))
			return
		case "audio":
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			printResult(runAudioCommand(ctx, args[1:], *jsonOutput))
			return
		case "device":
			if helpRequested(args[1:]) {
				printResult(deviceCommandHelp, nil)
				return
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			deviceArgs := args[1:]
			if len(deviceArgs) == 0 {
				deviceArgs = []string{"list"}
			} else if deviceArgs[0] == "set" {
				deviceArgs = append([]string{"device"}, deviceArgs[1:]...)
			} else if deviceArgs[0] == "default" {
				deviceArgs = []string{"device", "auto"}
			} else if deviceArgs[0] != "list" {
				deviceArgs = append([]string{"device"}, deviceArgs...)
			}
			printResult(runAudioCommand(ctx, deviceArgs, *jsonOutput))
			return
		case "mono":
			printResult(runAudioCommand(context.Background(), append([]string{"mono"}, args[1:]...), false))
			return
		case "providers":
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			printResult(runProvidersCommand(ctx, args[1:], *jsonOutput))
			return
		case "search":
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			printResult(runSearchCommand(ctx, args[1:], *jsonOutput, *fg))
			return
		case "browse":
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			printResult(runBrowseCommand(ctx, args[1:], *jsonOutput, *fg))
			return
		case "setup":
			if helpRequested(args[1:]) {
				printResult("Usage: chill setup [provider]", nil)
				return
			}
			if len(args) > 2 || *jsonOutput {
				printResult("", errors.New("usage: chill setup [provider]"))
				return
			}
			provider := ""
			if len(args) == 2 {
				provider = args[1]
			}
			printResult(runProviderSetup(provider))
			return
		case "link":
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			printResult(runLinkCommand(ctx, args[1:], *fg))
			return
		case "completion":
			printResult(runCompletionCommand(args[1:]))
			return
		case "theme":
			printResult(runThemeCommand(args[1:], *jsonOutput))
			return
		case "keys":
			printResult(runKeysCommand(args[1:], *jsonOutput))
			return
		case "interface":
			printResult(runInterfaceCommand(args[1:], *jsonOutput))
			return
		case "queue":
			queueArgs := args[1:]
			if *jsonOutput {
				queueArgs = append(queueArgs, "--json")
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			printResult(runQueueCommand(ctx, queueArgs))
			return
		case "playlist":
			playlistArgs := args[1:]
			if *jsonOutput {
				playlistArgs = append(playlistArgs, "--json")
			}
			if *fg {
				playlistArgs = append(playlistArgs, "--fg")
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			printResult(runPlaylistCommand(ctx, playlistArgs))
			return
		case "library":
			libraryArgs := args[1:]
			if *jsonOutput {
				libraryArgs = append(libraryArgs, "--json")
			}
			printResult(runLibraryCommand(libraryArgs))
			return
		case "shuffle", "repeat", "favorite", "bookmark":
			if len(args) > 2 || (args[0] == "favorite" || args[0] == "bookmark") && len(args) != 1 {
				printResult("", fmt.Errorf("usage: chill %s [value]", args[0]))
				return
			}
			if err := ensureDaemon(); err != nil {
				printResult("", err)
				return
			}
			printResult(ask(strings.TrimSpace(strings.Join(args, " "))))
			return
		case "open":
			if len(args) == 1 {
				runReplLibrary()
				return
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			items, err := loadMediaInputs(ctx, args[1:])
			if err != nil {
				printResult("", err)
				return
			}
			if *fg {
				printResult("", runForegroundMediaItems(items))
				return
			}
			printResult(playMediaItems(items))
			return
		}
	}
	if flag.NArg() > 0 && (flag.Arg(0) == "podcasts" || flag.Arg(0) == "podcast") {
		if !applyStartupEqualizer() {
			return
		}
		args := flag.Args()[1:]
		if *jsonOutput {
			args = append(args, "--json")
		}
		if *fg {
			args = append(args, "--fg")
		}
		if len(args) == 0 {
			runRepl("")
			return
		}
		if len(args) == 1 && podcast.ValidURL(args[0]) {
			runRepl(args[0])
			return
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		printResult(runPodcastCommand(ctx, args))
		return
	}
	if flag.NArg() > 0 && flag.Arg(0) == "radio" {
		if !applyStartupEqualizer() {
			return
		}
		args := flag.Args()[1:]
		if (len(args) == 0 || len(args) == 1 && args[0] == "--fg") && !*jsonOutput {
			runReplRadio(*fg || len(args) == 1)
			return
		}
		if *jsonOutput {
			args = append(args, "--json")
		}
		if *fg {
			args = append(args, "--fg")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		printResult(runRadioCommand(ctx, args, false))
		return
	}
	if flag.NArg() > 0 && flag.Arg(0) == "history" {
		args := flag.Args()[1:]
		if *jsonOutput {
			args = append(args, "--json")
		}
		printResult(runHistoryCommand(args, false))
		return
	}
	if flag.NArg() > 0 && flag.Arg(0) == "lyrics" {
		args := flag.Args()[1:]
		if *jsonOutput {
			args = append(args, "--json")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		printResult(runLyricsCommand(ctx, args, false))
		return
	}
	if flag.NArg() > 0 && (flag.Arg(0) == "seek" || flag.Arg(0) == "speed" || flag.Arg(0) == "next" || flag.Arg(0) == "prev") {
		if *jsonOutput || flag.NArg() > 2 {
			printResult("", fmt.Errorf("usage: chill %s [value]", flag.Arg(0)))
			return
		}
		arg := strings.Join(flag.Args()[1:], " ")
		if flag.Arg(0) == "seek" {
			printResult(clientSeek(arg))
		} else {
			printResult(clientPodcastControl(flag.Arg(0), arg))
		}
		return
	}
	if flag.NArg() == 1 && isDeepLinkInput(flag.Arg(0)) {
		if !applyStartupEqualizer() {
			return
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		printResult(runDeepLink(ctx, flag.Arg(0), *fg))
		return
	}
	if flag.NArg() > 0 && isMediaInput(flag.Arg(0)) {
		if !applyStartupEqualizer() {
			return
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		items, err := loadMediaInputs(ctx, flag.Args())
		if err != nil {
			printResult("", err)
			return
		}
		if *fg {
			printResult("", runForegroundMediaItems(items))
			return
		}
		printResult(playMediaItems(items))
		return
	}
	if *jsonOutput {
		if !*status || flag.NArg() != 0 || flag.NFlag() != 2 {
			fmt.Fprintln(os.Stderr, "usage: chill --status --json")
			os.Exit(1)
		}
		printStatusJSON()
		return
	}
	if flag.NArg() > 0 && flag.Arg(0) == "doctor" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		if err := runDoctorContext(ctx, flag.Args()[1:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	cleanupUpdateBackups()
	if configErr != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", configErr)
	}
	switch {
	case *daemon:
		runDaemon()
	case *repl || flag.NArg() == 0 && (flag.NFlag() == 0 || onlyInterfaceFlags()):
		if !applyStartupEqualizer() {
			return
		}
		runRepl()
	case *version:
		fmt.Println("chill " + buildVersion())
	case *update || *upgrade || flag.NArg() > 0 && (flag.Arg(0) == "update" || flag.Arg(0) == "upgrade"):
		if (*update || *upgrade) && flag.NArg() != 0 || !*update && !*upgrade && flag.NArg() != 1 {
			printResult("", fmt.Errorf("usage: chill update (or chill -update)"))
		}
		printResult(updateChill())
	case flag.NArg() > 0 && flag.Arg(0) == "add":
		addStation(flag.Args()[1:])
	case flag.NArg() > 0 && flag.Arg(0) == "remove":
		printResult(removeStation(flag.Args()[1:]))
	case flag.NArg() > 0 && flag.Arg(0) == "default":
		printResult(saveDefaultStation(flag.Args()[1:]))
	case *list:
		printStations()
	case *status:
		printResult(clientStatus())
	case *toggle:
		printResult(clientToggle())
	case *skip:
		printResult(clientSkip())
	case *stop:
		printResult(clientStop())
	case *sleep != "":
		printResult(clientSleep(*sleep))
	case *vol != "":
		printResult(clientVolume(*vol))
	case *mute:
		printResult(clientMute())
	case *seek != "":
		printResult(clientSeek(*seek))
	case *speed != "":
		printResult(clientPodcastControl("speed", *speed))
	case *fg:
		// Foreground mode uses the same decoded PCM and equalizer as the daemon.
		if !applyStartupEqualizer() {
			return
		}
		s := *station
		if s == "" && flag.NArg() > 0 {
			s = flag.Arg(0)
		}
		if s == "" {
			s = defaultStation()
		}
		st := findStation(s)
		if st == nil {
			fmt.Fprintf(os.Stderr, "unknown station: %s\n", s)
			os.Exit(1)
		}
		playForeground(st)
	default:
		// Play the requested station via the daemon.
		if !applyStartupEqualizer() {
			return
		}
		s := *station
		if s == "" && flag.NArg() > 0 {
			s = flag.Arg(0)
		}
		if podcast.ValidURL(s) {
			runRepl(s)
			return
		}
		printResult(clientPlay(s))
	}
}

func interfaceOverridesFromArgs(args []string) interfaceSessionOverrides {
	_, noColorEnvironment := os.LookupEnv("NO_COLOR")
	overrides := interfaceSessionOverrides{NoColor: noColorEnvironment}
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--theme", "-theme":
			if index+1 < len(args) {
				overrides.Theme = args[index+1]
				index++
			}
		case "--no-color", "-no-color":
			overrides.NoColor = true
		case "--simplified", "-simplified":
			overrides.Simplified = true
		case "--low-power", "-low-power":
			overrides.LowPower = true
		default:
			if value, ok := strings.CutPrefix(args[index], "--theme="); ok {
				overrides.Theme = value
			} else if value, ok := strings.CutPrefix(args[index], "-theme="); ok {
				overrides.Theme = value
			}
		}
	}
	return overrides
}

func onlyInterfaceFlags() bool {
	only := true
	flag.Visit(func(value *flag.Flag) {
		if value.Name != "theme" && value.Name != "no-color" && value.Name != "simplified" && value.Name != "low-power" {
			only = false
		}
	})
	return only
}

// promoteTrailingPlaybackFlags keeps the conventional `chill file --fg`
// spelling working with Go's flag parser, which otherwise stops at file.
func promoteTrailingPlaybackFlags() {
	args := os.Args[1:]
	var promoted, rest []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-psn_") {
			continue
		}
		switch args[i] {
		case "--fg", "-fg", "--shuffle", "-shuffle", "--mono", "-mono", "--no-color", "-no-color", "--simplified", "-simplified", "--low-power", "-low-power":
			promoted = append(promoted, args[i])
		case "--repeat", "-repeat", "--eq", "-eq", "--device", "-device", "--audio-profile", "-audio-profile", "--sample-rate", "-sample-rate", "--buffer", "-buffer", "--resample-quality", "-resample-quality", "--theme", "-theme":
			if i+1 < len(args) {
				promoted = append(promoted, args[i], args[i+1])
				i++
			} else {
				rest = append(rest, args[i])
			}
		default:
			if strings.HasPrefix(args[i], "--repeat=") || strings.HasPrefix(args[i], "-repeat=") || strings.HasPrefix(args[i], "--eq=") || strings.HasPrefix(args[i], "-eq=") || strings.HasPrefix(args[i], "--device=") || strings.HasPrefix(args[i], "-device=") || strings.HasPrefix(args[i], "--audio-profile=") || strings.HasPrefix(args[i], "-audio-profile=") || strings.HasPrefix(args[i], "--sample-rate=") || strings.HasPrefix(args[i], "-sample-rate=") || strings.HasPrefix(args[i], "--buffer=") || strings.HasPrefix(args[i], "-buffer=") || strings.HasPrefix(args[i], "--resample-quality=") || strings.HasPrefix(args[i], "-resample-quality=") || strings.HasPrefix(args[i], "--theme=") || strings.HasPrefix(args[i], "-theme=") {
				promoted = append(promoted, args[i])
			} else {
				rest = append(rest, args[i])
			}
		}
	}
	os.Args = append([]string{os.Args[0]}, append(promoted, rest...)...)
}

// buildVersion returns the version this binary was built from, which the go
// command records from the module version or the git tag.
func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// printResult prints what a command has to say, or reports its error and exits.
func printResult(out string, err error) {
	exitIfMissing(err)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if currentInterfaceSettings().ascii() && !jsontext.Value(out).IsValid() {
		out = interfaceASCIIReplacer.Replace(out)
	}
	fmt.Println(out)
}

func printStatusJSON() {
	out, err := statusJSON()
	fmt.Println(out)
	if err != nil {
		os.Exit(1)
	}
}

// printStations displays all available stations and usage information.
func printStations() {
	palette := currentCLIPalette()
	fmt.Print(palette.logo)
	fmt.Println(palette.dim + "  available stations:" + palette.reset)
	fmt.Println()
	for _, s := range stationSnapshot() {
		fmt.Printf("    %s%-16s%s  %s%s%s\n", palette.cyan, s.Name, palette.reset, palette.dim, s.Desc, palette.reset)
	}
	fmt.Println()
	fmt.Println(palette.dim + "  usage:" + palette.reset)
	fmt.Println()
	usage := [][2]string{
		{"chill", "open the interactive REPL"},
		{"chill chillhop", "play specific station"},
		{"chill -i", "interactive mode (repl)"},
		{"chill --skip", "skip to random station"},
		{"chill --toggle", "pause/resume"},
		{"chill --vol 60", "set volume (or +5, -10, up, down)"},
		{"chill eq Rock", "select an equalizer preset"},
		{"chill --mute", "toggle mute"},
		{"chill --status", "show what's playing"},
		{"chill --status --json", "machine-readable status"},
		{"chill doctor", "diagnose setup (--stations checks streams)"},
		{"chill --stop", "stop playback"},
		{"chill add n url", "save a station"},
		{"chill remove n", "remove a custom station or override"},
		{"chill default n", "set the default station"},
		{"chill --sleep 45m", "stop after 45 minutes (off to cancel)"},
		{"chill --fg", "run in foreground"},
		{"chill update", "install the latest release"},
	}
	for _, row := range usage {
		fmt.Printf("    %s%-20s%s  %s%s%s\n", palette.cyan, row[0], palette.reset, palette.dim, row[1], palette.reset)
	}
	fmt.Println()
}

// exitIfMissing explains what to install and exits, if that is what err is about.
func exitIfMissing(err error) {
	var missing *requirementsError
	if errors.As(err, &missing) {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, missing.chill())
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}
}

// addStation handles the CLI's add command.
func addStation(args []string) {
	msg, err := saveStation(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	fmt.Println(msg)
}

// saveStation writes a station to the config file and has the daemon pick it
// up. It returns what to show for it.
func saveStation(args []string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("usage: chill add <name> <url> [description...]")
	}

	s := Station{
		Name: strings.ToLower(args[0]),
		URL:  args[1],
		Desc: strings.Join(args[2:], " "),
	}
	if strings.ContainsAny(s.Name, " \t\r\n") || s.Name == "" || strings.ContainsAny(s.URL, "\r\n") {
		return "", fmt.Errorf("station names must be one word and URLs must be one line")
	}
	if s.Desc == "" {
		s.Desc = s.Name
	}

	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}

	// a station with this name gets replaced, not duplicated
	found := false
	for i, old := range cfg.Stations {
		if strings.EqualFold(old.Name, s.Name) {
			cfg.Stations[i] = s
			found = true
			break
		}
	}
	if !found {
		cfg.Stations = append(cfg.Stations, s)
	}

	if err := writeJSON(configPath(), cfg); err != nil {
		return "", err
	}

	// the daemon keeps its own copy of the list, so tell it to reload
	if err := reloadConfig(); err != nil {
		return "", err
	}

	palette := currentCLIPalette()
	return fmt.Sprintf("%s+ %s%s  %s%s%s", palette.pink, s.Name, palette.reset, palette.dim, s.Desc, palette.reset), nil
}

// findStation returns the station with the given name (case-insensitive),
// or nil if no matching station is found.
func findStation(name string) *Station {
	name = strings.ToLower(name)
	for _, s := range stationSnapshot() {
		if strings.ToLower(s.Name) == name {
			return &s
		}
	}
	return nil
}

// playForeground plays a station with terminal controls and no daemon.
func playForeground(s *Station) {
	if err := runForeground(s); err != nil {
		exitIfMissing(err)
		fmt.Fprintf(os.Stderr, "foreground playback: %v\n", err)
		os.Exit(1)
	}
	palette := currentCLIPalette()
	fmt.Println(palette.dim + "~ stay chill ~" + palette.reset)
}
