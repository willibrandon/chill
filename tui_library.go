package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type libraryBrowser struct {
	open, loading, editing                     bool
	page, title, cwd, filter, note, editAction string
	selected                                   int
	back                                       []libraryPage
	state                                      *libraryState
	items                                      []MediaItem
	files                                      []os.DirEntry
	playlist                                   string
	input                                      textinput.Model
	id                                         uint64
}

type libraryPage struct {
	page, title, cwd, playlist string
	selected                   int
}

type libraryResultMsg struct {
	id        uint64
	state     *libraryState
	items     []MediaItem
	files     []os.DirEntry
	cwd, note string
	reload    bool
	err       error
}

func (t *tui) openLibrary() tea.Cmd {
	t.closeProviders()
	t.closeAudio()
	b := &t.libraryUI
	b.open = true
	b.page = "home"
	b.title = "Library"
	b.selected = 0
	b.note = ""
	t.closeLyrics()
	t.closeRadio()
	t.closePodcasts()
	t.closeEqualizer()
	t.help = false
	t.viz.focused = false
	if b.input.Prompt == "" {
		b.input = textinput.New()
		b.input.Prompt = "/ "
		b.input.SetVirtualCursor(false)
	}
	return t.loadLibraryPage()
}

func (t *tui) closeLibrary() {
	b := &t.libraryUI
	b.id++
	b.open, b.loading, b.editing = false, false, false
	b.back = nil
}

func (t *tui) loadLibraryPage() tea.Cmd {
	b := &t.libraryUI
	b.id++
	id := b.id
	b.loading = true
	page, cwd, note, playlistName := b.page, b.cwd, b.note, b.playlist
	return func() tea.Msg {
		result := libraryResultMsg{id: id, note: note}
		state, err := fetchLibraryState()
		if err != nil {
			result.err = err
			return result
		}
		result.state = state
		switch page {
		case "queue":
			result.items = append(cloneItems(state.PlayNext), state.Queue...)
		case "next":
			result.items = cloneItems(state.PlayNext)
		case "recent":
			result.items = cloneItems(state.Recent)
		case "favorites":
			result.items, _ = libraryCollection(state, "favorites")
		case "bookmarks":
			result.items, _ = libraryCollection(state, "bookmarks")
		case "playlist":
			if playlist, ok := state.Playlists[playlistKey(playlistName)]; ok {
				result.items = cloneItems(playlist.Items)
			} else {
				result.err = fmt.Errorf("playlist not found: %s", playlistName)
			}
		case "files":
			if cwd == "" {
				cwd, _ = os.UserHomeDir()
			}
			absolute, err := filepath.Abs(cwd)
			if err != nil {
				result.err = err
				return result
			}
			result.cwd = absolute
			entries, err := os.ReadDir(absolute)
			if err != nil {
				result.err = err
				return result
			}
			for _, entry := range entries {
				if entry.IsDir() || isAudioPath(entry.Name()) || isPlaylistPath(entry.Name()) {
					result.files = append(result.files, entry)
				}
			}
			sort.SliceStable(result.files, func(i, j int) bool {
				return result.files[i].IsDir() && !result.files[j].IsDir() || result.files[i].IsDir() == result.files[j].IsDir() && strings.ToLower(result.files[i].Name()) < strings.ToLower(result.files[j].Name())
			})
		}
		return result
	}
}

func (t *tui) libraryResult(msg libraryResultMsg) tea.Cmd {
	b := &t.libraryUI
	if msg.id != b.id || !b.open {
		return nil
	}
	b.loading = false
	if msg.err != nil {
		b.note = msg.err.Error()
		return nil
	}
	if msg.state == nil {
		b.note = msg.note
		return t.loadLibraryPage()
	}
	b.state = msg.state
	b.items = msg.items
	b.files = msg.files
	if msg.cwd != "" {
		b.cwd = msg.cwd
	}
	b.note = msg.note
	b.selected = min(b.selected, max(0, len(b.rows())-1))
	return refreshStatus
}

