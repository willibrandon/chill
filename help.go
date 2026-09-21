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
	fmt.Fprint(out, `chill - terminal audio, radio, and podcast player

Usage:
  chill [options] [station]
  chill <command> [arguments]

With no arguments, chill opens the interactive REPL.
`)
	printHelpSection(out, "Commands", [][2]string{
		{"play [source...]", "resume or play stations, files, folders, playlists, and URLs"},
		{"open", "browse local audio, queues, playlists, and listening history"},
		{"queue [command]", "list, search, append, replace, reorder, remove, or undo"},
		{"playlist [command]", "manage and import or export saved playlists"},
		{"library [collection]", "list recent items, favorites, and bookmarks"},
		{"shuffle [on|off]", "control source-neutral queue shuffle"},
		{"repeat [off|all|one]", "control source-neutral queue repeat"},
		{"pause / resume / toggle / stop", "control current playback"},
		{"status [--json]", "show current playback"},
		{"volume [level]", "report or change volume (0-100)"},
		{"eq [preset|next|prev|list]", "show or select the persistent 10-band equalizer"},
		{"eq --band <band> <dB>", "edit one band (-12 to +12 dB)"},
		{"podcasts [command|feed-url]", "browse podcasts; --help lists all commands"},
		{"radio [command]", "discover and favorite stations; --help lists commands"},
		{"history [--limit N|clear]", "show or clear recently heard radio tracks"},
		{"lyrics [--json]", "show lyrics for the current track"},
		{"notifications [on|off]", "control track-change notifications"},
		{"seek <seconds>", "jump within finite media (-30, +30)"},
		{"speed [0.5-3]", "set finite-media playback speed"},
		{"next / prev", "navigate the universal queue"},
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
		{"chill ~/Music", "recursively queue a local music folder"},
		{"chill album.m3u --fg", "play a playlist without the daemon"},
		{"chill song.flac --eq Rock", "play a local track with an EQ preset"},
		{"chill queue next song.flac", "put a local track next"},
		{"chill playlist import mix.m3u8", "save an external playlist"},
		{"chill podcasts search history --json", "search for shows as JSON"},
		{"chill radio", "open the radio browser"},
		{"chill radio --fg", "browse, then play the selection in the foreground"},
		{"chill radio search jazz --json", "search stations as JSON"},
		{"chill history --limit 20", "show recently heard live tracks"},
		{"chill notifications on", "enable track-change notifications"},
		{"chill --vol +5", "raise the volume"},
		{"chill eq Bass-Boost", "select an equalizer preset"},
		{"chill eq --band 1k +3", "edit one band and select Custom"},
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
	printStyledHelpSection(out, title, rows, false)
}

// printStyledHelpSection measures plain labels before adding REPL colors.
func printStyledHelpSection(out io.Writer, title string, rows [][2]string, colored bool) {
	heading := title + ":"
	if colored {
		heading = cyan + heading + reset
	}
	fmt.Fprintf(out, "\n%s\n", heading)
	width := 0
	for _, row := range rows {
		width = max(width, len(row[0]))
	}
	for _, row := range rows {
		if colored {
			fmt.Fprintf(out, "  %s%-*s%s  %s%s%s\n", cyan, width, row[0], reset, dim, row[1], reset)
		} else {
			fmt.Fprintf(out, "  %-*s  %s\n", width, row[0], row[1])
		}
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
