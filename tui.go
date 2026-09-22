// tui.go implements the fullscreen REPL: a scrolling transcript between a
// fixed welcome header and the suggestions, prompt, and status bar below it.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/podcast"
)

const (
	maxPaletteRows  = 8    // suggestions visible at once
	minPaletteRoom  = 3    // rows that must be free before suggestions are shown
	minPaletteLines = 8    // terminal height below which suggestions are hidden
	maxTranscript   = 1000 // lines kept in the transcript
)

var (
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("#787C86"))
	stylePrompt   = lipgloss.NewStyle().Foreground(lipgloss.Color("#61AFEF"))
	styleInput    = lipgloss.NewStyle().Foreground(lipgloss.Color("#DCDFE4"))
	styleCommand  = lipgloss.NewStyle().Foreground(lipgloss.Color("#56B6C2"))
	styleStation  = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5C07B"))
	styleSuccess  = lipgloss.NewStyle()
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
	id    uint64
	out   string
	lines []transcriptLine
	err   error
}

// tui is the bubbletea model for the REPL.
type tui struct {
	width, height int

	lines    []transcriptLine // transcript, one entry per line
	rows     []string         // the lines wrapped to the screen, which is what scrolls
	viewport viewport.Model   // scrolls the transcript
	input    textinput.Model

	sel      selection // transcript text picked for copying
	dragging bool      // the mouse button is down on the transcript
	flash    selection // what was just copied, lit up briefly
	flashing bool
	notice   string // what the status bar says was copied
	modeNote string // presentation-mode warning shown below the banner
	copies   int    // identifies the latest copy, whose feedback is showing

	suggestions []suggestion // offered for what is being typed
	selected    int          // highlighted suggestion
	navigated   bool         // the highlight was moved, so enter accepts it
	dismissed   bool         // esc closed the suggestions until the next edit

	history *history
	histPos int    // position in history while browsing, len(lines) when not
	draft   string // what was typed before browsing history

	status         *Status  // last known daemon state
	running        bool     // a command is in flight
	pending        []string // submitted while another command was running
	active         string   // name of the command in flight
	spinner        spinner.Model
	activeLine     int // submitted command's transcript line, or -1
	activeRow      int // row where its spinner is drawn, or -1
	activeExtraRow bool
	commandID      uint64
	task           *replTask
	cancelling     bool

	help            bool           // the help screen is showing
	helpView        viewport.Model // scrolls the help screen
	viz             replVisualizer
	eq              replEqualizer
	podcasts        podcastBrowser
	podcastStart    *string
	radio           radioBrowser
	radioStart      bool
	radioFG         bool
	radioChoice     *Station
	lyrics          replLyrics
	libraryUI       libraryBrowser
	libraryStart    bool
	providersUI     providerBrowser
	audioUI         audioBrowser
	presentation    interfaceSettings
	inlineUsed      bool
	terminalProfile colorprofile.Profile
	appearance      appearanceBrowser
	keyOverlay      keyOverlay
	panelLibrary    *podcastLibrary
	panelLoading    bool
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

	settings := currentInterfaceSettings()
	configureInterfaceInput(&input)

	h := loadHistory()
	t := &tui{
		viewport:     viewport.New(),
		helpView:     viewport.New(),
		input:        input,
		history:      h,
		histPos:      len(h.lines),
		eq:           replEqualizer{config: defaultEqualizerConfig()},
		presentation: settings,
		modeNote:     presentationModeWarning(settings),
		inlineUsed:   settings.Simplified,
	}
	t.viewport.MouseWheelDelta = 3
	t.helpView.SoftWrap = true
	t.helpView.MouseWheelDelta = 3
	t.clear()
	return t
}