func (b *libraryBrowser) rows() []int {
	n := 0
	switch b.page {
	case "home":
		n = 7
	case "playlists":
		if b.state != nil {
			n = len(playlistNames(b.state))
		}
	case "files":
		n = len(b.files) + 1
	default:
		n = len(b.items)
	}
	var rows []int
	for i := range n {
		if strings.Contains(strings.ToLower(b.rowText(i)), strings.ToLower(b.filter)) {
			rows = append(rows, i)
		}
	}
	return rows
}

func (b *libraryBrowser) rowText(i int) string {
	switch b.page {
	case "home":
		return []string{"Queue", "Play Next", "Saved Playlists", "Browse Files", "Favorites", "Bookmarks", "Recently Played"}[i]
	case "playlists":
		names := playlistNames(b.state)
		p := b.state.Playlists[playlistKey(names[i])]
		return fmt.Sprintf("%-30s %d item(s)", p.Name, len(p.Items))
	case "files":
		if i == 0 {
			return "../"
		}
		entry := b.files[i-1]
		suffix := ""
		if entry.IsDir() {
			suffix = "/"
		}
		return entry.Name() + suffix
	default:
		item := b.items[i]
		marks := "  "
		if b.state != nil && b.state.Favorites[item.ID] {
			marks = "♥ "
		}
		if b.state != nil && b.state.Bookmarks[item.ID] {
			marks = "★ "
		}
		return fmt.Sprintf("%s%-8s %s", marks, item.Kind, item.display())
	}
}

func (t *tui) libraryNavigate(page, title, note string) tea.Cmd {
	b := &t.libraryUI
	b.back = append(b.back, libraryPage{b.page, b.title, b.cwd, b.playlist, b.selected})
	b.page, b.title, b.selected, b.filter, b.note = page, title, 0, "", note
	return t.loadLibraryPage()
}

func (t *tui) librarySelect(index int) tea.Cmd {
	b := &t.libraryUI
	switch b.page {
	case "home":
		pages := [][2]string{{"queue", "Queue"}, {"next", "Play Next"}, {"playlists", "Saved Playlists"}, {"files", "Browse Files"}, {"favorites", "Favorites"}, {"bookmarks", "Bookmarks"}, {"recent", "Recently Played"}}
		note := ""
		if pages[index][0] == "files" && b.cwd == "" {
			b.cwd = resolveInitialDirectory(t.presentation.InitialDirectory)
			if info, err := os.Stat(b.cwd); err != nil || !info.IsDir() {
				b.cwd, _ = os.UserHomeDir()
				note = "Initial browser directory is unavailable; opened home"
			}
		}
		return t.libraryNavigate(pages[index][0], pages[index][1], note)
	case "playlists":
		name := playlistNames(b.state)[index]
		p := b.state.Playlists[playlistKey(name)]
		b.playlist = name
		b.items = cloneItems(p.Items)
		return t.libraryNavigate("playlist", p.Name, "")
	case "files":
		path := filepath.Dir(b.cwd)
		if index > 0 {
			path = filepath.Join(b.cwd, b.files[index-1].Name())
		}
		info, err := os.Stat(path)
		if err != nil {
			b.note = err.Error()
			return nil
		}
		if info.IsDir() {
			b.cwd = path
			b.selected = 0
			return t.loadLibraryPage()
		}
		return func() tea.Msg {
			items, err := loadMediaInputs(context.Background(), []string{path})
			if err == nil {
				_, err = playMediaItems(items)
			}
			return libraryResultMsg{id: b.id, note: "loading: " + filepath.Base(path), reload: true, err: err, state: b.state, files: b.files, cwd: b.cwd}
		}
	default:
		if index >= len(b.items) {
			return nil
		}
		if b.page == "queue" {
			position := index + 1
			return func() tea.Msg {
				if err := ensureDaemon(); err != nil {
					return libraryResultMsg{id: b.id, err: err}
				}
				out, err := ask(fmt.Sprintf("queue-play %d", position))
				return libraryResultMsg{id: b.id, note: out, err: err}
			}
		}
		item := b.items[index]
		return func() tea.Msg {
			out, err := playMedia(item, false)
			return libraryResultMsg{id: b.id, note: out, reload: true, err: err, state: b.state, items: b.items, cwd: b.cwd, files: b.files}
		}
	}
}

