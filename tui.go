// tui.go implements the fullscreen REPL: a transcript that fills the screen,
// and pinned under it a rule, the suggestions for what is being typed, the
// prompt, and a status bar.

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	maxPaletteRows  = 8    // suggestions visible at once
	minPaletteRoom  = 3    // rows that must be free before suggestions are shown
	minPaletteLines = 8    // terminal height below which suggestions are hidden
	maxTranscript   = 1000 // lines kept in the transcript

	banner = "chill  type a station or a command, tab completes.  help for more"
)

var (
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("#787C86"))
	stylePrompt   = lipgloss.NewStyle().Foreground(lipgloss.Color("#61AFEF"))
	styleInput    = lipgloss.NewStyle().Foreground(lipgloss.Color("#DCDFE4"))
	styleCommand  = lipgloss.NewStyle().Foreground(lipgloss.Color("#56B6C2"))
	styleStation  = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5C07B"))
	styleSelected = lipgloss.NewStyle().Foreground(lipgloss.Color("#98FFEE"))
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.Color("#E06C75"))
	styleHeading  = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5C07B"))
	styleBorder   = lipgloss.NewStyle().Foreground(lipgloss.Color("#808080"))
	styleTitle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))
	styleTrack    = lipgloss.NewStyle().Foreground(lipgloss.Color("#404040"))
	styleThumb    = lipgloss.NewStyle().Foreground(lipgloss.Color("#808080"))
	styleStatus   = lipgloss.NewStyle().Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("#FFFFFF"))
)

// statusMsg carries the daemon's playback state, nil when it isn't running.
type statusMsg struct {
	status *Status
	poll   bool // from the once-a-second poll, which schedules the next one
}

// resultMsg carries the outcome of a finished command.
type resultMsg struct {
	out string
	err error
}

// tui is the bubbletea model for the REPL.
type tui struct {
	width, height int

	lines    []string       // transcript, one entry per line
	viewport viewport.Model // scrolls the transcript
	input    textinput.Model

	suggestions []suggestion // offered for what is being typed
	selected    int          // highlighted suggestion
	navigated   bool         // the highlight was moved, so enter accepts it
	dismissed   bool         // esc closed the suggestions until the next edit

	history *history
	histPos int    // position in history while browsing, len(lines) when not
	draft   string // what was typed before browsing history

	status  *Status  // last known daemon state
	running bool     // a command is in flight
	pending []string // submitted while another command was running

	help     bool           // the help screen is showing
	helpView viewport.Model // scrolls the help screen
}

func newTUI() *tui {
	input := textinput.New()
	input.SetVirtualCursor(false)
	input.ShowSuggestions = true
	input.Focus()

	// the palette handles these, the input only draws the ghost text
	input.KeyMap.AcceptSuggestion = key.NewBinding(key.WithDisabled())
	input.KeyMap.NextSuggestion = key.NewBinding(key.WithDisabled())
	input.KeyMap.PrevSuggestion = key.NewBinding(key.WithDisabled())

	styles := input.Styles()
	styles.Focused.Text = styleInput
	styles.Focused.Suggestion = styleDim
	styles.Cursor.Color = lipgloss.Color("#61AFEF")
	styles.Cursor.Shape = tea.CursorBlock
	styles.Cursor.Blink = false
	input.SetStyles(styles)

	h := loadHistory()
	t := &tui{
		viewport: viewport.New(),
		helpView: viewport.New(),
		input:    input,
		history:  h,
		histPos:  len(h.lines),
	}
	t.viewport.SoftWrap = true
	t.viewport.MouseWheelDelta = 3
	t.helpView.SoftWrap = true
	t.helpView.MouseWheelDelta = 3
	t.clear()
	return t
}

func (t *tui) Init() tea.Cmd {
	return pollStatus
}

// pollStatus asks the daemon what it is doing, once a second.
func pollStatus() tea.Msg {
	s, _ := fetchStatus()
	return statusMsg{s, true}
}

