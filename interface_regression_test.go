package main

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/audio"
	"github.com/willibrandon/chill/internal/visualizer"
)

// TestCLIPaletteSnapshotsAreConcurrentSafe exercises formatting during previews.
func TestCLIPaletteSnapshotsAreConcurrentSafe(t *testing.T) {
	t.Cleanup(func() { setCLIColors(defaultInterfaceSettings(), builtinInterfaceThemes[0]) })
	var group sync.WaitGroup
	for worker := range 8 {
		group.Go(func() {
			for iteration := range 100 {
				settings := defaultInterfaceSettings()
				theme := builtinInterfaceThemes[(worker+iteration)%len(builtinInterfaceThemes)]
				if iteration%3 == 0 {
					settings.ColorMode = "none"
				}
				setCLIColors(settings, theme)
				if output := replHelp(); output == "" {
					t.Error("palette snapshot produced empty help")
				}
			}
		})
	}
	group.Wait()
}

// TestStatusPreservesOperationalIndicators keeps silence and persistence visible.
func TestStatusPreservesOperationalIndicators(t *testing.T) {
	settings := defaultInterfaceSettings()
	settings.StatusFields = []string{"state"}
	if err := normalizeInterfaceSettings(&settings); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"shuffle", "repeat", "muted", "storage-error"} {
		if !strings.Contains(strings.Join(settings.StatusFields, ","), required) {
			t.Fatalf("required status field %q was not restored", required)
		}
	}
	model := newTUI()
	model.presentation = settings
	model.status = &Status{State: "playing", Shuffle: true, Repeat: "all", Muted: true, StorageError: "progress was not saved"}
	status := strings.Join(model.interfaceStatusFacts(), " | ")
	for _, indicator := range []string{"shuffle", "repeat all", "muted", "progress was not saved"} {
		if !strings.Contains(status, indicator) {
			t.Errorf("status omitted %q: %s", indicator, status)
		}
	}
}

// TestCommandDrivenInterfaceChangesUpdateRenderer checks the live profile command.
func TestCommandDrivenInterfaceChangesUpdateRenderer(t *testing.T) {
	withConfigDir(t)
	setInterfaceSessionOverrides(interfaceSessionOverrides{})
	settings := defaultInterfaceSettings()
	settings.ColorMode = "truecolor"
	if err := saveInterfaceSettings(settings); err != nil {
		t.Fatal(err)
	}
	model := newTUI()
	model.running, model.active, model.commandID = true, "interface", 7
	command := model.update(resultMsg{id: 7})
	if !commandContainsColorProfile(command, colorprofile.TrueColor) {
		t.Fatal("interface command did not update the renderer color profile")
	}
}

func commandContainsColorProfile(command tea.Cmd, want colorprofile.Profile) bool {
	if command == nil {
		return false
	}
	switch message := command().(type) {
	case tea.ColorProfileMsg:
		return message.Profile == want
	case tea.BatchMsg:
		for _, child := range message {
			if commandContainsColorProfile(child, want) {
				return true
			}
		}
	}
	return false
}

// TestVisualizerFocusAndGlobalToggle keeps hidden visualizers out of key dispatch.
func TestVisualizerFocusAndGlobalToggle(t *testing.T) {
	withConfigDir(t)
	model := newTUI()
	model.width, model.height = 100, 30
	model.update(tea.KeyPressMsg{Code: tea.KeyF2})
	if !model.viz.focused {
		t.Fatal("F2 did not focus a visible visualizer")
	}
	model.update(tea.KeyPressMsg{Code: tea.KeyF2})
	if model.viz.focused {
		t.Fatal("second F2 did not return to the prompt")
	}

	model.width, model.height = 80, 12
	model.update(tea.KeyPressMsg{Code: tea.KeyF2})
	if model.viz.focused {
		t.Fatal("minimal layout gave focus to an invisible visualizer")
	}
	model.width, model.height = 100, 30
	model.presentation.VisualizerHeight = 0
	model.update(tea.KeyPressMsg{Code: tea.KeyF2})
	if model.viz.focused {
		t.Fatal("zero-height visualizer retained keyboard focus")
	}
}

