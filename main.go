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
	reset   = "\033[0m"
	dim     = "\033[2m"
	purple  = "\033[38;5;183m"
	pink    = "\033[38;5;218m"
	blue    = "\033[38;5;117m"
	cyan    = "\033[38;5;159m"
)

var logo = `
` + purple + `        ╭──────────────────╮` + reset + `
` + pink + `        │ ` + reset + `  ░▒▓ ` + cyan + `chill` + reset + ` ▓▒░  ` + pink + `│` + reset + `
` + purple + `        ╰──────────────────╯` + reset + `
`

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

type Station struct {
	Name string
	URL  string
	Desc string
}

var stations = []Station{
	{"lofi-girl", "https://www.youtube.com/watch?v=jfKfPfyJRdk", "Lofi Girl - beats to relax/study to"},
	{"chillhop", "https://www.youtube.com/watch?v=5yx6BWlEVcY", "Chillhop Radio - jazzy & lofi hip hop"},
	{"lofi-girl-sleep", "https://www.youtube.com/watch?v=rUxyKA_-grg", "Lofi Girl - sleepy beats"},
	{"jazz-hop", "https://www.youtube.com/watch?v=9UMxZofMNbA", "Jazz Hop Cafe - smooth jazz beats"},
	{"the-bootleg-boy", "https://www.youtube.com/watch?v=p2ljmqDV8eY", "The Bootleg Boy - chill lofi"},
	{"college-music", "https://www.youtube.com/watch?v=5NG_WQkVvDs", "College Music - lofi hip hop"},
}

func main() {
	list := flag.Bool("list", false, "list available stations")
	station := flag.String("station", "lofi-girl", "station to play")
	flag.Parse()

	if *list {
		printStations()
		return
	}

	if flag.NArg() > 0 {
		*station = flag.Arg(0)
	}

	s := findStation(*station)
	if s == nil {
		fmt.Fprintf(os.Stderr, "unknown station: %s\n", *station)
		fmt.Fprintf(os.Stderr, "use --list to see available stations\n")
		os.Exit(1)
	}

	play(s)
}

func printStations() {
	fmt.Print(logo)
	fmt.Println(dim + "  available stations:" + reset)
	fmt.Println()
	for _, s := range stations {
		fmt.Printf("    %s%-16s%s  %s%s%s\n", cyan, s.Name, reset, dim, s.Desc, reset)
	}
	fmt.Println()
}

func findStation(name string) *Station {
	name = strings.ToLower(name)
	for _, s := range stations {
		if strings.ToLower(s.Name) == name {
			return &s
		}
	}
	return nil
}

func play(s *Station) {
	rand.Seed(time.Now().UnixNano())
	vibe := vibes[rand.Intn(len(vibes))]

	fmt.Print("\033[2J\033[H") // clear screen
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
