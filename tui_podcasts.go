package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/podcast"
)

type podcastPage struct {
	kind, title, query, filter, genre string
	shows                             []podcast.Show
	feed                              podcast.Feed
	selected                          int
	inboxFilter                       string
}

type podcastBrowser struct {
	open, loading, editing bool
	searching              bool
	input                  textinput.Model
	page                   podcastPage
	back                   []podcastPage
	library                *podcastLibrary
	client                 *podcast.Client
	id                     uint64
	cancel                 context.CancelFunc
	note                   string
}

type podcastResultMsg struct {
	id      uint64
	page    *podcastPage
	library *podcastLibrary
	note    string
	err     error
}

func (t *tui) openPodcasts(query string) tea.Cmd {
	t.closeProviders()
	t.closeAudio()
	p := &t.podcasts
	p.open = true
	t.viz.focused, t.viz.fullscreen = false, false
	t.sel, t.flashing, t.dragging = selection{}, false, false
	if p.client == nil {
		p.client = podcast.NewClient()
		p.input = textinput.New()
		p.input.Prompt = "/ "
		p.input.SetVirtualCursor(false)
	}
	if p.page.kind == "" {
		p.page = podcastPage{kind: "home", title: "Podcasts"}
	}
	if query != "" {
		return t.podcastSearch(query)
	}
	return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
		l, err := fetchPodcastLibrary()
		return podcastResultMsg{library: l, err: err}
	})
}

func (t *tui) closePodcasts() {
	p := &t.podcasts
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.id++
	p.open, p.loading, p.editing = false, false, false
}

func (t *tui) podcastRequest(work func(context.Context) podcastResultMsg) tea.Cmd {
	p := &t.podcasts
	if p.cancel != nil {
		p.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	p.cancel = cancel
	p.id++
	p.loading, p.note = true, ""
	id := p.id
	return func() tea.Msg { defer cancel(); result := work(ctx); result.id = id; return result }
}

func (t *tui) podcastResult(msg podcastResultMsg) tea.Cmd {
	p := &t.podcasts
	if msg.id != p.id || !p.open {
		return nil
	}
	p.loading = false
	if msg.err != nil {
		p.note = msg.err.Error()
		return nil
	}
	wasSyncing := p.library != nil && p.library.Syncing
	if msg.page != nil {
		p.page = *msg.page
	}
	if msg.library != nil {
		p.library = msg.library
		if p.page.kind == "subscriptions" {
			p.page.shows = p.library.Subscriptions
		}
	}
	p.page.selected = min(p.page.selected, max(0, len(p.rows())-1))
	p.note = msg.note
	if p.page.kind == "inbox" && p.library != nil && p.library.Syncing {
		p.note = "Syncing subscriptions…"
		return t.podcastLibraryPoll()
	}
	if p.page.kind == "inbox" && p.library != nil && p.library.SyncError != "" && p.note == "" {
		p.note = p.library.SyncError
	} else if p.page.kind == "inbox" && p.library != nil && wasSyncing && !p.library.Syncing && p.note == "" {
		p.note = "Inbox refreshed"
	}
	if p.page.kind == "downloads" && p.library != nil {
		for _, download := range p.library.Downloads {
			if download.State == "queued" || download.State == "downloading" || download.State == "retrying" {
				return t.podcastLibraryPoll()
			}
		}
	}
	return refreshStatus
}

func (t *tui) podcastLibraryPoll() tea.Cmd {
	id := t.podcasts.id
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		library, err := fetchPodcastLibrary()
		return podcastResultMsg{id: id, library: library, err: err}
	})
}

func (t *tui) podcastNavigate(page podcastPage) {
	p := &t.podcasts
	p.back = append(p.back, p.page)
	if len(p.back) > 8 {
		p.back = p.back[len(p.back)-8:]
	}
	p.page, p.editing, p.note = page, false, ""
}