func (t *tui) libraryKey(msg tea.KeyPressMsg) tea.Cmd {
	b := &t.libraryUI
	key := msg.String()
	if b.editing {
		key = t.presentation.mapKey("editor", key)
		switch key {
		case "esc", "ctrl+c":
			b.editing = false
			return nil
		case "enter":
			value := strings.TrimSpace(b.input.Value())
			b.editing = false
			if b.editAction == "save" && value != "" {
				items := append(cloneItems(b.state.PlayNext), b.state.Queue...)
				return func() tea.Msg {
					out, err := mutatePlaylist("playlist-put", playlistMutation{Name: value, Items: items})
					return libraryResultMsg{id: b.id, note: out, err: err}
				}
			}
			if b.editAction == "rename" && value != "" {
				old := b.playlist
				b.playlist, b.title = value, value
				return func() tea.Msg {
					out, err := mutatePlaylist("playlist-rename", playlistMutation{Name: old, NewName: value})
					return libraryResultMsg{id: b.id, note: out, err: err}
				}
			}
		}
		var cmd tea.Cmd
		b.input, cmd = b.input.Update(msg)
		if b.editAction == "filter" {
			b.filter = b.input.Value()
			b.selected = 0
		}
		return cmd
	}
	key = t.presentation.mapKey("library", key)
	rows := b.rows()
	switch key {
	case "esc", "b":
		if len(b.back) == 0 {
			t.closeLibrary()
			return nil
		}
		last := b.back[len(b.back)-1]
		b.back = b.back[:len(b.back)-1]
		b.page, b.title, b.cwd, b.playlist, b.selected = last.page, last.title, last.cwd, last.playlist, last.selected
		return t.loadLibraryPage()
	case "up", "k":
		b.selected = max(0, b.selected-1)
	case "down", "j":
		b.selected = min(max(0, len(rows)-1), b.selected+1)
	case "pgup":
		b.selected = max(0, b.selected-10)
	case "pgdown":
		b.selected = min(max(0, len(rows)-1), b.selected+10)
	case "shift+up", "K", "shift+down", "J":
		if (b.page == "queue" || b.page == "playlist") && len(rows) > 1 {
			from := rows[b.selected]
			to := from - 1
			if key == "shift+down" || key == "J" {
				to = from + 1
			}
			if to < 0 || to >= len(b.items) {
				return nil
			}
			b.selected = max(0, min(len(rows)-1, b.selected+map[bool]int{true: 1, false: -1}[to > from]))
			if b.page == "queue" {
				return func() tea.Msg {
					if err := ensureDaemon(); err != nil {
						return libraryResultMsg{id: b.id, err: err}
					}
					data, _ := json.Marshal(queueRequest{Index: from + 1, To: to + 1})
					out, err := ask("queue-move " + string(data))
					return libraryResultMsg{id: b.id, note: out, err: err}
				}
			}
			items := cloneItems(b.items)
			items[from], items[to] = items[to], items[from]
			name := b.playlist
			return func() tea.Msg {
				out, err := mutatePlaylist("playlist-put", playlistMutation{Name: name, Items: items})
				return libraryResultMsg{id: b.id, note: out, err: err}
			}
		}
	case "enter":
		if len(rows) > 0 {
			return t.librarySelect(rows[b.selected])
		}
	case "/":
		b.editing = true
		b.editAction = "filter"
		b.input.Prompt = "/ "
		b.input.SetValue(b.filter)
		return b.input.Focus()
	case "w":
		if b.page == "queue" {
			b.editing = true
			b.editAction = "save"
			b.input.Prompt = "playlist name: "
			b.input.SetValue("")
			return b.input.Focus()
		}
	case "r":
		if b.page == "playlist" {
			b.editing = true
			b.editAction = "rename"
			b.input.Prompt = "new name: "
			b.input.SetValue(b.playlist)
			return b.input.Focus()
		}
		if err := ensureDaemon(); err != nil {
			b.note = err.Error()
			return nil
		}
		return func() tea.Msg {
			out, err := ask("repeat cycle")
			return libraryResultMsg{id: b.id, note: out, err: err}
		}
	case "R":
		if err := ensureDaemon(); err != nil {
			b.note = err.Error()
			return nil
		}
		return func() tea.Msg {
			out, err := ask("repeat cycle")
			return libraryResultMsg{id: b.id, note: out, err: err}
		}
	case "z":
		if err := ensureDaemon(); err != nil {
			b.note = err.Error()
			return nil
		}
		return func() tea.Msg {
			out, err := ask("shuffle toggle")
			return libraryResultMsg{id: b.id, note: out, err: err}
		}
	case "u":
		if b.page == "queue" {
			return func() tea.Msg {
				if err := ensureDaemon(); err != nil {
					return libraryResultMsg{id: b.id, err: err}
				}
				out, err := ask("queue-undo")
				return libraryResultMsg{id: b.id, note: out, err: err}
			}
		}
	case "x":
		if b.page == "queue" && len(rows) > 0 {
			index := rows[b.selected] + 1
			return func() tea.Msg {
				if err := ensureDaemon(); err != nil {
					return libraryResultMsg{id: b.id, err: err}
				}
				out, err := ask(fmt.Sprintf("queue-remove %d", index))
				return libraryResultMsg{id: b.id, note: out, err: err}
			}
		}
		if b.page == "playlist" && len(rows) > 0 {
			index := rows[b.selected]
			items := append(cloneItems(b.items[:index]), b.items[index+1:]...)
			name := b.playlist
			return func() tea.Msg {
				out, err := mutatePlaylist("playlist-put", playlistMutation{Name: name, Items: items})
				return libraryResultMsg{id: b.id, note: out, err: err}
			}
		}
	case "f", "B":
		action := "favorite"
		if key == "B" {
			action = "bookmark"
		}
		if b.page == "files" && len(rows) > 0 {
			index := rows[b.selected]
			if index == 0 || b.files[index-1].IsDir() || !isAudioPath(b.files[index-1].Name()) {
				b.note = "Choose an audio file to mark"
				return nil
			}
			path := filepath.Join(b.cwd, b.files[index-1].Name())
			return func() tea.Msg {
				items, err := loadMediaInputs(context.Background(), []string{path})
				if err != nil {
					return libraryResultMsg{id: b.id, err: err}
				}
				if err := ensureDaemon(); err != nil {
					return libraryResultMsg{id: b.id, err: err}
				}
				data, _ := json.Marshal(items[0])
				out, err := ask(action + " " + string(data))
				return libraryResultMsg{id: b.id, note: out, err: err}
			}
		}
		argument := ""
		if b.page != "home" && b.page != "files" && b.page != "playlists" && len(rows) > 0 {
			data, _ := json.Marshal(b.items[rows[b.selected]])
			argument = " " + string(data)
		}
		if argument == "" && !isDaemonRunning() {
			b.note = "nothing selected"
			return nil
		}
		return func() tea.Msg {
			if err := ensureDaemon(); err != nil {
				return libraryResultMsg{id: b.id, err: err}
			}
			out, err := ask(action + argument)
			return libraryResultMsg{id: b.id, note: out, err: err}
		}
	case "a", "n":
		if b.page == "files" && len(rows) > 0 {
			index := rows[b.selected]
			if index == 0 {
				return nil
			}
			path := filepath.Join(b.cwd, b.files[index-1].Name())
			action := "queue-append"
			if key == "n" {
				action = "queue-next"
			}
			return func() tea.Msg {
				items, err := loadMediaInputs(context.Background(), []string{path})
				var out string
				if err == nil {
					out, err = sendItems(action, items)
				}
				return libraryResultMsg{id: b.id, note: out, reload: true, err: err, state: b.state, files: b.files, cwd: b.cwd}
			}
		}
	case "d":
		if b.page == "playlist" {
			name := b.playlist
			b.page, b.title, b.playlist, b.selected = "playlists", "Saved Playlists", "", 0
			return func() tea.Msg {
				out, err := mutatePlaylist("playlist-delete", playlistMutation{Name: name})
				return libraryResultMsg{id: b.id, note: out, err: err}
			}
		}
	}
	return nil
}

