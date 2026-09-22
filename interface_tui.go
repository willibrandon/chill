package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

type appearanceBrowser struct {
	open       bool
	selected   int
	settings   interfaceSettings
	original   interfaceSettings
	themes     []interfaceTheme
	note       string
	editing    string
	input      textinput.Model
	visualizer struct {
		enabled, focused, fullscreen bool
	}
}

type keyOverlay struct {
	open      bool
	searching bool
	selected  int
	query     textinput.Model
}

type appearanceRow struct {
	id, label, value string
}

type interfacePanelMsg struct {
	library *podcastLibrary
}

type contentScreenLayout struct {
	room                        int
	note, firstHint, secondHint int
	input, status               int
	showHelp                    bool
}

func loadInterfacePanelState() tea.Msg {
	library, _ := fetchPodcastLibrary()
	return interfacePanelMsg{library: library}
}

func (t *tui) refreshInterfacePanel() tea.Cmd {
	if t.panelLoading {
		return nil
	}
	t.panelLoading = true
	return loadInterfacePanelState
}

func (t *tui) contentLayout(height int, editing bool, helpRows int) contentScreenLayout {
	layout := contentScreenLayout{note: -1, firstHint: -1, secondHint: -1, input: -1, status: -1}
	bottom := height
	if t.presentation.ShowStatus && bottom > 0 {
		bottom--
		layout.status = bottom
	}
	layout.showHelp = t.presentation.ShowHelp && interfaceLayoutTier(t.width, height, true, t.presentation.Simplified) != "minimal"
	hintRows := 0
	if layout.showHelp {
		hintRows = helpRows
	}
	if editing && hintRows == 0 {
		hintRows = 1
	}
	hintRows = min(hintRows, max(0, bottom-2))
	if layout.showHelp && hintRows < helpRows {
		layout.showHelp = false
		if !editing {
			hintRows = 0
		}
	}
	bottom -= hintRows
	if layout.showHelp && hintRows > 0 {
		layout.firstHint = bottom
		if hintRows > 1 {
			layout.secondHint = bottom + 1
		}
	}
	if editing && hintRows > 0 {
		layout.input = bottom + hintRows - 1
	}
	if bottom > 2 {
		bottom--
		layout.note = bottom
	}
	layout.room = max(0, bottom-2)
	return layout
}

func (t *tui) interfaceBanner() string {
	if !t.presentation.ShowHelp || t.presentation.Simplified {
		return "chill  type a station, path, or command"
	}
	if interfaceLayoutTier(t.width, t.height, false, t.presentation.Simplified) == "full" || t.width == 0 {
		hints := []string{
			t.presentation.bindingHint("global.visualizer", "visualizer"), t.presentation.bindingHint("global.podcasts", "podcasts"),
			t.presentation.bindingHint("global.equalizer", "equalizer"), t.presentation.bindingHint("global.radio", "radio"),
			t.presentation.bindingHint("global.lyrics", "lyrics"), t.presentation.bindingHint("global.library", "library"),
			t.presentation.bindingHint("global.providers", "providers"), t.presentation.bindingHint("global.audio", "audio"),
			t.presentation.bindingHint("global.interface", "interface"), t.presentation.bindingHint("global.help", "help"),
		}
		return "chill  type a station, path, or command · " + strings.Join(hints, " · ")
	}
	return "chill  type a station, path, or command · " + t.presentation.bindingHint("global.help", "help") + " · " +
		t.presentation.bindingHint("global.interface", "interface") + " · " + t.presentation.bindingHint("global.keys", "keys")
}

func (t *tui) refreshInterfaceBanner() {
	if len(t.lines) > 0 && strings.HasPrefix(ansi.Strip(t.lines[0]), "chill  type a station") {
		t.lines[0] = styleDim.Render(t.interfaceBanner())
	}
}

func configureInterfaceInput(input *textinput.Model) {
	styles := input.Styles()
	styles.Focused.Text = styleInput
	styles.Focused.Suggestion = styleDim
	styles.Cursor.Color = stylePrompt.GetForeground()
	styles.Cursor.Shape = tea.CursorBlock
	styles.Cursor.Blink = false
	input.SetStyles(styles)
}

