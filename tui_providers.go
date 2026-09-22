package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type providerBrowser struct {
	open, loading, editing, searching bool
	page, title, provider             string
	searchProvider, searchQuery       string
	selected                          int
	items                             []MediaItem
	entries                           []providerCollection
	infos                             []providerInfo
	input                             textinput.Model
	filter, note                      string
	id                                uint64
	cancel                            context.CancelFunc
	results                           <-chan providerSearchBatch
	pending, searchTotal              int
	groups                            map[string][]MediaItem
	spinner                           spinner.Model
	setup                             *providerSetupModel
	request                           providerBrowseRequest
	next                              int
	back                              []providerBrowserState
}

type providerBrowserState struct {
	page, title, provider string
	selected              int
	items                 []MediaItem
	entries               []providerCollection
	request               providerBrowseRequest
	next                  int
}

type providerResultMsg struct {
	id          uint64
	infos       []providerInfo
	page        *providerPage
	batch       *providerSearchBatch
	done        bool
	selectFirst bool
	note        string
	err         error
}

func (t *tui) openProviders() tea.Cmd {
	browser := &t.providersUI
	t.closeAudio()
	browser.open, browser.page, browser.title = true, "home", "Providers"
	browser.selected, browser.note, browser.searchProvider, browser.searchQuery = 0, "", "", ""
	browser.searching = false
	browser.back, browser.request, browser.next = nil, providerBrowseRequest{}, 0
	browser.spinner = spinner.New(spinner.WithSpinner(spinner.Points))
	browser.spinner.Style = styleDim
	if browser.input.Prompt == "" {
		browser.input = textinput.New()
		browser.input.Prompt = "Search: "
		browser.input.SetVirtualCursor(false)
	}
	t.closeLyrics()
	t.closeRadio()
	t.closePodcasts()
	t.closeEqualizer()
	t.closeLibrary()
	t.help = false
	t.viz.focused = false
	return t.providerRequest(func(ctx context.Context) providerResultMsg {
		registry, err := providers()
		if err != nil {
			return providerResultMsg{err: err}
		}
		return providerResultMsg{infos: registry.list(ctx, false)}
	})
}

func (t *tui) closeProviders() {
	browser := &t.providersUI
	if browser.cancel != nil {
		browser.cancel()
	}
	browser.id++
	browser.open, browser.loading, browser.editing, browser.searching = false, false, false, false
	browser.searchProvider = ""
	browser.results, browser.setup = nil, nil
}

