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
func podcastHelp() string {
	var b strings.Builder
	fmt.Fprintln(&b, "Usage: chill podcasts [command] [--json]")
	printHelpSection(&b, "Commands", [][2]string{
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
		{"country [code]", "show or save the chart country"},
		{"play <feed-url> [number]", "play an episode (default: newest)"},
		{"latest <feed-url>", "play the newest episode"},
		{"queue <feed-url> [number]", "add an episode to the queue"},
		{"queue", "list queued episodes"},
		{"clear", "clear the queue"},
	})
	printHelpSection(&b, "Options", [][2]string{
		{"--json", "print machine-readable results"},
		{"--restart", "start an episode from the beginning"},
		{"--country <code>", "use this country for top shows"},
		{"--help", "show this help"},
	})
	fmt.Fprint(&b, `
Playback: chill seek -30 | chill seek +30 | chill speed 1.5
          chill next | chill prev | chill --toggle | chill --stop

Browser: Enter open/play · f subscribe · / search or RSS URL
         Shift+Left/Right ±30s · Space pause · Esc back · F3 prompt`)
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
		return "", fmt.Errorf("no podcast playing")
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
	index := 0
	for i, e := range episodes {
		if e.Published.After(episodes[index].Published) {
			index = i
		}
	}
	return index
}

func runPodcastCommand(ctx context.Context, args []string) (string, error) {
	jsonOutput, restart, country := false, false, ""
	var words []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			return podcastHelp(), nil
		case "--json":
			jsonOutput = true
		case "--restart":
			restart = true
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
		return podcastHelp(), nil
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
		return podcastHelp(), nil
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
	case "episodes", "subscribe", "unsubscribe", "play", "latest":
		if len(rest) == 0 || len(rest) > 2 || len(rest) == 2 && action != "play" && action != "queue" {
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
			if len(rest) == 2 {
				n, e := strconv.Atoi(rest[1])
				if e != nil || n < 1 || n > len(feed.Episodes) {
					return "", fmt.Errorf("episode number must be between 1 and %d", len(feed.Episodes))
				}
				index = n - 1
			}
			var out string
			out, err = clientEpisode(feed.Episodes[index], restart, action == "queue")
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