func (t *tui) openAppearance() tea.Cmd {
	if t.appearance.open {
		return t.closeAppearance(false)
	}
	t.appearance.open = true
	persisted, err := loadInterfaceSettings()
	if err != nil {
		persisted = defaultInterfaceSettings()
	}
	t.appearance.original = persisted
	t.appearance.settings = persisted
	t.appearance.settings.Panels = cloneBoolMap(persisted.Panels)
	t.appearance.settings.Bindings = cloneBindings(persisted.Bindings)
	t.appearance.visualizer.enabled = t.viz.enabled
	t.appearance.visualizer.focused = t.viz.focused
	t.appearance.visualizer.fullscreen = t.viz.fullscreen
	t.appearance.themes, _ = availableInterfaceThemes()
	t.appearance.note = "Changes preview immediately · " + t.presentation.bindingHint("interface.save", "saves") + " · " + t.presentation.bindingHint("interface.cancel", "cancels")
	if note := interfaceSessionOverrideNote(); note != "" {
		t.appearance.note = note + " · changes still save for later sessions"
	}
	t.appearance.editing = ""
	if t.appearance.input.Prompt == "" {
		t.appearance.input = textinput.New()
		t.appearance.input.Prompt = "> "
		t.appearance.input.SetVirtualCursor(false)
	}
	configureInterfaceInput(&t.appearance.input)
	return nil
}

func (t *tui) closeAppearance(save bool) tea.Cmd {
	if !t.appearance.open {
		return nil
	}
	if save {
		if err := saveInterfaceSettings(t.appearance.settings); err != nil {
			t.appearance.note = "error: " + err.Error()
			return nil
		}
		t.presentation, _, _ = activateInterfaceSettings(t.appearance.settings)
	} else {
		t.presentation, _, _ = activateInterfaceSettings(t.appearance.original)
	}
	t.enforcePresentationMode()
	if !t.presentation.Simplified && !t.presentation.LowPower {
		t.viz.enabled = t.appearance.visualizer.enabled
		t.viz.focused = t.appearance.visualizer.focused
		t.viz.fullscreen = t.appearance.visualizer.fullscreen
	}
	t.appearance.open = false
	t.appearance.editing = ""
	t.configureInputs()
	t.refreshInterfaceBanner()
	return t.interfaceColorProfileCommand()
}

func (t *tui) configureInputs() {
	configureInterfaceInput(&t.input)
	if t.libraryUI.input.Prompt != "" {
		configureInterfaceInput(&t.libraryUI.input)
	}
	if t.podcasts.input.Prompt != "" {
		configureInterfaceInput(&t.podcasts.input)
	}
	if t.radio.input.Prompt != "" {
		configureInterfaceInput(&t.radio.input)
	}
	if t.providersUI.input.Prompt != "" {
		configureInterfaceInput(&t.providersUI.input)
	}
}

func (t *tui) appearanceRows() []appearanceRow {
	s := t.appearance.settings
	rows := []appearanceRow{
		{"theme", "Theme", s.Theme},
		{"colors", "Color capability", s.ColorMode},
		{"characters", "Characters", s.CharacterMode},
		{"simplified", "Simplified", interfaceOnOff(s.Simplified)},
		{"low-power", "Low power", interfaceOnOff(s.LowPower)},
		{"visualizer", "Visualizer height", strconv.Itoa(s.VisualizerHeight)},
		{"status", "Status bar", interfaceOnOff(s.ShowStatus)},
		{"help", "Help hints", interfaceOnOff(s.ShowHelp)},
		{"status-fields", "Status content", strings.Join(s.StatusFields, ",")},
		{"seek", "Seek step", fmt.Sprintf("%ds", s.SeekStep)},
		{"seek-large", "Large seek step", fmt.Sprintf("%ds", s.SeekLargeStep)},
		{"directory", "Initial browser directory", s.InitialDirectory},
		{"screen", "Default screen", s.DefaultScreen},
	}
	for _, panel := range interfacePanelNames() {
		rows = append(rows, appearanceRow{"panel:" + panel, "Panel: " + panel, interfaceOnOff(s.Panels[panel])})
	}
	return rows
}

func interfaceOnOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func cycleString(current string, values []string, delta int) string {
	index := 0
	for i, value := range values {
		if strings.EqualFold(value, current) {
			index = i
			break
		}
	}
	index = (index + delta + len(values)) % len(values)
	return values[index]
}

