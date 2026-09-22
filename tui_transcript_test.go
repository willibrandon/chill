package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func transcriptLines(model *tui) []string {
	lines := make([]string, len(model.lines))
	for index, line := range model.lines {
		lines[index] = line.render()
	}
	return lines
}

func helpTranscript() *tui {
	model := newTUI()
	model.width, model.height = 160, 30
	model.fit()
	model.start("help")
	model.update(run("help", model.commandID)())
	model.printLine(transcriptSpan{transcriptError, "  error: "}, transcriptSpan{transcriptBody, "example diagnostic"})
	return model
}

// TestTranscriptThemePreview recolors existing output just like newly printed
// output, while preserving its scroll position and selected text.
func TestTranscriptThemePreview(t *testing.T) {
	withConfigDir(t)
	setInterfaceSessionOverrides(interfaceSessionOverrides{})
	t.Cleanup(func() { _, _, _ = activateInterfaceSettings(defaultInterfaceSettings()) })
	for _, theme := range []string{"Midnight", "Monochrome", "High Contrast"} {
		for _, mode := range []string{"truecolor", "ansi256", "ansi16", "none"} {
			t.Run(theme+"/"+mode, func(t *testing.T) {
				settings := defaultInterfaceSettings()
				settings.Theme, settings.ColorMode = theme, mode
				if err := saveInterfaceSettings(settings); err != nil {
					t.Fatal(err)
				}
				if _, _, err := activateInterfaceSettings(settings); err != nil {
					t.Fatal(err)
				}
				model := helpTranscript()
				original := slices.Clone(model.rows)
				model.viewport.SetYOffset(5)
				model.sel = selection{active: true, lines: true, anchor: point{row: 6}, cursor: point{row: 8}}
				selected := model.selectedText()
				model.openAppearance()
				previewTranscriptTheme(t, model, "Paper")
				fresh := helpTranscript()
				if !slices.Equal(model.rows, fresh.rows) {
					t.Fatalf("Paper did not render old output like new output:\n got %q\nwant %q", model.rows, fresh.rows)
				}
				if model.viewport.YOffset() != 5 || !model.sel.active || model.selectedText() != selected {
					t.Fatal("theme preview changed the scroll position or selection")
				}
				previewTranscriptTheme(t, model, "High Contrast")
				model.closeAppearance(false)
				if !slices.Equal(model.rows, original) {
					t.Fatal("canceling repeated theme previews did not restore the original transcript")
				}
			})
		}
	}
}

func previewTranscriptTheme(t *testing.T, model *tui, name string) {
	t.Helper()
	for range len(model.appearance.themes) {
		if model.presentation.Theme == name {
			return
		}
		model.adjustAppearance(1)
	}
	t.Fatalf("theme %q was not available", name)
}

// TestTranscriptThemeCommandAndColorMode updates history through saved commands
// and retains its original styling when colors are temporarily disabled.
func TestTranscriptThemeCommandAndColorMode(t *testing.T) {
	withConfigDir(t)
	setInterfaceSessionOverrides(interfaceSessionOverrides{})
	t.Cleanup(func() { _, _, _ = activateInterfaceSettings(defaultInterfaceSettings()) })
	settings := defaultInterfaceSettings()
	settings.ColorMode = "truecolor"
	if _, _, err := activateInterfaceSettings(settings); err != nil {
		t.Fatal(err)
	}
	model := helpTranscript()
	original := slices.Clone(model.rows)
	for _, change := range []struct{ theme, mode string }{{"Paper", "truecolor"}, {"Paper", "none"}, {"Midnight", "truecolor"}} {
		settings.Theme, settings.ColorMode = change.theme, change.mode
		if err := saveInterfaceSettings(settings); err != nil {
			t.Fatal(err)
		}
		model.running, model.active = true, "theme"
		model.commandID++
		model.update(resultMsg{id: model.commandID})
		if change.mode == "none" {
			view := model.View().Content
			if ansi.Strip(view) != view {
				t.Fatal("disabled colors left styling in the transcript")
			}
		} else if fresh := helpTranscript(); !slices.Equal(model.rows, fresh.rows) {
			t.Fatalf("theme command left old colors after switching to %s", change.theme)
		}
	}
	if !slices.Equal(model.rows, original) {
		t.Fatal("turning colors back on lost the original transcript styling")
	}
	model.clear()
	model.print("new output")
	if got := ansi.Strip(strings.Join(model.rows, "\n")); got != "new output" {
		t.Fatalf("clear retained themed history: %q", got)
	}
}