// refreshStatus asks right away, after a command that may have changed it.
func refreshStatus() tea.Msg {
	s, _ := fetchStatus()
	return statusMsg{s, false}
}

// run executes a submitted line off the UI goroutine.
func run(line string) tea.Cmd {
	return func() tea.Msg {
		out, err := execute(line)
		return resultMsg{out, err}
	}
}

func (t *tui) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := t.update(msg)
	t.fit()
	return t, cmd
}

func (t *tui) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		t.width, t.height = msg.Width, msg.Height
		t.refreshSuggestions()
		return nil

	case statusMsg:
		t.status = msg.status
		if !msg.poll {
			return nil
		}
		return tea.Tick(time.Second, func(time.Time) tea.Msg { return pollStatus() })

	case resultMsg:
		t.running = false
		if msg.err != nil {
			t.print(styleError.Render("  error: ") + msg.err.Error())
		} else if msg.out != "" {
			for _, line := range strings.Split(msg.out, "\n") {
				t.print(styleDim.Render("  ┊ ") + line)
			}
		}
		if len(t.pending) > 0 {
			next := t.pending[0]
			t.pending = t.pending[1:]
			return tea.Batch(t.start(next), refreshStatus)
		}
		return refreshStatus

	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		if t.help {
			t.helpView, cmd = t.helpView.Update(msg)
		} else {
			t.viewport, cmd = t.viewport.Update(msg)
		}
		return cmd

	case tea.KeyPressMsg:
		if t.help {
			return t.helpKey(msg)
		}
		return t.promptKey(msg)
	}

	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)
	t.refreshSuggestions()
	return cmd
}

// promptKey handles a key press at the prompt.
func (t *tui) promptKey(msg tea.KeyPressMsg) tea.Cmd {
	open := t.paletteOpen()

	switch msg.String() {
	case "ctrl+q":
		return tea.Quit

	case "ctrl+c":
		// clears the line, never quits
		t.setInput("")
		return nil

	case "ctrl+l":
		t.clear()
		return nil

	case "f1":
		t.help = true
		t.helpView.SetContent(helpBody())
		t.helpView.GotoTop()
		return nil

	case "pgup":
		t.viewport.PageUp()
		return nil

	case "pgdown":
		t.viewport.PageDown()
		return nil

	case "esc":
		t.dismissed = true
		t.refreshSuggestions()
		return nil

	case "tab":
		if open {
			t.accept()
		}
		return nil

	case "right":
		// takes the ghost text when there is some, otherwise moves the cursor
		if open {
			t.accept()
			return nil
		}

	case "up":
		if open {
			t.selected = max(t.selected-1, 0)
			t.navigated = true
			t.showGhost()
			return nil
		}
		t.browseHistory(-1)
		return nil

	case "down":
		if open {
			t.selected = min(t.selected+1, len(t.suggestions)-1)
			t.navigated = true
			t.showGhost()
			return nil
		}
		t.browseHistory(1)
		return nil

	case "ctrl+p":
		t.browseHistory(-1)
		return nil

	case "ctrl+n":
		t.browseHistory(1)
		return nil

	case "enter":
		// enter only takes a suggestion that was picked on purpose
		if open && t.navigated {
			t.accept()
			return nil
		}
		return t.submit()
	}

	before := t.input.Value()
	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)
	if t.input.Value() != before {
		t.dismissed = false
		t.navigated = false
		t.selected = 0
		t.histPos = len(t.history.lines)
	}
	t.refreshSuggestions()
	return cmd
}

// helpKey handles a key press on the help screen, which swallows typing.
func (t *tui) helpKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+q":
		return tea.Quit
	case "f1", "esc":
		t.help = false
	case "pgup":
		t.helpView.PageUp()
	case "pgdown":
		t.helpView.PageDown()
	case "up":
		t.helpView.ScrollUp(1)
	case "down":
		t.helpView.ScrollDown(1)
	}
	return nil
}