// Init starts status polling and opens any requested podcast browser.
func (t *tui) Init() tea.Cmd {
	if t.libraryStart {
		return tea.Batch(pollStatus, t.refreshInterfacePanel(), t.openLibrary())
	}
	if t.radioStart {
		return tea.Batch(pollStatus, t.refreshInterfacePanel(), t.openRadio())
	}
	if t.podcastStart != nil {
		return tea.Batch(pollStatus, t.refreshInterfacePanel(), t.openPodcasts(*t.podcastStart))
	}
	switch t.presentation.DefaultScreen {
	case "visualizer":
		t.visualizerCommand("")
	case "podcasts":
		return tea.Batch(pollStatus, t.refreshInterfacePanel(), t.openPodcasts(""))
	case "equalizer":
		t.openEqualizer()
	case "radio":
		return tea.Batch(pollStatus, t.refreshInterfacePanel(), t.openRadio())
	case "lyrics":
		return tea.Batch(pollStatus, t.refreshInterfacePanel(), t.openLyrics())
	case "library":
		return tea.Batch(pollStatus, t.refreshInterfacePanel(), t.openLibrary())
	case "providers":
		return tea.Batch(pollStatus, t.refreshInterfacePanel(), t.openProviders())
	case "audio":
		return tea.Batch(pollStatus, t.refreshInterfacePanel(), t.openAudio())
	case "interface":
		t.openAppearance()
	}
	return tea.Batch(pollStatus, t.refreshInterfacePanel())
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
func run(line string, id uint64) tea.Cmd {
	return func() tea.Msg {
		if parts, err := splitCommandLine(line); err == nil && len(parts) > 0 {
			switch strings.ToLower(parts[0]) {
			case "help", "?":
				return resultMsg{id: id, lines: replHelpLines()}
			case "list":
				return resultMsg{id: id, lines: stationListLines()}
			}
		}
		out, err := execute(line)
		return resultMsg{id: id, out: out, err: err}
	}
}

// Update handles a Bubble Tea message and reconciles layout and subscriptions.
func (t *tui) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := t.update(msg)
	t.fit()
	return t, tea.Batch(cmd, t.syncVisualizer())
}

