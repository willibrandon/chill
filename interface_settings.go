package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

const interfaceSettingsVersion = 1

var interfaceSettingsMu sync.RWMutex
var interfacePersistenceMu sync.Mutex
var activeInterface = defaultInterfaceSettings()
var activeTheme interfaceTheme
var activeInterfaceOverrides interfaceSessionOverrides
var interfaceASCIIReplacer = strings.NewReplacer(
	"│", "|", "─", "-", "┊", "|", "╭", "+", "╮", "+", "╰", "+", "╯", "+", "┌", "+", "┐", "+", "└", "+", "┘", "+", "├", "+", "┤", "+", "┬", "+", "┴", "+", "┼", "+",
	"▶", ">", "▸", ">", "❯", ">", "›", ">", "←", "<", "→", ">", "↑", "^", "↓", "v", "•", "*", "●", "*", "▪", "*", "◆", "*", "▰", "#", "★", "*", "✓", "x", "♥", "*", "·", "-", "…", ".", "±", "+/-", "“", "\"", "”", "\"", "—", "-", "–", "-", "⏸", "||", "♪", "*", "♫", "*", "█", "#", "▉", "#", "▇", "#", "▆", "#", "▅", "#", "▄", "=", "▃", "=", "▂", "-", "▁", "-", "░", ".", "▒", ":", "▓", "#", "━", "-", "¦", "|",
	"⠋", "*", "⠙", "*", "⠹", "*", "⠸", "*", "⠼", "*", "⠴", "*", "⠦", "*", "⠧", "*", "⠇", "*", "⠏", "*",
)

type interfaceSessionOverrides struct {
	Theme      string // Theme temporarily replaces the persisted theme.
	NoColor    bool   // NoColor forces plain terminal output.
	Simplified bool   // Simplified forces the accessible inline layout.
	LowPower   bool   // LowPower forces reduced background activity.
}

// interfaceSettings contains durable terminal presentation preferences.
type interfaceSettings struct {
	Version          int                 `json:"version"`                   // Version identifies the settings schema.
	Theme            string              `json:"theme"`                     // Theme selects a built-in or user palette.
	ColorMode        string              `json:"color_mode"`                // ColorMode is auto, truecolor, ansi256, ansi16, or none.
	CharacterMode    string              `json:"character_mode"`            // CharacterMode is auto, unicode, or ascii.
	Simplified       bool                `json:"simplified"`                // Simplified reduces decoration for assistive technology.
	LowPower         bool                `json:"low_power"`                 // LowPower reduces redraws and disables animation.
	VisualizerHeight int                 `json:"visualizer_height"`         // VisualizerHeight is the requested graph height.
	ShowStatus       bool                `json:"show_status"`               // ShowStatus controls the persistent status row.
	ShowHelp         bool                `json:"show_help_hints"`           // ShowHelp controls contextual key hints.
	StatusFields     []string            `json:"status_fields"`             // StatusFields selects and orders status values.
	SeekStep         int                 `json:"seek_step"`                 // SeekStep is the normal seek distance in seconds.
	SeekLargeStep    int                 `json:"seek_large_step"`           // SeekLargeStep is the page seek distance in seconds.
	InitialDirectory string              `json:"initial_browser_directory"` // InitialDirectory is where file browsing begins.
	DefaultScreen    string              `json:"default_screen"`            // DefaultScreen selects the initial TUI surface.
	Panels           map[string]bool     `json:"panels"`                    // Panels controls optional information panels.
	Bindings         map[string][]string `json:"bindings,omitzero"`         // Bindings contains action-specific key overrides.
}

// interfaceTheme is a complete terminal color palette.
type interfaceTheme struct {
	Name        string `json:"name"`                 // Name identifies the theme.
	Description string `json:"description"`          // Description summarizes the intended use.
	Background  string `json:"background"`           // Background is the primary canvas color.
	Foreground  string `json:"foreground"`           // Foreground is the normal text color.
	Bright      string `json:"bright"`               // Bright is prominent text.
	Muted       string `json:"muted"`                // Muted is secondary text.
	Accent      string `json:"accent"`               // Accent marks interactive controls.
	Secondary   string `json:"secondary"`            // Secondary distinguishes related controls.
	Success     string `json:"success"`              // Success marks positive state.
	Warning     string `json:"warning"`              // Warning marks cautionary state.
	Error       string `json:"error"`                // Error marks failures.
	SelectionFG string `json:"selection_foreground"` // SelectionFG is selected text.
	SelectionBG string `json:"selection_background"` // SelectionBG is the selection fill.
	Border      string `json:"border"`               // Border draws rules and tracks.
	StatusFG    string `json:"status_foreground"`    // StatusFG is status-bar text.
	StatusBG    string `json:"status_background"`    // StatusBG is the status-bar fill.
}