func (t *tui) adjustAppearance(delta int) tea.Cmd {
	rows := t.appearanceRows()
	if len(rows) == 0 {
		return nil
	}
	row := rows[min(t.appearance.selected, len(rows)-1)]
	s := &t.appearance.settings
	switch row.id {
	case "theme":
		names := make([]string, len(t.appearance.themes))
		for i, theme := range t.appearance.themes {
			names[i] = theme.Name
		}
		s.Theme = cycleString(s.Theme, names, delta)
	case "colors":
		s.ColorMode = cycleString(s.ColorMode, []string{"auto", "truecolor", "ansi256", "ansi16", "none"}, delta)
	case "characters":
		s.CharacterMode = cycleString(s.CharacterMode, []string{"auto", "unicode", "ascii"}, delta)
	case "simplified":
		s.Simplified = !s.Simplified
	case "low-power":
		s.LowPower = !s.LowPower
	case "visualizer":
		s.VisualizerHeight = max(0, min(20, s.VisualizerHeight+delta))
	case "status":
		s.ShowStatus = !s.ShowStatus
	case "help":
		s.ShowHelp = !s.ShowHelp
	case "seek":
		s.SeekStep = max(1, min(3600, s.SeekStep+delta))
	case "seek-large":
		s.SeekLargeStep = max(1, min(3600, s.SeekLargeStep+delta*5))
	case "screen":
		s.DefaultScreen = cycleString(s.DefaultScreen, []string{"prompt", "visualizer", "podcasts", "equalizer", "radio", "lyrics", "library", "providers", "audio", "interface"}, delta)
	default:
		if panel, ok := strings.CutPrefix(row.id, "panel:"); ok {
			s.Panels[panel] = !s.Panels[panel]
		}
	}
	effective, _, err := activateInterfaceSettings(*s)
	if err != nil {
		t.appearance.note = "error: " + err.Error()
		return nil
	}
	t.presentation = effective
	t.enforcePresentationMode()
	t.configureInputs()
	t.refreshInterfaceBanner()
	return t.interfaceColorProfileCommand()
}

func (t *tui) appearanceKey(msg tea.KeyPressMsg) tea.Cmd {
	browser := &t.appearance
	if browser.editing != "" {
		switch t.presentation.mapKey("editor", msg.String()) {
		case "esc":
			browser.editing = ""
			browser.input.Blur()
			return nil
		case "enter":
			value := strings.TrimSpace(browser.input.Value())
			if browser.editing == "directory" {
				if value == "" {
					browser.note = "directory cannot be empty"
					return nil
				}
				browser.settings.InitialDirectory = value
			} else {
				fields := []string{}
				for field := range strings.SplitSeq(value, ",") {
					fields = append(fields, strings.TrimSpace(field))
				}
				candidate := browser.settings
				candidate.StatusFields = fields
				if err := normalizeInterfaceSettings(&candidate); err != nil {
					browser.note = "error: " + err.Error()
					return nil
				}
				browser.settings.StatusFields = fields
			}
			browser.editing = ""
			browser.input.Blur()
			browser.note = "Preview updated · " + t.presentation.bindingHint("interface.save", "saves") + " · " + t.presentation.bindingHint("interface.cancel", "cancels")
			t.presentation, _, _ = activateInterfaceSettings(browser.settings)
			t.enforcePresentationMode()
			t.configureInputs()
			return t.interfaceColorProfileCommand()
		}
		var cmd tea.Cmd
		browser.input, cmd = browser.input.Update(msg)
		return cmd
	}
	rows := t.appearanceRows()
	switch t.presentation.mapKey("interface", msg.String()) {
	case "esc", "f10":
		return t.closeAppearance(false)
	case "up", "k":
		browser.selected = max(0, browser.selected-1)
	case "down", "j":
		browser.selected = min(len(rows)-1, browser.selected+1)
	case "left", "h":
		return t.adjustAppearance(-1)
	case "right", "l", "enter", "space":
		row := rows[browser.selected]
		if row.id == "directory" || row.id == "status-fields" {
			browser.editing = row.id
			browser.input.SetValue(row.value)
			browser.input.CursorEnd()
			return browser.input.Focus()
		}
		return t.adjustAppearance(1)
	case "s":
		return t.closeAppearance(true)
	case "r":
		browser.settings = defaultInterfaceSettings()
		t.presentation, _, _ = activateInterfaceSettings(browser.settings)
		t.enforcePresentationMode()
		t.configureInputs()
		browser.note = "Defaults previewed · " + t.presentation.bindingHint("interface.save", "saves") + " · " + t.presentation.bindingHint("interface.cancel", "cancels")
		t.refreshInterfaceBanner()
		return t.interfaceColorProfileCommand()
	}
	return nil
}