func (t *tui) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.ColorProfileMsg:
		if t.presentation.ColorMode == "auto" {
			t.terminalProfile = msg.Profile
		}
		return nil
	case interfacePanelMsg:
		t.panelLoading = false
		if msg.library != nil {
			t.panelLibrary = msg.library
		}
		return nil
	case lyricsResultMsg:
		return t.lyricsResult(msg)
	case radioResultMsg:
		return t.radioResult(msg)
	case podcastResultMsg:
		return t.podcastResult(msg)
	case libraryResultMsg:
		cmd := t.libraryResult(msg)
		if t.libraryUI.open && !t.libraryUI.loading && msg.err == nil && msg.reload {
			return tea.Batch(cmd, t.loadLibraryPage())
		}
		return cmd
	case providerResultMsg:
		return t.providerResult(msg)
	case audioResultMsg:
		return t.audioResult(msg)
	case providerSetupValidation:
		if t.providersUI.open && t.providersUI.setup != nil {
			updated, command := t.providersUI.setup.Update(msg)
			setup := updated.(providerSetupModel)
			t.providersUI.setup = &setup
			if setup.done {
				if setup.saved {
					t.providersUI.note = setup.provider + " settings saved"
					if registry, err := providers(); err == nil {
						t.providersUI.infos = registry.list(context.Background(), false)
					}
				} else {
					t.providersUI.note = "Provider setup canceled"
				}
				t.providersUI.setup = nil
			}
			return command
		}
		return nil
	case visualizerConnectedMsg, visualizerFrameMsg, visualizerRetryMsg:
		return t.visualizerMessage(msg)
	case equalizerResultMsg:
		return t.equalizerResult(msg)
	case tea.WindowSizeMsg:
		rewrap := msg.Width != t.width
		t.width, t.height = msg.Width, msg.Height
		if t.providersUI.setup != nil {
			updated, _ := t.providersUI.setup.Update(msg)
			setup := updated.(providerSetupModel)
			t.providersUI.setup = &setup
		}
		if rewrap {
			t.wrap()
		}
		t.refreshSuggestions()
		return nil

	case flashDoneMsg:
		if int(msg) == t.copies {
			t.flashing = false
			t.redraw()
		}
		return nil

	case noticeDoneMsg:
		if int(msg) == t.copies {
			t.notice = ""
		}
		return nil

	case spinner.TickMsg:
		if t.providersUI.open && t.providersUI.loading && msg.ID == t.providersUI.spinner.ID() {
			var cmd tea.Cmd
			t.providersUI.spinner, cmd = t.providersUI.spinner.Update(msg)
			return cmd
		}
		if !t.running {
			return nil
		}
		var cmd tea.Cmd
		t.spinner, cmd = t.spinner.Update(msg)
		if cmd != nil {
			t.redraw()
		}
		return cmd

	case statusMsg:
		if msg.status != nil && msg.status.Error != "" && (t.status == nil || t.status.Error != msg.status.Error || t.status.State != msg.status.State) {
			t.printLine(transcriptSpan{transcriptError, "  playback: "}, transcriptSpan{transcriptBody, strings.Join(statusFacts(msg.status), " │ ")})
		}
		t.status = msg.status
		if s := msg.status; s != nil && s.Episode != nil && t.podcasts.library != nil {
			l := t.podcasts.library
			if l.Progress == nil {
				l.Progress = map[string]episodeProgress{}
			}
			l.Progress[s.Episode.Key()] = episodeProgress{Position: s.Position, Duration: s.Duration, Played: s.State == "ended", Updated: time.Now()}
			if strings.HasPrefix(t.podcasts.note, "loading:") && s.State != "loading" && s.State != "reconnecting" {
				t.podcasts.note = ""
			}
		}
		var lyricUpdate tea.Cmd
		if t.lyrics.open && !t.lyrics.loading && msg.status != nil && msg.status.NowPlaying != nil && msg.status.NowPlaying.Raw != t.lyrics.raw {
			lyricUpdate = t.openLyrics()
		}
		var panelUpdate tea.Cmd
		if t.panelHeight() > 0 && t.presentation.Panels["downloads"] {
			panelUpdate = t.refreshInterfacePanel()
		}
		if !msg.poll {
			return tea.Batch(lyricUpdate, panelUpdate)
		}
		return tea.Batch(lyricUpdate, panelUpdate, tea.Tick(t.statusPollInterval(), func(time.Time) tea.Msg { return pollStatus() }))

	case outputMsg:
		if msg.id != t.commandID || t.task == nil {
			return nil
		}
		t.printOutput(commandOutputLine(msg.line))
		return t.task.next

	case resultMsg:
		if msg.id != t.commandID || !t.running {
			return nil
		}
		if t.podcasts.open {
			if msg.err != nil {
				t.podcasts.note = msg.err.Error()
			} else if msg.out != "" {
				t.podcasts.note = ansi.Strip(msg.out)
			}
		}
		if t.task != nil {
			t.task.cancel()
			t.task = nil
		}
		completed := t.active
		t.cancelling = false
		t.running = false
		t.active = ""
		t.activeLine, t.activeRow = -1, -1
		if t.activeExtraRow {
			t.wrap()
		} else {
			t.redraw()
		}
		// Diagnostic commands can return useful findings alongside a failure.
		for _, line := range msg.lines {
			t.printOutput(line)
		}
		if msg.out != "" {
			for line := range strings.SplitSeq(msg.out, "\n") {
				if completed == "theme" {
					t.printOutput(transcriptLine{{transcriptLiteral, line}})
				} else {
					t.printOutput(commandOutputLine(line))
				}
			}
		}
		if missing, ok := errors.AsType[*requirementsError](msg.err); ok {
			for _, line := range missing.transcript() {
				t.printLine(line...)
			}
		} else if errors.Is(msg.err, context.Canceled) {
			t.printLine(transcriptSpan{transcriptDim, "  cancelled"})
		} else if msg.err != nil {
			t.printLine(transcriptSpan{transcriptError, "  error: "}, transcriptSpan{transcriptBody, msg.err.Error()})
		}
		var presentationCommand tea.Cmd
		if msg.err == nil && (completed == "theme" || completed == "keys" || completed == "interface") {
			if settings, err := loadInterfaceSettings(); err == nil {
				t.presentation, _, _ = activateInterfaceSettings(settings)
				t.enforcePresentationMode()
				t.configureInputs()
				t.wrap()
				presentationCommand = t.interfaceColorProfileCommand()
			}
		}
		if len(t.pending) > 0 {
			next := t.pending[0]
			t.pending = t.pending[1:]
			return tea.Batch(t.start(next), refreshStatus, presentationCommand)
		}
		return tea.Batch(refreshStatus, presentationCommand)

	case tea.MouseWheelMsg:
		if t.audioUI.open {
			browser := &t.audioUI
			if msg.Button == tea.MouseWheelUp {
				browser.selected = max(0, browser.selected-3)
			}
			if msg.Button == tea.MouseWheelDown {
				browser.selected = min(max(0, len(browser.rows())-1), browser.selected+3)
			}
			return nil
		}
		if t.providersUI.open {
			browser := &t.providersUI
			if msg.Button == tea.MouseWheelUp {
				browser.selected = max(0, browser.selected-3)
			}
			if msg.Button == tea.MouseWheelDown {
				browser.selected = min(max(0, len(browser.rows())-1), browser.selected+3)
			}
			return nil
		}
		if t.libraryUI.open {
			b := &t.libraryUI
			if msg.Button == tea.MouseWheelUp {
				b.selected = max(0, b.selected-3)
			}
			if msg.Button == tea.MouseWheelDown {
				b.selected = min(max(0, len(b.rows())-1), b.selected+3)
			}
			return nil
		}
		if t.lyrics.open {
			if msg.Button == tea.MouseWheelUp {
				t.lyrics.offset = max(0, t.lyrics.offset-3)
			}
			if msg.Button == tea.MouseWheelDown {
				t.lyrics.offset = min(max(0, len(t.lyrics.lines)-max(1, t.height-6)), t.lyrics.offset+3)
			}
			return nil
		}
		if t.radio.open {
			r := &t.radio
			if msg.Button == tea.MouseWheelUp {
				r.page.selected = max(0, r.page.selected-3)
			}
			if msg.Button == tea.MouseWheelDown {
				r.page.selected = min(max(0, len(r.rows())-1), r.page.selected+3)
			}
			return nil
		}
		if t.podcasts.open {
			p := &t.podcasts
			if msg.Button == tea.MouseWheelUp {
				p.page.selected = max(0, p.page.selected-3)
			}
			if msg.Button == tea.MouseWheelDown {
				p.page.selected = min(max(0, len(p.rows())-1), p.page.selected+3)
			}
			return nil
		}
		if t.viz.focused && !t.help {
			return nil
		}
		if t.eq.open {
			return nil
		}
		var cmd tea.Cmd
		if t.help {
			t.helpView, cmd = t.helpView.Update(msg)
		} else {
			t.viewport, cmd = t.viewport.Update(msg)
		}
		return cmd

	case tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		if t.help || t.viz.focused || t.eq.open || t.podcasts.open || t.radio.open || t.lyrics.open || t.libraryUI.open || t.providersUI.open || t.audioUI.open {
			return nil
		}
		return t.mouse(msg.(tea.MouseMsg))

	case tea.KeyPressMsg:
		if command, handled := t.handleGlobalKey(msg); handled {
			return command
		}
		if t.keyOverlay.open {
			return t.keyOverlayKey(msg)
		}
		if t.appearance.open {
			return t.appearanceKey(msg)
		}
		if t.lyrics.open {
			return t.lyricsKey(msg)
		}
		if t.audioUI.open {
			return t.audioKey(msg)
		}
		if t.providersUI.open {
			return t.providerKey(msg)
		}
		if t.libraryUI.open {
			return t.libraryKey(msg)
		}
		if t.radio.open {
			return t.radioKey(msg)
		}
		if t.podcasts.open {
			return t.podcastKey(msg)
		}
		if t.eq.open {
			return t.equalizerKey(msg)
		}
		if t.help {
			return t.helpKey(msg)
		}
		if t.viz.focused {
			return t.visualizerKey(msg)
		}
		if t.sel.active {
			if cmd, handled := t.selectionKey(msg); handled {
				return cmd
			}
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
	pressed := t.presentation.mapKey("prompt", msg.String())

	switch pressed {
	case "f9":
		return t.openAudio()
	case "f8":
		return t.openProviders()
	case "f7":
		return t.openLibrary()
	case "f6":
		return t.openLyrics()
	case "f5":
		return t.openRadio()
	case "f3":
		return t.openPodcasts("")
	case "f4":
		t.openEqualizer()
		return nil
	case "f2":
		t.visualizerCommand("")
		return nil
	case "ctrl+q":
		return tea.Quit

	case "ctrl+c":
		if t.cancelCommand() {
			return nil
		}
		t.setInput("")
		return nil

	case "shift+up":
		t.selectLines()
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
		t.edited()
	} else {
		t.refreshSuggestions()
	}
	return cmd
}

