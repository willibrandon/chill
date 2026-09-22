package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/willibrandon/chill/internal/radio"
)

func radioHelp(colored bool) string {
	palette := currentCLIPalette()
	var b strings.Builder
	usage := "Usage: chill radio [command] [options]"
	if colored {
		usage = palette.dim + usage + palette.reset
	}
	fmt.Fprintln(&b, usage)
	printStyledHelpSectionWithPalette(&b, "Commands", [][2]string{
		{"(no command)", "open the radio browser"},
		{"top", "most-voted stations"},
		{"popular", "most-listened stations"},
		{"trending", "stations gaining listeners"},
		{"random", "a fresh random selection"},
		{"search <words>", "search station names"},
		{"country <code>", "browse a country"},
		{"tag <name>", "browse a genre or tag"},
		{"countries", "list countries"},
		{"tags", "list genres and tags"},
		{"favorites", "list favorite stations"},
		{"favorite <url> [name]", "save a station"},
		{"unfavorite <url>", "remove a favorite"},
		{"play <url> [name]", "play a stream directly"},
		{"nearby <code|auto|none|ask>", "configure local country suggestions"},
	}, colored, palette)
	printStyledHelpSectionWithPalette(&b, "Options", [][2]string{
		{"--sort <order>", "top, popular, trending, name, or random"},
		{"--state <name>", "restrict a country to one region"},
		{"--language <name>", "restrict results by language"},
		{"--limit <1-200>", "number of results (default 50)"},
		{"--offset <number>", "skip results for paging"},
		{"--play <number>", "play one result"},
		{"--favorite <number>", "toggle one result as a favorite"},
		{"--fg", "play the selected result in the foreground"},
		{"--json", "print machine-readable results"},
		{"--help", "show this help"},
	}, colored, palette)
	return strings.TrimRight(b.String(), "\n")
}

func stationFromCatalog(s radio.Station) Station {
	desc := s.Name
	var details []string
	if s.Country != "" {
		details = append(details, s.Country)
	}
	if s.Codec != "" {
		codec := s.Codec
		if s.Bitrate > 0 {
			codec += fmt.Sprintf(" %dk", s.Bitrate)
		}
		details = append(details, codec)
	}
	if len(details) > 0 {
		desc += " · " + strings.Join(details, " · ")
	}
	return Station{
		Name: s.Name, URL: s.URL, Desc: desc, CatalogID: s.ID, Country: s.Country,
		CountryCode: s.CountryCode, Region: s.State, Tags: s.Tags, Codec: s.Codec,
		Bitrate: s.Bitrate, Homepage: s.Homepage, Artwork: s.Favicon,
	}
}

func catalogFromStation(s Station) radio.Station {
	return radio.Station{
		ID: s.CatalogID, Name: s.Name, URL: s.URL, Country: s.Country,
		CountryCode: s.CountryCode, State: s.Region, Tags: s.Tags, Codec: s.Codec,
		Bitrate: s.Bitrate, Homepage: s.Homepage, Favicon: s.Artwork,
	}
}

func clientPlayRadio(station radio.Station) (string, error) {
	if err := checkMediaRequirements([]MediaItem{itemFromStation(stationFromCatalog(station))}); err != nil {
		return "", err
	}
	if err := ensureDaemon(); err != nil {
		return "", err
	}
	data, err := json.Marshal(stationFromCatalog(station))
	if err != nil {
		return "", err
	}
	out, err := ask("radio-play " + string(data))
	if err == nil {
		recordRadioClick(station)
	}
	return out, err
}

const radioClickTimeout = 5 * time.Second

func recordRadioClick(station radio.Station) {
	if station.ID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), radioClickTimeout)
		defer cancel()
		_ = radio.NewClient().RecordClick(ctx, station.ID)
	}()
}

