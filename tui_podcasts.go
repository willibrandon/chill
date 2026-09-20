package main

import (
	"context"
	"fmt"
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
	return refreshStatus
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
		n = 4
	case "categories":
		n = len(podcast.Categories)
	case "episodes":
		n = len(p.page.feed.Episodes)
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
		return []string{"Top Shows (" + strings.ToUpper(p.country()) + ")", "Browse Categories", "Subscriptions", "Search shows or open an RSS feed"}[i]
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
	default:
		return t.podcastSearch(p.page.shows[index].FeedURL)
	}
	return nil
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
	if key == "ctrl+q" {
		return tea.Quit
	}
	if key == "f3" {
		t.closePodcasts()
		return nil
	}
	if p.editing {
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
	rows := p.rows()
	switch key {
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
	case "/", "ctrl+f":
		p.editing, p.searching = true, key == "ctrl+f" || p.page.kind == "home"
		p.input.SetValue("")
		return p.input.Focus()
	case "ctrl+r":
		return t.podcastRefresh()
	case "shift+left", "shift+right", "space", "[", "]":
		if key == "space" && (t.status == nil || t.status.Station == "" && t.status.Episode == nil) {
			if p.page.kind == "episodes" && len(rows) > 0 {
				return t.podcastSelect(rows[min(p.page.selected, len(rows)-1)])
			}
			p.note = "Choose an episode to play"
			return nil
		}
		action := map[string]string{"shift+left": "seek -30", "shift+right": "seek +30", "space": "toggle", "[": "speed -0.25", "]": "speed +0.25"}[key]
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
	case "enter", "f", "q", "r", "l":
		if p.loading || len(rows) == 0 {
			return nil
		}
		index := rows[min(p.page.selected, len(rows)-1)]
		if key == "enter" {
			return t.podcastSelect(index)
		}
		var show podcast.Show
		if p.page.kind == "episodes" {
			show = p.page.feed.Show
			if key == "q" || key == "r" {
				return t.podcastPlay(p.page.feed.Episodes[index], key == "r", key == "q")
			}
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
				out, err := clientEpisode(feed.Episodes[newestEpisode(feed.Episodes)], false, true)
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
	room := max(0, height-6)
	first := max(0, min(p.page.selected-room/2, len(rows)-room))
	for row := 0; row < room && first+row < len(rows); row++ {
		index := first + row
		text := p.rowText(rows[index])
		if p.page.kind == "episodes" && t.status != nil && t.status.Episode != nil && t.status.Episode.Key() == p.page.feed.Episodes[rows[index]].Key() && (t.status.Playing || t.status.Paused) {
			text = "▶ " + strings.TrimPrefix(text, "  ")
		}
		style, prefix := styleInput, "   "
		if index == p.page.selected {
			style, prefix = styleSelected, " ❯ "
		}
		lines[row+2] = style.Render(fit(prefix + text))
	}
	if room > 0 && len(rows) == 0 && !p.loading {
		lines[2] = styleDim.Render(fit("  No results. Ctrl+F searches shows or opens an RSS URL."))
	}
	if height >= 5 {
		note := p.note
		if p.loading {
			note = "Loading… Ctrl+C cancels"
		} else if note == "" {
			note = fmt.Sprintf("%d items", len(rows))
			if p.page.filter != "" {
				note += " · filter: " + p.page.filter
			}
		}
		lines[height-4] = fit(styleDim.Render(note))
		lines[height-3] = fit(styleDim.Render("Enter open/play · f subscribe · / filter · Ctrl+F search/RSS · Ctrl+R refresh"))
		lines[height-2] = fit(styleDim.Render("Shift+←/→ ±30s · Space pause · [ ] speed · q queue · l latest · r restart · Esc back · F3 prompt"))
		if p.editing {
			p.input.SetWidth(max(1, width-3))
			lines[height-2] = fit(p.input.View())
		}
		lines[height-1] = t.statusBar()
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if p.editing && height >= 5 {
		if c := p.input.Cursor(); c != nil {
			c.Y += height - 2
			v.Cursor = c
		}
	}
	return v
}
