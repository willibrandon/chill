package main

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/radio"
	"github.com/willibrandon/chill/internal/tracklog"
)

type radioPage struct {
	kind, title, filter string
	query               radio.Query
	stations            []radio.Station
	places              []radio.Place
	tags                []radio.Tag
	history             []tracklog.Entry
	selected            int
}

type radioBrowser struct {
	open, loading, editing, searching, consent bool
	input                                      textinput.Model
	page                                       radioPage
	back                                       []radioPage
	library                                    radio.Library
	client                                     *radio.Client
	store                                      *radio.Store
	id                                         uint64
	cancel                                     context.CancelFunc
	note                                       string
	detectCountry                              func() string
}

type radioResultMsg struct {
	id      uint64
	page    *radioPage
	library *radio.Library
	history []tracklog.Entry
	note    string
	err     error
}

func (t *tui) openRadio() tea.Cmd {
	t.closeProviders()
	t.closeAudio()
	r := &t.radio
	r.open = true
	t.viz.focused, t.viz.fullscreen = false, false
	t.sel, t.flashing, t.dragging = selection{}, false, false
	if r.client == nil {
		r.client, r.store = radio.NewClient(), radio.DefaultStore()
		r.input = textinput.New()
		r.input.Prompt = "/ "
		r.input.SetVirtualCursor(false)
	}
	if r.page.kind == "" {
		r.page = radioPage{kind: "home", title: "Radio"}
	}
	return t.radioRequest(func(ctx context.Context) radioResultMsg {
		library, err := loadRadioViewLibrary(r.store)
		if err != nil {
			return radioResultMsg{err: err}
		}
		history, err := tracklog.DefaultStore().Load(200)
		return radioResultMsg{library: &library, history: history, err: err}
	})
}

func (t *tui) closeRadio() {
	r := &t.radio
	if r.cancel != nil {
		r.cancel()
	}
	r.id++
	r.open, r.loading, r.editing, r.consent = false, false, false, false
}

