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
//	chill --status     # show what's playing
//	chill --stop       # stop playback
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	reset  = "\033[0m"
	dim    = "\033[2m"
	purple = "\033[38;5;183m"
	pink   = "\033[38;5;218m"
	blue   = "\033[38;5;117m"
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
	Name string // short identifier (e.g., "lofi-girl")
	URL  string // YouTube video/stream URL
	Desc string // human-readable description
}

// stations contains the available 24/7 lofi radio streams.
var stations = []Station{
	{"lofi-girl", "https://www.youtube.com/watch?v=jfKfPfyJRdk", "Lofi Girl - beats to relax/study to"},
	{"chillhop", "https://www.youtube.com/watch?v=5yx6BWlEVcY", "Chillhop Radio - jazzy & lofi hip hop"},
	{"chillout", "https://www.youtube.com/watch?v=9UMxZofMNbA", "Chillout Lounge - calm & relaxing"},
	{"code-radio", "https://www.youtube.com/watch?v=ByZGu229-yA", "Code Radio - beats to study & code to"},
	{"sleep", "https://www.youtube.com/watch?v=rPjez8z61rI", "Lofi - beats to sleep/relax to"},
	{"study", "https://www.youtube.com/watch?v=7NOSDKb0HlU", "Lofi - beats to study/relax to"},
}

func init() {
	rand.Seed(time.Now().UnixNano())
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
	toggle := flag.Bool("toggle", false, "toggle play/pause")
	skip := flag.Bool("skip", false, "skip to random station")
	stop := flag.Bool("stop", false, "stop playback")
	fg := flag.Bool("fg", false, "run in foreground (no daemon)")

	// options
	station := flag.String("station", "", "station to play")

	flag.Parse()

	switch {
	case *daemon:
		runDaemon()
	case *repl:
		runRepl()
	case *list:
		printStations()
	case *status:
		clientStatus()
	case *toggle:
		clientToggle()
	case *skip:
		clientSkip()
	case *stop:
		clientStop()
	case *fg:
		// foreground mode (original behavior)
		s := *station
		if s == "" && flag.NArg() > 0 {
			s = flag.Arg(0)
		}
		if s == "" {
			s = "lofi-girl"
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
		if s == "" {
			s = "lofi-girl"
		}
		clientPlay(s)
	}
}

// printStations displays all available stations and usage information.
func printStations() {
	fmt.Print(logo)
	fmt.Println(dim + "  available stations:" + reset)
	fmt.Println()
	for _, s := range stations {
		fmt.Printf("    %s%-16s%s  %s%s%s\n", cyan, s.Name, reset, dim, s.Desc, reset)
	}
	fmt.Println()
	fmt.Println(dim + "  usage:" + reset)
	fmt.Println()
	fmt.Printf("    %schill%s              %splay default station%s\n", cyan, reset, dim, reset)
	fmt.Printf("    %schill chillhop%s     %splay specific station%s\n", cyan, reset, dim, reset)
	fmt.Printf("    %schill -i%s           %sinteractive mode (repl)%s\n", cyan, reset, dim, reset)
	fmt.Printf("    %schill --skip%s       %sskip to random station%s\n", cyan, reset, dim, reset)
	fmt.Printf("    %schill --toggle%s     %spause/resume%s\n", cyan, reset, dim, reset)
	fmt.Printf("    %schill --status%s     %sshow what's playing%s\n", cyan, reset, dim, reset)
	fmt.Printf("    %schill --stop%s       %sstop playback%s\n", cyan, reset, dim, reset)
	fmt.Printf("    %schill --fg%s         %srun in foreground%s\n", cyan, reset, dim, reset)
	fmt.Println()
}

// findStation returns the station with the given name (case-insensitive),
// or nil if no matching station is found.
func findStation(name string) *Station {
	name = strings.ToLower(name)
	for _, s := range stations {
		if strings.ToLower(s.Name) == name {
			return &s
		}
	}
	return nil
}

// playForeground plays a station in foreground mode with mpv's interactive
// terminal interface, allowing volume control, seeking, and other mpv keybindings.
func playForeground(s *Station) {
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
		"--volume=70",
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