func (t *tui) providerRequest(work func(context.Context) providerResultMsg) tea.Cmd {
	browser := &t.providersUI
	if browser.cancel != nil {
		browser.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	browser.cancel = cancel
	browser.id++
	browser.loading, browser.note = true, ""
	browser.results, browser.pending, browser.searchTotal = nil, 0, 0
	id := browser.id
	request := func() tea.Msg {
		defer cancel()
		result := work(ctx)
		result.id = id
		return result
	}
	return tea.Batch(browser.spinner.Tick, request)
}

func (t *tui) providerResult(message providerResultMsg) tea.Cmd {
	browser := &t.providersUI
	if message.id != browser.id || !browser.open {
		return nil
	}
	if message.err != nil {
		browser.loading = false
		browser.results = nil
		browser.note = message.err.Error()
		return nil
	}
	if message.infos != nil {
		browser.infos = message.infos
		browser.loading = false
	}
	if message.page != nil {
		browser.page, browser.title, browser.provider = "results", message.page.Title, message.page.Provider
		browser.items, browser.entries, browser.next = message.page.Items, message.page.Entries, message.page.Next
		if message.selectFirst {
			browser.selected = 0
		}
		browser.loading = false
	}
	if message.batch != nil {
		browser.pending = max(0, browser.pending-1)
		if message.batch.err != nil {
			failure := message.batch.provider + ": " + message.batch.err.Error()
			if browser.note == "" {
				browser.note = failure
			} else {
				browser.note += " · " + failure
			}
		} else {
			if browser.groups == nil {
				browser.groups = map[string][]MediaItem{}
			}
			browser.groups[message.batch.provider] = message.batch.items
			browser.items = browser.groupedItems()
		}
	}
	if message.done {
		browser.loading, browser.results = false, nil
		if len(browser.items)+len(browser.entries) == 0 && browser.note == "" {
			browser.note = "No results"
		}
		return nil
	}
	if message.note != "" {
		browser.note = message.note
	}
	browser.selected = min(browser.selected, max(0, len(browser.rows())-1))
	if browser.results != nil {
		return waitProviderResult(browser.id, browser.results)
	}
	return refreshStatus
}

func waitProviderResult(id uint64, results <-chan providerSearchBatch) tea.Cmd {
	return func() tea.Msg {
		result, open := <-results
		if !open {
			return providerResultMsg{id: id, done: true}
		}
		return providerResultMsg{id: id, batch: &result}
	}
}

func (t *tui) providerSearch(query, provider string) tea.Cmd {
	browser := &t.providersUI
	if browser.cancel != nil {
		browser.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	registry, err := providers()
	if err != nil {
		cancel()
		browser.note = err.Error()
		return nil
	}
	results, total, err := registry.searchStream(ctx, provider, query, 30)
	if err != nil {
		cancel()
		browser.note = err.Error()
		return nil
	}
	browser.back = append(browser.back, providerBrowserState{page: browser.page, title: browser.title, provider: browser.provider, selected: browser.selected, items: browser.items, entries: browser.entries, request: browser.request, next: browser.next})
	if len(browser.back) > 20 {
		browser.back = browser.back[len(browser.back)-20:]
	}
	browser.cancel = cancel
	browser.id++
	title := "Search all providers: " + query
	if provider != "all" {
		title = "Search " + browser.providerName(provider) + ": " + query
	}
	browser.page, browser.title, browser.provider = "results", title, provider
	browser.items, browser.entries, browser.selected, browser.note = nil, nil, 0, ""
	browser.groups = map[string][]MediaItem{}
	browser.request, browser.next = providerBrowseRequest{}, 0
	browser.results, browser.pending, browser.searchTotal, browser.loading = results, total, total, true
	browser.searchQuery = query
	return tea.Batch(browser.spinner.Tick, waitProviderResult(browser.id, results))
}

func (browser *providerBrowser) groupedItems() []MediaItem {
	seen := map[string]bool{}
	var items []MediaItem
	appendGroup := func(provider string) {
		for _, item := range browser.groups[provider] {
			identity := providerSearchIdentity(item)
			if !seen[identity] {
				seen[identity] = true
				items = append(items, item)
			}
		}
	}
	for _, info := range browser.infos {
		appendGroup(info.Key)
	}
	return items
}

func (browser *providerBrowser) rowCount() int {
	if browser.page == "home" {
		return len(browser.infos) + 1
	}
	return len(browser.entries) + len(browser.items)
}

func (browser *providerBrowser) rowText(index int) string {
	if browser.page == "home" {
		if index == 0 {
			return "Search All Providers"
		}
		info := browser.infos[index-1]
		return fmt.Sprintf("%-18s %s", info.Name, strings.Join(info.Capabilities, " · "))
	}
	if index < len(browser.entries) {
		entry := browser.entries[index]
		count := ""
		if entry.Count > 0 {
			count = fmt.Sprintf("  %d", entry.Count)
		}
		return fmt.Sprintf("%-13s %s%s", entry.Kind, cleanProviderText(entry.Title), count)
	}
	item := browser.items[index-len(browser.entries)]
	return fmt.Sprintf("%-13s %s", item.Provider, item.display())
}

func (browser *providerBrowser) rows() []int {
	filter := strings.ToLower(browser.filter)
	rows := make([]int, 0, browser.rowCount())
	for i := range browser.rowCount() {
		if strings.Contains(strings.ToLower(browser.rowText(i)), filter) {
			rows = append(rows, i)
		}
	}
	return rows
}

func (browser *providerBrowser) providerName(key string) string {
	if key == "all" || key == "" {
		return "all providers"
	}
	for _, info := range browser.infos {
		if info.Key == key {
			return info.Name
		}
	}
	return key
}

func (browser *providerBrowser) searchScope(rows []int) string {
	if browser.page == "home" && len(rows) > 0 && browser.selected < len(rows) {
		index := rows[browser.selected]
		if index > 0 && index <= len(browser.infos) {
			return browser.infos[index-1].Key
		}
		return "all"
	}
	if supportedProvider(browser.provider) {
		return browser.provider
	}
	return "all"
}

func (browser *providerBrowser) loadingText(configured ...interfaceSettings) string {
	settings := currentInterfaceSettings()
	if len(configured) > 0 {
		settings = configured[0]
	}
	indicator := browser.spinner.View()
	cancel := settings.bindingHint("provider.cancel", "cancels")
	if browser.results == nil {
		return indicator + " Loading providers · " + cancel
	}
	name := browser.providerName(browser.provider)
	if browser.provider != "all" {
		return fmt.Sprintf("%s %s · searching for %q · %s", indicator, name, browser.searchQuery, cancel)
	}
	complete := max(0, browser.searchTotal-browser.pending)
	return fmt.Sprintf("%s All providers · searching for %q · %d of %d complete · %s", indicator, browser.searchQuery, complete, browser.searchTotal, cancel)
}

func (t *tui) beginProviderSearch(provider string) tea.Cmd {
	browser := &t.providersUI
	browser.editing, browser.searching = true, true
	browser.searchProvider = provider
	browser.input.Prompt = "Search " + browser.providerName(provider) + ": "
	browser.input.SetValue("")
	return browser.input.Focus()
}

func (t *tui) providerKey(message tea.KeyPressMsg) tea.Cmd {
	browser := &t.providersUI
	if browser.setup != nil {
		updated, command := browser.setup.Update(message)
		setup := updated.(providerSetupModel)
		browser.setup = &setup
		if setup.done {
			if setup.saved {
				browser.note = setup.provider + " settings saved"
				if registry, err := providers(); err == nil {
					browser.infos = registry.list(context.Background(), false)
				}
			} else {
				browser.note = "Provider setup canceled"
			}
			browser.setup = nil
		}
		return command
	}
	key := message.String()
	if browser.editing {
		key = t.presentation.mapKey("editor", key)
		switch key {
		case "esc", "ctrl+c":
			browser.editing, browser.searching = false, false
			browser.searchProvider = ""
			return nil
		case "enter":
			query := strings.TrimSpace(browser.input.Value())
			searching, provider := browser.searching, browser.searchProvider
			browser.editing, browser.searching = false, false
			browser.searchProvider = ""
			if query == "" {
				return nil
			}
			if searching {
				return t.providerSearch(query, provider)
			}
			return nil
		}
		var command tea.Cmd
		browser.input, command = browser.input.Update(message)
		if !browser.searching {
			browser.filter, browser.selected = browser.input.Value(), 0
		}
		return command
	}
	key = t.presentation.mapKey("providers", key)
	rows := browser.rows()
	switch key {
	case "ctrl+q":
		return tea.Quit
	case "f8":
		t.closeProviders()
	case "f7":
		t.closeProviders()
		return t.openLibrary()
	case "f6":
		t.closeProviders()
		return t.openLyrics()
	case "f5":
		t.closeProviders()
		return t.openRadio()
	case "f4":
		t.closeProviders()
		t.openEqualizer()
		return nil
	case "f3":
		t.closeProviders()
		return t.openPodcasts("")
	case "esc", "b":
		if browser.loading && browser.cancel != nil {
			browser.cancel()
			browser.id++
			browser.loading, browser.note, browser.results = false, "Cancelled", nil
			return nil
		}
		if len(browser.back) > 0 {
			state := browser.back[len(browser.back)-1]
			browser.back = browser.back[:len(browser.back)-1]
			browser.page, browser.title, browser.provider, browser.selected = state.page, state.title, state.provider, state.selected
			browser.items, browser.entries, browser.request, browser.next = state.items, state.entries, state.request, state.next
			browser.filter, browser.note = "", ""
			return nil
		}
		if browser.page != "home" {
			browser.page, browser.title, browser.provider = "home", "Providers", ""
			browser.items, browser.entries, browser.selected, browser.filter = nil, nil, 0, ""
			return nil
		}
		t.closeProviders()
	case "ctrl+c":
		if browser.cancel != nil {
			browser.cancel()
		}
		browser.id++
		browser.loading, browser.note, browser.results = false, "Cancelled", nil
	case "up", "k":
		browser.selected = max(0, browser.selected-1)
	case "down", "j":
		browser.selected = min(max(0, len(rows)-1), browser.selected+1)
	case "pgup":
		browser.selected = max(0, browser.selected-max(1, t.height-8))
	case "pgdown":
		browser.selected = min(max(0, len(rows)-1), browser.selected+max(1, t.height-8))
	case "home":
		browser.selected = 0
	case "end":
		browser.selected = max(0, len(rows)-1)
	case "/":
		if browser.page == "home" {
			return t.beginProviderSearch(browser.searchScope(rows))
		}
		browser.filter, browser.selected = "", 0
		browser.input.Prompt = "/ "
		browser.input.SetValue("")
		browser.editing, browser.searching = true, false
		browser.searchProvider = ""
		return browser.input.Focus()
	case "ctrl+f":
		return t.beginProviderSearch(browser.searchScope(rows))
	case "ctrl+r":
		if browser.page == "results" && browser.request.Kind != "" {
			return t.providerBrowse(browser.provider, browser.request, false)
		}
	case "[", "]":
		if browser.page == "results" && browser.request.Kind != "" {
			request := browser.request
			if key == "[" {
				request.Offset = max(0, request.Offset-max(1, request.Limit))
			} else if browser.next > 0 {
				request.Offset = browser.next
			} else {
				return nil
			}
			return t.providerBrowse(browser.provider, request, false)
		}
	case "s":
		document, secrets, err := loadProviderDocuments()
		if err != nil {
			browser.note = err.Error()
			return nil
		}
		provider := browser.provider
		if provider == "all" || !supportedProvider(provider) {
			provider = ""
		}
		setup := providerSetupModel{document: document, secrets: secrets, provider: provider, picker: provider == "", embedded: true, width: t.width, height: t.height, presentation: t.presentation}
		if !setup.picker {
			setup.openForm(provider)
		}
		browser.setup = &setup
	case "enter", "q", "n", "f", "B":
		if browser.loading || len(rows) == 0 {
			return nil
		}
		index := rows[browser.selected]
		if browser.page == "home" {
			if key != "enter" {
				return nil
			}
			if index == 0 {
				return t.beginProviderSearch("all")
			}
			info := browser.infos[index-1]
			return t.providerBrowse(info.Key, providerBrowseRequest{Kind: "music", Limit: 50}, true)
		}
		if index < len(browser.entries) {
			if key != "enter" {
				browser.note = "Choose a track for that action"
				return nil
			}
			entry := browser.entries[index]
			return t.providerBrowse(entry.Provider, providerBrowseRequest{Kind: entry.Kind, ID: entry.ID, Limit: 50}, true)
		}
		item := browser.items[index-len(browser.entries)]
		return t.providerItemAction(key, item)
	}
	return nil
}

func (t *tui) providerBrowse(provider string, request providerBrowseRequest, push bool) tea.Cmd {
	browser := &t.providersUI
	selectFirst := push || request.Offset != browser.request.Offset
	if push {
		browser.back = append(browser.back, providerBrowserState{page: browser.page, title: browser.title, provider: browser.provider, selected: browser.selected, items: browser.items, entries: browser.entries, request: browser.request, next: browser.next})
		if len(browser.back) > 20 {
			browser.back = browser.back[len(browser.back)-20:]
		}
	}
	browser.request = request
	return t.providerRequest(func(ctx context.Context) providerResultMsg {
		registry, err := providers()
		if err != nil {
			return providerResultMsg{err: err}
		}
		page, err := registry.browse(ctx, provider, request)
		return providerResultMsg{page: &page, selectFirst: selectFirst, err: err}
	})
}

func (t *tui) providerItemAction(key string, item MediaItem) tea.Cmd {
	browser := &t.providersUI
	id := browser.id
	return func() tea.Msg {
		var out string
		var err error
		if key == "enter" || key == "q" || key == "n" {
			if unavailable := providerPlaybackAvailable(item); unavailable != nil {
				return providerResultMsg{id: id, err: unavailable}
			}
		}
		switch key {
		case "enter":
			out, err = playMediaItems([]MediaItem{item})
		case "q":
			out, err = sendItems("queue-append", []MediaItem{item})
		case "n":
			out, err = sendItems("queue-next", []MediaItem{item})
		case "f", "B":
			if daemonErr := ensureDaemon(); daemonErr != nil {
				err = daemonErr
				break
			}
			data, marshalErr := json.Marshal(item)
			if marshalErr != nil {
				err = marshalErr
				break
			}
			action := "favorite"
			if key == "B" {
				action = "bookmark"
			}
			out, err = ask(action + " " + string(data))
		}
		return providerResultMsg{id: id, note: out, err: err}
	}
}

func (t *tui) providerView() tea.View {
	browser := &t.providersUI
	if browser.setup != nil {
		return browser.setup.View()
	}
	height, width := max(1, t.height), max(1, t.width)
	lines := make([]string, height)
	fit := func(value string) string { return ansi.Truncate(value, width, "") }
	lines[0] = fit(styleHeading.Render("Providers  /  " + browser.title))
	layout := t.contentLayout(height, browser.editing, 2)
	rows, room := browser.rows(), layout.room
	first := max(0, min(browser.selected-room/2, len(rows)-room))
	for row := 0; row < room && first+row < len(rows); row++ {
		index := first + row
		text := browser.rowText(rows[index])
		lines[row+2] = renderBrowserRow(text, width, index == browser.selected)
	}
	if room > 0 && len(rows) == 0 && !browser.loading {
		lines[2] = styleDim.Render("  No results. " + t.presentation.bindingLabel("provider.global-search") + " searches every provider.")
	}
	if layout.note >= 0 {
		note := browser.note
		if browser.loading {
			note = browser.loadingText(t.presentation)
		} else if note == "" {
			note = fmt.Sprintf("%d items", len(rows))
		}
		if t.presentation.Panels["metadata"] && len(rows) > 0 {
			note += " · selected: " + browser.rowText(rows[min(browser.selected, len(rows)-1)])
		}
		lines[layout.note] = fit(styleDim.Render(note))
		if layout.showHelp {
			lines[layout.firstHint] = fit(styleDim.Render(strings.Join([]string{t.presentation.bindingHint("browser.select", "open/play"), t.presentation.bindingHint("provider.queue", "queue"), t.presentation.bindingHint("provider.play-next", "play next"), t.presentation.bindingHint("browser.favorite", "favorite"), t.presentation.bindingHint("browser.bookmark", "bookmark"), t.presentation.bindingHint("provider.global-search", "search "+browser.providerName(browser.searchScope(rows)))}, " · ")))
			filterHint := t.presentation.bindingHint("browser.search", "filter")
			if browser.page == "home" {
				filterHint = t.presentation.bindingHint("browser.search", "search selected")
			}
			lines[layout.secondHint] = fit(styleDim.Render(strings.Join([]string{filterHint, t.presentation.bindingPairHint("provider.previous-page", "provider.next-page", "page"), t.presentation.bindingHint("browser.refresh", "refresh"), t.presentation.bindingHint("provider.setup", "setup"), t.presentation.bindingHint("browser.back", "back"), t.presentation.bindingHint("global.providers", "prompt")}, " · ")))
		}
		if browser.editing && layout.input >= 0 {
			browser.input.SetWidth(max(1, width-len(browser.input.Prompt)-1))
			lines[layout.input] = fit(browser.input.View())
		}
		if layout.status >= 0 {
			lines[layout.status] = t.statusBar()
		}
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	if browser.editing && layout.input >= 0 {
		if cursor := browser.input.Cursor(); cursor != nil {
			cursor.Y += layout.input
			view.Cursor = cursor
		}
	}
	return view
}