func (t *tui) libraryView() tea.View {
	b := &t.libraryUI
	height, width := max(1, t.height), max(1, t.width)
	lines := make([]string, height)
	fit := func(s string) string { return ansi.Truncate(s, width, "") }
	heading := "Library  /  " + b.title
	if b.page == "files" {
		heading += "  " + b.cwd
	}
	lines[0] = fit(styleHeading.Render(heading))
	rows := b.rows()
	layout := t.contentLayout(height, b.editing, 2)
	room := layout.room
	first := max(0, min(b.selected-room/2, len(rows)-room))
	for row := 0; row < room && first+row < len(rows); row++ {
		index := first + row
		text := b.rowText(rows[index])
		lines[row+2] = renderBrowserRow(text, width, index == b.selected)
	}
	if layout.note >= 0 {
		note := b.note
		if b.loading {
			note = "Loading…"
		} else if note == "" {
			note = fmt.Sprintf("%d items", len(rows))
			if b.filter != "" {
				note += " · filter: " + b.filter
			}
		}
		if t.presentation.Panels["metadata"] && len(rows) > 0 {
			note += " · selected: " + b.rowText(rows[min(b.selected, len(rows)-1)])
		}
		lines[layout.note] = fit(styleDim.Render(note))
		if layout.showHelp {
			lines[layout.firstHint] = fit(styleDim.Render(strings.Join([]string{t.presentation.bindingHint("browser.select", "open/play"), t.presentation.bindingHint("browser.append", "append"), t.presentation.bindingHint("browser.play-next", "play next"), t.presentation.bindingHint("browser.remove", "remove"), t.presentation.bindingHint("library.move-down", "move"), t.presentation.bindingHint("browser.favorite", "favorite"), t.presentation.bindingHint("browser.bookmark", "bookmark")}, " · ")))
			lines[layout.secondHint] = fit(styleDim.Render(strings.Join([]string{t.presentation.bindingHint("browser.search", "filter"), t.presentation.bindingHint("library.save", "save queue"), t.presentation.bindingHint("browser.shuffle", "shuffle"), t.presentation.bindingHint("library.repeat-rename", "repeat/rename"), t.presentation.bindingHint("browser.undo", "undo"), t.presentation.bindingHint("library.delete", "delete"), t.presentation.bindingHint("browser.back", "back"), t.presentation.bindingHint("global.library", "prompt")}, " · ")))
		}
		if b.editing && layout.input >= 0 {
			b.input.SetWidth(max(1, width-len(b.input.Prompt)-1))
			lines[layout.input] = fit(b.input.View())
		}
		if layout.status >= 0 {
			lines[layout.status] = t.statusBar()
		}
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if b.editing && layout.input >= 0 {
		if c := b.input.Cursor(); c != nil {
			c.Y += layout.input
			v.Cursor = c
		}
	}
	return v
}

func runReplLibrary() {
	model := newTUI()
	model.libraryStart = true
	_, err := tea.NewProgram(model, interfaceProgramOptions(model.presentation)...).Run()
	model.shutdown()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
}