var builtinInterfaceThemes = []interfaceTheme{
	{
		Name: "Midnight", Description: "A calm dark palette", Background: "#111318", Foreground: "#E8EAF0", Bright: "#FFFFFF", Muted: "#A7ABB7",
		Accent: "#78B7FF", Secondary: "#70D7E5", Success: "#78D6A1", Warning: "#F4C95D", Error: "#FF8A96", SelectionFG: "#08111B", SelectionBG: "#9BD2FF", Border: "#737987", StatusFG: "#101319", StatusBG: "#E8EAF0",
	},
	{
		Name: "Paper", Description: "A clear light palette", Background: "#FAF8F3", Foreground: "#20242B", Bright: "#000000", Muted: "#5E6470",
		Accent: "#005FCC", Secondary: "#007482", Success: "#167548", Warning: "#855B00", Error: "#B42332", SelectionFG: "#FFFFFF", SelectionBG: "#005FCC", Border: "#777C84", StatusFG: "#FFFFFF", StatusBG: "#30353D",
	},
	{
		Name: "High Contrast", Description: "Maximum separation for low vision", Background: "#000000", Foreground: "#FFFFFF", Bright: "#FFFFFF", Muted: "#D9D9D9",
		Accent: "#00FFFF", Secondary: "#FFFF00", Success: "#00FF66", Warning: "#FFD700", Error: "#FF6B6B", SelectionFG: "#000000", SelectionBG: "#FFFFFF", Border: "#FFFFFF", StatusFG: "#000000", StatusBG: "#FFFFFF",
	},
	{
		Name: "Monochrome", Description: "A grayscale palette without color cues", Background: "#111111", Foreground: "#E6E6E6", Bright: "#FFFFFF", Muted: "#A6A6A6",
		Accent: "#D6D6D6", Secondary: "#BDBDBD", Success: "#E6E6E6", Warning: "#CACACA", Error: "#FFFFFF", SelectionFG: "#111111", SelectionBG: "#E6E6E6", Border: "#8C8C8C", StatusFG: "#111111", StatusBG: "#E6E6E6",
	},
	{
		Name: "Colorblind Dark", Description: "A dark palette based on distinguishable hues", Background: "#101418", Foreground: "#F2F2F2", Bright: "#FFFFFF", Muted: "#B3B8BD",
		Accent: "#56B4E9", Secondary: "#E69F00", Success: "#009E73", Warning: "#F0E442", Error: "#FF7189", SelectionFG: "#101418", SelectionBG: "#F0E442", Border: "#8A9299", StatusFG: "#101418", StatusBG: "#F2F2F2",
	},
}

func defaultInterfaceSettings() interfaceSettings {
	return interfaceSettings{
		Version: interfaceSettingsVersion, Theme: "Midnight", ColorMode: "auto", CharacterMode: "auto",
		VisualizerHeight: 6, ShowStatus: true, ShowHelp: true,
		StatusFields: []string{"state", "position", "title", "queue", "shuffle", "repeat", "volume", "muted", "equalizer", "network", "audio", "storage-error"},
		SeekStep:     5, SeekLargeStep: 30, InitialDirectory: "~", DefaultScreen: "prompt",
		Panels:   map[string]bool{"source": true, "queue": true, "equalizer": true, "audio": true, "downloads": true, "network": true, "metadata": true},
		Bindings: map[string][]string{},
	}
}

func interfaceSettingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "chill", "interface.json")
}

func userThemesDirectory() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "chill", "themes")
}

func loadInterfaceSettings() (interfaceSettings, error) {
	interfacePersistenceMu.Lock()
	defer interfacePersistenceMu.Unlock()
	return loadInterfaceSettingsUnlocked()
}