// TestLightThemeCLIColors keeps standalone ANSI16 labels visible on the terminal
// canvas while the TUI uses its own dark foreground on the Paper canvas.
func TestLightThemeCLIColors(t *testing.T) {
	withConfigDir(t)
	setInterfaceSessionOverrides(interfaceSessionOverrides{})
	t.Cleanup(func() { _, _, _ = activateInterfaceSettings(defaultInterfaceSettings()) })
	settings := defaultInterfaceSettings()
	settings.Theme, settings.ColorMode = "Paper", "ansi16"
	if _, _, err := activateInterfaceSettings(settings); err != nil {
		t.Fatal(err)
	}
	if palette := currentCLIPalette(); palette.cyan != "\x1b[97m" {
		t.Fatalf("standalone ANSI16 labels lost their bright foreground: %q", palette.cyan)
	}
	if output := replHelp(); !strings.HasPrefix(output, "\x1b[97mplay") {
		t.Fatalf("standalone help labels lost their bright foreground: %q", output)
	}
	model := helpTranscript()
	if !strings.Contains(model.rows[1], styleSelected.Render("play")) {
		// The style surrounds the padded label, so compare its opening sequence.
		prefix, _, _ := strings.Cut(styleSelected.Render("play"), "play")
		if !strings.Contains(model.rows[1], prefix+"play") {
			t.Fatalf("REPL help labels did not use the Paper foreground: %q", model.rows[1])
		}
	}
}

// TestTranscriptThemeSwatchesStayLiteral preserves the colors that theme show
// describes, even when the surrounding interface switches to another palette.
func TestTranscriptThemeSwatchesStayLiteral(t *testing.T) {
	withConfigDir(t)
	t.Setenv("NO_COLOR", "")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	setInterfaceSessionOverrides(interfaceSessionOverrides{})
	t.Cleanup(func() { _, _, _ = activateInterfaceSettings(defaultInterfaceSettings()) })
	settings := defaultInterfaceSettings()
	settings.ColorMode = "truecolor"
	if err := saveInterfaceSettings(settings); err != nil {
		t.Fatal(err)
	}
	if _, _, err := activateInterfaceSettings(settings); err != nil {
		t.Fatal(err)
	}
	model := newTUI()
	model.width, model.height = 180, 40
	model.fit()
	model.start("theme show")
	result := run("theme show", model.commandID)().(resultMsg)
	_, swatches, _ := strings.Cut(result.out, "\n")
	if swatches == "" || ansi.Strip(swatches) == swatches {
		t.Fatal("fixture did not produce colored swatches")
	}
	model.update(result)
	swatchIndex := len(model.lines) - 1
	model.start("theme set Paper")
	model.update(run("theme set Paper", model.commandID)())
	if got := model.lines[swatchIndex].render(); got != styleDim.Render("  ┊ ")+swatches {
		t.Fatalf("Midnight swatches changed on Paper:\n got %q\nwant %q", got, swatches)
	}
	if !strings.Contains(ansi.Strip(strings.Join(transcriptLines(model), "\n")), "Midnight") {
		t.Fatal("swatch heading was lost")
	}
	model.openAppearance()
	previewTranscriptTheme(t, model, "Monochrome")
	model.closeAppearance(false)
	if got := model.lines[swatchIndex].render(); got != styleDim.Render("  ┊ ")+swatches {
		t.Fatal("preview or cancellation changed explicit swatch colors")
	}
}

// TestTranscriptDelayedOutputUsesCurrentTheme keeps output produced before a
// theme change readable when its asynchronous result arrives afterward.
func TestTranscriptDelayedOutputUsesCurrentTheme(t *testing.T) {
	withConfigDir(t)
	setInterfaceSessionOverrides(interfaceSessionOverrides{})
	t.Cleanup(func() { _, _, _ = activateInterfaceSettings(defaultInterfaceSettings()) })
	settings := defaultInterfaceSettings()
	settings.Theme, settings.ColorMode = "Monochrome", "ansi16"
	if err := saveInterfaceSettings(settings); err != nil {
		t.Fatal(err)
	}
	if _, _, err := activateInterfaceSettings(settings); err != nil {
		t.Fatal(err)
	}
	model := newTUI()
	model.width, model.height = 160, 30
	model.fit()
	model.start("help")
	result := run("help", model.commandID)()
	model.openAppearance()
	previewTranscriptTheme(t, model, "Paper")
	model.closeAppearance(true)
	model.update(result)
	model.printLine(transcriptSpan{transcriptError, "  error: "}, transcriptSpan{transcriptBody, "example diagnostic"})
	if fresh := helpTranscript(); !slices.Equal(model.rows, fresh.rows) {
		t.Fatal("delayed output retained its producer's old colors")
	}
}