func (t *tui) podcastSearch(query string) tea.Cmd {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	client := t.podcasts.client
	if podcast.ValidURL(query) {
		t.podcastNavigate(podcastPage{kind: "episodes", title: "Loading feed", query: query})
		return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
			feed, err := client.Feed(ctx, query)
			if err != nil {
				return podcastResultMsg{err: err}
			}
			library, err := fetchPodcastLibrary()
			return podcastResultMsg{page: &podcastPage{kind: "episodes", title: feed.Show.Title, feed: feed, query: query}, library: library, err: err}
		})
	}
	t.podcastNavigate(podcastPage{kind: "search", title: "Search: " + query, query: query})
	return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
		shows, err := client.Search(ctx, query, "")
		if err != nil {
			return podcastResultMsg{err: err}
		}
		library, err := fetchPodcastLibrary()
		return podcastResultMsg{page: &podcastPage{kind: "search", title: "Search: " + query, query: query, shows: shows}, library: library, err: err}
	})
}

func (p *podcastBrowser) rows() []int {
	n := len(p.page.shows)
	switch p.page.kind {
	case "home":
		n = 7
	case "categories":
		n = len(podcast.Categories)
	case "episodes":
		n = len(p.page.feed.Episodes)
	case "inbox":
		n = len(p.inboxEpisodes())
	case "downloads":
		if p.library != nil {
			n = len(p.library.Downloads)
		}
	case "download-settings":
		n = 5
	}
	var rows []int
	for i := range n {
		label := p.rowText(i)
		if strings.Contains(strings.ToLower(label), strings.ToLower(p.page.filter)) {
			rows = append(rows, i)
		}
	}
	return rows
}

func (p *podcastBrowser) country() string {
	if p.library != nil {
		return p.library.Country
	}
	return "us"
}
func (p *podcastBrowser) subscribed(feed string) bool {
	if p.library != nil {
		for _, s := range p.library.Subscriptions {
			if s.FeedURL == feed {
				return true
			}
		}
	}
	return false
}

func (p *podcastBrowser) rowText(i int) string {
	switch p.page.kind {
	case "home":
		return []string{"Top Shows (" + strings.ToUpper(p.country()) + ")", "Browse Categories", "Subscriptions", "Inbox", "Downloads", "Download Settings", "Search shows or open an RSS feed"}[i]
	case "categories":
		return podcast.Categories[i].Name
	case "episodes":
		e := p.page.feed.Episodes[i]
		mark := "  "
		if p.library != nil && p.library.Progress[e.Key()].Played {
			mark = "✓ "
		}
		date := "          "
		if !e.Published.IsZero() {
			date = e.Published.Format("2006-01-02")
		}
		duration := "     ?"
		if e.Duration > 0 {
			duration = clock(e.Duration)
		}
		return fmt.Sprintf("%s%s  %7s  %s", mark, date, duration, e.Title)
	case "inbox":
		episodes := p.inboxEpisodes()
		e := episodes[i]
		state := ""
		if download, ok := p.library.Downloads[e.Key()]; ok {
			state = " [" + download.State + "]"
		}
		return fmt.Sprintf("%s  %s — %s%s", e.Published.Format("2006-01-02"), e.Show, e.Title, state)
	case "downloads":
		downloads := p.downloadRows()
		d := downloads[i]
		pin := ""
		if d.Pinned {
			pin = " · pinned"
		}
		progress := ""
		if d.Total > 0 && d.State == "downloading" {
			progress = fmt.Sprintf(" %d%%", min(100, d.Bytes*100/d.Total))
		}
		return fmt.Sprintf("%-11s%s  %s — %s%s", d.State, progress, d.Episode.Show, d.Episode.Title, pin)
	case "download-settings":
		if p.library == nil {
			return ""
		}
		settings := p.library.DownloadSettings
		return []string{
			fmt.Sprintf("Automatic downloads  %t", settings.Auto),
			fmt.Sprintf("Newest per show     %d", settings.Latest),
			fmt.Sprintf("Concurrent          %d", settings.Concurrency),
			fmt.Sprintf("Disk quota          %.1f GiB", float64(settings.MaxBytes)/float64(1<<30)),
			fmt.Sprintf("Keep played         %d days", settings.RetainPlayedDays),
		}[i]
	default:
		s := p.page.shows[i]
		mark := "  "
		if p.subscribed(s.FeedURL) {
			mark = "♥ "
		}
		text := mark + s.Title
		if s.Author != "" {
			text += " — " + s.Author
		}
		return text
	}
}