func parseRadioOptions(args []string) (words []string, query radio.Query, jsonOutput bool, play, favorite int, foreground bool, err error) {
	play, favorite = -1, -1
	for i := 0; i < len(args); i++ {
		value := func(name string) (string, bool) {
			i++
			if i >= len(args) {
				err = fmt.Errorf("%s needs a value", name)
				return "", false
			}
			return args[i], true
		}
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--fg":
			foreground = true
		case "--sort":
			if v, ok := value("--sort"); ok {
				query.Sort = v
			}
		case "--state":
			if v, ok := value("--state"); ok {
				query.State = v
			}
		case "--language":
			if v, ok := value("--language"); ok {
				query.Language = v
			}
		case "--limit", "--offset", "--play", "--favorite":
			name := args[i]
			v, ok := value(name)
			if !ok {
				break
			}
			n, parseErr := strconv.Atoi(v)
			if parseErr != nil {
				err = fmt.Errorf("%s needs a number", name)
				break
			}
			switch name {
			case "--limit":
				query.Limit = n
			case "--offset":
				query.Offset = n
			case "--play":
				play = n - 1
			case "--favorite":
				favorite = n - 1
			}
		case "--help", "-h":
			words = append(words, "help")
		default:
			if strings.HasPrefix(args[i], "--") {
				err = fmt.Errorf("unknown option %s", args[i])
			} else {
				words = append(words, args[i])
			}
		}
		if err != nil {
			return
		}
	}
	return
}

func formatRadioStations(stations []radio.Station) []string {
	lines := make([]string, 0, len(stations))
	for i, station := range stations {
		var meta []string
		if station.Bitrate > 0 {
			meta = append(meta, fmt.Sprintf("%dk", station.Bitrate))
		}
		if station.Codec != "" {
			meta = append(meta, station.Codec)
		}
		if station.Country != "" {
			meta = append(meta, station.Country)
		}
		line := fmt.Sprintf("%3d  %s", i+1, station.Name)
		if len(meta) > 0 {
			line += " [" + strings.Join(meta, " · ") + "]"
		}
		lines = append(lines, line+"\n     "+station.URL)
	}
	return lines
}

func radioJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}