func (t *tui) enforcePresentationMode() {
	if !t.presentation.Simplified && !t.presentation.LowPower {
		return
	}
	t.viz.enabled, t.viz.focused, t.viz.fullscreen = false, false, false
	t.closeVisualizer()
}

func (t *tui) interfaceColorProfileCommand() tea.Cmd {
	profile := t.terminalProfile
	if t.presentation.ColorMode != "auto" || profile == colorprofile.Unknown {
		switch resolvedCLIColorMode(t.presentation.ColorMode) {
		case "truecolor":
			profile = colorprofile.TrueColor
		case "ansi256":
			profile = colorprofile.ANSI256
		case "ansi16":
			profile = colorprofile.ANSI
		default:
			profile = colorprofile.ASCII
		}
	}
	return func() tea.Msg { return tea.ColorProfileMsg{Profile: profile} }
}

func (t *tui) appearanceView() tea.View {
	view := tea.NewView("")
	view.AltScreen = true
	if t.width == 0 || t.height == 0 {
		return view
	}
	rows := t.appearanceRows()
	lines := make([]string, t.height)
	lines[0] = ansi.Truncate(styleHeading.Render("interface")+styleDim.Render("  live preview"), t.width, "")
	room := max(0, t.height-5)
	first := max(0, min(t.appearance.selected-room/2, len(rows)-room))
	for line := 0; line < room && first+line < len(rows); line++ {
		index := first + line
		row := rows[index]
		prefix, style := "  ", styleInput
		if index == t.appearance.selected {
			prefix, style = "❯ ", styleSelected
		}
		labelWidth := min(30, max(16, t.width/3))
		content := prefix + column(row.label, labelWidth) + row.value
		lines[line+2] = style.Render(ansi.Truncate(content, t.width, "…"))
	}
	if t.height >= 4 {
		lines[t.height-3] = styleDim.Render(ansi.Truncate(t.appearance.note, t.width, "…"))
		if t.appearance.editing != "" {
			t.appearance.input.SetWidth(max(1, t.width-3))
			lines[t.height-2] = t.appearance.input.View()
		} else if t.presentation.ShowHelp {
			hints := []string{
				t.presentation.bindingHint("interface.up", "choose"), t.presentation.bindingHint("interface.previous", "change"),
				t.presentation.bindingHint("interface.next", "change/edit"), t.presentation.bindingHint("interface.save", "save"),
				t.presentation.bindingHint("interface.reset", "defaults"), t.presentation.bindingHint("interface.cancel", "cancel"),
				t.presentation.bindingHint("global.keys", "keys"),
			}
			lines[t.height-2] = styleDim.Render(ansi.Truncate(strings.Join(hints, " · "), t.width, ""))
		}
		lines[t.height-1] = t.statusBar()
	}
	view.SetContent(strings.Join(lines, "\n"))
	if t.appearance.editing != "" {
		if cursor := t.appearance.input.Cursor(); cursor != nil {
			cursor.Y += t.height - 2
			view.Cursor = cursor
		}
	}
	return view
}

func (t *tui) activeKeyScope() string {
	switch {
	case t.sel.active:
		return "selection"
	case t.appearance.editing != "" || t.libraryUI.editing || t.providersUI.editing || t.radio.editing || t.podcasts.editing:
		return "editor"
	case t.providersUI.setup != nil && t.providersUI.setup.picker:
		return "setup-picker"
	case t.providersUI.setup != nil:
		return "setup-form"
	case t.radio.consent:
		return "radio-consent"
	case t.appearance.open:
		return "interface"
	case t.help:
		return "help"
	case t.eq.open:
		return "equalizer"
	case t.viz.focused:
		return "visualizer"
	case t.libraryUI.open:
		return "library"
	case t.providersUI.open:
		return "providers"
	case t.radio.open:
		return "radio"
	case t.podcasts.open:
		return "podcasts"
	case t.audioUI.open:
		return "audio"
	case t.lyrics.open:
		return "lyrics"
	default:
		return "prompt"
	}
}

