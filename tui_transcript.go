package main

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type transcriptRole uint8

const (
	transcriptLiteral transcriptRole = iota // Explicit command data, including theme swatches.
	transcriptBody
	transcriptDim
	transcriptPrompt
	transcriptCommand
	transcriptStation
	transcriptError
	transcriptBright
)

// transcriptSpan records the intended style before colors are rendered. Roles
// remain distinct even when a theme or terminal gives them identical colors.
type transcriptSpan struct {
	role transcriptRole
	text string
}

type transcriptLine []transcriptSpan

func (span transcriptSpan) render() string {
	var style lipgloss.Style
	switch span.role {
	case transcriptBody:
		style = styleInput
	case transcriptDim:
		style = styleDim
	case transcriptPrompt:
		style = stylePrompt
	case transcriptCommand:
		style = styleCommand
	case transcriptStation:
		style = styleStation
	case transcriptError:
		style = styleError
	case transcriptBright:
		style = styleSelected
	default:
		return span.text
	}
	return style.Render(span.text)
}

func (line transcriptLine) render() string {
	var out strings.Builder
	for _, span := range line {
		out.WriteString(span.render())
	}
	return out.String()
}

// renderCLI renders shared help and station data using the standalone palette.
func (line transcriptLine) renderCLI(palette cliPalette) string {
	var out strings.Builder
	for _, span := range line {
		var prefix string
		switch span.role {
		case transcriptDim:
			prefix = palette.dim
		case transcriptPrompt:
			prefix = palette.pink
		case transcriptCommand:
			prefix = palette.purple
		case transcriptBright:
			prefix = palette.cyan
		}
		if prefix == "" {
			out.WriteString(span.text)
		} else {
			out.WriteString(prefix + span.text + palette.reset)
		}
	}
	return out.String()
}

func renderCLITranscript(lines []transcriptLine) string {
	palette := currentCLIPalette()
	out := make([]string, len(lines))
	for index, line := range lines {
		out[index] = line.renderCLI(palette)
	}
	return strings.Join(out, "\n")
}

func (t *tui) printOutput(line transcriptLine) {
	spans := make(transcriptLine, 0, len(line)+1)
	spans = append(spans, transcriptSpan{transcriptDim, "  ┊ "})
	t.printLine(append(spans, line...)...)
}

// Command responses without structured styles use the current body style.
// Explicit color data is kept literal by the command that supplies it.
func commandOutputLine(line string) transcriptLine {
	return transcriptLine{{transcriptBody, ansi.Strip(line)}}
}

// refreshTranscriptTheme reapplies colors without moving the scroll position,
// losing a selection, or including animation frames in the saved output.
func (t *tui) refreshTranscriptTheme() {
	sel, flash, flashing := t.sel, t.flash, t.flashing
	t.wrap()
	t.sel, t.flash, t.flashing = sel, flash, flashing
	t.redraw()
}