func runRadioCommand(ctx context.Context, args []string, coloredHelp bool) (string, error) {
	words, query, jsonOutput, playIndex, favoriteIndex, foreground, err := parseRadioOptions(args)
	if err != nil {
		return "", err
	}
	if len(words) == 0 {
		if jsonOutput {
			return "", fmt.Errorf("a radio command is required with --json")
		}
		return radioHelp(coloredHelp), nil
	}
	action, rest := strings.ToLower(words[0]), words[1:]
	if action == "help" {
		return radioHelp(coloredHelp), nil
	}
	client, store := radio.NewClient(), radio.DefaultStore()

	if action == "nearby" {
		if len(rest) > 1 {
			return "", fmt.Errorf("usage: chill radio nearby [code|auto|none|ask]")
		}
		library, loadErr := store.Load()
		if loadErr != nil {
			return "", loadErr
		}
		if len(rest) == 0 {
			if jsonOutput {
				return radioJSON(map[string]string{"country": library.Country})
			}
			value := library.Country
			if value == "" {
				value = "not decided"
			}
			return "nearby country: " + value, nil
		}
		code := strings.ToUpper(rest[0])
		if code == "AUTO" {
			code = radio.DetectLocalCountry()
			if code == "" {
				return "", fmt.Errorf("could not infer a country from the system timezone or locale")
			}
		} else if code == "ASK" {
			code = ""
		}
		library, err = store.SetCountry(code)
		if err != nil {
			return "", err
		}
		if jsonOutput {
			return radioJSON(map[string]string{"country": library.Country})
		}
		value := library.Country
		if value == "" {
			value = "ask next time"
		}
		return "nearby country: " + value, nil
	}

	if action == "play" || action == "favorite" || action == "unfavorite" {
		if len(rest) == 0 {
			return "", fmt.Errorf("usage: chill radio %s <url> [name]", action)
		}
		u, parseErr := url.Parse(rest[0])
		if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", fmt.Errorf("station URL must be HTTP(S)")
		}
		name := strings.TrimSpace(strings.Join(rest[1:], " "))
		if name == "" {
			name = u.Hostname()
		}
		station := radio.Station{Name: name, URL: rest[0], ID: rest[0]}
		switch action {
		case "play":
			if foreground {
				recordRadioClick(station)
				return "", runForeground(&Station{Name: name, URL: rest[0], Desc: name})
			}
			return clientPlayRadio(station)
		case "favorite":
			desired := true
			library, added, changed, e := updateRadioFavorite(store, station, &desired)
			if e != nil {
				return "", e
			}
			if jsonOutput {
				return radioJSON(library.Favorites)
			}
			if added && changed {
				return "favorite: " + name, nil
			}
			return "already a favorite: " + name, nil
		case "unfavorite":
			desired := false
			library, _, removed, e := updateRadioFavorite(store, station, &desired)
			if e != nil {
				return "", e
			}
			if jsonOutput {
				return radioJSON(library.Favorites)
			}
			if !removed {
				return "", fmt.Errorf("station is not a favorite")
			}
			return "removed favorite: " + name, nil
		}
	}

	var result any
	var lines []string
	var stations []radio.Station
	switch action {
	case "favorites":
		if len(rest) != 0 {
			return "", fmt.Errorf("usage: chill radio favorites")
		}
		library, e := loadRadioViewLibrary(store)
		if e != nil {
			return "", e
		}
		stations, result = library.Favorites, library.Favorites
		lines = formatRadioStations(stations)
	case "countries":
		if len(rest) != 0 {
			return "", fmt.Errorf("usage: chill radio countries")
		}
		places, e := client.Countries(ctx)
		if e != nil {
			return "", e
		}
		result = places
		for _, place := range places {
			lines = append(lines, fmt.Sprintf("%s  %-32s %d", place.Code, place.Name, place.StationCount))
		}
	case "tags":
		if len(rest) != 0 {
			return "", fmt.Errorf("usage: chill radio tags")
		}
		tags, e := client.Tags(ctx)
		if e != nil {
			return "", e
		}
		result = tags
		for _, tag := range tags {
			lines = append(lines, fmt.Sprintf("%-32s %d", tag.Name, tag.StationCount))
		}
	default:
		switch action {
		case "top":
			query.Sort = radio.SortVotes
		case "popular":
			query.Sort = radio.SortListens
		case "trending":
			query.Sort = radio.SortTrending
		case "random":
			query.Sort = radio.SortRandom
		case "search":
			if len(rest) == 0 {
				return "", fmt.Errorf("usage: chill radio search <words>")
			}
			query.Name = strings.Join(rest, " ")
		case "country":
			if len(rest) != 1 {
				return "", fmt.Errorf("usage: chill radio country <code> [--state name]")
			}
			query.CountryCode = rest[0]
		case "tag":
			if len(rest) == 0 {
				return "", fmt.Errorf("usage: chill radio tag <name>")
			}
			query.Tag = strings.Join(rest, " ")
		default:
			return "", fmt.Errorf("unknown radio command %q", action)
		}
		stations, err = client.Stations(ctx, query)
		if err != nil {
			return "", err
		}
		result, lines = stations, formatRadioStations(stations)
	}
	if playIndex >= 0 || favoriteIndex >= 0 {
		index := playIndex
		if index < 0 {
			index = favoriteIndex
		}
		if index < 0 || index >= len(stations) {
			return "", fmt.Errorf("station number is outside the result list")
		}
		station := stations[index]
		if playIndex >= 0 {
			if foreground {
				recordRadioClick(station)
				return "", runForeground(func() *Station { s := stationFromCatalog(station); return &s }())
			}
			return clientPlayRadio(station)
		}
		library, added, _, e := updateRadioFavorite(store, station, nil)
		if e != nil {
			return "", e
		}
		if jsonOutput {
			return radioJSON(library.Favorites)
		}
		if added {
			return "favorite: " + station.Name, nil
		}
		return "removed favorite: " + station.Name, nil
	}
	if jsonOutput {
		return radioJSON(result)
	}
	if len(lines) == 0 {
		return "no stations found", nil
	}
	return strings.Join(lines, "\n"), nil
}