func (t *tui) toggleKeyOverlay() tea.Cmd {
	overlay := &t.keyOverlay
	overlay.open = !overlay.open
	overlay.selected = 0
	overlay.searching = false
	if overlay.query.Prompt == "" {
		overlay.query = textinput.New()
		overlay.query.Prompt = "/ "
		overlay.query.SetVirtualCursor(false)
	}
	configureInterfaceInput(&overlay.query)
	if !overlay.open {
		overlay.query.Blur()
	}
	return nil
}

func (t *tui) keyOverlayKey(msg tea.KeyPressMsg) tea.Cmd {
	overlay := &t.keyOverlay
	if overlay.searching {
		switch msg.String() {
		case "esc":
			overlay.searching = false
			overlay.query.Blur()
			return nil
		case "enter":
			overlay.searching = false
			overlay.query.Blur()
			return nil
		}
		var cmd tea.Cmd
		overlay.query, cmd = overlay.query.Update(msg)
		overlay.selected = 0
		return cmd
	}
	rows := interfaceKeyRows(t.presentation, t.activeKeyScope(), overlay.query.Value())
	switch t.presentation.mapKey("overlay", msg.String()) {
	case "esc":
		overlay.open = false
	case "/":
		overlay.searching = true
		return overlay.query.Focus()
	case "up":
		overlay.selected = max(0, overlay.selected-1)
	case "down":
		overlay.selected = min(max(0, len(rows)-1), overlay.selected+1)
	case "pgup":
		overlay.selected = max(0, overlay.selected-max(1, t.height-7))
	case "pgdown":
		overlay.selected = min(max(0, len(rows)-1), overlay.selected+max(1, t.height-7))
	}
	return nil
}

func (t *tui) keyOverlayView() tea.View {
	view := tea.NewView("")
	view.AltScreen = true
	rows := interfaceKeyRows(t.presentation, t.activeKeyScope(), t.keyOverlay.query.Value())
	lines := make([]string, t.height)
	heading := "keybindings · " + t.activeKeyScope()
	if query := t.keyOverlay.query.Value(); query != "" {
		heading += " · “" + query + "”"
	}
	lines[0] = styleHeading.Render(ansi.Truncate(heading, t.width, "…"))
	room := max(0, t.height-4)
	first := max(0, min(t.keyOverlay.selected-room/2, len(rows)-room))
	for line := 0; line < room && first+line < len(rows); line++ {
		index := first + line
		style, prefix := styleInput, "  "
		if index == t.keyOverlay.selected {
			style, prefix = styleSelected, "❯ "
		}
		lines[line+2] = style.Render(ansi.Truncate(prefix+rows[index], t.width, "…"))
	}
	if len(rows) == 0 && room > 0 {
		lines[2] = styleDim.Render("  no matching bindings")
	}
	if t.height >= 2 {
		if t.keyOverlay.searching {
			t.keyOverlay.query.SetWidth(max(1, t.width-3))
			lines[t.height-1] = t.keyOverlay.query.View()
		} else {
			hints := []string{
				t.presentation.bindingHint("overlay.search", "search"), t.presentation.bindingHint("overlay.down", "scroll"),
				t.presentation.bindingHint("overlay.close", "close"), "configure with chill keys set",
			}
			lines[t.height-1] = styleDim.Render(ansi.Truncate(strings.Join(hints, " · "), t.width, ""))
		}
	}
	view.SetContent(strings.Join(lines, "\n"))
	if t.keyOverlay.searching {
		if cursor := t.keyOverlay.query.Cursor(); cursor != nil {
			cursor.Y += t.height - 1
			view.Cursor = cursor
		}
	}
	return view
}

