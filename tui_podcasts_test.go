package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/podcast"
)

// TestPodcastBrowserPreservesPromptAndRejectsLateResults checks focus and request ownership.
func TestPodcastBrowserPreservesPromptAndRejectsLateResults(t *testing.T) {
	withConfigDir(t)
	m := newTUI()
	m.width, m.height = 100, 30
	m.setInput("vol 50")
	cmd := m.openPodcasts("")
	m.update(cmd())
	if !m.podcasts.open || m.input.Value() != "vol 50" {
		t.Fatal("opening podcasts changed draft")
	}
	oldID := m.podcasts.id
	m.podcastKey(tea.KeyPressMsg{Code: tea.KeyF3})
	m.podcastResult(podcastResultMsg{id: oldID, page: &podcastPage{kind: "search", title: "Late"}})
	if m.podcasts.open || m.podcasts.page.title == "Late" || m.input.Value() != "vol 50" {
		t.Fatal("late request revived browser or lost draft")
	}
}

// TestPodcastBrowserFeedAndFilter exercises real feed parsing through UI commands.
func TestPodcastBrowserFeedAndFilter(t *testing.T) {
	withConfigDir(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<rss><channel><title>My Show</title><item><title>Alpha</title><guid>one</guid><enclosure type="audio/mpeg" url="https://example.com/one"/></item><item><title>Beta</title><guid>two</guid><enclosure type="audio/mpeg" url="https://example.com/two"/></item></channel></rss>`)
	}))
	defer server.Close()
	m := newTUI()
	m.width, m.height = 90, 24
	cmd := m.openPodcasts(server.URL)
	m.update(cmd())
	if m.podcasts.page.kind != "episodes" || len(m.podcasts.rows()) != 2 {
		t.Fatal("feed was not loaded")
	}
	m.podcastKey(vizKey("/"))
	m.podcastKey(vizKey("B"))
	if len(m.podcasts.rows()) != 1 || m.podcasts.rows()[0] != 1 {
		t.Fatal("filter did not narrow episodes")
	}
	m.podcastKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	view := m.View()
	if strings.Count(view.Content, "\n")+1 != m.height || !strings.Contains(ansi.Strip(view.Content), "Beta") {
		t.Fatal("browser layout is incorrect")
	}
	m.podcastKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.podcasts.open || m.podcasts.page.kind != "home" {
		t.Fatal("back did not restore previous page")
	}
	m.podcastKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.podcasts.open {
		t.Fatal("root Esc did not restore prompt")
	}
	for _, size := range [][2]int{{1, 1}, {15, 4}, {30, 8}} {
		m.width, m.height = size[0], size[1]
		m.podcasts.open = true
		view := m.View()
		if strings.Count(view.Content, "\n")+1 != size[1] {
			t.Fatal("tiny browser overflowed")
		}
	}
}

// TestPodcastCLIJSONAndValidation checks offline CLI discovery and strict arguments.
func TestPodcastCLIJSONAndValidation(t *testing.T) {
	withConfigDir(t)
	out, err := runPodcastCommand(context.Background(), []string{"categories", "--json"})
	if err != nil || !strings.Contains(out, "Technology") {
		t.Fatalf("categories: %s %v", out, err)
	}
	for _, args := range [][]string{{"play"}, {"play", "file:///a"}, {"top", "extra"}, {"search"}, {"country", "USA"}, {"no-such-command"}, {"top", "--no-such-option"}} {
		if _, err := runPodcastCommand(context.Background(), args); err == nil {
			t.Fatal("accepted invalid arguments", args)
		}
	}
	out, err = runPodcastCommand(context.Background(), []string{"subscriptions", "--json"})
	if err != nil || out != "null" && out != "[]" {
		t.Fatalf("empty subscriptions: %s %v", out, err)
	}
	if len(podcast.Categories) != 19 {
		t.Fatal("category count")
	}
}

// TestPodcastSeekBurstsKeepControlsResponsive checks commands during feed loading.
func TestPodcastSeekBurstsKeepControlsResponsive(t *testing.T) {
	withConfigDir(t)
	m := newTUI()
	m.width, m.height = 100, 30
	m.podcasts.open, m.podcasts.loading = true, true
	key := tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift}
	if m.podcastKey(key) == nil || m.active != "seek" {
		t.Fatal("feed loading blocked playback controls")
	}
	m.podcastKey(key)
	m.podcastKey(key)
	if len(m.pending) != 1 || m.pending[0] != "seek 60.000" {
		t.Fatalf("seek repeats were dropped: %v", m.pending)
	}
}