// TestReleasedGlobalAndOverlayBindings checks dispatch order across scopes.
func TestReleasedGlobalAndOverlayBindings(t *testing.T) {
	settings := defaultInterfaceSettings()
	settings.Bindings["global.library"] = []string{"ctrl+g"}
	settings.Bindings["prompt.clear"] = []string{"f7"}
	settings.Bindings["browser.search"] = []string{"f7"}
	settings.Bindings["overlay.search"] = []string{"f7"}
	if err := normalizeInterfaceSettings(&settings); err != nil {
		t.Fatal(err)
	}
	model := newTUI()
	model.presentation = settings
	model.lines = append(model.lines, "sentinel")
	model.update(tea.KeyPressMsg{Code: tea.KeyF7})
	if strings.Contains(strings.Join(model.lines, "\n"), "sentinel") {
		t.Fatal("released global F7 swallowed the prompt replacement")
	}

	model.openLibrary()
	model.update(tea.KeyPressMsg{Code: tea.KeyF7})
	if !model.libraryUI.open || !model.libraryUI.editing || model.libraryUI.editAction != "filter" {
		t.Fatal("released global F7 did not reach the library search binding")
	}

	model.toggleKeyOverlay()
	model.update(tea.KeyPressMsg{Code: tea.KeyF7})
	if !model.keyOverlay.open || !model.keyOverlay.searching {
		t.Fatal("released global F7 did not reach the overlay search binding")
	}
	model.keyOverlay.searching = false
	command, handled := model.handleGlobalKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if !handled || command == nil {
		t.Fatal("key overlay swallowed the global quit action")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("global quit did not return tea.Quit")
	}
}

// TestReleasedGlobalBindingsReachEveryBrowser checks routing before screen handlers.
func TestReleasedGlobalBindingsReachEveryBrowser(t *testing.T) {
	tests := []struct {
		name         string
		globalAction string
		defaultKey   tea.KeyPressMsg
		open         func(*tui)
		state        func(*tui) (open, editing bool)
	}{
		{
			name: "radio", globalAction: "global.radio", defaultKey: tea.KeyPressMsg{Code: tea.KeyF5},
			open:  func(model *tui) { model.openRadio() },
			state: func(model *tui) (bool, bool) { return model.radio.open, model.radio.editing },
		},
		{
			name: "podcasts", globalAction: "global.podcasts", defaultKey: tea.KeyPressMsg{Code: tea.KeyF3},
			open:  func(model *tui) { model.openPodcasts("") },
			state: func(model *tui) (bool, bool) { return model.podcasts.open, model.podcasts.editing },
		},
		{
			name: "providers", globalAction: "global.providers", defaultKey: tea.KeyPressMsg{Code: tea.KeyF8},
			open:  func(model *tui) { model.openProviders() },
			state: func(model *tui) (bool, bool) { return model.providersUI.open, model.providersUI.editing },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, key := range []tea.KeyPressMsg{test.defaultKey, {Code: 'q', Mod: tea.ModCtrl}} {
				settings := defaultInterfaceSettings()
				settings.Bindings[test.globalAction] = []string{"ctrl+g"}
				settings.Bindings["global.quit"] = []string{"ctrl+x"}
				settings.Bindings["browser.search"] = []string{key.String()}
				if err := normalizeInterfaceSettings(&settings); err != nil {
					t.Fatal(err)
				}
				model := newTUI()
				model.presentation = settings
				test.open(model)
				model.update(key)
				open, editing := test.state(model)
				model.closeContentScreens()
				if !open || !editing {
					t.Errorf("released %s did not reach browser search", key.String())
				}
			}
		})
	}
}