func (t *tui) radioRequest(work func(context.Context) radioResultMsg) tea.Cmd {
	r := &t.radio
	if r.cancel != nil {
		r.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	r.cancel = cancel
	r.id++
	r.loading, r.note = true, ""
	id := r.id
	return func() tea.Msg { defer cancel(); result := work(ctx); result.id = id; return result }
}

func (t *tui) radioResult(msg radioResultMsg) tea.Cmd {
	r := &t.radio
	if msg.id != r.id || !r.open {
		return nil
	}
	r.loading = false
	if msg.err != nil {
		if msg.library != nil {
			r.library = *msg.library
		}
		r.note = msg.err.Error()
		return nil
	}
	if msg.page != nil {
		r.page = *msg.page
	}
	if msg.library != nil {
		r.library = *msg.library
	}
	if msg.history != nil {
		r.page.history = msg.history
	}
	r.page.selected = min(r.page.selected, max(0, len(r.rows())-1))
	r.note = msg.note
	return refreshStatus
}

func (t *tui) radioNavigate(page radioPage) {
	r := &t.radio
	r.back = append(r.back, r.page)
	if len(r.back) > 10 {
		r.back = r.back[len(r.back)-10:]
	}
	r.page, r.editing, r.note = page, false, ""
}

type radioHomeRow struct {
	kind, title, value string
	pin                *radio.Pin
}

func (r *radioBrowser) homeRows() []radioHomeRow {
	rows := []radioHomeRow{
		{kind: "favorites", title: fmt.Sprintf("Favorites (%d)", len(r.library.Favorites))},
		{kind: "history", title: fmt.Sprintf("Recently Heard (%d)", len(r.page.history))},
	}
	switch r.library.Country {
	case "":
		rows = append(rows, radioHomeRow{kind: "consent", title: "Set up nearby stations"})
	case "NONE":
		rows = append(rows, radioHomeRow{kind: "consent", title: "Nearby stations: Off"})
	default:
		rows = append(rows, radioHomeRow{kind: "nearby", title: "Nearby stations: " + r.library.Country, value: r.library.Country})
	}
	for i := range r.library.Pins {
		pin := &r.library.Pins[i]
		rows = append(rows, radioHomeRow{kind: pin.Kind, title: "★ " + pin.Name, value: pin.CountryCode, pin: pin})
	}
	return append(rows,
		radioHomeRow{kind: "top", title: "Top Voted"},
		radioHomeRow{kind: "popular", title: "Most Listened"},
		radioHomeRow{kind: "trending", title: "Trending"},
		radioHomeRow{kind: "random", title: "Random"},
		radioHomeRow{kind: "countries", title: "Browse Countries"},
		radioHomeRow{kind: "tags", title: "Browse Genres & Tags"},
		radioHomeRow{kind: "search", title: "Search Stations"},
	)
}

func (r *radioBrowser) rowCount() int {
	switch r.page.kind {
	case "home":
		return len(r.homeRows())
	case "countries", "country":
		return len(r.page.places)
	case "tags":
		return len(r.page.tags)
	case "history":
		return len(r.page.history)
	default:
		return len(r.page.stations)
	}
}

func (r *radioBrowser) rowText(i int) string {
	switch r.page.kind {
	case "home":
		return r.homeRows()[i].title
	case "countries", "country":
		p := r.page.places[i]
		name := p.Name
		if r.page.kind == "countries" {
			name = p.Code + "  " + name
		}
		return fmt.Sprintf("%-42s %d", name, p.StationCount)
	case "tags":
		tag := r.page.tags[i]
		return fmt.Sprintf("%-42s %d", tag.Name, tag.StationCount)
	case "history":
		entry := r.page.history[i]
		title := entry.Title
		if entry.Artist != "" {
			title = entry.Artist + " — " + entry.Title
		}
		return fmt.Sprintf("%-12s  %s  %s", relativeTime(entry.PlayedAt), entry.Station, title)
	default:
		s := r.page.stations[i]
		star := "  "
		for _, favorite := range r.library.Favorites {
			if favorite.URL == s.URL {
				star = "★ "
				break
			}
		}
		var meta []string
		if s.Bitrate > 0 {
			meta = append(meta, fmt.Sprintf("%dk", s.Bitrate))
		}
		if s.Codec != "" {
			meta = append(meta, s.Codec)
		}
		if s.Country != "" {
			meta = append(meta, s.Country)
		}
		text := star + s.Name
		if len(meta) > 0 {
			text += " [" + strings.Join(meta, " · ") + "]"
		}
		return text
	}
}

func (r *radioBrowser) rows() []int {
	var rows []int
	filter := strings.ToLower(r.page.filter)
	for i := range r.rowCount() {
		if strings.Contains(strings.ToLower(r.rowText(i)), filter) {
			rows = append(rows, i)
		}
	}
	return rows
}

func radioSortName(sort string) string {
	switch sort {
	case radio.SortListens:
		return "popular"
	case radio.SortTrending:
		return "trending"
	case radio.SortName:
		return "name"
	case radio.SortRandom:
		return "random"
	default:
		return "top"
	}
}

func nextRadioSort(sort string) string {
	all := []string{radio.SortVotes, radio.SortListens, radio.SortTrending, radio.SortName, radio.SortRandom}
	for i, value := range all {
		if sort == value {
			return all[(i+1)%len(all)]
		}
	}
	return all[1]
}

func (t *tui) radioStations(title string, query radio.Query) tea.Cmd {
	client := t.radio.client
	query.Limit = 100
	t.radioNavigate(radioPage{kind: "stations", title: title, query: query})
	return t.radioRequest(func(ctx context.Context) radioResultMsg {
		stations, err := client.Stations(ctx, query)
		return radioResultMsg{page: &radioPage{kind: "stations", title: title, query: query, stations: stations}, err: err}
	})
}

func (t *tui) radioSearch(query string) tea.Cmd {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	return t.radioStations("Search: "+query, radio.Query{Name: query, Sort: radio.SortVotes})
}

func (t *tui) radioSelect(index int) tea.Cmd {
	r := &t.radio
	switch r.page.kind {
	case "home":
		row := r.homeRows()[index]
		switch row.kind {
		case "favorites":
			radioPage := radioPage{kind: "favorites", title: "Favorites", stations: append([]radio.Station(nil), r.library.Favorites...)}
			t.radioNavigate(radioPage)
		case "history":
			t.radioNavigate(radioPage{kind: "history", title: "Recently Heard", history: append([]tracklog.Entry(nil), r.page.history...)})
		case "consent":
			r.consent, r.note = true, "Use your system timezone or locale to suggest a country? No location service is contacted. (y/n)"
		case "nearby":
			return t.radioStations("Nearby: "+row.value, radio.Query{CountryCode: row.value, Sort: radio.SortVotes})
		case "top":
			return t.radioStations("Top Voted", radio.Query{Sort: radio.SortVotes})
		case "popular":
			return t.radioStations("Most Listened", radio.Query{Sort: radio.SortListens})
		case "trending":
			return t.radioStations("Trending", radio.Query{Sort: radio.SortTrending})
		case "random":
			return t.radioStations("Random", radio.Query{Sort: radio.SortRandom})
		case "countries":
			client := r.client
			t.radioNavigate(radioPage{kind: "countries", title: "Countries"})
			return t.radioRequest(func(ctx context.Context) radioResultMsg {
				places, err := client.Countries(ctx)
				return radioResultMsg{page: &radioPage{kind: "countries", title: "Countries", places: places}, err: err}
			})
		case "tags":
			client := r.client
			t.radioNavigate(radioPage{kind: "tags", title: "Genres & Tags"})
			return t.radioRequest(func(ctx context.Context) radioResultMsg {
				tags, err := client.Tags(ctx)
				return radioResultMsg{page: &radioPage{kind: "tags", title: "Genres & Tags", tags: tags}, err: err}
			})
		case "search":
			r.editing, r.searching = true, true
			r.input.SetValue("")
			return r.input.Focus()
		case "country":
			name := ""
			if row.pin != nil {
				name = row.pin.Name
			}
			return t.radioOpenCountry(row.value, name, 0)
		case "state":
			return t.radioStations(row.pin.Name, radio.Query{CountryCode: row.pin.CountryCode, State: row.pin.State})
		case "tag":
			return t.radioStations(row.pin.Name, radio.Query{Tag: row.pin.Name})
		}
	case "countries":
		place := r.page.places[index]
		return t.radioOpenCountry(place.Code, place.Name, place.StationCount)
	case "country":
		place := r.page.places[index]
		if index == 0 {
			return t.radioStations(place.Name, radio.Query{CountryCode: place.Code})
		}
		return t.radioStations(place.Name, radio.Query{CountryCode: place.Code, State: place.Name})
	case "tags":
		tag := r.page.tags[index]
		return t.radioStations(tag.Name, radio.Query{Tag: tag.Name})
	case "history":
		entry := r.page.history[index]
		if t.radioFG {
			t.radioChoice = &Station{Name: entry.Station, URL: entry.StationURL, Desc: entry.Station, Artwork: entry.Artwork}
			return tea.Quit
		}
		return t.radioRequest(func(context.Context) radioResultMsg {
			out, err := clientPlayRadio(radio.Station{Name: entry.Station, URL: entry.StationURL, ID: entry.StationURL, Favicon: entry.Artwork})
			return radioResultMsg{note: out, err: err}
		})
	default:
		station := r.page.stations[index]
		if t.radioFG {
			choice := stationFromCatalog(station)
			t.radioChoice = &choice
			return tea.Quit
		}
		return t.radioRequest(func(ctx context.Context) radioResultMsg {
			out, err := clientPlayRadio(station)
			return radioResultMsg{note: out, err: err}
		})
	}
	return nil
}

func (t *tui) radioOpenCountry(code, name string, stationCount int) tea.Cmd {
	client := t.radio.client
	title := name
	if title == "" {
		title = code
	}
	t.radioNavigate(radioPage{kind: "country", title: title, query: radio.Query{CountryCode: code}})
	return t.radioRequest(func(ctx context.Context) radioResultMsg {
		if name == "" {
			countries, err := client.Countries(ctx)
			if err != nil {
				return radioResultMsg{err: err}
			}
			for _, country := range countries {
				if country.Code == code {
					name, stationCount = country.Name, country.StationCount
					break
				}
			}
			if name == "" {
				return radioResultMsg{err: fmt.Errorf("country %s is not in the directory", code)}
			}
		}
		states, err := client.States(ctx, name, code)
		if err != nil {
			return radioResultMsg{err: err}
		}
		if stationCount == 0 {
			for _, state := range states {
				stationCount += state.StationCount
			}
		}
		// The first row opens all stations; following rows narrow to a region.
		places := append([]radio.Place{{Name: "All of " + name, Code: code, StationCount: stationCount}}, states...)
		return radioResultMsg{page: &radioPage{kind: "country", title: name, query: radio.Query{CountryCode: code}, places: places}}
	})
}

func (t *tui) radioRefresh() tea.Cmd {
	r := &t.radio
	page, client := r.page, r.client
	return t.radioRequest(func(ctx context.Context) radioResultMsg {
		var err error
		switch page.kind {
		case "home":
			library, loadErr := loadRadioViewLibrary(r.store)
			history, historyErr := tracklog.DefaultStore().Load(200)
			if loadErr != nil {
				err = loadErr
			} else {
				err = historyErr
			}
			return radioResultMsg{page: &page, library: &library, history: history, err: err}
		case "stations":
			page.stations, err = client.Stations(ctx, page.query)
		case "countries":
			page.places, err = client.Countries(ctx)
		case "tags":
			page.tags, err = client.Tags(ctx)
		case "favorites":
			library, e := loadRadioViewLibrary(r.store)
			err = e
			page.stations = library.Favorites
			return radioResultMsg{page: &page, library: &library, err: err}
		case "history":
			page.history, err = tracklog.DefaultStore().Load(200)
		}
		page.selected = 0
		return radioResultMsg{page: &page, err: err}
	})
}

func stationSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func (t *tui) radioKey(msg tea.KeyPressMsg) tea.Cmd {
	r := &t.radio
	key := msg.String()
	if key == "ctrl+q" {
		return tea.Quit
	}
	if key == "f5" {
		t.closeRadio()
		return nil
	}
	if r.consent {
		switch key {
		case "y":
			detect := r.detectCountry
			if detect == nil {
				detect = radio.DetectLocalCountry
			}
			r.consent = false
			return t.radioRequest(func(context.Context) radioResultMsg {
				code := detect()
				if code == "" {
					return radioResultMsg{note: "Could not infer a country; use radio nearby <code>"}
				}
				library, err := r.store.SetCountry(code)
				if err == nil {
					library, err = attachRadioFavorites(library)
				}
				return radioResultMsg{library: &library, note: "Nearby country: " + code, err: err}
			})
		case "n":
			r.consent = false
			return t.radioRequest(func(context.Context) radioResultMsg {
				library, err := r.store.SetCountry("NONE")
				if err == nil {
					library, err = attachRadioFavorites(library)
				}
				return radioResultMsg{library: &library, note: "Nearby suggestions disabled", err: err}
			})
		case "esc", "ctrl+c":
			r.consent, r.note = false, "Nearby setup cancelled"
		}
		return nil
	}
	if r.editing {
		switch key {
		case "esc", "ctrl+c":
			r.editing, r.page.filter = false, ""
			return nil
		case "enter":
			r.editing = false
			if r.searching {
				return t.radioSearch(r.input.Value())
			}
			return nil
		}
		var cmd tea.Cmd
		r.input, cmd = r.input.Update(msg)
		if !r.searching {
			r.page.filter, r.page.selected = r.input.Value(), 0
		}
		return cmd
	}
	rows := r.rows()
	switch key {
	case "esc", "b":
		if r.cancel != nil {
			r.cancel()
		}
		r.id++
		r.loading, r.note = false, ""
		if len(r.back) > 0 {
			r.page = r.back[len(r.back)-1]
			r.back = r.back[:len(r.back)-1]
		} else {
			t.closeRadio()
		}
	case "ctrl+c":
		if r.cancel != nil {
			r.cancel()
		}
		r.id++
		r.loading, r.note = false, "Cancelled"
	case "up", "k":
		r.page.selected = max(0, r.page.selected-1)
	case "down", "j":
		r.page.selected = min(max(0, len(rows)-1), r.page.selected+1)
	case "pgup":
		r.page.selected = max(0, r.page.selected-max(1, t.height-8))
	case "pgdown":
		r.page.selected = min(max(0, len(rows)-1), r.page.selected+max(1, t.height-8))
	case "home":
		r.page.selected = 0
	case "end":
		r.page.selected = max(0, len(rows)-1)
	case "/", "ctrl+f":
		r.editing, r.searching = true, key == "ctrl+f" || r.page.kind == "home"
		r.input.SetValue("")
		return r.input.Focus()
	case "ctrl+r":
		return t.radioRefresh()
	case "o":
		if r.page.kind == "stations" {
			r.page.query.Sort = nextRadioSort(r.page.query.Sort)
			r.page.title = strings.Split(r.page.title, " · ")[0] + " · " + radioSortName(r.page.query.Sort)
			return t.radioRefresh()
		}
	case "[", "]":
		if r.page.kind == "stations" {
			step := r.page.query.Limit
			if step == 0 {
				step = 100
			}
			if key == "[" {
				r.page.query.Offset = max(0, r.page.query.Offset-step)
			} else {
				r.page.query.Offset += step
			}
			return t.radioRefresh()
		}
	case "space":
		if t.radioFG {
			r.note = "Choose a station to start foreground playback"
			return nil
		}
		if t.status != nil && (t.status.Station != "" || t.status.Episode != nil) {
			return t.start("toggle")
		}
	case "l":
		t.closeRadio()
		return t.openLyrics()
	case "enter", "f", "a", "p", "q", "n":
		if r.loading || len(rows) == 0 {
			return nil
		}
		index := rows[min(r.page.selected, len(rows)-1)]
		if key == "enter" {
			return t.radioSelect(index)
		}
		if (key == "q" || key == "n") && !t.radioFG && (r.page.kind == "stations" || r.page.kind == "favorites" || r.page.kind == "history") {
			var station Station
			if r.page.kind == "history" {
				entry := r.page.history[index]
				station = Station{Name: entry.Station, URL: entry.StationURL, Desc: entry.Station, Artwork: entry.Artwork}
			} else {
				station = stationFromCatalog(r.page.stations[index])
			}
			action := "queue-append"
			if key == "n" {
				action = "queue-next"
			}
			return t.radioRequest(func(context.Context) radioResultMsg {
				out, err := sendItems(action, []MediaItem{itemFromStation(station)})
				return radioResultMsg{note: out, err: err}
			})
		}
		if key == "f" && (r.page.kind == "stations" || r.page.kind == "favorites" || r.page.kind == "history") {
			var station radio.Station
			if r.page.kind == "history" {
				entry := r.page.history[index]
				station = radio.Station{Name: entry.Station, URL: entry.StationURL, ID: entry.StationURL, Favicon: entry.Artwork}
			} else {
				station = r.page.stations[index]
			}
			return t.radioRequest(func(context.Context) radioResultMsg {
				library, added, _, err := updateRadioFavorite(r.store, station, nil)
				note := "Removed favorite: " + station.Name
				if added {
					note = "Favorite: " + station.Name
				}
				page := r.page
				if page.kind == "favorites" {
					page.stations = library.Favorites
				}
				return radioResultMsg{page: &page, library: &library, note: note, err: err}
			})
		}
		if key == "a" && (r.page.kind == "stations" || r.page.kind == "favorites") {
			station := r.page.stations[index]
			name := stationSlug(station.Name)
			if name == "" {
				name = "radio"
			}
			return t.radioRequest(func(context.Context) radioResultMsg {
				out, err := saveStation([]string{name, station.URL, station.Name})
				return radioResultMsg{note: ansi.Strip(out), err: err}
			})
		}
		if key == "p" {
			var pin radio.Pin
			switch r.page.kind {
			case "countries":
				place := r.page.places[index]
				pin = radio.Pin{Kind: "country", Name: place.Name, CountryCode: place.Code}
			case "country":
				place := r.page.places[index]
				if index == 0 {
					pin = radio.Pin{Kind: "country", Name: strings.TrimPrefix(place.Name, "All of "), CountryCode: place.Code}
				} else {
					pin = radio.Pin{Kind: "state", Name: place.Name, CountryCode: place.Code, State: place.Name}
				}
			case "tags":
				pin = radio.Pin{Kind: "tag", Name: r.page.tags[index].Name}
			default:
				return nil
			}
			return t.radioRequest(func(context.Context) radioResultMsg {
				library, added, err := r.store.TogglePin(pin)
				if err == nil {
					library, err = attachRadioFavorites(library)
				}
				note := "Unpinned: " + pin.Name
				if added {
					note = "Pinned: " + pin.Name
				}
				return radioResultMsg{library: &library, note: note, err: err}
			})
		}
	}
	return nil
}

func (t *tui) radioView() tea.View {
	r := &t.radio
	height, width := max(1, t.height), max(1, t.width)
	lines := make([]string, height)
	fit := func(s string) string { return ansi.Truncate(s, width, "") }
	heading := "Radio  /  " + r.page.title
	if r.page.kind == "stations" {
		heading += " · " + radioSortName(r.page.query.Sort)
	}
	lines[0] = fit(styleHeading.Render(heading))
	rows, room := r.rows(), max(0, height-6)
	first := max(0, min(r.page.selected-room/2, len(rows)-room))
	for row := 0; row < room && first+row < len(rows); row++ {
		index := first + row
		text := r.rowText(rows[index])
		style, prefix := styleInput, "   "
		if index == r.page.selected {
			style, prefix = styleSelected, " ❯ "
		}
		lines[row+2] = style.Render(fit(prefix + text))
	}
	if room > 0 && len(rows) == 0 && !r.loading {
		lines[2] = styleDim.Render("  No results. Ctrl+F searches stations.")
	}
	if height >= 5 {
		note := r.note
		if r.loading {
			note = "Loading… Ctrl+C cancels"
		} else if note == "" {
			note = fmt.Sprintf("%d items", len(rows))
			if r.page.filter != "" {
				note += " · filter: " + r.page.filter
			}
		}
		lines[height-4] = fit(styleDim.Render(note))
		lines[height-3] = fit(styleDim.Render("Enter open/play · q queue · n play next · f favorite · p pin · a save · / filter · Ctrl+F search · Ctrl+R refresh"))
		footer := "o sort · [ ] page · l lyrics · Space pause · Esc back · F5 prompt"
		if t.radioFG {
			footer = "Enter play in foreground · o sort · [ ] page · Esc back · F5 prompt"
		}
		lines[height-2] = fit(styleDim.Render(footer))
		if r.editing {
			r.input.SetWidth(max(1, width-3))
			lines[height-2] = fit(r.input.View())
		}
		lines[height-1] = t.statusBar()
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if r.editing && height >= 5 {
		if c := r.input.Cursor(); c != nil {
			c.Y += height - 2
			v.Cursor = c
		}
	}
	return v
}