func loadInterfaceSettingsUnlocked() (interfaceSettings, error) {
	settings, err := loadInterfaceSettingsUnvalidatedUnlocked()
	if err != nil {
		return defaultInterfaceSettings(), err
	}
	path := interfaceSettingsPath()
	if err := normalizeInterfaceSettingsWithoutTheme(&settings); err != nil {
		return defaultInterfaceSettings(), fmt.Errorf("%s: %w", path, err)
	}
	if _, _, err := resolveInterfaceTheme(settings.Theme); err != nil {
		settings.Theme = defaultInterfaceSettings().Theme
	}
	return settings, nil
}

func loadInterfaceSettingsUnvalidatedUnlocked() (interfaceSettings, error) {
	settings := defaultInterfaceSettings()
	path := interfaceSettingsPath()
	if path == "" {
		return settings, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return defaultInterfaceSettings(), fmt.Errorf("%s: %w", path, err)
	}
	return settings, nil
}

func resetInterfaceBindings(action string) (interfaceSettings, error) {
	interfacePersistenceMu.Lock()
	defer interfacePersistenceMu.Unlock()
	settings, err := loadInterfaceSettingsUnvalidatedUnlocked()
	if err != nil {
		return defaultInterfaceSettings(), err
	}
	if settings.Bindings == nil || action == "" {
		settings.Bindings = map[string][]string{}
	} else {
		delete(settings.Bindings, action)
	}
	if err := saveInterfaceSettingsUnlocked(settings); err != nil {
		return defaultInterfaceSettings(), err
	}
	return settings, nil
}

func saveInterfaceSettings(settings interfaceSettings) error {
	interfacePersistenceMu.Lock()
	defer interfacePersistenceMu.Unlock()
	return saveInterfaceSettingsUnlocked(settings)
}

