// Package main implements chill, a terminal lofi radio that streams
// 24/7 lofi beats from YouTube. It uses a client-server architecture
// where a background daemon manages mpv playback and clients communicate
// over a Unix socket.
//
// Usage:
//
//	chill              # play default station
//	chill chillhop     # play specific station
//	chill -i           # interactive mode (repl)
//	chill --vol 60     # set volume (or +5, -10, up, down)
//	chill --mute       # toggle mute
//	chill --status     # show what's playing
//	chill --stop       # stop playback
//	chill add n url    # save your own station
//	chill update       # install the latest release
package main

import (
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
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

// Station represents a lofi radio stream with a name, YouTube URL, and description.
type Station struct {
	Name string `json:"name"` // short identifier (e.g., "lofi-girl")
	URL  string `json:"url"`  // YouTube video/stream URL
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
	toggle := flag.Bool("toggle", false, "toggle play/pause")
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

	flag.Usage = printCLIHelp
	flag.Parse()
	enableANSI()
	if *jsonOutput {
		if !*status || flag.NArg() != 0 || flag.NFlag() != 2 {
			fmt.Fprintln(os.Stderr, "usage: chill --status --json")
			os.Exit(1)
		}
		out, err := statusJSON()
		fmt.Println(out)
		if err != nil {
			os.Exit(1)
		}
		return
	}
	if flag.NArg() > 0 && flag.Arg(0) == "doctor" {
		if err := runDoctor(flag.Args()[1:], os.Stdout); err != nil {
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
	case *repl:
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
	case *fg:
		// foreground mode (original behavior)
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
		// default: play via daemon
		s := *station
		if s == "" && flag.NArg() > 0 {
			s = flag.Arg(0)
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
		{"chill", "play default station"},
		{"chill chillhop", "play specific station"},
		{"chill -i", "interactive mode (repl)"},
		{"chill --skip", "skip to random station"},
		{"chill --toggle", "pause/resume"},
		{"chill --vol 60", "set volume (or +5, -10, up, down)"},
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

// playForeground plays a station in foreground mode with mpv's interactive
// terminal interface, allowing volume control, seeking, and other mpv keybindings.
func playForeground(s *Station) {
	exitIfMissing(checkRequirements())

	vibe := vibes[randInt(len(vibes))]

	fmt.Print("\033[2J\033[H")
	fmt.Print(logo)
	fmt.Printf("  %s♪ %s%s\n", pink, s.Desc, reset)
	fmt.Printf("  %s~ %s ~%s\n\n", dim, vibe, reset)
	fmt.Printf("  %s[q]uit  [m]ute  [9/0] volume  [←/→] seek%s\n\n", dim, reset)

	cmd := exec.Command("mpv",
		"--no-video",
		"--term-osd-bar",
		"--term-osd-bar-chars=╺━━╸",
		"--term-status-msg=  ${playback-time} │ ${audio-codec-name} ${audio-params/samplerate}Hz │ ${audio-bitrate}",
		"--msg-level=all=no,statusline=status",
		fmt.Sprintf("--volume=%d", rememberedVolume()),
		s.URL,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sig
		fmt.Print("\n\n  " + dim + "~ stay chill ~" + reset + "\n\n")
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}()

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == -1 {
				return
			}
		}
		fmt.Fprintf(os.Stderr, "mpv error: %v\n", err)
		os.Exit(1)
	}
}