func (p *podcastBrowser) inboxEpisodes() []podcast.Episode {
	if p.library == nil {
		return nil
	}
	var episodes []podcast.Episode
	for _, episode := range p.library.Inbox {
		played := p.library.Progress[episode.Key()].Played
		if p.page.inboxFilter == "all" || p.page.inboxFilter == "played" && played || p.page.inboxFilter == "" && !played {
			episodes = append(episodes, episode)
		}
	}
	return episodes
}

func (p *podcastBrowser) downloadRows() []episodeDownload {
	if p.library == nil {
		return nil
	}
	downloads := make([]episodeDownload, 0, len(p.library.Downloads))
	for _, download := range p.library.Downloads {
		downloads = append(downloads, download)
	}
	slices.SortFunc(downloads, func(a, b episodeDownload) int { return b.Updated.Compare(a.Updated) })
	return downloads
}

func (t *tui) podcastSelect(index int) tea.Cmd {
	p := &t.podcasts
	switch p.page.kind {
	case "home":
		switch index {
		case 0:
			country, client := p.country(), p.client
			t.podcastNavigate(podcastPage{kind: "top", title: "Top Shows (" + strings.ToUpper(country) + ")"})
			return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
				shows, err := client.Top(ctx, country)
				return podcastResultMsg{page: &podcastPage{kind: "top", title: "Top Shows (" + strings.ToUpper(country) + ")", shows: shows}, err: err}
			})
		case 1:
			t.podcastNavigate(podcastPage{kind: "categories", title: "Categories"})
		case 2:
			t.podcastNavigate(podcastPage{kind: "subscriptions", title: "Subscriptions"})
			return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
				l, err := fetchPodcastLibrary()
				return podcastResultMsg{library: l, err: err}
			})
		case 3:
			t.podcastNavigate(podcastPage{kind: "inbox", title: "Inbox"})
			return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
				_, err := func() (string, error) {
					if err := ensureDaemon(); err != nil {
						return "", err
					}
					return ask("podcast-sync")
				}()
				l, libraryErr := fetchPodcastLibrary()
				if err == nil {
					err = libraryErr
				}
				return podcastResultMsg{library: l, err: err}
			})
		case 4:
			t.podcastNavigate(podcastPage{kind: "downloads", title: "Downloads"})
			return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
				l, err := fetchPodcastLibrary()
				return podcastResultMsg{library: l, err: err}
			})
		case 5:
			t.podcastNavigate(podcastPage{kind: "download-settings", title: "Download Settings"})
			return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
				l, err := fetchPodcastLibrary()
				return podcastResultMsg{library: l, err: err}
			})
		case 6:
			p.editing, p.searching = true, true
			p.input.SetValue("")
			return p.input.Focus()
		}
	case "categories":
		category, client := podcast.Categories[index], p.client
		t.podcastNavigate(podcastPage{kind: "category", title: category.Name, query: category.Name, genre: category.ID})
		return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
			shows, err := client.Search(ctx, category.Name, category.ID)
			return podcastResultMsg{page: &podcastPage{kind: "category", title: category.Name, query: category.Name, genre: category.ID, shows: shows}, err: err}
		})
	case "episodes":
		e := p.page.feed.Episodes[index]
		return t.podcastPlay(e, false, false)
	case "inbox":
		return t.podcastPlay(p.inboxEpisodes()[index], false, false)
	case "downloads":
		return t.podcastPlay(p.downloadRows()[index].Episode, false, false)
	case "download-settings":
		return t.podcastAdjustDownloadSetting(index, 1)
	default:
		return t.podcastSearch(p.page.shows[index].FeedURL)
	}
	return nil
}

