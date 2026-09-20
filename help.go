package main

import (
	"flag"
	"fmt"
	"io"
)

// printCLIHelp supplements the registered flags with the positional commands
// that Go's default usage output cannot discover.
func printCLIHelp() {
	out := flag.CommandLine.Output()
	fmt.Fprint(out, `chill - terminal radio and podcast player

Usage:
  chill [options] [station]
  chill <command> [arguments]

With no arguments, chill opens the interactive REPL.
`)
	printHelpSection(out, "Commands", [][2]string{
		{"play [station]", "resume playback or play a station"},
		{"pause / resume / toggle / stop", "control current playback"},
		{"status [--json]", "show current playback"},
		{"volume [level]", "report or change volume (0-100)"},
		{"podcasts [command|feed-url]", "browse podcasts; --help lists all commands"},
		{"seek <seconds>", "jump within a podcast (-30, +30)"},
		{"speed [0.5-3]", "set podcast playback speed"},
		{"next / prev", "next queued episode / previous episode"},
		{"doctor [options]", "check dependencies, config, and daemon compatibility"},
		{"add <name> <url> [description]", "save a custom station or override a built-in"},
		{"remove <name>", "remove a custom station or restore a built-in"},
		{"default <name>", "choose the default station"},
		{"update", "install the latest release"},
		{"upgrade", "alias for update"},
	})
	printHelpSection(out, "Diagnostics", [][2]string{
		{"chill doctor", "check setup and client/daemon versions"},
		{"chill doctor --stations", "check all configured streams"},
		{"chill doctor --stream <name|url>", "check one stream"},
		{"chill doctor --logs", "show the latest daemon startup log"},
		{"chill doctor --help", "show all diagnostic options"},
	})
	fmt.Fprintln(out, "\nOptions (accept either - or --):")
	flag.PrintDefaults()
	fmt.Fprint(out, `  -h, --help
        show this help
`)
	printHelpSection(out, "Examples", [][2]string{
		{"chill", "open the interactive REPL"},
		{"chill chillhop", "play a station"},
		{"chill -i", "same as chill"},
		{"chill podcasts", "open the podcast browser"},
		{"chill podcasts search history --json", "search for shows as JSON"},
		{"chill --vol +5", "raise the volume"},
		{"chill --sleep 45m", "stop playback in 45 minutes"},
		{"chill --status --json", "show read-only, machine-readable status"},
	})
	fmt.Fprint(out, `
Package updates:
  brew upgrade willibrandon/tap/chill
  scoop update chill
`)
}

func printHelpSection(out io.Writer, title string, rows [][2]string) {
	fmt.Fprintf(out, "\n%s:\n", title)
	width := 0
	for _, row := range rows {
		width = max(width, len(row[0]))
	}
	for _, row := range rows {
		fmt.Fprintf(out, "  %-*s  %s\n", width, row[0], row[1])
	}
}

func printDoctorHelp(flags *flag.FlagSet) {
	fmt.Fprint(flags.Output(), `Usage:
  chill doctor [options]

Check dependencies, station configuration, and client/daemon compatibility.

Options:
`)
	flags.PrintDefaults()
	fmt.Fprint(flags.Output(), `  -h, --help
        show this help

Examples:
  chill doctor
  chill doctor --stations
  chill doctor --stream lofi-girl --timeout 30s
  chill doctor --logs
`)
}