func saveInterfaceSettingsUnlocked(settings interfaceSettings) error {
	if err := normalizeInterfaceSettings(&settings); err != nil {
		return err
	}
	data, err := json.Marshal(&settings, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	path := interfaceSettingsPath()
	if path == "" {
		return errors.New("no config directory on this system")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".interface-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func normalizeInterfaceSettings(settings *interfaceSettings) error {
	if err := normalizeInterfaceSettingsWithoutTheme(settings); err != nil {
		return err
	}
	if _, _, err := resolveInterfaceTheme(settings.Theme); err != nil {
		return err
	}
	return nil
}

func normalizeInterfaceSettingsWithoutTheme(settings *interfaceSettings) error {
	if settings.Version != 0 && settings.Version != interfaceSettingsVersion {
		return fmt.Errorf("unsupported interface settings version %d", settings.Version)
	}
	settings.Version = interfaceSettingsVersion
	settings.Theme = strings.TrimSpace(settings.Theme)
	if settings.Theme == "" {
		settings.Theme = "Midnight"
	}
	settings.ColorMode = strings.ToLower(strings.TrimSpace(settings.ColorMode))
	if !slices.Contains([]string{"auto", "truecolor", "ansi256", "ansi16", "none"}, settings.ColorMode) {
		return fmt.Errorf("unknown color mode %q", settings.ColorMode)
	}
	settings.CharacterMode = strings.ToLower(strings.TrimSpace(settings.CharacterMode))
	if !slices.Contains([]string{"auto", "unicode", "ascii"}, settings.CharacterMode) {
		return fmt.Errorf("unknown character mode %q", settings.CharacterMode)
	}
	settings.VisualizerHeight = max(0, min(settings.VisualizerHeight, 20))
	if settings.SeekStep <= 0 || settings.SeekStep > 3600 {
		return errors.New("seek step must be between 1 and 3600 seconds")
	}
	if settings.SeekLargeStep <= 0 || settings.SeekLargeStep > 3600 {
		return errors.New("large seek step must be between 1 and 3600 seconds")
	}
	settings.DefaultScreen = strings.ToLower(strings.TrimSpace(settings.DefaultScreen))
	if !slices.Contains([]string{"prompt", "visualizer", "podcasts", "equalizer", "radio", "lyrics", "library", "providers", "audio", "interface"}, settings.DefaultScreen) {
		return fmt.Errorf("unknown default screen %q", settings.DefaultScreen)
	}
	validFields := map[string]bool{"state": true, "position": true, "title": true, "queue": true, "shuffle": true, "repeat": true, "volume": true, "muted": true, "equalizer": true, "network": true, "audio": true, "speed": true, "sleep": true, "storage-error": true}
	seen := map[string]bool{}
	fields := settings.StatusFields[:0]
	for _, field := range settings.StatusFields {
		field = strings.ToLower(strings.TrimSpace(field))
		if !validFields[field] {
			return fmt.Errorf("unknown status field %q", field)
		}
		if !seen[field] {
			fields = append(fields, field)
			seen[field] = true
		}
	}
	for _, field := range []string{"shuffle", "repeat", "muted", "storage-error"} {
		if !seen[field] {
			fields = append(fields, field)
			seen[field] = true
		}
	}
	settings.StatusFields = fields
	if settings.InitialDirectory == "" {
		settings.InitialDirectory = "~"
	}
	if settings.Panels == nil {
		settings.Panels = map[string]bool{}
	}
	for panel := range settings.Panels {
		if !slices.Contains(interfacePanelNames(), panel) {
			return fmt.Errorf("unknown panel %q", panel)
		}
	}
	if settings.Bindings == nil {
		settings.Bindings = map[string][]string{}
	}
	for action, keys := range settings.Bindings {
		actionInfo, ok := keyActionByID(action)
		if !ok {
			return fmt.Errorf("unknown key action %q", action)
		}
		normalized := make([]string, 0, len(keys))
		for _, key := range keys {
			key = normalizeKeyName(key)
			if !validKeyName(key) {
				return fmt.Errorf("invalid key %q for action %q", key, action)
			}
			if actionInfo.scope == "global" && !validGlobalKeyName(key) {
				return fmt.Errorf("global key %q for action %q needs a modifier or function key", key, action)
			}
			if !slices.Contains(normalized, key) {
				normalized = append(normalized, key)
			}
		}
		if len(normalized) == 0 {
			return fmt.Errorf("action %q needs at least one key", action)
		}
		settings.Bindings[action] = normalized
	}
	if conflicts := keyBindingConflicts(*settings); len(conflicts) > 0 {
		return errors.New(strings.Join(conflicts, "; "))
	}
	return nil
}

func interfacePanelNames() []string {
	return []string{"source", "queue", "equalizer", "audio", "downloads", "network", "metadata"}
}

func availableInterfaceThemes() ([]interfaceTheme, []error) {
	themes := slices.Clone(builtinInterfaceThemes)
	directory := userThemesDirectory()
	if directory == "" {
		return themes, nil
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return themes, nil
	}
	if err != nil {
		return themes, []error{err}
	}
	var problems []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			problems = append(problems, fmt.Errorf("%s: %w", path, readErr))
			continue
		}
		var theme interfaceTheme
		if decodeErr := json.Unmarshal(data, &theme); decodeErr != nil {
			problems = append(problems, fmt.Errorf("%s: %w", path, decodeErr))
			continue
		}
		if validateErr := validateInterfaceTheme(theme); validateErr != nil {
			problems = append(problems, fmt.Errorf("%s: %w", path, validateErr))
			continue
		}
		replaced := false
		for i := range themes {
			if strings.EqualFold(themes[i].Name, theme.Name) {
				themes[i], replaced = theme, true
				break
			}
		}
		if !replaced {
			themes = append(themes, theme)
		}
	}
	return themes, problems
}

func resolveInterfaceTheme(name string) (interfaceTheme, string, error) {
	themes, problems := availableInterfaceThemes()
	for _, theme := range themes {
		if strings.EqualFold(strings.TrimSpace(name), theme.Name) {
			return theme, "", nil
		}
	}
	note := ""
	if len(problems) > 0 {
		note = problems[0].Error()
	}
	return interfaceTheme{}, note, fmt.Errorf("unknown or invalid theme %q", name)
}