// submit runs the line in the prompt.
func (t *tui) submit() tea.Cmd {
	line := strings.TrimSpace(t.input.Value())
	t.setInput("")
	if line == "" {
		return nil
	}

	t.history.add(line)
	t.histPos = len(t.history.lines)

	switch strings.ToLower(line) {
	case "quit", "exit", "q":
		return tea.Quit
	case "clear":
		t.clear()
		return nil
	}

	if t.running {
		t.pending = append(t.pending, line)
		return nil
	}
	return t.start(line)
}

// start echoes a line into the transcript and runs it.
func (t *tui) start(line string) tea.Cmd {
	t.running = true
	t.print(t.promptLabel() + highlight(line))
	return run(line)
}

// highlight colors a submitted line for the transcript.
func highlight(line string) string {
	words := strings.Fields(line)
	for i, w := range words {
		switch {
		case findStation(w) != nil && (i == 0 || i == 1 && strings.EqualFold(words[0], "play")):
			words[i] = styleStation.Render(w)
		case i == 0 && isCommand(w):
			words[i] = styleCommand.Render(w)
		default:
			words[i] = styleInput.Render(w)
		}
	}
	return strings.Join(words, " ")
}

// isCommand reports whether word is a REPL command.
func isCommand(word string) bool {
	for _, c := range replCommands {
		if strings.EqualFold(c.name, word) {
			return true
		}
	}
	return false
}

// print adds a line to the transcript and brings it into view.
func (t *tui) print(line string) {
	t.lines = append(t.lines, line)
	if len(t.lines) > maxTranscript {
		t.lines = t.lines[len(t.lines)-maxTranscript:]
	}
	t.viewport.SetContentLines(t.lines)
	t.viewport.GotoBottom()
}

// clear empties the transcript, leaving the banner.
func (t *tui) clear() {
	t.lines = nil
	t.print(styleDim.Render(banner))
}

// setInput replaces what is in the prompt and puts the cursor at the end.
func (t *tui) setInput(s string) {
	t.input.SetValue(s)
	t.input.CursorEnd()
	t.dismissed = false
	t.navigated = false
	t.selected = 0
	t.refreshSuggestions()
}

// accept completes the word being typed with the highlighted suggestion.
func (t *tui) accept() {
	t.setInput(acceptSuggestion(t.input.Value(), t.suggestions[t.selected]))
}

// browseHistory moves through submitted lines, older for -1 and newer for 1.
func (t *tui) browseHistory(step int) {
	pos := t.histPos + step
	if pos < 0 || pos > len(t.history.lines) {
		return
	}
	if t.histPos == len(t.history.lines) {
		t.draft = t.input.Value()
	}
	t.histPos = pos

	line := t.draft
	if pos < len(t.history.lines) {
		line = t.history.lines[pos]
	}
	t.input.SetValue(line)
	t.input.CursorEnd()

	// a recalled line is complete, don't open suggestions over it
	t.dismissed = true
	t.refreshSuggestions()
}

// refreshSuggestions recomputes what to offer for the current input.
func (t *tui) refreshSuggestions() {
	t.suggestions = nil
	value := t.input.Value()
	if !t.dismissed && t.input.Position() == len([]rune(value)) {
		t.suggestions = suggest(value)
	}
	if t.paletteRows() == 0 {
		// no room to show them
		t.suggestions = nil
	}
	if t.selected >= len(t.suggestions) {
		t.selected = 0
	}
	t.showGhost()
}

// showGhost has the input draw the rest of the highlighted suggestion.
func (t *tui) showGhost() {
	if !t.paletteOpen() {
		t.input.SetSuggestions(nil)
		return
	}
	full := acceptSuggestion(t.input.Value(), t.suggestions[t.selected])
	t.input.SetSuggestions([]string{strings.TrimRight(full, " ")})
}

func (t *tui) paletteOpen() bool {
	return len(t.suggestions) > 0
}

