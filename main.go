// Package main implements chill, a terminal radio and podcast player.
// It uses a client-server architecture
// where a background daemon manages mpv playback and clients communicate
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
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"

	"github.com/willibrandon/chill/internal/podcast"
)

const (
	reset  = "\033[0m"
	dim    = "\033[2m"
	purple = "\033[38;5;183m"
	pink   = "\033[38;5;218m"
	cyan   = "\033[38;5;159m"
)

var logo = `
` + purple + `        ╭──────────────────╮` + reset + `
` + pink + `        │ ` + reset + `  ░▒▓ ` + cyan + `chill` + reset + ` ▓▒░  ` + pink + `│` + reset + `
` + purple + `        ╰──────────────────╯` + reset + `
`

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
	Name string `json:"name"` // short identifier (e.g., "lofi-girl")
	URL  string `json:"url"`  // video or direct stream URL
	Desc string `json:"desc"` // human-readable description
}

// builtinStations is the immutable starting point for every config reload.
var builtinStations = []Station{
	// These channels run multiple broadcasts: /live can select the wrong mix.
	// Keep the intended stream IDs and audit with chill doctor --stations.
	{"lofi-girl", "https://www.youtube.com/watch?v=rFZHOHl-L8A", "Lofi Girl - beats to relax/study to"},
	{"chillhop", "https://www.youtube.com/watch?v=5yx6BWlEVcY", "Chillhop Radio - jazzy & lofi hip hop"},
	{"chillout", "https://www.youtube.com/watch?v=9UMxZofMNbA", "Chillout Lounge - calm & relaxing"},
	{"code-radio", "https://www.youtube.com/watch?v=ByZGu229-yA", "Code Radio - beats to study & code to"},
	{"sleep", "https://www.youtube.com/watch?v=rPjez8z61rI", "Lofi - beats to sleep/relax to"},
	{"study", "https://www.youtube.com/watch?v=7NOSDKb0HlU", "Lofi - beats to study/relax to"},
}

// configErr is why the stations file did not load, reported on the way out.
var configErr error

func init() {
	configErr = loadUserStations()
}

func randInt(n int) int {
	return rand.Intn(n)
}

func main() {
	// commands
	daemon := flag.Bool("daemon", false, "run as daemon")
	repl := flag.Bool("i", false, "interactive mode (repl)")
	list := flag.Bool("list", false, "list stations")
	status := flag.Bool("status", false, "show current status")
	jsonOutput := flag.Bool("json", false, "machine-readable status (with --status; read-only)")
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
	seek := flag.String("seek", "", "jump within a podcast (-30, +30, 2m)")
	speed := flag.String("speed", "", "set podcast playback speed (0.5-3)")
	eqPreset := flag.String("eq", "", "set the 10-band EQ preset")

	flag.Usage = printCLIHelp
	flag.Parse()
	enableANSI()
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
	if *jsonOutput && !*status && (flag.NArg() == 0 || flag.Arg(0) != "status" && flag.Arg(0) != "podcasts" && flag.Arg(0) != "podcast") {
		printResult("", fmt.Errorf("--json is supported by status and podcast commands"))
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
			} else if len(args) == 2 {
				printResult(clientPlay(args[1]))
			} else {
				printResult("", fmt.Errorf("usage: chill play [station]"))
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
		case "status":
			if len(args) == 2 && args[1] == "--json" || len(args) == 1 && *jsonOutput {
				printStatusJSON()
			} else if len(args) == 1 {
				printResult(clientStatus())
			} else {
				printResult("", fmt.Errorf("usage: chill status [--json]"))
			}
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
	case *repl || flag.NArg() == 0 && flag.NFlag() == 0:
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
	fmt.Print(logo)
	fmt.Println(dim + "  available stations:" + reset)
	fmt.Println()
	for _, s := range stationSnapshot() {
		fmt.Printf("    %s%-16s%s  %s%s%s\n", cyan, s.Name, reset, dim, s.Desc, reset)
	}
	fmt.Println()
	fmt.Println(dim + "  usage:" + reset)
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
		fmt.Printf("    %s%-20s%s  %s%s%s\n", cyan, row[0], reset, dim, row[1], reset)
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

	return fmt.Sprintf("%s+ %s%s  %s%s%s", pink, s.Name, reset, dim, s.Desc, reset), nil
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
	fmt.Println(dim + "~ stay chill ~" + reset)
}