func (t *tui) handleGlobalKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	action, ok := t.presentation.actionFor("global", msg.String())
	if !ok {
		if strings.HasPrefix(t.presentation.mapKey("global", msg.String()), "unbound:") {
			scope := t.activeKeyScope()
			if t.keyOverlay.open {
				scope = "overlay"
			}
			if _, scoped := t.presentation.actionFor(scope, msg.String()); scoped {
				return nil, false
			}
			return nil, true
		}
		return nil, false
	}
	if action.id == "global.keys" {
		return t.toggleKeyOverlay(), true
	}
	if action.id == "global.quit" {
		return tea.Quit, true
	}
	if t.keyOverlay.open {
		return nil, true
	}
	switch action.id {
	case "global.help":
		opening := !t.help
		if opening {
			t.closeContentScreens()
			t.help = true
			t.helpView.SetContent(helpBody(t.presentation))
			t.helpView.GotoTop()
		} else {
			t.help = false
		}
		return nil, true
	case "global.interface":
		return t.openAppearance(), true
	}
	if t.appearance.open {
		return nil, true
	}
	switch action.id {
	case "global.visualizer":
		wasFocused := t.viz.focused
		t.closeContentScreens()
		if wasFocused {
			return nil, true
		}
		t.visualizerCommand("")
		return nil, true
	case "global.podcasts":
		if t.podcasts.open {
			t.closePodcasts()
			return nil, true
		}
		t.closeContentScreens()
		return t.openPodcasts(""), true
	case "global.equalizer":
		if t.eq.open {
			t.closeEqualizer()
		} else {
			t.closeContentScreens()
			t.openEqualizer()
		}
		return nil, true
	case "global.radio":
		if t.radio.open {
			t.closeRadio()
			return nil, true
		}
		t.closeContentScreens()
		return t.openRadio(), true
	case "global.lyrics":
		if t.lyrics.open {
			t.closeLyrics()
			return nil, true
		}
		t.closeContentScreens()
		return t.openLyrics(), true
	case "global.library":
		if t.libraryUI.open {
			t.closeLibrary()
			return nil, true
		}
		t.closeContentScreens()
		return t.openLibrary(), true
	case "global.providers":
		if t.providersUI.open {
			t.closeProviders()
			return nil, true
		}
		t.closeContentScreens()
		return t.openProviders(), true
	case "global.audio":
		if t.audioUI.open {
			t.closeAudio()
			return nil, true
		}
		t.closeContentScreens()
		return t.openAudio(), true
	}
	return nil, false
}

func (t *tui) closeContentScreens() {
	t.closeLyrics()
	t.closeRadio()
	t.closePodcasts()
	t.closeEqualizer()
	t.closeLibrary()
	t.closeProviders()
	t.closeAudio()
	t.help = false
	t.viz.focused, t.viz.fullscreen = false, false
}