// paletteRows is how many suggestions fit on screen, 0 when there is no room.
func (t *tui) paletteRows() int {
	if len(t.suggestions) == 0 || t.height < minPaletteLines {
		return 0
	}
	// the rule, the prompt, the status bar, the border and a line of transcript
	room := t.height - 6
	if room < minPaletteRoom {
		return 0
	}
	return min(len(t.suggestions), maxPaletteRows, room)
}

// paletteHeight is the number of lines the suggestions take, border included.
func (t *tui) paletteHeight() int {
	if rows := t.paletteRows(); rows > 0 {
		return rows + 2
	}
	return 0
}

// promptLabel is the prompt, which shows the station that is loaded.
func (t *tui) promptLabel() string {
	if t.status == nil || t.status.Station == "" {
		return stylePrompt.Render("chill> ")
	}
	return stylePrompt.Render("chill[" + t.status.Station + "]> ")
}

// fit sizes the transcript and the prompt to the window and what is showing.
func (t *tui) fit() {
	if t.width == 0 {
		return
	}
	t.input.Prompt = t.promptLabel()
	t.input.SetWidth(max(t.width-lipgloss.Width(t.input.Prompt)-1, 1))

	// the last column is the scrollbar's
	follow := t.viewport.AtBottom()
	t.viewport.SetWidth(max(t.width-1, 1))
	t.viewport.SetHeight(max(t.height-3-t.paletteHeight(), 1))
	if follow || t.viewport.PastBottom() {
		t.viewport.GotoBottom()
	}

	// the heading, the rule and the footer
	t.helpView.SetWidth(max(t.width-1, 1))
	t.helpView.SetHeight(max(t.height-3, 1))
}

func (t *tui) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if t.width == 0 {
		return v
	}

	rule := strings.Repeat("─", t.width)

	if t.help {
		v.SetContent(strings.Join([]string{
			styleHeading.Render("help"),
			rule,
			withScrollbar(t.helpView),
			styleDim.Render("Esc back · PgUp/PgDn scroll"),
		}, "\n"))
		return v
	}

	parts := []string{withScrollbar(t.viewport), rule}
	if t.paletteOpen() {
		parts = append(parts, t.palette())
	}
	parts = append(parts, t.input.View(), t.statusBar())
	v.SetContent(strings.Join(parts, "\n"))

	if c := t.input.Cursor(); c != nil {
		c.Y += t.viewport.Height() + 1 + t.paletteHeight()
		v.Cursor = c
	}
	return v
}

// withScrollbar renders a viewport with a scrollbar in the column after it,
// which stays empty while everything fits.
func withScrollbar(vp viewport.Model) string {
	rows := strings.Split(vp.View(), "\n")
	height, total := vp.Height(), vp.TotalLineCount()
	if total <= height {
		return strings.Join(rows, "\n")
	}

	size := max(height*height/total, 1)
	top := int(vp.ScrollPercent()*float64(height-size) + 0.5)
	for i := range rows {
		if i >= top && i < top+size {
			rows[i] += styleThumb.Render("▉")
		} else {
			rows[i] += styleTrack.Render("│")
		}
	}
	return strings.Join(rows, "\n")
}

// palette renders the suggestions in a box as wide as the screen, with the
// highlighted one kept near the middle.
func (t *tui) palette() string {
	rows := t.paletteRows()
	inner := max(t.width-2, 1)
	first := min(max(t.selected-rows/2, 0), len(t.suggestions)-rows)

	title, nameWidth := "stations", 0
	for _, s := range t.suggestions {
		if !s.station {
			title = "commands"
		}
		nameWidth = max(nameWidth, lipgloss.Width(s.text))
	}
	if len(t.suggestions) > rows {
		title += fmt.Sprintf(" %d/%d", t.selected+1, len(t.suggestions))
	}
	nameWidth = min(nameWidth+2, inner*45/100)

	// the title sits in the middle of the top border
	title = " " + title + " "
	fill := max(inner-lipgloss.Width(title), 0)
	lines := []string{
		styleBorder.Render("┌"+strings.Repeat("─", fill/2)) + styleTitle.Render(title) +
			styleBorder.Render(strings.Repeat("─", fill-fill/2)+"┐"),
	}

	for i := first; i < first+rows; i++ {
		s := t.suggestions[i]
		marker, style := "   ", styleCommand
		if s.station {
			style = styleStation
		}
		if i == t.selected {
			marker, style = " ❯ ", styleSelected
		}

		text := ansi.Truncate(marker+column(s.text, nameWidth)+s.desc, inner, "…")
		text += strings.Repeat(" ", inner-lipgloss.Width(text))
		lines = append(lines, styleBorder.Render("│")+style.Render(text)+styleBorder.Render("│"))
	}

	lines = append(lines, styleBorder.Render("└"+strings.Repeat("─", inner)+"┘"))
	return strings.Join(lines, "\n")
}