func validateInterfaceTheme(theme interfaceTheme) error {
	if strings.TrimSpace(theme.Name) == "" {
		return errors.New("theme name is required")
	}
	colors := map[string]string{
		"background": theme.Background, "foreground": theme.Foreground, "bright": theme.Bright, "muted": theme.Muted,
		"accent": theme.Accent, "secondary": theme.Secondary, "success": theme.Success, "warning": theme.Warning,
		"error": theme.Error, "selection_foreground": theme.SelectionFG, "selection_background": theme.SelectionBG,
		"border": theme.Border, "status_foreground": theme.StatusFG, "status_background": theme.StatusBG,
	}
	for field, value := range colors {
		if _, err := parseHexColor(value); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
	}
	checks := []struct {
		name, foreground, background string
		minimum                      float64
	}{
		{"foreground/background", theme.Foreground, theme.Background, 4.5},
		{"bright/background", theme.Bright, theme.Background, 4.5},
		{"muted/background", theme.Muted, theme.Background, 3.0},
		{"accent/background", theme.Accent, theme.Background, 3.0},
		{"secondary/background", theme.Secondary, theme.Background, 3.0},
		{"success/background", theme.Success, theme.Background, 3.0},
		{"warning/background", theme.Warning, theme.Background, 3.0},
		{"error/background", theme.Error, theme.Background, 3.0},
		{"border/background", theme.Border, theme.Background, 3.0},
		{"selection", theme.SelectionFG, theme.SelectionBG, 4.5},
		{"status", theme.StatusFG, theme.StatusBG, 4.5},
	}
	for _, check := range checks {
		if ratio := colorContrast(check.foreground, check.background); ratio < check.minimum {
			return fmt.Errorf("%s contrast %.2f is below %.1f", check.name, ratio, check.minimum)
		}
	}
	return nil
}

func parseHexColor(value string) ([3]float64, error) {
	var result [3]float64
	if len(value) != 7 || value[0] != '#' {
		return result, fmt.Errorf("%q is not #RRGGBB", value)
	}
	for i := range 3 {
		n, err := strconv.ParseUint(value[1+i*2:3+i*2], 16, 8)
		if err != nil {
			return result, fmt.Errorf("%q is not #RRGGBB", value)
		}
		result[i] = float64(n) / 255
	}
	return result, nil
}

