package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/willibrandon/chill/internal/podcast"
)

// podcastHelp uses the same column formatting as the main CLI help.
func podcastHelp(colored bool) string {
	var b strings.Builder
	usage := "Usage: chill podcasts [command] [--json]"
	if colored {
		usage = dim + usage + reset
	}
	fmt.Fprintln(&b, usage)
	printStyledHelpSection(&b, "Commands", [][2]string{
		{"(no command)", "open the podcast browser"},
		{"<feed-url>", "open a feed in the browser"},
		{"top [--country us]", "Apple's top 100 shows"},
		{"search <words>", "search for shows"},
		{"categories", "list the 19 genres"},
		{"category <name|id>", "browse a genre"},
		{"episodes <feed-url>", "list playable episodes"},
		{"subscribe <feed-url>", "save a show locally"},
		{"unsubscribe <feed-url>", "remove a subscription"},
		{"subscriptions", "list saved shows"},
		{"inbox [--played|--all]", "episodes across subscriptions"},
		{"inbox queue", "queue the newest unplayed episode from each show"},
		{"sync", "refresh the subscription inbox and automatic downloads"},
		{"download <feed-url> [number]", "save an episode for offline playback"},
		{"downloads", "show offline download state"},
		{"downloads remove|retry|pin <number>", "manage an offline episode"},
		{"auto [on|off]", "show or control automatic downloads"},
		{"country [code]", "show or save the chart country"},
		{"play <feed-url> [number]", "play an episode (default: newest)"},
		{"latest <feed-url>", "play the newest episode"},
		{"queue <feed-url> [number]", "add an episode to the queue"},
		{"queue", "list queued episodes"},
		{"clear", "clear the queue"},
	}, colored)
	printStyledHelpSection(&b, "Options", [][2]string{
		{"--json", "print machine-readable results"},
		{"--restart", "start an episode from the beginning"},
		{"--fg", "play without the background daemon"},
		{"--all", "include played episodes in the inbox"},
		{"--played", "show only played inbox episodes"},
		{"--latest <count>", "automatic downloads per subscribed show (1-20)"},
		{"--concurrency <count>", "simultaneous downloads (1-8)"},
		{"--quota <size>", "managed storage limit, such as 10GB"},
		{"--retain <days>", "days to keep played downloads (0 keeps them)"},
		{"--country <code>", "use this country for top shows"},
		{"--help", "show this help"},
	}, colored)
	footer := `
Playback: chill seek -30 | chill seek +30 | chill speed 1.5
          chill next | chill prev | chill --toggle | chill --stop

Browser: Enter open/play · f subscribe · / search or RSS URL
         Shift+Left/Right ±30s · Space pause · Esc back · F3 prompt`
	if colored {
		lines := strings.Split(footer, "\n")
		for i, line := range lines {
			if line != "" {
				lines[i] = dim + line + reset
			}
		}
		footer = strings.Join(lines, "\n")
	}
	b.WriteString(footer)
	return b.String()
}

func fetchPodcastLibrary() (*podcastLibrary, error) {
	if !isDaemonRunning() {
		return loadPodcastLibrary()
	}
	out, err := ask("podcasts")
	if err != nil {
		return nil, err
	}
	var library podcastLibrary
	if err := json.Unmarshal([]byte(out), &library); err != nil {
		return nil, err
	}
	return &library, nil
}

func podcastMutation(action string, value any) (string, error) {
	if err := ensureDaemon(); err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return ask(action + " " + string(data))
}

func clientEpisode(e podcast.Episode, restart, queue bool) (string, error) {
	if err := checkPodcastRequirements(); err != nil {
		return "", err
	}
	if queue {
		return podcastMutation("podcast-queue", e)
	}
	return podcastMutation("episode", episodeRequest{Episode: e, Restart: restart})
}

func clientSeek(arg string) (string, error) {
	if _, err := seekDelta(arg); err != nil {
		return "", err
	}
	if !isDaemonRunning() {
		return "", fmt.Errorf("no finite media playing")
	}
	return ask("seek " + arg)
}

func clientPodcastControl(action, arg string) (string, error) {
	if action == "speed" {
		if err := ensureDaemon(); err != nil {
			return "", err
		}
	}
	if !isDaemonRunning() {
		return "", fmt.Errorf("nothing playing")
	}
	return ask(strings.TrimSpace(action + " " + arg))
}

func newestEpisode(episodes []podcast.Episode) int {
	if len(episodes) == 0 {
		return -1
	}
	index := 0
	for i, e := range episodes {
		if e.Published.After(episodes[index].Published) {
			index = i
		}
	}
	return index
}

