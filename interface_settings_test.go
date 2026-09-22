package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestBuiltInInterfaceThemesMeetContrastRules checks every bundled palette.
func TestBuiltInInterfaceThemesMeetContrastRules(t *testing.T) {
	for _, theme := range builtinInterfaceThemes {
		t.Run(theme.Name, func(t *testing.T) {
			if err := validateInterfaceTheme(theme); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestBuiltInThemesOwnScreenHighlights guards against replacing the palette
// with fixed or screen-global colors. Every interactive surface must render
// the active theme's selection pair, and theme-specific content uses its
// semantic palette roles.
func TestBuiltInThemesOwnScreenHighlights(t *testing.T) {
	withConfigDir(t)
	t.Cleanup(func() { _, _, _ = activateInterfaceSettings(defaultInterfaceSettings()) })
	selectionPrefixes := map[string]string{}
	for _, theme := range builtinInterfaceThemes {
		t.Run(theme.Name, func(t *testing.T) {
			settings := defaultInterfaceSettings()
			settings.Theme = theme.Name
			settings.ColorMode = "truecolor"
			if _, _, err := activateInterfaceSettings(settings); err != nil {
				t.Fatal(err)
			}
			selection, _, found := strings.Cut(styleSelection.Render("selected"), "selected")
			if !found || selection == "" {
				t.Fatal("theme selection style did not emit a color sequence")
			}
			if previous, exists := selectionPrefixes[selection]; exists {
				t.Fatalf("selection palette duplicates %s", previous)
			}
			selectionPrefixes[selection] = theme.Name

			model := newTUI()
			model.width, model.height, model.presentation = 100, 24, settings
			model.appearance.settings = settings
			model.appearance.themes = builtinInterfaceThemes
			model.libraryUI.page, model.libraryUI.title = "home", "Library"
			model.podcasts.page = podcastPage{kind: "home", title: "Podcasts"}
			model.radio.page = radioPage{kind: "home", title: "Radio"}
			model.providersUI.page, model.providersUI.title = "home", "Providers"
			model.providersUI.infos = []providerInfo{{Key: "youtube", Name: "YouTube"}}
			model.eq.config = defaultEqualizerConfig()
			model.suggestions = []suggestion{{text: "play", desc: "play a station"}}

			screens := map[string]string{
				"appearance": model.appearanceView().Content,
				"keys":       model.keyOverlayView().Content,
				"podcasts":   model.podcastView().Content,
				"equalizer":  strings.Join(model.equalizerList(5), "\n"),
				"radio":      model.radioView().Content,
				"library":    model.libraryView().Content,
				"providers":  model.providerView().Content,
				"audio":      model.audioView().Content,
				"palette":    model.palette(),
			}
			for name, content := range screens {
				if !strings.Contains(content, selection) {
					t.Errorf("%s did not use %s's selection palette", name, theme.Name)
				}
			}

			secondary, _, found := strings.Cut(styleCommand.Render("lyrics"), "lyrics")
			if !found || secondary == "" {
				t.Fatal("theme secondary style did not emit a color sequence")
			}
			model.lyrics.lines = []string{"theme-owned lyric color"}
			if content := model.lyricsView().Content; !strings.Contains(content, secondary) {
				t.Errorf("lyrics did not use %s's secondary palette", theme.Name)
			}
		})
	}
}

// TestInterfaceSettingsPersistAndPreserveDefaults checks atomic durable settings.
func TestInterfaceSettingsPersistAndPreserveDefaults(t *testing.T) {
	withConfigDir(t)
	settings := defaultInterfaceSettings()
	settings.Theme = "Paper"
	settings.SeekStep = 12
	settings.Panels["network"] = false
	settings.Bindings["global.library"] = []string{"ctrl+g"}
	if err := saveInterfaceSettings(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadInterfaceSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Theme != "Paper" || loaded.SeekStep != 12 || loaded.Panels["network"] || !loaded.ShowStatus || loaded.Bindings["global.library"][0] != "ctrl+g" {
		t.Fatalf("settings did not round-trip: %+v", loaded)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(interfaceSettingsPath())
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("settings permissions are %o", info.Mode().Perm())
		}
	}
}

// TestUserThemeValidationAndSafeFallback checks custom loading and recovery.
func TestUserThemeValidationAndSafeFallback(t *testing.T) {
	withConfigDir(t)
	if err := os.MkdirAll(userThemesDirectory(), 0o700); err != nil {
		t.Fatal(err)
	}
	theme := builtinInterfaceThemes[0]
	theme.Name = "Ocean"
	data, err := json.Marshal(&theme, jsontext.WithIndent("  "))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userThemesDirectory(), "ocean.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, _, err := resolveInterfaceTheme("ocean")
	if err != nil || resolved.Name != "Ocean" {
		t.Fatalf("custom theme was not loaded: %+v %v", resolved, err)
	}
	bad := theme
	bad.Name, bad.Foreground = "Unreadable", bad.Background
	data, _ = json.Marshal(&bad)
	if err := os.WriteFile(filepath.Join(userThemesDirectory(), "bad.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	settings := defaultInterfaceSettings()
	settings.Theme = "Unreadable"
	if _, err := setActiveInterface(settings); err == nil || currentInterfaceSettings().Theme != "Midnight" {
		t.Fatal("invalid selected theme did not fall back to Midnight")
	}
	setActiveInterface(defaultInterfaceSettings())
}

// TestInvalidPersistedThemePreservesOtherPreferences checks theme recovery is narrow.
func TestInvalidPersistedThemePreservesOtherPreferences(t *testing.T) {
	withConfigDir(t)
	settings := defaultInterfaceSettings()
	settings.Theme = "Missing theme"
	settings.SeekStep = 17
	data, err := json.Marshal(&settings)
	if err != nil {
		t.Fatal(err)
	}
	path := interfaceSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadInterfaceSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Theme != "Midnight" || loaded.SeekStep != 17 {
		t.Fatalf("theme fallback changed unrelated preferences: %+v", loaded)
	}
}

// TestSessionOverridesRemainTemporary checks command-line presentation choices.
func TestSessionOverridesRemainTemporary(t *testing.T) {
	withConfigDir(t)
	t.Cleanup(func() {
		setInterfaceSessionOverrides(interfaceSessionOverrides{})
		setActiveInterface(defaultInterfaceSettings())
	})
	settings := defaultInterfaceSettings()
	settings.Theme = "Paper"
	if err := saveInterfaceSettings(settings); err != nil {
		t.Fatal(err)
	}
	setInterfaceSessionOverrides(interfaceSessionOverrides{Theme: "High Contrast", NoColor: true, LowPower: true})
	effective, _, err := activateInterfaceSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Theme != "High Contrast" || effective.ColorMode != "none" || !effective.LowPower {
		t.Fatalf("session overrides were not applied: %+v", effective)
	}
	loaded, err := loadInterfaceSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Theme != "Paper" || loaded.ColorMode != "auto" || loaded.LowPower {
		t.Fatalf("session overrides leaked into persistence: %+v", loaded)
	}
}

// TestInterfaceOverridesFromArgs checks presentation flags before help parsing.
func TestInterfaceOverridesFromArgs(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	overrides := interfaceOverridesFromArgs([]string{"album.m3u", "--theme=Paper", "--simplified", "--low-power"})
	if overrides.Theme != "Paper" || !overrides.NoColor || !overrides.Simplified || !overrides.LowPower {
		t.Fatalf("argument overrides = %+v", overrides)
	}
}

// TestScopedKeyRemappingAndConflicts checks override and collision semantics.
func TestScopedKeyRemappingAndConflicts(t *testing.T) {
	settings := defaultInterfaceSettings()
	settings.Bindings["global.library"] = []string{"ctrl+g"}
	if got := settings.mapKey("global", "ctrl+g"); got != "f7" {
		t.Fatalf("remapped key became %q", got)
	}
	if got := settings.mapKey("global", "f7"); !strings.HasPrefix(got, "unbound:") {
		t.Fatalf("old default remained active: %q", got)
	}
	settings.Bindings["global.library"] = []string{"ctrl+k"}
	if conflicts := keyBindingConflicts(settings); len(conflicts) == 0 {
		t.Fatal("global conflict was not detected")
	}
	settings = defaultInterfaceSettings()
	settings.Bindings["radio.sort"] = []string{"up"}
	if conflicts := keyBindingConflicts(settings); len(conflicts) == 0 {
		t.Fatal("screen-specific conflict was not detected")
	}
}

// TestKeyRegistryDispatchesEveryDefault keeps help, remapping, and handlers aligned.
func TestKeyRegistryDispatchesEveryDefault(t *testing.T) {
	settings := defaultInterfaceSettings()
	for _, action := range interfaceKeyActions {
		for _, key := range action.defaults {
			resolved, ok := settings.actionFor(action.scope, key)
			if !ok || resolved.id != action.id {
				t.Errorf("%s does not own %q in %s; got %q", action.id, key, action.scope, resolved.id)
			}
		}
	}
}

// TestBrowserKeyScopesExposeOnlyImplementedActions checks inherited bindings.
func TestBrowserKeyScopesExposeOnlyImplementedActions(t *testing.T) {
	settings := defaultInterfaceSettings()
	if action, ok := settings.actionFor("podcasts", "R"); !ok || action.id != "podcast.retry-download" {
		t.Fatalf("podcast R resolved to %q", action.id)
	}
	if rows := strings.Join(interfaceKeyRows(settings, "podcasts", ""), "\n"); strings.Contains(rows, "browser.repeat") {
		t.Fatal("podcast overlay advertised the unavailable queue repeat action")
	}
	settings.Bindings["browser.repeat"] = []string{"t"}
	if got := settings.mapKey("library", "t"); got != "R" {
		t.Fatalf("library repeat remap became %q", got)
	}
	if got := settings.mapKey("podcasts", "t"); got != "t" {
		t.Fatalf("unavailable podcast repeat binding became active as %q", got)
	}
}

// TestInvalidKeyNamesAreRejected checks remaps cannot become unreachable.
func TestInvalidKeyNamesAreRejected(t *testing.T) {
	settings := defaultInterfaceSettings()
	settings.Bindings["global.library"] = []string{"not-a-real-key"}
	if err := normalizeInterfaceSettings(&settings); err == nil || !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("invalid key was accepted: %v", err)
	}
	for _, key := range []string{"a", "+", "f12", "ctrl+g", "shift+left", "alt+enter"} {
		if !validKeyName(key) {
			t.Errorf("valid key %q was rejected", key)
		}
	}
	settings = defaultInterfaceSettings()
	settings.Bindings["global.library"] = []string{"g"}
	if err := normalizeInterfaceSettings(&settings); err == nil || !strings.Contains(err.Error(), "needs a modifier") {
		t.Fatalf("global typing key was accepted: %v", err)
	}
}

// TestAdaptiveLayoutsAndFallbackRendering checks size and capability tiers.
func TestAdaptiveLayoutsAndFallbackRendering(t *testing.T) {
	if got := interfaceLayoutTier(120, 35, false, false); got != "full" {
		t.Fatalf("large terminal selected %q", got)
	}
	if got := interfaceLayoutTier(80, 20, true, false); got != "content-first" {
		t.Fatalf("browser selected %q", got)
	}
	if got := interfaceLayoutTier(80, 20, false, false); got != "compact" {
		t.Fatalf("medium terminal selected %q", got)
	}
	if got := interfaceLayoutTier(50, 12, false, false); got != "minimal" {
		t.Fatalf("small terminal selected %q", got)
	}
	if got := interfaceLayoutTier(30, 8, false, false); got != "too-small" {
		t.Fatalf("tiny terminal selected %q", got)
	}
	settings := defaultInterfaceSettings()
	settings.ColorMode, settings.CharacterMode = "none", "ascii"
	view := tea.NewView("\x1b[31m♪ ─── → ▓ ❯ ┌─┐ ● ★ ✓ ♥ ± “test” ⠋\x1b[0m")
	plain := settings.decorateView(view, 80).Content
	if strings.Contains(plain, "\x1b[") || strings.ContainsAny(plain, "♪─→▓❯┌┐●★✓♥±“”⠋") || ansi.Strip(plain) != plain {
		t.Fatalf("fallback rendering retained terminal decoration: %q", plain)
	}
	settings = defaultInterfaceSettings()
	settings.Simplified = true
	view = tea.NewView("content")
	view.AltScreen, view.MouseMode = true, tea.MouseModeCellMotion
	view = settings.decorateView(view, 80)
	if view.AltScreen || view.MouseMode != tea.MouseModeNone {
		t.Fatal("simplified mode retained alternate-screen or mouse tracking")
	}
}

// TestANSIColorQuantization checks explicit limited-color output modes.
func TestANSIColorQuantization(t *testing.T) {
	if got := nearestANSIIndex(255, 0, 0, 16); got != 9 {
		t.Fatalf("bright red mapped to ANSI %d", got)
	}
	if got := nearestANSIIndex(95, 135, 175, 256); got != 67 {
		t.Fatalf("color cube value mapped to ANSI %d", got)
	}
}

// TestAppearancePreviewCancelAndKeyOverlay checks interactive discovery surfaces.
func TestAppearancePreviewCancelAndKeyOverlay(t *testing.T) {
	withConfigDir(t)
	setActiveInterface(defaultInterfaceSettings())
	model := newTUI()
	model.width, model.height = 100, 28
	model.openAppearance()
	model.adjustAppearance(1)
	if model.presentation.Theme == model.appearance.original.Theme {
		t.Fatal("theme preview did not apply")
	}
	model.closeAppearance(false)
	if model.presentation.Theme != "Midnight" {
		t.Fatal("cancel did not restore the original theme")
	}
	model.toggleKeyOverlay()
	if !model.keyOverlay.open || !strings.Contains(ansi.Strip(model.keyOverlayView().Content), "global.library") {
		t.Fatal("searchable key overlay was not generated from the registry")
	}
}

// TestKeyOverlayPreservesStructuredFields checks long bindings and action IDs
// are rendered from their source fields rather than parsed from padded text.
func TestKeyOverlayPreservesStructuredFields(t *testing.T) {
	model := newTUI()
	model.width, model.height = 160, 12
	model.audioUI.open = true
	model.toggleKeyOverlay()
	model.keyOverlay.query.SetValue("audio.adjust-right")
	plain := ansi.Strip(model.keyOverlayView().Content)
	for _, value := range []string{"right, enter, space", "audio.adjust-right", "Select the next value"} {
		if !strings.Contains(plain, value) {
			t.Errorf("audio binding omitted %q: %q", value, plain)
		}
	}
	if strings.Contains(plain, "right, enter, sp…") {
		t.Fatalf("audio binding was truncated before layout: %q", plain)
	}

	model.audioUI.open = false
	model.keyOverlay.query.SetValue("prompt.suggestion-previous")
	plain = ansi.Strip(model.keyOverlayView().Content)
	if !strings.Contains(plain, "prompt.suggestion-previous") {
		t.Fatalf("long action ID was truncated before layout: %q", plain)
	}
}

// TestLowPowerAndPanelLayout checks reduced work and bounded full-layout cards.
func TestLowPowerAndPanelLayout(t *testing.T) {
	withConfigDir(t)
	model := newTUI()
	model.width, model.height = 100, 30
	model.status = &Status{State: "playing"}
	model.viz.enabled = true
	model.presentation.LowPower = true
	if height := model.visualizerHeight(); height != 0 {
		t.Fatalf("low-power visualizer height = %d", height)
	}
	model.presentation.LowPower = false
	panel := model.panelView()
	lines := strings.Split(panel, "\n")
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > model.width {
			t.Fatalf("panel row is %d cells in a %d-cell terminal", width, model.width)
		}
	}
	if len(lines) != 3 || ansi.StringWidth(lines[2]) != model.width {
		t.Fatalf("incomplete panel row did not fill the terminal: %q", ansi.Strip(lines[2]))
	}
	plain := ansi.Strip(panel)
	if !strings.Contains(plain, "NETWORK online") || strings.Contains(plain, "NETWORK playing") {
		t.Fatalf("network panel reports playback state: %q", plain)
	}
	if got := interfaceNetworkStatus(&Status{State: "reconnecting", Retries: 4}); got != "retry 4" {
		t.Fatalf("reconnecting network status = %q", got)
	}
}

// TestContentLayoutReclaimsOptionalChrome checks compact browser sizing.
func TestContentLayoutReclaimsOptionalChrome(t *testing.T) {
	model := newTUI()
	model.width, model.height = 50, 12
	model.presentation.ShowStatus = false
	layout := model.contentLayout(model.height, false, 2)
	if layout.showHelp || layout.status >= 0 || layout.room != 9 {
		t.Fatalf("minimal content layout retained optional chrome: %+v", layout)
	}
	model.width, model.height = 100, 30
	model.presentation.ShowStatus = true
	layout = model.contentLayout(model.height, false, 2)
	if !layout.showHelp || layout.status != 29 || layout.room != 24 {
		t.Fatalf("full content layout = %+v", layout)
	}
}