func relativeLuminance(value string) float64 {
	rgb, _ := parseHexColor(value)
	for i, component := range rgb {
		if component <= 0.04045 {
			rgb[i] = component / 12.92
		} else {
			rgb[i] = math.Pow((component+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*rgb[0] + 0.7152*rgb[1] + 0.0722*rgb[2]
}

func colorContrast(foreground, background string) float64 {
	first, second := relativeLuminance(foreground), relativeLuminance(background)
	if first < second {
		first, second = second, first
	}
	return (first + 0.05) / (second + 0.05)
}

func setActiveInterface(settings interfaceSettings) (string, error) {
	if err := normalizeInterfaceSettings(&settings); err != nil {
		fallback := defaultInterfaceSettings()
		interfaceSettingsMu.Lock()
		activeInterface = fallback
		activeTheme = builtinInterfaceThemes[0]
		interfaceSettingsMu.Unlock()
		applyInterfaceTheme(builtinInterfaceThemes[0], fallback)
		return "using Midnight because interface settings are invalid: " + err.Error(), err
	}
	theme, note, err := resolveInterfaceTheme(settings.Theme)
	if err != nil {
		fallback := defaultInterfaceSettings()
		interfaceSettingsMu.Lock()
		activeInterface = fallback
		activeTheme = builtinInterfaceThemes[0]
		interfaceSettingsMu.Unlock()
		applyInterfaceTheme(builtinInterfaceThemes[0], fallback)
		return "using Midnight because " + err.Error(), err
	}
	interfaceSettingsMu.Lock()
	activeInterface = settings
	activeTheme = theme
	interfaceSettingsMu.Unlock()
	applyInterfaceTheme(theme, settings)
	return note, nil
}

func setInterfaceSessionOverrides(overrides interfaceSessionOverrides) {
	interfaceSettingsMu.Lock()
	activeInterfaceOverrides = overrides
	interfaceSettingsMu.Unlock()
}

func applyInterfaceSessionOverrides(settings interfaceSettings) interfaceSettings {
	interfaceSettingsMu.RLock()
	overrides := activeInterfaceOverrides
	interfaceSettingsMu.RUnlock()
	if overrides.Theme != "" {
		settings.Theme = overrides.Theme
	}
	if overrides.NoColor {
		settings.ColorMode = "none"
	}
	if overrides.Simplified {
		settings.Simplified = true
	}
	if overrides.LowPower {
		settings.LowPower = true
	}
	return settings
}

func activateInterfaceSettings(settings interfaceSettings) (interfaceSettings, string, error) {
	effective := applyInterfaceSessionOverrides(settings)
	note, err := setActiveInterface(effective)
	return effective, note, err
}

func interfaceSessionOverrideNote() string {
	interfaceSettingsMu.RLock()
	overrides := activeInterfaceOverrides
	interfaceSettingsMu.RUnlock()
	var values []string
	if overrides.Theme != "" {
		values = append(values, "theme "+overrides.Theme)
	}
	if overrides.NoColor {
		values = append(values, "no color")
	}
	if overrides.Simplified {
		values = append(values, "simplified")
	}
	if overrides.LowPower {
		values = append(values, "low power")
	}
	if len(values) == 0 {
		return ""
	}
	return "Session override: " + strings.Join(values, ", ")
}

func currentInterfaceSettings() interfaceSettings {
	interfaceSettingsMu.RLock()
	defer interfaceSettingsMu.RUnlock()
	settings := activeInterface
	settings.StatusFields = slices.Clone(settings.StatusFields)
	settings.Panels = cloneBoolMap(settings.Panels)
	settings.Bindings = cloneBindings(settings.Bindings)
	return settings
}

func effectiveInterfaceSettings(settings interfaceSettings) interfaceSettings {
	if settings.Version == 0 {
		return currentInterfaceSettings()
	}
	return settings
}

func currentInterfaceTheme() interfaceTheme {
	interfaceSettingsMu.RLock()
	defer interfaceSettingsMu.RUnlock()
	if activeTheme.Name == "" {
		return builtinInterfaceThemes[0]
	}
	return activeTheme
}

func cloneBoolMap(input map[string]bool) map[string]bool {
	result := make(map[string]bool, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneBindings(input map[string][]string) map[string][]string {
	result := make(map[string][]string, len(input))
	for key, value := range input {
		result[key] = slices.Clone(value)
	}
	return result
}

func applyInterfaceTheme(theme interfaceTheme, settings interfaceSettings) {
	if settings.ColorMode == "none" {
		styleDim, stylePrompt, styleInput, styleCommand = lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle()
		styleStation, styleSuccess, styleSelected, styleError, styleHeading = lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle()
		styleBorder, styleTitle, styleTrack, styleThumb, styleStatus = lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle()
		styleSelection, styleFlash, styleNotice = lipgloss.NewStyle().Reverse(true), lipgloss.NewStyle().Reverse(true), lipgloss.NewStyle().Reverse(true)
		setCLIColors(settings, theme)
		return
	}
	background := terminalThemeColor(theme.Background, settings.ColorMode)
	base := func(color string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(terminalThemeColor(color, settings.ColorMode)).Background(background)
	}
	styleDim = base(theme.Muted)
	stylePrompt = base(theme.Accent)
	styleInput = base(theme.Foreground)
	styleCommand = base(theme.Secondary)
	styleStation = base(theme.Warning)
	styleSuccess = base(theme.Success)
	styleSelected = base(theme.Bright)
	styleError = base(theme.Error)
	styleHeading = base(theme.Warning)
	styleBorder = base(theme.Border)
	styleTitle = base(theme.Bright)
	styleTrack = base(theme.Border)
	styleThumb = base(theme.Muted)
	styleStatus = lipgloss.NewStyle().Foreground(terminalThemeColor(theme.StatusFG, settings.ColorMode)).Background(terminalThemeColor(theme.StatusBG, settings.ColorMode))
	styleSelection = lipgloss.NewStyle().Foreground(terminalThemeColor(theme.SelectionFG, settings.ColorMode)).Background(terminalThemeColor(theme.SelectionBG, settings.ColorMode))
	styleFlash = styleSelection
	styleNotice = lipgloss.NewStyle().Foreground(terminalThemeColor(theme.SelectionFG, settings.ColorMode)).Background(terminalThemeColor(theme.SelectionBG, settings.ColorMode))
	setCLIColors(settings, theme)
}

func terminalThemeColor(value, mode string) color.Color {
	limit := 0
	if mode == "ansi16" {
		limit = 16
	} else if mode == "ansi256" {
		limit = 256
	}
	if limit == 0 {
		return lipgloss.Color(value)
	}
	rgb, err := parseHexColor(value)
	if err != nil {
		return lipgloss.Color(value)
	}
	return lipgloss.Color(strconv.Itoa(nearestANSIIndex(int(rgb[0]*255), int(rgb[1]*255), int(rgb[2]*255), limit)))
}

func resolvedCLIColorMode(requested string) string {
	if requested != "auto" {
		return requested
	}
	switch colorprofile.Detect(os.Stdout, os.Environ()) {
	case colorprofile.TrueColor:
		return "truecolor"
	case colorprofile.ANSI256:
		return "ansi256"
	case colorprofile.ANSI:
		return "ansi16"
	default:
		return "none"
	}
}

func nearestANSIIndex(red, green, blue, limit int) int {
	palette := [][3]int{
		{0, 0, 0}, {128, 0, 0}, {0, 128, 0}, {128, 128, 0}, {0, 0, 128}, {128, 0, 128}, {0, 128, 128}, {192, 192, 192},
		{128, 128, 128}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0}, {0, 0, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
	}
	if limit > 16 {
		levels := []int{0, 95, 135, 175, 215, 255}
		for _, r := range levels {
			for _, g := range levels {
				for _, b := range levels {
					palette = append(palette, [3]int{r, g, b})
				}
			}
		}
		for value := 8; value <= 238; value += 10 {
			palette = append(palette, [3]int{value, value, value})
		}
	}
	best, bestDistance := 0, math.MaxInt
	for index, color := range palette[:min(limit, len(palette))] {
		distance := (red-color[0])*(red-color[0]) + (green-color[1])*(green-color[1]) + (blue-color[2])*(blue-color[2])
		if distance < bestDistance {
			best, bestDistance = index, distance
		}
	}
	return best
}

func interfaceProgramOptions(settings interfaceSettings) []tea.ProgramOption {
	options := []tea.ProgramOption{}
	switch settings.ColorMode {
	case "none":
		options = append(options, tea.WithColorProfile(colorprofile.Ascii))
	case "ansi16":
		options = append(options, tea.WithColorProfile(colorprofile.ANSI))
	case "ansi256":
		options = append(options, tea.WithColorProfile(colorprofile.ANSI256))
	case "truecolor":
		options = append(options, tea.WithColorProfile(colorprofile.TrueColor))
	}
	if settings.LowPower {
		options = append(options, tea.WithFPS(10))
	}
	return options
}

func (settings interfaceSettings) ascii() bool {
	return settings.CharacterMode == "ascii" || settings.CharacterMode == "auto" && strings.EqualFold(os.Getenv("TERM"), "dumb")
}

func (settings interfaceSettings) decorateView(view tea.View, width int) tea.View {
	if settings.Simplified {
		view.AltScreen = false
		view.MouseMode = tea.MouseModeNone
	}
	_, noColorEnvironment := os.LookupEnv("NO_COLOR")
	if settings.ColorMode == "none" || noColorEnvironment {
		view.Content = ansi.Strip(view.Content)
	} else {
		theme := currentInterfaceTheme()
		view.BackgroundColor = terminalThemeColor(theme.Background, settings.ColorMode)
		view.ForegroundColor = terminalThemeColor(theme.Foreground, settings.ColorMode)
	}
	if settings.ascii() {
		view.Content = interfaceASCIIReplacer.Replace(view.Content)
		if width > 0 {
			lines := strings.Split(view.Content, "\n")
			for i, line := range lines {
				lines[i] = ansi.Truncate(line, width, "")
			}
			view.Content = strings.Join(lines, "\n")
		}
	}
	return view
}

func interfaceLayoutTier(width, height int, content, simplified bool) string {
	if width < 40 || height < 10 {
		return "too-small"
	}
	if simplified || width < 60 || height < 16 {
		return "minimal"
	}
	if content {
		return "content-first"
	}
	if width < 96 || height < 24 {
		return "compact"
	}
	return "full"
}

func resolveInitialDirectory(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			if len(value) > 2 {
				return filepath.Join(home, value[2:])
			}
			return home
		}
	}
	if absolute, err := filepath.Abs(value); err == nil {
		return absolute
	}
	return value
}

func runThemeCommand(args []string, jsonOutput bool) (string, error) {
	args, jsonOutput = consumeLocalJSONFlag(args, jsonOutput)
	if helpRequested(args) {
		return "Usage: chill theme [list|show|set <name>|preview <name>|validate [file]] [--json]", nil
	}
	settings, loadErr := loadInterfaceSettings()
	if loadErr != nil {
		return "", loadErr
	}
	command := "show"
	if len(args) > 0 {
		command = strings.ToLower(args[0])
		args = args[1:]
	}
	switch command {
	case "list":
		themes, problems := availableInterfaceThemes()
		if jsonOutput {
			data, err := json.Marshal(&themes, json.Deterministic(true), jsontext.WithIndent("  "))
			return string(data), err
		}
		var lines []string
		for _, theme := range themes {
			marker := " "
			if strings.EqualFold(theme.Name, settings.Theme) {
				marker = "*"
			}
			lines = append(lines, fmt.Sprintf("%s %-18s %s", marker, theme.Name, theme.Description))
		}
		for _, problem := range problems {
			lines = append(lines, "! "+problem.Error())
		}
		return strings.Join(lines, "\n"), nil
	case "show":
		theme, note, err := resolveInterfaceTheme(settings.Theme)
		if err != nil {
			return "", err
		}
		if jsonOutput {
			data, marshalErr := json.Marshal(&theme, json.Deterministic(true), jsontext.WithIndent("  "))
			return string(data), marshalErr
		}
		out := theme.Name + " — " + theme.Description + "\n" + themeSwatches(theme)
		if note != "" {
			out += "\n" + note
		}
		return out, nil
	case "set":
		if len(args) == 0 {
			return "", errors.New("usage: chill theme set <name>")
		}
		name := strings.Join(args, " ")
		theme, _, err := resolveInterfaceTheme(name)
		if err != nil {
			return "", err
		}
		settings.Theme = theme.Name
		if err := saveInterfaceSettings(settings); err != nil {
			return "", err
		}
		if jsonOutput {
			return marshalInterfaceJSON(theme)
		}
		return "theme: " + theme.Name, nil
	case "preview":
		if len(args) == 0 {
			return "", errors.New("usage: chill theme preview <name>")
		}
		theme, _, err := resolveInterfaceTheme(strings.Join(args, " "))
		if err != nil {
			return "", err
		}
		if jsonOutput {
			return marshalInterfaceJSON(theme)
		}
		return theme.Name + " — " + theme.Description + "\n" + themeSwatches(theme), nil
	case "validate":
		if len(args) > 1 {
			return "", errors.New("usage: chill theme validate [file]")
		}
		if len(args) == 1 {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return "", err
			}
			var theme interfaceTheme
			if err := json.Unmarshal(data, &theme); err != nil {
				return "", err
			}
			if err := validateInterfaceTheme(theme); err != nil {
				return "", err
			}
			if jsonOutput {
				return marshalInterfaceJSON(theme)
			}
			return theme.Name + ": valid", nil
		}
		themes, problems := availableInterfaceThemes()
		if len(problems) > 0 {
			messages := make([]string, len(problems))
			for i, problem := range problems {
				messages[i] = problem.Error()
			}
			return "", errors.New(strings.Join(messages, "; "))
		}
		if jsonOutput {
			return marshalInterfaceJSON(map[string]any{"valid": true, "count": len(themes)})
		}
		return fmt.Sprintf("%d themes valid", len(themes)), nil
	default:
		// A bare theme name is the convenient form of `theme set`.
		return runThemeCommand(append([]string{"set", command}, args...), jsonOutput)
	}
}

func marshalInterfaceJSON(value any) (string, error) {
	data, err := json.Marshal(value, json.Deterministic(true), jsontext.WithIndent("  "))
	return string(data), err
}

func consumeLocalJSONFlag(args []string, enabled bool) ([]string, bool) {
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--json" {
			enabled = true
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, enabled
}

func themeSwatches(theme interfaceTheme) string {
	values := []struct{ label, color string }{
		{"foreground", theme.Foreground}, {"accent", theme.Accent}, {"secondary", theme.Secondary}, {"success", theme.Success}, {"warning", theme.Warning}, {"error", theme.Error},
	}
	settings := currentInterfaceSettings()
	mode := resolvedCLIColorMode(settings.ColorMode)
	_, noColorEnvironment := os.LookupEnv("NO_COLOR")
	parts := make([]string, len(values))
	for i, value := range values {
		if mode == "none" || noColorEnvironment {
			parts[i] = value.label + " " + value.color
		} else {
			parts[i] = lipgloss.NewStyle().Foreground(terminalThemeColor(value.color, mode)).Background(terminalThemeColor(theme.Background, mode)).Render("● " + value.label)
		}
	}
	return strings.Join(parts, "  ")
}