func parseByteSize(value string) (int64, error) {
	raw := strings.ToUpper(strings.TrimSpace(value))
	multiplier := int64(1)
	for _, unit := range []struct {
		suffix string
		mult   int64
	}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}} {
		if strings.HasSuffix(raw, unit.suffix) {
			raw = strings.TrimSpace(strings.TrimSuffix(raw, unit.suffix))
			multiplier = unit.mult
			break
		}
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || n <= 0 || n > float64(1<<50)/float64(multiplier) {
		return 0, fmt.Errorf("quota needs a size such as 10GB")
	}
	return int64(n * float64(multiplier)), nil
}

func runPodcastCommand(ctx context.Context, args []string) (string, error) {
	return runPodcast(ctx, args, false)
}

// runPodcast keeps help presentation separate from command and JSON results.
func runPodcast(ctx context.Context, args []string, coloredHelp bool) (string, error) {
	jsonOutput, restart, foreground, inboxAll, inboxPlayed, country := false, false, false, false, false, ""
	latest, concurrency, retain := -1, -1, -1
	quota := int64(-1)
	var words []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			return podcastHelp(coloredHelp), nil
		case "--json":
			jsonOutput = true
		case "--restart":
			restart = true
		case "--fg":
			foreground = true
		case "--all":
			inboxAll = true
		case "--played":
			inboxPlayed = true
		case "--latest", "--concurrency", "--quota", "--retain":
			option := args[i]
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s needs a value", option)
			}
			if option == "--quota" {
				var parseErr error
				quota, parseErr = parseByteSize(args[i])
				if parseErr != nil {
					return "", parseErr
				}
				continue
			}
			value, parseErr := strconv.Atoi(args[i])
			if parseErr != nil {
				return "", fmt.Errorf("%s needs a number", option)
			}
			switch option {
			case "--latest":
				latest = value
			case "--concurrency":
				concurrency = value
			case "--retain":
				retain = value
			}
		case "--country":
			i++
			if i >= len(args) {
				return "", fmt.Errorf("--country needs a two-letter code")
			}
			var err error
			country, err = podcast.Country(args[i])
			if err != nil {
				return "", err
			}
		default:
			if strings.HasPrefix(args[i], "--") {
				return "", fmt.Errorf("unknown option %s", args[i])
			}
			words = append(words, args[i])
		}
	}
	if len(words) == 0 {
		return podcastHelp(coloredHelp), nil
	}
	client := podcast.NewClient()
	action, rest := strings.ToLower(words[0]), words[1:]
	if podcast.ValidURL(words[0]) {
		action, rest = "episodes", words
	}
	var result any
	var lines []string
	formatShows := func(shows []podcast.Show) {
		if shows == nil {
			shows = []podcast.Show{}
		}
		result = shows
		for i, s := range shows {
			lines = append(lines, fmt.Sprintf("%3d  %s — %s\n     %s", i+1, s.Title, s.Author, s.FeedURL))
		}
	}
	var err error
	switch action {
	case "help":
		return podcastHelp(coloredHelp), nil
	case "top":
		if len(rest) != 0 {
			return "", fmt.Errorf("usage: podcasts top [--country us]")
		}
		if country == "" {
			l, e := fetchPodcastLibrary()
			if e != nil {
				return "", e
			}
			country = l.Country
		}
		var shows []podcast.Show
		shows, err = client.Top(ctx, country)
		formatShows(shows)
	case "search":
		if len(rest) == 0 {
			return "", fmt.Errorf("usage: podcasts search <words>")
		}
		var shows []podcast.Show
		shows, err = client.Search(ctx, strings.Join(rest, " "), "")
		formatShows(shows)
	case "categories":
		if len(rest) != 0 {
			return "", fmt.Errorf("usage: podcasts categories")
		}
		result = podcast.Categories
		for _, c := range podcast.Categories {
			lines = append(lines, c.ID+"  "+c.Name)
		}
	case "category":
		name := strings.Join(rest, " ")
		i := slices.IndexFunc(podcast.Categories, func(c podcast.Category) bool { return c.ID == name || strings.EqualFold(c.Name, name) })
		if i < 0 {
			return "", fmt.Errorf("unknown category; use podcasts categories")
		}
		c := podcast.Categories[i]
		var shows []podcast.Show
		shows, err = client.Search(ctx, c.Name, c.ID)
		formatShows(shows)
	case "subscriptions":
		if len(rest) != 0 {
			return "", fmt.Errorf("usage: podcasts subscriptions")
		}
		var l *podcastLibrary
		l, err = fetchPodcastLibrary()
		if err == nil {
			formatShows(l.Subscriptions)
		}
	case "inbox":
		if len(rest) > 1 || len(rest) == 1 && strings.ToLower(rest[0]) != "queue" {
			return "", fmt.Errorf("usage: podcasts inbox [queue] [--played|--all]")
		}
		l, e := fetchPodcastLibrary()
		if e != nil {
			return "", e
		}
		episodes := make([]podcast.Episode, 0, len(l.Inbox))
		for _, episode := range l.Inbox {
			played := l.Progress[episode.Key()].Played
			if inboxAll || inboxPlayed && played || !inboxPlayed && !played {
				episodes = append(episodes, episode)
			}
		}
		if len(rest) == 1 {
			newest := make([]podcast.Episode, 0, len(l.Subscriptions))
			seen := map[string]bool{}
			for _, episode := range episodes {
				identity := episode.FeedURL
				if identity == "" {
					identity = episode.Show
				}
				if !seen[identity] {
					seen[identity] = true
					newest = append(newest, episode)
				}
			}
			if len(newest) == 0 {
				return "inbox has no matching episodes", nil
			}
			items := make([]MediaItem, len(newest))
			for i, episode := range newest {
				items[i] = itemFromEpisode(episode)
			}
			out, queueErr := sendItems("queue-append", items)
			return out, queueErr
		}
		result = episodes
		for i, episode := range episodes {
			state := ""
			if download, ok := l.Downloads[episode.Key()]; ok {
				state = " [" + download.State + "]"
			}
			lines = append(lines, fmt.Sprintf("%3d  %s  %s — %s%s", i+1, episode.Published.Format("2006-01-02"), episode.Show, episode.Title, state))
		}
	case "sync":
		if len(rest) != 0 {
			return "", fmt.Errorf("usage: podcasts sync")
		}
		if err = ensureDaemon(); err == nil {
			var out string
			out, err = ask("podcast-sync")
			result, lines = out, []string{out}
		}
	case "downloads":
		l, e := fetchPodcastLibrary()
		if e != nil {
			return "", e
		}
		downloads := make([]episodeDownload, 0, len(l.Downloads))
		for _, download := range l.Downloads {
			downloads = append(downloads, download)
		}
		slices.SortFunc(downloads, func(a, b episodeDownload) int { return b.Updated.Compare(a.Updated) })
		if len(rest) > 0 {
			if len(rest) != 2 {
				return "", fmt.Errorf("usage: podcasts downloads [remove|retry|pin <number>]")
			}
			index, e := strconv.Atoi(rest[1])
			if e != nil || index < 1 || index > len(downloads) {
				return "", fmt.Errorf("download number is out of range")
			}
			action := map[string]string{"remove": "podcast-download-remove", "retry": "podcast-download-retry", "pin": "podcast-download-pin"}[strings.ToLower(rest[0])]
			if action == "" {
				return "", fmt.Errorf("download action must be remove, retry, or pin")
			}
			if err = ensureDaemon(); err == nil {
				var out string
				out, err = ask(action + " " + downloads[index-1].Episode.Key())
				result, lines = out, []string{out}
			}
			break
		}
		result = downloads
		for i, download := range downloads {
			size := fmt.Sprintf("%d", download.Bytes)
			if download.Total > 0 {
				size = fmt.Sprintf("%d/%d", download.Bytes, download.Total)
			}
			pin := ""
			if download.Pinned {
				pin = " pinned"
			}
			lines = append(lines, fmt.Sprintf("%3d  %-11s %12s  %s — %s%s", i+1, download.State, size, download.Episode.Show, download.Episode.Title, pin))
		}
	case "auto":
		l, e := fetchPodcastLibrary()
		if e != nil {
			return "", e
		}
		settings := l.DownloadSettings
		changed := latest >= 0 || concurrency >= 0 || quota >= 0 || retain >= 0
		if latest >= 0 {
			settings.Latest = latest
		}
		if concurrency >= 0 {
			settings.Concurrency = concurrency
		}
		if quota >= 0 {
			settings.MaxBytes = quota
		}
		if retain >= 0 {
			settings.RetainPlayedDays = retain
		}
		if len(rest) == 0 && !changed {
			result = settings
			lines = []string{fmt.Sprintf("automatic downloads: %t · latest %d · concurrency %d · quota %d bytes · retain played %d days", settings.Auto, settings.Latest, settings.Concurrency, settings.MaxBytes, settings.RetainPlayedDays)}
		} else if len(rest) <= 1 && (len(rest) == 0 || rest[0] == "on" || rest[0] == "off") {
			if len(rest) == 1 {
				settings.Auto = rest[0] == "on"
			}
			data, _ := json.Marshal(settings)
			if err = ensureDaemon(); err == nil {
				var out string
				out, err = ask("podcast-download-settings " + string(data))
				result, lines = out, []string{out}
			}
		} else {
			return "", fmt.Errorf("usage: podcasts auto [on|off] [--latest N] [--concurrency N] [--quota 10GB] [--retain days]")
		}
	case "country":
		if len(rest) == 0 {
			l, e := fetchPodcastLibrary()
			if e != nil {
				return "", e
			}
			result = l.Country
			lines = []string{l.Country}
		} else if len(rest) == 1 {
			var code string
			code, err = podcast.Country(rest[0])
			if err == nil {
				err = ensureDaemon()
			}
			if err == nil {
				_, err = ask("podcast-country " + code)
			}
			result = code
			lines = []string{"chart country: " + code}
		} else {
			return "", fmt.Errorf("usage: podcasts country [code]")
		}
	case "queue", "clear":
		if len(rest) == 0 {
			if action == "clear" {
				out, e := clientPodcastControl("queue-clear", "")
				if e != nil {
					return "", e
				}
				result, lines = out, []string{out}
			} else {
				episodes := []podcast.Episode{}
				if isDaemonRunning() {
					out, e := ask("queue")
					if e != nil {
						return "", e
					}
					if e = json.Unmarshal([]byte(out), &episodes); e != nil {
						return "", e
					}
				}
				if episodes == nil {
					episodes = []podcast.Episode{}
				}
				result = episodes
				for i, e := range episodes {
					lines = append(lines, fmt.Sprintf("%d  %s — %s", i+1, e.Show, e.Title))
				}
			}
			break
		}
		if action == "clear" {
			return "", fmt.Errorf("usage: podcasts clear")
		}
		fallthrough
	case "episodes", "subscribe", "unsubscribe", "play", "latest", "download":
		if len(rest) == 0 || len(rest) > 2 || len(rest) == 2 && action != "play" && action != "queue" && action != "download" {
			return "", fmt.Errorf("usage: podcasts %s <feed-url>", action)
		}
		if !podcast.ValidURL(rest[0]) {
			return "", fmt.Errorf("enter an HTTP(S) feed URL")
		}
		if action == "unsubscribe" {
			var out string
			out, err = podcastMutation("podcast-unsubscribe", podcast.Show{FeedURL: rest[0]})
			result, lines = out, []string{out}
			break
		}
		feed, e := client.Feed(ctx, rest[0])
		if e != nil {
			return "", e
		}
		switch action {
		case "episodes":
			result = feed
			for i, e := range feed.Episodes {
				date := ""
				if !e.Published.IsZero() {
					date = e.Published.Format("2006-01-02")
				}
				lines = append(lines, fmt.Sprintf("%3d  %s  %s  %s", i+1, date, clock(e.Duration), e.Title))
			}
		case "subscribe":
			var out string
			out, err = podcastMutation("podcast-subscribe", feed.Show)
			result, lines = out, []string{"subscribed: " + feed.Show.Title}
		default:
			index := newestEpisode(feed.Episodes)
			if index < 0 {
				return "", fmt.Errorf("podcast feed has no playable episodes")
			}
			if len(rest) == 2 {
				n, e := strconv.Atoi(rest[1])
				if e != nil || n < 1 || n > len(feed.Episodes) {
					return "", fmt.Errorf("episode number must be between 1 and %d", len(feed.Episodes))
				}
				index = n - 1
			}
			var out string
			if action == "download" {
				out, err = podcastMutation("podcast-download", feed.Episodes[index])
			} else if foreground {
				err = runForegroundMediaItems([]MediaItem{itemFromEpisode(feed.Episodes[index])})
				out = "foreground playback ended"
			} else {
				out, err = clientEpisode(feed.Episodes[index], restart, action == "queue")
			}
			result, lines = out, []string{out}
		}
	default:
		return "", fmt.Errorf("unknown podcast command %q; use podcasts --help", action)
	}
	if err != nil {
		return "", err
	}
	if jsonOutput {
		b, e := json.MarshalIndent(result, "", "  ")
		return string(b), e
	}
	if len(lines) == 0 {
		return "no results", nil
	}
	return strings.Join(lines, "\n"), nil
}