func (t *tui) podcastAdjustDownloadSetting(index, delta int) tea.Cmd {
	p := &t.podcasts
	if p.library == nil {
		return nil
	}
	settings := p.library.DownloadSettings
	switch index {
	case 0:
		settings.Auto = !settings.Auto
	case 1:
		settings.Latest = max(1, min(20, settings.Latest+delta))
	case 2:
		settings.Concurrency = max(1, min(8, settings.Concurrency+delta))
	case 3:
		settings.MaxBytes = max(int64(100<<20), min(int64(1<<50), settings.MaxBytes+int64(delta)*(1<<30)))
	case 4:
		settings.RetainPlayedDays = max(0, min(3650, settings.RetainPlayedDays+delta*7))
	default:
		return nil
	}
	return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
		data, _ := json.Marshal(settings)
		if err := ensureDaemon(); err != nil {
			return podcastResultMsg{err: err}
		}
		out, err := ask("podcast-download-settings " + string(data))
		library, libraryErr := fetchPodcastLibrary()
		if err == nil {
			err = libraryErr
		}
		return podcastResultMsg{library: library, note: out, err: err}
	})
}

func (t *tui) podcastPlay(e podcast.Episode, restart, queue bool) tea.Cmd {
	return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
		out, err := clientEpisode(e, restart, queue)
		return podcastResultMsg{note: out, err: err}
	})
}

func (t *tui) podcastRefresh() tea.Cmd {
	p := &t.podcasts
	page, client, country := p.page, p.client, p.country()
	return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
		var err error
		switch page.kind {
		case "top":
			page.shows, err = client.Top(ctx, country)
			page.title = "Top Shows (" + strings.ToUpper(country) + ")"
		case "search", "category":
			page.shows, err = client.Search(ctx, page.query, page.genre)
		case "episodes":
			page.feed, err = client.Feed(ctx, page.query)
		}
		l, libraryErr := fetchPodcastLibrary()
		if err == nil {
			err = libraryErr
		}
		page.selected = 0
		return podcastResultMsg{page: &page, library: l, err: err}
	})
}