func (t *tui) panelHeight() int {
	if interfaceLayoutTier(t.width, t.height, false, t.presentation.Simplified) != "full" {
		return 0
	}
	count := 0
	for _, panel := range interfacePanelNames() {
		if t.presentation.Panels[panel] {
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return (count + 2) / 3
}

func (t *tui) panelView() string {
	if t.panelHeight() == 0 {
		return ""
	}
	values := map[string]string{
		"source": "idle", "queue": "0 waiting", "equalizer": "Flat", "audio": "default", "downloads": "0 ready", "network": "daemon off", "metadata": "nothing selected",
	}
	if status := t.status; status != nil {
		if status.Item != nil {
			values["source"] = string(status.Item.Kind)
			values["metadata"] = status.Item.display()
		} else if status.Station != "" {
			values["source"] = "radio · " + status.Station
			if status.NowPlaying != nil && status.NowPlaying.Raw != "" {
				values["metadata"] = status.NowPlaying.Raw
			} else {
				values["metadata"] = status.Desc
			}
		}
		values["queue"] = fmt.Sprintf("%d waiting", status.Queued)
		values["equalizer"] = status.EQPreset
		values["audio"] = status.Audio.ActiveDevice + " · " + status.Audio.Format
		values["network"] = interfaceNetworkStatus(status)
	}
	if t.panelLibrary != nil {
		values["downloads"] = interfaceDownloadStatus(t.panelLibrary)
	}
	type panelCard struct {
		name  string
		value string
	}
	var cards []panelCard
	for _, panel := range interfacePanelNames() {
		if !t.presentation.Panels[panel] {
			continue
		}
		cards = append(cards, panelCard{name: panel, value: values[panel]})
	}
	var lines []string
	for len(cards) > 0 {
		take := min(3, len(cards))
		row := cards[:take]
		available := max(3*take, t.width-(take-1))
		baseWidth, extra := available/take, available%take
		rendered := make([]string, 0, take)
		for index, card := range row {
			cardWidth := baseWidth
			if index < extra {
				cardWidth++
			}
			contentWidth := max(1, cardWidth-2)
			content := ansi.Truncate(strings.ToUpper(card.name)+" "+card.value, contentWidth, "…")
			content += strings.Repeat(" ", max(0, contentWidth-lipgloss.Width(content)))
			rendered = append(rendered, styleBorder.Render("[")+styleDim.Render(content)+styleBorder.Render("]"))
		}
		lines = append(lines, strings.Join(rendered, " "))
		cards = cards[take:]
	}
	return strings.Join(lines, "\n")
}

func interfaceDownloadStatus(library *podcastLibrary) string {
	counts := map[string]int{}
	if library != nil {
		for _, download := range library.Downloads {
			counts[download.State]++
		}
	}
	parts := []string{fmt.Sprintf("%d ready", counts["ready"])}
	for _, state := range []string{"downloading", "queued", "retrying", "error"} {
		if counts[state] > 0 {
			label := state
			if state == "error" {
				label = "failed"
			}
			parts = append(parts, fmt.Sprintf("%d %s", counts[state], label))
		}
	}
	return strings.Join(parts, " · ")
}

func interfaceNetworkStatus(status *Status) string {
	if status == nil {
		return "daemon off"
	}
	if status.State == "reconnecting" {
		return fmt.Sprintf("retry %d", status.Retries)
	}
	if status.State == "failed" || status.Error != "" {
		return "error"
	}
	return "online"
}

func (t *tui) statusPollInterval() time.Duration {
	if t.presentation.LowPower {
		return 3 * time.Second
	}
	return time.Second
}

func (t *tui) interfaceStatusFacts() []string {
	if t.status == nil {
		return []string{"daemon off"}
	}
	s := t.status
	state := s.State
	if state == "" {
		state = "idle"
		if s.Playing {
			state = "playing"
		} else if s.Paused {
			state = "paused"
		}
	}
	title := s.Station
	if s.Item != nil {
		title = s.Item.display()
	} else if s.NowPlaying != nil && s.NowPlaying.Raw != "" {
		title = s.NowPlaying.Raw
	}
	values := map[string]string{
		"state": state, "position": clock(s.Position), "title": title, "queue": fmt.Sprintf("queued %d", s.Queued),
		"volume": fmt.Sprintf("vol %d", s.Volume), "equalizer": "eq " + s.EQPreset, "network": interfaceNetworkStatus(s), "audio": s.Audio.ActiveDevice,
		"speed": fmt.Sprintf("%.2fx", s.Speed), "sleep": s.Sleep, "storage-error": s.StorageError,
	}
	if s.Shuffle {
		values["shuffle"] = "shuffle"
	}
	if s.Repeat != "" && s.Repeat != "off" {
		values["repeat"] = "repeat " + s.Repeat
	}
	if s.Muted {
		values["muted"] = "muted"
	}
	if s.Duration > 0 {
		values["position"] += " / " + clock(s.Duration)
	}
	var facts []string
	for _, field := range t.presentation.StatusFields {
		value := values[field]
		if field == "queue" && s.Queued == 0 || field == "speed" && (s.Speed == 0 || s.Speed == 1) || field == "sleep" && value == "" || value == "" {
			continue
		}
		facts = append(facts, value)
	}
	return facts
}

func orderedInterfaceStatus(settings interfaceSettings, values map[string]string) string {
	parts := make([]string, 0, len(settings.StatusFields))
	for _, field := range settings.StatusFields {
		if value := strings.TrimSpace(values[field]); value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " · ")
}

func (t *tui) decoratedView(view tea.View) tea.View {
	return t.presentation.decorateView(view, t.width)
}

func foregroundPanelLines(settings interfaceSettings, width, height int, values map[string]string) []string {
	if interfaceLayoutTier(width, height, false, settings.Simplified) != "full" {
		return nil
	}
	cardWidth := max(20, (width-8)/3)
	var cards []string
	for _, panel := range interfacePanelNames() {
		if !settings.Panels[panel] {
			continue
		}
		value := values[panel]
		if value == "" {
			value = "not active"
		}
		content := ansi.Truncate(strings.ToUpper(panel)+" "+value, cardWidth, "…")
		content += strings.Repeat(" ", max(0, cardWidth-lipgloss.Width(content)))
		cards = append(cards, styleBorder.Render("[")+styleDim.Render(content)+styleBorder.Render("]"))
	}
	var lines []string
	for len(cards) > 0 {
		take := min(3, len(cards))
		lines = append(lines, strings.Join(cards[:take], " "))
		cards = cards[take:]
	}
	return lines
}