// edited starts suggestions over after the text in the prompt changed.
func (t *tui) edited() {
	t.dismissed = false
	t.navigated = false
	t.selected = 0
	t.histPos = len(t.history.lines)
	t.refreshSuggestions()
}

// helpKey handles a key press on the help screen, which swallows typing.
func (t *tui) helpKey(msg tea.KeyPressMsg) tea.Cmd {
	switch t.presentation.mapKey("help", msg.String()) {
	case "ctrl+q":
		return tea.Quit
	case "ctrl+c":
		t.cancelCommand()
	case "f1", "esc":
		t.help = false
	case "f4":
		t.help = false
		t.openEqualizer()
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
	words := strings.Fields(strings.ToLower(line))
	if words[0] == "podcasts" || words[0] == "podcast" {
		args := strings.Fields(line)[1:]
		if len(args) == 0 {
			return t.openPodcasts("")
		}
		if len(args) == 1 && podcast.ValidURL(args[0]) {
			return t.openPodcasts(args[0])
		}
	}
	if words[0] == "radio" && len(words) == 1 {
		return t.openRadio()
	}
	if words[0] == "lyrics" && len(words) == 1 {
		return t.openLyrics()
	}
	if words[0] == "viz" {
		t.visualizerCommand(strings.Join(words[1:], " "))
		return nil
	}
	if words[0] == "eq" && len(words) == 1 {
		t.openEqualizer()
		return nil
	}
	if (words[0] == "theme" || words[0] == "interface") && len(words) == 1 {
		t.openAppearance()
		return nil
	}
	if words[0] == "keys" && len(words) == 1 {
		return t.toggleKeyOverlay()
	}

	switch strings.ToLower(line) {
	case "quit", "exit", "q":
		return tea.Quit
	case "clear":
		t.clear()
		return nil
	case "cancel":
		if !t.cancelCommand() {
			t.printLine(transcriptSpan{transcriptDim, "  no diagnostics running"})
		}
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
	t.commandID++
	t.running = true
	t.active = strings.ToLower(strings.Fields(line)[0])
	// A fresh ID keeps late ticks from a previous command out of this animation.
	t.spinner = spinner.New(spinner.WithSpinner(spinner.MiniDot))
	t.activeLine = len(t.lines)
	t.printLine(append(transcriptLine{{transcriptPrompt, t.promptText()}}, highlight(line)...)...)
	command := run(line, t.commandID)
	if t.active == "doctor" {
		args := strings.Fields(line)[1:]
		t.task = newREPLTask(t.commandID, func(ctx context.Context, out io.Writer) error {
			return runDoctorContext(ctx, args, out)
		})
		command = t.task.next
	}
	if t.active == "podcasts" || t.active == "podcast" {
		args := strings.Fields(line)[1:]
		t.task = newREPLTask(t.commandID, func(ctx context.Context, out io.Writer) error {
			result, err := runPodcast(ctx, args, true)
			if result != "" {
				fmt.Fprintln(out, result)
			}
			return err
		})
		command = t.task.next
	}
	return tea.Batch(t.spinner.Tick, command)
}

// Cancellation waits for subprocess cleanup before dispatching another command.
func (t *tui) cancelCommand() bool {
	if t.task == nil {
		return false
	}
	t.pending = nil
	t.cancelling = true
	t.task.cancel()
	return true
}

func (t *tui) shutdown() {
	t.closeLyrics()
	t.closeRadio()
	t.closePodcasts()
	t.closeVisualizer()
	t.closeLibrary()
	if t.task != nil {
		t.task.stop()
	}
	equalizerWasSaving := t.eq.sending
	if equalizerWasSaving && t.eq.done != nil {
		// Let the serialized write finish before storing the final curve; this
		// also retries a transient failure without letting an older write land last.
		<-t.eq.done
	}
	if equalizerWasSaving && t.eq.config.Preset != "" {
		_ = clientSetEqualizerState(t.eq.config)
	}
}

// highlight assigns styles to a submitted command before rendering it.
func highlight(line string) transcriptLine {
	var spans transcriptLine
	words := strings.Fields(line)
	for i, word := range words {
		if i > 0 {
			spans = append(spans, transcriptSpan{transcriptLiteral, " "})
		}
		role := transcriptBody
		switch {
		case findStation(word) != nil && (i == 0 || i == 1 && strings.EqualFold(words[0], "play")):
			role = transcriptStation
		case i == 0 && isCommand(word):
			role = transcriptCommand
		}
		spans = append(spans, transcriptSpan{role, word})
	}
	return spans
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
	t.printLine(transcriptSpan{transcriptLiteral, line})
}

func (t *tui) printLine(spans ...transcriptSpan) {
	entry := transcriptLine(spans)
	t.lines = append(t.lines, entry)
	if len(t.lines) > maxTranscript {
		t.activeLine = max(-1, t.activeLine-(len(t.lines)-maxTranscript))
		t.lines = t.lines[len(t.lines)-maxTranscript:]
		t.wrap()
	} else {
		t.appendRows(len(t.lines)-1, entry)
		t.redraw()
	}
	t.viewport.GotoBottom()
}

// appendRows reserves room for the spinner without putting animation frames in
// the transcript or copied text.
func (t *tui) appendRows(index int, line transcriptLine) {
	rows := t.wrapped(line.render())
	if t.running && index == t.activeLine {
		t.activeExtraRow = t.width > 1 && lipgloss.Width(rows[len(rows)-1])+2 > t.width-1
		if t.activeExtraRow {
			rows = append(rows, "")
		}
		t.activeRow = len(t.rows) + len(rows) - 1
	}
	t.rows = append(t.rows, rows...)
}

// wrapped splits a transcript line into rows that fit beside the scrollbar.
func (t *tui) wrapped(line string) []string {
	if t.width < 2 {
		return []string{line}
	}
	return strings.Split(lipgloss.Wrap(line, t.width-1, ""), "\n")
}

// wrap lays the whole transcript out again, for a new width or after old
// lines were dropped. Rows move, so what was selected no longer holds.
func (t *tui) wrap() {
	follow := t.viewport.AtBottom()
	offset := t.viewport.YOffset()
	t.rows = nil
	t.activeRow, t.activeExtraRow = -1, false
	for i, line := range t.lines {
		t.appendRows(i, line)
	}
	t.sel, t.flashing = selection{}, false
	t.redraw()
	if follow {
		t.viewport.GotoBottom()
	} else {
		t.viewport.SetYOffset(offset)
	}
}

// clear empties the transcript without changing the fixed header.
func (t *tui) clear() {
	t.lines = nil
	t.rows = nil
	t.activeLine, t.activeRow, t.activeExtraRow = -1, -1, false
	t.sel, t.flashing = selection{}, false
	t.redraw()
	t.viewport.GotoTop()
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
	if t.presentation.Simplified || interfaceLayoutTier(t.width, t.height, false, t.presentation.Simplified) == "minimal" || len(t.suggestions) == 0 || t.height < minPaletteLines {
		return 0
	}
	// Reserve the header, rule, prompt, status, border and a transcript row.
	room := t.height - 6 - t.headerHeight()
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
func (t *tui) promptLabel() string { return stylePrompt.Render(t.promptText()) }

func (t *tui) promptText() string {
	if t.status != nil && t.status.Item != nil && t.status.Item.Kind != MediaStation {
		return "chill[" + string(t.status.Item.Kind) + "]> "
	}
	if t.status != nil && t.status.Episode != nil {
		return "chill[podcast]> "
	}
	if t.status == nil || t.status.Station == "" {
		return "chill> "
	}
	return "chill[" + t.status.Station + "]> "
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
	statusHeight := 0
	if t.presentation.ShowStatus {
		statusHeight = 1
	}
	t.viewport.SetHeight(max(t.height-2-statusHeight-t.paletteHeight()-t.visualizerHeight()-t.panelHeight()-t.headerHeight(), 1))
	if follow || t.viewport.PastBottom() {
		t.viewport.GotoBottom()
	}

	// the heading, the rule and the footer
	t.helpView.SetWidth(max(t.width-1, 1))
	helpFooter := 0
	if t.presentation.ShowHelp {
		helpFooter = 1
	}
	t.helpView.SetHeight(max(t.height-2-helpFooter, 1))
}

// View renders the active REPL, help, podcast, or visualizer screen.
func (t *tui) View() tea.View {
	if t.width > 0 && t.height > 0 && interfaceLayoutTier(t.width, t.height, false, t.presentation.Simplified) == "too-small" {
		lines := make([]string, t.height)
		lines[0] = styleError.Render(ansi.Truncate(fmt.Sprintf("resize: %dx%d", t.width, t.height), t.width, ""))
		if t.height > 1 {
			lines[1] = styleDim.Render(ansi.Truncate("minimum 40x10", t.width, ""))
		}
		view := tea.NewView(strings.Join(lines, "\n"))
		view.AltScreen = true
		return t.decoratedView(view)
	}
	if t.keyOverlay.open {
		return t.decoratedView(t.keyOverlayView())
	}
	if t.appearance.open {
		return t.decoratedView(t.appearanceView())
	}
	if t.audioUI.open {
		return t.decoratedView(t.audioView())
	}
	if t.providersUI.open {
		return t.decoratedView(t.providerView())
	}
	if t.libraryUI.open {
		return t.decoratedView(t.libraryView())
	}
	if t.lyrics.open {
		return t.decoratedView(t.lyricsView())
	}
	if t.radio.open {
		return t.decoratedView(t.radioView())
	}
	if t.podcasts.open {
		return t.decoratedView(t.podcastView())
	}
	if t.eq.open {
		return t.decoratedView(t.equalizerView())
	}
	var v tea.View
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if t.width == 0 {
		return t.decoratedView(v)
	}

	rule := strings.Repeat("─", t.width)

	if t.help {
		parts := []string{
			styleHeading.Render("help"),
			rule,
			withScrollbar(t.helpView),
		}
		if t.presentation.ShowHelp {
			parts = append(parts, styleDim.Render(t.presentation.bindingHint("help.close", "back")+" · "+t.presentation.bindingHint("help.page-down", "scroll")))
		}
		v.SetContent(strings.Join(parts, "\n"))
		return t.decoratedView(v)
	}
	if t.viz.fullscreen && t.visualizerHeight() > 0 {
		var bottom []string
		if footer := t.visualizerFooter(); footer != "" {
			bottom = append(bottom, footer)
		}
		if t.presentation.ShowStatus {
			bottom = append(bottom, t.statusBar())
		}
		parts := append([]string{t.visualizerView(max(1, t.height-len(bottom)))}, bottom...)
		v.SetContent(strings.Join(parts, "\n"))
		return t.decoratedView(v)
	}

	parts := []string{t.transcriptView()}
	if panels := t.panelView(); panels != "" {
		parts = append(parts, panels)
	}
	if height := t.visualizerHeight(); height > 0 {
		parts = append(parts, t.visualizerView(height))
	}
	parts = append(parts, rule)
	if t.paletteOpen() {
		parts = append(parts, t.palette())
	}
	// Completion hints can extend the input's padding past its configured width.
	parts = append(parts, ansi.Truncate(t.input.View(), t.width, ""))
	if t.presentation.ShowStatus {
		parts = append(parts, t.statusBar())
	}
	v.SetContent(strings.Join(parts, "\n"))

	if c := t.input.Cursor(); c != nil && !t.viz.focused {
		c.Y += t.transcriptHeight() + 1 + t.paletteHeight() + t.visualizerHeight() + t.panelHeight()
		v.Cursor = c
	}
	return t.decoratedView(v)
}

func (t *tui) transcriptHeight() int {
	return t.headerHeight() + t.viewport.Height()
}

func (t *tui) transcriptView() string {
	return strings.Join(append(t.replHeader(), withScrollbar(t.viewport)), "\n")
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
			marker, style = " ❯ ", styleSelection
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
	if !t.presentation.ShowStatus {
		return ""
	}
	facts := t.interfaceStatusFacts()
	if t.running {
		action := "Running "
		if t.cancelling {
			action = "Cancelling "
		}
		facts = append([]string{t.spinner.View() + " " + action + t.active + "..."}, facts...)
	}

	p := t.presentation
	hints := []string{p.bindingHint("global.interface", "interface"), p.bindingHint("global.keys", "keys"), p.bindingHint("global.help", "help"), p.bindingHint("prompt.complete", "complete"), p.bindingHint("global.quit", "quit")}
	switch {
	case t.audioUI.open:
		hints = []string{p.bindingHint("global.audio", "prompt"), p.bindingHint("global.quit", "quit")}
	case t.providersUI.open:
		hints = []string{p.bindingHint("global.providers", "prompt"), p.bindingHint("global.quit", "quit")}
	case t.libraryUI.open:
		hints = []string{p.bindingHint("global.library", "prompt"), p.bindingHint("global.quit", "quit")}
	case t.lyrics.open:
		hints = []string{p.bindingHint("global.lyrics", "prompt"), p.bindingHint("global.quit", "quit")}
	case t.radio.open:
		hints = []string{p.bindingHint("global.radio", "prompt"), p.bindingHint("global.quit", "quit")}
	case t.podcasts.open:
		hints = []string{p.bindingHint("global.podcasts", "prompt"), p.bindingHint("global.quit", "quit")}
	case t.eq.open:
		hints = []string{p.bindingHint("equalizer.next-preset", "preset"), p.bindingHint("equalizer.right", "band"), p.bindingHint("equalizer.raise", "gain"), p.bindingHint("global.equalizer", "prompt"), p.bindingHint("global.quit", "quit")}
	case t.viz.focused:
		hints = []string{p.bindingHint("visualizer.next", "next"), p.bindingHint("visualizer.fullscreen", "fullscreen"), p.bindingHint("visualizer.back", "prompt"), p.bindingHint("visualizer.disable", "off"), p.bindingHint("global.quit", "quit")}
	case t.sel.active && t.sel.lines:
		hints = []string{p.bindingHint("selection.down", "extend"), p.bindingHint("selection.copy", "yank"), p.bindingHint("selection.cancel", "cancel")}
	case t.sel.active:
		hints = []string{p.bindingHint("selection.copy", "yank"), p.bindingHint("selection.cancel", "cancel")}
	case t.task != nil:
		hints = []string{p.bindingHint("global.help", "help"), p.bindingHint("prompt.cancel-command", "cancel"), p.bindingHint("global.quit", "quit")}
	case t.running:
		hints = []string{p.bindingHint("global.help", "help"), p.bindingHint("global.quit", "quit")}
	case t.paletteOpen() && t.navigated:
		hints = []string{p.bindingHint("global.help", "help"), p.bindingHint("prompt.cancel", "dismiss"), p.bindingHint("global.quit", "quit"), p.bindingHint("prompt.submit", "accepts")}
	}
	if !t.presentation.ShowHelp {
		hints = nil
	}

	// what was just copied goes in front of the hints
	notice, divider := t.notice, ""
	if notice != "" {
		divider = " │ "
	}

	// when it gets narrow the hints go first, from the left
	left := strings.Join(facts, " │ ")
	used := lipgloss.Width(left) + 2 + lipgloss.Width(notice+divider)
	for len(hints) > 1 && used+lipgloss.Width(strings.Join(hints, " │ ")) > t.width {
		hints = hints[1:]
	}
	right := strings.Join(hints, " │ ")

	gap := t.width - lipgloss.Width(left) - lipgloss.Width(notice+divider+right)
	if gap < 2 {
		return styleStatus.Render(ansi.Truncate(left+strings.Repeat(" ", t.width), t.width, ""))
	}
	return styleStatus.Render(left+strings.Repeat(" ", gap)) + styleNotice.Render(notice) + styleStatus.Render(divider+right)
}

// helpBody is what the help screen shows under its heading.
func helpBody(settings ...interfaceSettings) string {
	presentation := currentInterfaceSettings()
	if len(settings) > 0 {
		presentation = settings[0]
	}
	keys := [][2]string{
		{presentation.bindingLabel("global.visualizer"), "focus the visualizer"},
		{presentation.bindingLabel("global.podcasts"), "open podcasts or return to the prompt"},
		{presentation.bindingLabel("global.equalizer"), "open the ten-band equalizer or return to the prompt"},
		{presentation.bindingLabel("global.radio"), "open radio discovery or return to the prompt"},
		{presentation.bindingLabel("global.lyrics"), "show lyrics for the current track"},
		{presentation.bindingLabel("global.library"), "open the queue, playlists, history, and local files"},
		{presentation.bindingLabel("global.providers"), "search and browse connected music providers"},
		{presentation.bindingLabel("global.audio"), "change audio devices and quality profiles"},
		{presentation.bindingLabel("global.interface"), "preview and configure the terminal interface"},
		{presentation.bindingLabel("global.keys"), "search the active keybindings"},
		{presentation.bindingLabel("prompt.complete"), "complete with the highlighted suggestion"},
		{presentation.bindingLabel("prompt.ghost"), "take the ghost text"},
		{presentation.bindingLabel("prompt.suggestion-next"), "pick a suggestion, otherwise walk through history"},
		{presentation.bindingLabel("prompt.submit"), "run the line or accept a selected suggestion"},
		{presentation.bindingLabel("prompt.cancel"), "dismiss the suggestions"},
		{presentation.bindingLabel("prompt.history-previous") + "/" + presentation.bindingLabel("prompt.history-next"), "walk through history"},
		{presentation.bindingLabel("prompt.page-up") + "/" + presentation.bindingLabel("prompt.page-down"), "scroll the transcript, as does the mouse wheel"},
		{presentation.bindingLabel("prompt.select-lines"), "select lines of the transcript"},
		{presentation.bindingLabel("selection.copy"), "copy what is selected"},
		{"mouse", "drag to select, right click to copy, or to paste"},
		{presentation.bindingLabel("prompt.cancel-command"), "cancel diagnostics and queued commands, otherwise clear the line"},
		{presentation.bindingLabel("prompt.clear"), "clear the screen"},
		{presentation.bindingLabel("global.quit"), "quit, music keeps playing"},
	}

	var b strings.Builder
	row := func(name, desc string) {
		fmt.Fprintf(&b, "  %s%s\n", styleCommand.Render(column(name, helpWidth())), desc)
	}

	fmt.Fprintf(&b, "\n  %s\n", styleDim.Render("commands"))
	names := helpNames()
	for i, c := range replCommands {
		row(names[i], c.desc)
	}
	row("<station>", "same as play <station>")

	fmt.Fprintf(&b, "\n  %s\n", styleDim.Render("diagnostics"))
	row("doctor --stations", "check all configured streams")
	row("doctor --stream sleep", "check one station (or supply a URL)")
	row("doctor --logs", "show the latest daemon startup log")
	row("doctor --help", "show all options, including timeouts")

	fmt.Fprintf(&b, "\n  %s\n", styleDim.Render("keys"))
	for _, k := range keys {
		row(k[0], k[1])
	}
	fmt.Fprintf(&b, "\n  %s\n", styleDim.Render("effective bindings"))
	for _, binding := range interfaceKeyRows(presentation, "prompt", "") {
		fmt.Fprintf(&b, "  %s\n", binding)
	}
	return b.String()
}

// runRepl starts the fullscreen REPL.
func runRepl(podcastQuery ...string) {
	model := newTUI()
	if len(podcastQuery) > 0 {
		model.podcastStart = &podcastQuery[0]
	}
	_, err := tea.NewProgram(model, interfaceProgramOptions(model.presentation)...).Run()
	model.shutdown()
	model.cleanupTerminal(os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	palette := currentCLIPalette()
	fmt.Println(palette.dim + "~ stay chill ~" + palette.reset)
}

func runReplRadio(foreground bool) {
	model := newTUI()
	model.radioStart, model.radioFG = true, foreground
	_, err := tea.NewProgram(model, interfaceProgramOptions(model.presentation)...).Run()
	model.shutdown()
	model.cleanupTerminal(os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return
	}
	if model.radioChoice != nil {
		recordRadioClick(catalogFromStation(*model.radioChoice))
		if err := runForeground(model.radioChoice); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return
		}
	}
	palette := currentCLIPalette()
	fmt.Println(palette.dim + "~ stay chill ~" + palette.reset)
}

func (t *tui) cleanupTerminal(output io.Writer) {
	if t.inlineUsed {
		_, _ = io.WriteString(output, ansi.EraseEntireScreen+ansi.CursorHomePosition)
	}
}