func (t *tui) podcastKey(msg tea.KeyPressMsg) tea.Cmd {
	p := &t.podcasts
	key := msg.String()
	if p.editing {
		key = t.presentation.mapKey("editor", key)
		switch key {
		case "esc", "ctrl+c":
			p.editing = false
			p.page.filter = ""
			return nil
		case "enter":
			p.editing = false
			if p.searching {
				return t.podcastSearch(p.input.Value())
			}
			return nil
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		if !p.searching {
			p.page.filter = p.input.Value()
			p.page.selected = 0
		}
		return cmd
	}
	key = t.presentation.mapKey("podcasts", key)
	rows := p.rows()
	switch key {
	case "ctrl+q":
		return tea.Quit
	case "f3":
		t.closePodcasts()
	case "f4":
		t.closePodcasts()
		t.openEqualizer()
	case "esc", "b":
		if p.cancel != nil {
			p.cancel()
		}
		p.id++
		p.loading, p.note = false, ""
		if len(p.back) > 0 {
			p.page = p.back[len(p.back)-1]
			p.back = p.back[:len(p.back)-1]
		} else {
			t.closePodcasts()
		}
	case "ctrl+c":
		t.cancelCommand()
		t.pending = nil
		if p.cancel != nil {
			p.cancel()
		}
		p.id++
		p.loading, p.note = false, "Cancelled"
	case "up", "k":
		p.page.selected = max(0, p.page.selected-1)
	case "down", "j":
		p.page.selected = min(max(0, len(rows)-1), p.page.selected+1)
	case "pgup":
		p.page.selected = max(0, p.page.selected-max(1, t.height-8))
	case "pgdown":
		p.page.selected = min(max(0, len(rows)-1), p.page.selected+max(1, t.height-8))
	case "home":
		p.page.selected = 0
	case "end":
		p.page.selected = max(0, len(rows)-1)
	case "left", "right":
		if p.page.kind == "download-settings" && len(rows) > 0 {
			delta := -1
			if key == "right" {
				delta = 1
			}
			return t.podcastAdjustDownloadSetting(rows[p.page.selected], delta)
		}
	case "/", "ctrl+f":
		p.editing, p.searching = true, key == "ctrl+f" || p.page.kind == "home"
		p.input.SetValue("")
		return p.input.Focus()
	case "ctrl+r":
		if p.page.kind == "inbox" {
			return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
				if err := ensureDaemon(); err != nil {
					return podcastResultMsg{err: err}
				}
				_, err := ask("podcast-sync")
				l, libraryErr := fetchPodcastLibrary()
				if err == nil {
					err = libraryErr
				}
				return podcastResultMsg{library: l, err: err, note: "Inbox refreshed"}
			})
		}
		return t.podcastRefresh()
	case "v":
		if p.page.kind == "inbox" {
			p.page.inboxFilter = map[string]string{"": "all", "all": "played", "played": ""}[p.page.inboxFilter]
			p.page.selected = 0
			if p.page.inboxFilter == "all" {
				p.note = "Showing played and unplayed episodes"
			} else if p.page.inboxFilter == "played" {
				p.note = "Showing played episodes"
			} else {
				p.note = "Showing unplayed episodes"
			}
		}
	case "shift+left", "shift+right", "space", "[", "]":
		if key == "space" && (t.status == nil || t.status.Station == "" && t.status.Episode == nil) {
			if p.page.kind == "episodes" && len(rows) > 0 {
				return t.podcastSelect(rows[min(p.page.selected, len(rows)-1)])
			}
			p.note = "Choose an episode to play"
			return nil
		}
		action := map[string]string{
			"shift+left":  fmt.Sprintf("seek -%d", t.presentation.SeekLargeStep),
			"shift+right": fmt.Sprintf("seek +%d", t.presentation.SeekLargeStep),
			"space":       "toggle", "[": "speed -0.25", "]": "speed +0.25",
		}[key]
		if t.running {
			if len(t.pending) > 0 && strings.HasPrefix(action, "seek ") && strings.HasPrefix(t.pending[len(t.pending)-1], "seek ") {
				a, ae := seekDelta(strings.TrimPrefix(action, "seek "))
				b, be := seekDelta(strings.TrimPrefix(t.pending[len(t.pending)-1], "seek "))
				if ae == nil && be == nil {
					t.pending[len(t.pending)-1] = fmt.Sprintf("seek %.3f", (a + b).Seconds())
					return nil
				}
			}
			t.pending = append(t.pending, action)
			return nil
		}
		return t.start(action)
	case "enter", "f", "q", "r", "l", "d", "D", "P", "R":
		if p.loading || len(rows) == 0 {
			return nil
		}
		index := rows[min(p.page.selected, len(rows)-1)]
		if key == "enter" {
			return t.podcastSelect(index)
		}
		if key == "l" && p.page.kind == "inbox" {
			episodes := p.inboxEpisodes()
			seen := map[string]bool{}
			var items []MediaItem
			for _, episode := range episodes {
				if !seen[episode.FeedURL] {
					seen[episode.FeedURL] = true
					items = append(items, itemFromEpisode(episode))
				}
			}
			if len(items) == 0 {
				p.note = "Inbox has no matching episodes"
				return nil
			}
			return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
				out, err := sendItems("queue-append", items)
				return podcastResultMsg{note: out, err: err}
			})
		}
		var selectedEpisode *podcast.Episode
		switch p.page.kind {
		case "episodes":
			selectedEpisode = &p.page.feed.Episodes[index]
		case "inbox":
			episodes := p.inboxEpisodes()
			selectedEpisode = &episodes[index]
		case "downloads":
			downloads := p.downloadRows()
			selectedEpisode = &downloads[index].Episode
		}
		if selectedEpisode != nil {
			episode := *selectedEpisode
			switch key {
			case "q", "r":
				return t.podcastPlay(episode, key == "r", key == "q")
			case "d":
				return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
					out, err := podcastMutation("podcast-download", episode)
					l, libraryErr := fetchPodcastLibrary()
					if err == nil {
						err = libraryErr
					}
					return podcastResultMsg{library: l, note: out, err: err}
				})
			case "D", "P", "R":
				if p.page.kind != "downloads" {
					return nil
				}
				action := "podcast-download-remove"
				if key == "P" {
					action = "podcast-download-pin"
				} else if key == "R" {
					action = "podcast-download-retry"
				}
				return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
					if err := ensureDaemon(); err != nil {
						return podcastResultMsg{err: err}
					}
					out, err := ask(action + " " + episode.Key())
					l, libraryErr := fetchPodcastLibrary()
					if err == nil {
						err = libraryErr
					}
					return podcastResultMsg{library: l, note: out, err: err}
				})
			}
		}
		var show podcast.Show
		if p.page.kind == "episodes" {
			show = p.page.feed.Show
		} else if len(p.page.shows) > index {
			show = p.page.shows[index]
		}
		if show.FeedURL == "" {
			return nil
		}
		if key == "f" {
			action, note := "podcast-subscribe", "Subscribed: "
			if p.subscribed(show.FeedURL) {
				action, note = "podcast-unsubscribe", "Unsubscribed: "
			}
			return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
				_, err := podcastMutation(action, show)
				if err != nil {
					return podcastResultMsg{err: err}
				}
				l, err := fetchPodcastLibrary()
				return podcastResultMsg{library: l, err: err, note: note + show.Title}
			})
		}
		if key == "l" {
			client := p.client
			return t.podcastRequest(func(ctx context.Context) podcastResultMsg {
				feed, err := client.Feed(ctx, show.FeedURL)
				if err != nil {
					return podcastResultMsg{err: err}
				}
				index := newestEpisode(feed.Episodes)
				if index < 0 {
					return podcastResultMsg{err: fmt.Errorf("podcast feed has no playable episodes")}
				}
				out, err := clientEpisode(feed.Episodes[index], false, true)
				return podcastResultMsg{note: out, err: err}
			})
		}
	}
	return nil
}

