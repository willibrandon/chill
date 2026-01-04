package main

import (
	"fmt"
	"os"
	"strings"

	prompt "github.com/c-bata/go-prompt"
)

var suggestions = []prompt.Suggest{
	{Text: "play", Description: "play a station"},
	{Text: "skip", Description: "skip to random station"},
	{Text: "pause", Description: "pause playback"},
	{Text: "resume", Description: "resume playback"},
	{Text: "toggle", Description: "toggle play/pause"},
	{Text: "status", Description: "show current status"},
	{Text: "list", Description: "list all stations"},
	{Text: "stop", Description: "stop playback"},
	{Text: "quit", Description: "exit chill"},
}

func stationSuggestions() []prompt.Suggest {
	var s []prompt.Suggest
	for _, st := range stations {
		s = append(s, prompt.Suggest{Text: st.Name, Description: st.Desc})
	}
	return s
}

func completer(d prompt.Document) []prompt.Suggest {
	text := d.TextBeforeCursor()
	words := strings.Fields(text)

	// if typing first word, suggest commands
	if len(words) == 0 {
		return prompt.FilterHasPrefix(suggestions, "", true)
	}

	// if first word is complete and we have a space, suggest based on command
	if len(words) == 1 && !strings.HasSuffix(text, " ") {
		return prompt.FilterHasPrefix(suggestions, words[0], true)
	}

	// second argument completions
	if len(words) >= 1 {
		cmd := words[0]
		if cmd == "play" {
			prefix := ""
			if len(words) > 1 {
				prefix = words[1]
			}
			return prompt.FilterHasPrefix(stationSuggestions(), prefix, true)
		}
	}

	return nil
}

func executor(input string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return
	}

	parts := strings.Fields(input)
	cmd := parts[0]
	arg := ""
	if len(parts) > 1 {
		arg = parts[1]
	}

	switch cmd {
	case "play":
		if arg == "" {
			arg = "lofi-girl"
		}
		if err := ensureDaemon(); err != nil {
			fmt.Printf("%serror: %v%s\n", dim, err, reset)
			return
		}
		resp, err := sendCommand("play " + arg)
		if err != nil {
			fmt.Printf("%serror: %v%s\n", dim, err, reset)
			return
		}
		fmt.Printf("%s♪ %s%s\n", pink, resp, reset)

	case "skip":
		if err := ensureDaemon(); err != nil {
			fmt.Printf("%serror: %v%s\n", dim, err, reset)
			return
		}
		resp, err := sendCommand("skip")
		if err != nil {
			fmt.Printf("%serror: %v%s\n", dim, err, reset)
			return
		}
		fmt.Printf("%s♪ %s%s\n", pink, resp, reset)

	case "pause":
		if !isDaemonRunning() {
			fmt.Printf("%snot running%s\n", dim, reset)
			return
		}
		resp, err := sendCommand("pause")
		if err != nil {
			fmt.Printf("%serror: %v%s\n", dim, err, reset)
			return
		}
		fmt.Printf("%s⏸ %s%s\n", dim, resp, reset)

	case "resume":
		if !isDaemonRunning() {
			fmt.Printf("%snot running%s\n", dim, reset)
			return
		}
		resp, err := sendCommand("resume")
		if err != nil {
			fmt.Printf("%serror: %v%s\n", dim, err, reset)
			return
		}
		fmt.Printf("%s▶ %s%s\n", purple, resp, reset)

	case "toggle":
		clientToggle()

	case "status":
		clientStatus()

	case "list":
		for _, s := range stations {
			fmt.Printf("  %s%-16s%s  %s%s%s\n", cyan, s.Name, reset, dim, s.Desc, reset)
		}

	case "stop":
		clientStop()

	case "quit", "exit", "q":
		fmt.Printf("%s~ stay chill ~%s\n", dim, reset)
		os.Exit(0)

	default:
		// try as station name
		if findStation(cmd) != nil {
			if err := ensureDaemon(); err != nil {
				fmt.Printf("%serror: %v%s\n", dim, err, reset)
				return
			}
			resp, err := sendCommand("play " + cmd)
			if err != nil {
				fmt.Printf("%serror: %v%s\n", dim, err, reset)
				return
			}
			fmt.Printf("%s♪ %s%s\n", pink, resp, reset)
		} else {
			fmt.Printf("%sunknown: %s%s\n", dim, cmd, reset)
		}
	}
}

func runRepl() {
	fmt.Print(logo)
	fmt.Printf("%s  type 'list' for stations, 'quit' to exit%s\n\n", dim, reset)

	p := prompt.New(
		executor,
		completer,
		prompt.OptionPrefix("♪ "),
		prompt.OptionPrefixTextColor(prompt.Fuchsia),
		prompt.OptionSuggestionBGColor(prompt.DarkGray),
		prompt.OptionSuggestionTextColor(prompt.White),
		prompt.OptionSelectedSuggestionBGColor(prompt.Fuchsia),
		prompt.OptionSelectedSuggestionTextColor(prompt.White),
		prompt.OptionDescriptionBGColor(prompt.DarkGray),
		prompt.OptionDescriptionTextColor(prompt.LightGray),
		prompt.OptionSelectedDescriptionBGColor(prompt.Fuchsia),
		prompt.OptionSelectedDescriptionTextColor(prompt.White),
		prompt.OptionPreviewSuggestionTextColor(prompt.Fuchsia),
	)
	p.Run()
}