// column fits s into exactly width cells, leaving at least one blank after it.
func column(s string, width int) string {
	s = ansi.Truncate(s, max(width-1, 0), "…")
	return s + strings.Repeat(" ", max(width-lipgloss.Width(s), 0))
}

// statusBar renders the bottom line: playback on the left, keys on the right.
func (t *tui) statusBar() string {
	var facts []string
	switch {
	case t.status == nil:
		facts = []string{"daemon off"}
	case t.status.Station == "":
		facts = []string{"idle"}
	case t.status.Paused:
		facts = []string{"paused", t.status.Station, t.status.Uptime}
	default:
		facts = []string{"playing", t.status.Station, t.status.Uptime}
	}

	hints := []string{"F1 help", "Tab complete", "PgUp/PgDn scroll", "Ctrl+Q quit"}
	switch {
	case t.running:
		facts = append(facts, "working")
		hints = []string{"F1 help", "Ctrl+Q quit"}
	case t.paletteOpen() && t.navigated:
		hints = []string{"F1 help", "Esc dismiss", "Ctrl+Q quit", "Enter accepts"}
	}

	// when it gets narrow the hints go first, from the left
	left := strings.Join(facts, " │ ")
	for len(hints) > 1 && lipgloss.Width(left)+2+lipgloss.Width(strings.Join(hints, " │ ")) > t.width {
		hints = hints[1:]
	}
	right := strings.Join(hints, " │ ")

	gap := t.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		right, gap = "", max(t.width-lipgloss.Width(left), 0)
	}
	return styleStatus.Render(ansi.Truncate(left+strings.Repeat(" ", gap)+right, t.width, ""))
}

// helpBody is what the help screen shows under its heading.
func helpBody() string {
	keys := [][2]string{
		{"Tab", "complete with the highlighted suggestion"},
		{"→", "take the ghost text"},
		{"↑ / ↓", "pick a suggestion, otherwise walk through history"},
		{"Enter", "run the line, or take a suggestion picked with ↑ / ↓"},
		{"Esc", "dismiss the suggestions"},
		{"Ctrl+P / Ctrl+N", "walk through history"},
		{"PgUp / PgDn", "scroll the transcript, so does the mouse wheel"},
		{"Ctrl+C", "clear the line"},
		{"Ctrl+L", "clear the screen"},
		{"Ctrl+Q", "quit, music keeps playing"},
	}

	var b strings.Builder
	row := func(name, desc string) {
		fmt.Fprintf(&b, "  %s%s\n", styleCommand.Render(column(name, 20)), desc)
	}

	fmt.Fprintf(&b, "\n  %s\n", styleDim.Render("commands"))
	for _, c := range replCommands {
		row(strings.TrimSpace(c.name+" "+c.args), c.desc)
	}
	row("<station>", "same as play <station>")

	fmt.Fprintf(&b, "\n  %s\n", styleDim.Render("keys"))
	for _, k := range keys {
		row(k[0], k[1])
	}
	return b.String()
}

// runRepl starts the fullscreen REPL.
func runRepl() {
	if _, err := tea.NewProgram(newTUI()).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(dim + "~ stay chill ~" + reset)
}