// TestCanonicalModifiersAndSelectionRemaps checks reachable persisted bindings.
func TestCanonicalModifiersAndSelectionRemaps(t *testing.T) {
	if got := normalizeKeyName("shift+ctrl+g"); got != "ctrl+shift+g" {
		t.Fatalf("modifier order normalized to %q", got)
	}
	settings := defaultInterfaceSettings()
	settings.Bindings["global.library"] = []string{"shift+ctrl+g"}
	if err := normalizeInterfaceSettings(&settings); err != nil {
		t.Fatal(err)
	}
	if got := settings.Bindings["global.library"][0]; got != "ctrl+shift+g" {
		t.Fatalf("persisted modifier order = %q", got)
	}
	pressed := tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl | tea.ModShift}.String()
	if action, ok := settings.actionFor("global", pressed); !ok || action.id != "global.library" {
		t.Fatalf("Bubble Tea key %q did not reach the canonical binding", pressed)
	}

	model := newTUI()
	model.presentation = defaultInterfaceSettings()
	model.presentation.Bindings["selection.page-down"] = []string{"D"}
	model.viewport.SetWidth(40)
	model.viewport.SetHeight(2)
	model.viewport.SetContent(strings.Join([]string{"one", "two", "three", "four", "five"}, "\n"))
	model.sel.active = true
	if _, handled := model.selectionKey(tea.KeyPressMsg{Code: 'D', Text: "D"}); !handled || model.viewport.YOffset() == 0 {
		t.Fatal("remapped selection page-down did not scroll")
	}
	if got := model.presentation.mapKey("library", "t"); got != "t" {
		t.Fatalf("unexpected unconfigured library repeat mapping %q", got)
	}
	model.presentation.Bindings["browser.repeat"] = []string{"t"}
	if got := model.presentation.mapKey("library", "t"); got != "R" {
		t.Fatalf("library repeat remap became %q", got)
	}
}