func (t *tui) podcastView() tea.View {
	p := &t.podcasts
	height, width := max(1, t.height), max(1, t.width)
	lines := make([]string, height)
	fit := func(s string) string { return ansi.Truncate(s, width, "") }
	heading := "Podcasts  /  " + p.page.title
	if p.page.kind == "episodes" && p.subscribed(p.page.feed.Show.FeedURL) {
		heading += " · subscribed"
	}
	lines[0] = fit(styleHeading.Render(heading))
	rows := p.rows()
	layout := t.contentLayout(height, p.editing, 2)
	room := layout.room
	first := max(0, min(p.page.selected-room/2, len(rows)-room))
	for row := 0; row < room && first+row < len(rows); row++ {
		index := first + row
		text := p.rowText(rows[index])
		if p.page.kind == "episodes" && t.status != nil && t.status.Episode != nil && t.status.Episode.Key() == p.page.feed.Episodes[rows[index]].Key() && (t.status.Playing || t.status.Paused) {
			text = "▶ " + strings.TrimPrefix(text, "  ")
		}
		if index == p.page.selected {
			lines[row+2] = styleSelection.Render(fit(" ❯ " + text))
			continue
		}
		prefix := styleDim.Render("   ")
		markerStyle := styleDim
		marker := ""
		if strings.HasPrefix(text, "▶ ") {
			marker, markerStyle, text = "▶ ", stylePrompt, strings.TrimPrefix(text, "▶ ")
		} else if strings.HasPrefix(text, "✓ ") {
			marker, markerStyle, text = "✓ ", styleSuccess, strings.TrimPrefix(text, "✓ ")
		} else if strings.HasPrefix(text, "♥ ") {
			marker, markerStyle, text = "♥ ", styleStation, strings.TrimPrefix(text, "♥ ")
		}
		contentStyle := styleInput
		if p.page.kind == "home" || p.page.kind == "categories" {
			contentStyle = styleCommand
		}
		lines[row+2] = fit(prefix + markerStyle.Render(marker) + contentStyle.Render(text))
	}
	if room > 0 && len(rows) == 0 && !p.loading {
		lines[2] = styleDim.Render(fit("  No results. " + t.presentation.bindingLabel("podcast.global-search") + " searches shows or opens an RSS URL."))
	}
	if layout.note >= 0 {
		note := p.note
		if p.loading {
			note = "Loading… " + t.presentation.bindingHint("podcast.cancel", "cancels")
		} else if note == "" {
			note = fmt.Sprintf("%d items", len(rows))
			if p.page.filter != "" {
				note += " · filter: " + p.page.filter
			}
		}
		if t.presentation.Panels["metadata"] && len(rows) > 0 {
			note += " · selected: " + p.rowText(rows[min(p.page.selected, len(rows)-1)])
		}
		lines[layout.note] = fit(styleDim.Render(note))
		if layout.showHelp {
			if p.page.kind == "download-settings" {
				lines[layout.firstHint] = fit(styleDim.Render(t.presentation.bindingHint("podcast.setting-next", "increase/toggle") + " · " + t.presentation.bindingHint("podcast.setting-previous", "decrease") + " · automatic downloads apply on sync"))
				lines[layout.secondHint] = fit(styleDim.Render(t.presentation.bindingHint("browser.back", "back") + " · " + t.presentation.bindingHint("global.podcasts", "prompt")))
			} else {
				lines[layout.firstHint] = fit(styleDim.Render(strings.Join([]string{t.presentation.bindingHint("browser.select", "open/play"), t.presentation.bindingHint("browser.favorite", "subscribe"), t.presentation.bindingHint("podcast.download", "download"), t.presentation.bindingHint("podcast.remove-download", "remove"), t.presentation.bindingHint("podcast.retry-download", "retry"), t.presentation.bindingHint("podcast.pin-download", "pin"), t.presentation.bindingHint("browser.search", "filter"), t.presentation.bindingHint("browser.refresh", "refresh/sync")}, " · ")))
				lines[layout.secondHint] = fit(styleDim.Render(strings.Join([]string{t.presentation.bindingPairHint("podcast.seek-back", "podcast.seek-forward", fmt.Sprintf("±%ds", t.presentation.SeekLargeStep)), t.presentation.bindingHint("browser.pause", "pause"), t.presentation.bindingPairHint("podcast.speed-down", "podcast.speed-up", "speed"), t.presentation.bindingHint("podcast.queue", "queue"), t.presentation.bindingHint("podcast.latest", "latest/all shows"), t.presentation.bindingHint("podcast.played-filter", "played"), t.presentation.bindingHint("podcast.restart", "restart"), t.presentation.bindingHint("browser.back", "back"), t.presentation.bindingHint("global.podcasts", "prompt")}, " · ")))
			}
		}
		if p.editing && layout.input >= 0 {
			p.input.SetWidth(max(1, width-3))
			lines[layout.input] = fit(p.input.View())
		}
		if layout.status >= 0 {
			lines[layout.status] = t.statusBar()
		}
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if p.editing && layout.input >= 0 {
		if c := p.input.Cursor(); c != nil {
			c.Y += layout.input
			v.Cursor = c
		}
	}
	return v
}
