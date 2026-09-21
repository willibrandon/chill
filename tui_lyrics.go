package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/lyrics"
)

type replLyrics struct {
	open, loading bool
	id            uint64
	raw, heading  string
	lines         []string
	offset        int
	note          string
	cancel        context.CancelFunc
}

type lyricsResultMsg struct {
	id     uint64
	raw    string
	result lyrics.Result
	err    error
}

func (t *tui) openLyrics() tea.Cmd {
	l := &t.lyrics
	l.open, l.loading, l.note = true, true, ""
	t.viz.focused, t.viz.fullscreen = false, false
	if l.cancel != nil {
		l.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	l.cancel = cancel
	l.id++
	id := l.id
	return func() tea.Msg {
		defer cancel()
		status, err := fetchStatus()
		if err != nil {
			return lyricsResultMsg{id: id, err: err}
		}
		if status == nil || status.NowPlaying == nil {
			if status == nil || status.Item == nil || !status.Item.finite() {
				return lyricsResultMsg{id: id, err: fmt.Errorf("no recognized track")}
			}
			item := status.Item
			if strings.TrimSpace(item.EmbeddedLyrics) != "" {
				return lyricsResultMsg{id: id, raw: item.display(), result: lyrics.Result{Track: item.Title, Artist: item.Artist, Album: item.Album, Plain: item.EmbeddedLyrics}}
			}
			if item.Title == "" {
				return lyricsResultMsg{id: id, err: fmt.Errorf("current track has no title metadata")}
			}
			result, err := lyrics.NewClient().Get(ctx, item.Artist, item.Title)
			return lyricsResultMsg{id: id, raw: item.display(), result: result, err: err}
		}
		result, err := lyrics.NewClient().Get(ctx, status.NowPlaying.Artist, status.NowPlaying.Title)
		return lyricsResultMsg{id: id, raw: status.NowPlaying.Raw, result: result, err: err}
	}
}

func (t *tui) closeLyrics() {
	l := &t.lyrics
	if l.cancel != nil {
		l.cancel()
	}
	l.id++
	l.open, l.loading = false, false
}

func (t *tui) lyricsResult(msg lyricsResultMsg) tea.Cmd {
	l := &t.lyrics
	if msg.id != l.id || !l.open {
		return nil
	}
	l.loading, l.offset = false, 0
	if msg.raw != "" {
		l.raw = msg.raw
	}
	if msg.err != nil {
		l.note, l.lines = msg.err.Error(), nil
		return nil
	}
	l.heading = msg.result.Track
	if msg.result.Artist != "" {
		l.heading = msg.result.Artist + " — " + msg.result.Track
	}
	l.lines = msg.result.Lines()
	if msg.result.Instrumental {
		l.lines = []string{"Instrumental"}
	}
	if len(l.lines) == 0 {
		l.note = "Lyrics response was empty"
	} else {
		l.note = fmt.Sprintf("%d lines · live streams use manual scrolling", len(l.lines))
	}
	return nil
}

func (t *tui) lyricsKey(msg tea.KeyPressMsg) tea.Cmd {
	l := &t.lyrics
	room := max(1, t.height-6)
	switch msg.String() {
	case "ctrl+q":
		return tea.Quit
	case "f6", "esc", "b":
		t.closeLyrics()
	case "ctrl+r":
		return t.openLyrics()
	case "up", "k":
		l.offset = max(0, l.offset-1)
	case "down", "j":
		l.offset = min(max(0, len(l.lines)-room), l.offset+1)
	case "pgup":
		l.offset = max(0, l.offset-room)
	case "pgdown", "space":
		l.offset = min(max(0, len(l.lines)-room), l.offset+room)
	case "home", "g":
		l.offset = 0
	case "end", "G", "shift+g":
		l.offset = max(0, len(l.lines)-room)
	}
	return nil
}

func (t *tui) lyricsView() tea.View {
	l := &t.lyrics
	height, width := max(1, t.height), max(1, t.width)
	rows := make([]string, height)
	fit := func(s string) string { return ansi.Truncate(s, width, "") }
	heading := "Lyrics"
	if l.heading != "" {
		heading += "  /  " + l.heading
	}
	rows[0] = fit(styleHeading.Render(heading))
	room := max(0, height-5)
	for i := 0; i < room && l.offset+i < len(l.lines); i++ {
		rows[i+2] = fit(styleInput.Render("  " + l.lines[l.offset+i]))
	}
	if height >= 4 {
		note := l.note
		if l.loading {
			note = "Loading lyrics…"
		}
		rows[height-3] = fit(styleDim.Render(note))
		rows[height-2] = fit(styleDim.Render("↑/↓ scroll · PgUp/PgDn page · Ctrl+R refresh · Esc/F6 prompt"))
		rows[height-1] = t.statusBar()
	}
	v := tea.NewView(strings.Join(rows, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