// TestResetRecoversInvalidSettings keeps recovery independent of current validity.
func TestResetRecoversInvalidSettings(t *testing.T) {
	withConfigDir(t)
	invalid := defaultInterfaceSettings()
	invalid.SeekStep = 0
	if err := writeJSON(interfaceSettingsPath(), invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := runInterfaceCommand([]string{"reset"}, false); err != nil {
		t.Fatalf("interface reset could not recover invalid settings: %v", err)
	}
	if settings, err := loadInterfaceSettings(); err != nil || settings.SeekStep != defaultInterfaceSettings().SeekStep {
		t.Fatalf("interface reset result = %+v, %v", settings, err)
	}

	conflicted := defaultInterfaceSettings()
	conflicted.Bindings["global.library"] = []string{"ctrl+g"}
	conflicted.Bindings["global.audio"] = []string{"ctrl+g"}
	if err := writeJSON(interfaceSettingsPath(), conflicted); err != nil {
		t.Fatal(err)
	}
	if _, err := runKeysCommand([]string{"reset"}, false); err != nil {
		t.Fatalf("keys reset could not recover conflicts: %v", err)
	}
	settings, err := loadInterfaceSettings()
	if err != nil || len(settings.Bindings) != 0 {
		t.Fatalf("key reset result = %+v, %v", settings.Bindings, err)
	}
}

// TestDownloadPanelUsesLiveWorkerStates checks counts and snapshot replacement.
func TestDownloadPanelUsesLiveWorkerStates(t *testing.T) {
	library := &podcastLibrary{Downloads: map[string]episodeDownload{
		"ready": {State: "ready"}, "active": {State: "downloading"}, "queued": {State: "queued"},
		"retry": {State: "retrying"}, "failed": {State: "error"},
	}}
	status := interfaceDownloadStatus(library)
	for _, value := range []string{"1 ready", "1 downloading", "1 queued", "1 retrying", "1 failed"} {
		if !strings.Contains(status, value) {
			t.Errorf("download status omitted %q: %s", value, status)
		}
	}

	model := newTUI()
	old := &podcastLibrary{Downloads: map[string]episodeDownload{"old": {State: "ready"}}}
	model.panelLibrary = old
	model.update(interfacePanelMsg{library: library})
	if model.panelLibrary != library {
		t.Fatal("newer panel snapshot was ignored")
	}

	withConfigDir(t)
	model.width, model.height = 100, 30
	model.presentation = defaultInterfaceSettings()
	command := model.update(statusMsg{status: &Status{State: "playing"}})
	if command == nil {
		t.Fatal("visible download panel did not schedule a refresh")
	}
	if _, ok := command().(interfacePanelMsg); !ok {
		t.Fatal("visible download panel refresh did not load podcast state")
	}
}

// TestASCIIFallbackBoundsActualVisualizerModes covers generated decorations.
func TestASCIIFallbackBoundsActualVisualizerModes(t *testing.T) {
	settings := defaultInterfaceSettings()
	settings.ColorMode, settings.CharacterMode = "none", "ascii"
	var renderer visualizer.Renderer
	frame := audio.Frame{At: time.Now(), Peak: [2]float64{0.8, 0.6}, RMS: [2]float64{0.5, 0.4}}
	for i := range frame.Spectrum {
		frame.Spectrum[i] = 0.2 + float64(i%5)/10
	}
	for channel := range frame.Wave {
		for i := range frame.Wave[channel] {
			frame.Wave[channel][i] = float64((i+channel)%11-5) / 5
		}
	}
	for step := range 12 {
		frame.At = frame.At.Add(time.Duration(step+1) * time.Millisecond)
		renderer.Advance(frame, frame.At)
	}
	for _, mode := range visualizer.Modes {
		view := tea.NewView(renderer.Render(mode, 40, 8) + "\n⏸ ±30s")
		content := settings.decorateView(view, 40).Content
		for _, r := range content {
			if r > 127 {
				t.Fatalf("ASCII mode retained %q in %s", r, mode)
			}
		}
		for _, line := range strings.Split(content, "\n") {
			if width := ansi.StringWidth(line); width > 40 {
				t.Fatalf("ASCII %s row width = %d: %q", mode, width, line)
			}
		}
	}
}

// TestAppearanceCancelRestoresVisualizerState checks live-preview rollback.
func TestAppearanceCancelRestoresVisualizerState(t *testing.T) {
	withConfigDir(t)
	setInterfaceSessionOverrides(interfaceSessionOverrides{})
	_, _, _ = activateInterfaceSettings(defaultInterfaceSettings())
	model := newTUI()
	model.width, model.height = 100, 30
	model.viz.enabled, model.viz.focused, model.viz.fullscreen = true, true, true
	model.openAppearance()
	for index, row := range model.appearanceRows() {
		if row.id == "simplified" {
			model.appearance.selected = index
			break
		}
	}
	model.adjustAppearance(1)
	if model.viz.enabled || model.viz.focused || model.viz.fullscreen {
		t.Fatal("simplified preview did not suspend the visualizer")
	}
	model.closeAppearance(false)
	if !model.viz.enabled || !model.viz.focused || !model.viz.fullscreen {
		t.Fatalf("cancel did not restore visualizer state: %+v", model.viz)
	}
}

// TestInitialDirectoryWarningSurvivesAsyncLoad checks fallback disclosure.
func TestInitialDirectoryWarningSurvivesAsyncLoad(t *testing.T) {
	withConfigDir(t)
	model := newTUI()
	model.libraryUI.open = true
	model.libraryUI.page = "home"
	model.presentation.InitialDirectory = filepath.Join(t.TempDir(), "missing")
	command := model.librarySelect(3)
	if command == nil {
		t.Fatal("file browser selection did not start loading")
	}
	message := command()
	result, ok := message.(libraryResultMsg)
	if !ok {
		t.Fatalf("file browser returned %T", message)
	}
	if !strings.Contains(result.note, "unavailable") {
		t.Fatalf("directory fallback warning was lost: %q", result.note)
	}
	model.update(result)
	if model.libraryUI.loading {
		t.Fatal("directory fallback warning triggered another load")
	}
	if !strings.Contains(model.libraryUI.note, "unavailable") {
		t.Fatalf("directory fallback warning was not displayed: %q", model.libraryUI.note)
	}
}
