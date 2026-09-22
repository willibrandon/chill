// selection.go implements picking transcript text to copy it, by dragging the
// mouse over it or a line at a time from the keyboard.

package main

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
)

const (
	flashTime  = 150 * time.Millisecond  // how long copied rows light up
	noticeTime = 1500 * time.Millisecond // how long the status bar says what was copied
	noticeMax  = 40                      // characters of copied text the status bar shows
)

var (
	styleSelection = lipgloss.NewStyle().Reverse(true)
	styleFlash     = lipgloss.NewStyle().Foreground(lipgloss.Color("#1E1E2E")).Background(lipgloss.Color("#D7AFFF"))
	styleNotice    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6A4BBF")).Background(lipgloss.Color("#FFFFFF"))
)

// flashDoneMsg and noticeDoneMsg end the feedback for the copy with that id.
type (
	flashDoneMsg  int
	noticeDoneMsg int
)

// point is a cell of the transcript, by row in tui.rows and by column.
type point struct{ row, col int }

// selection is the transcript text picked for copying.
type selection struct {
	active bool
	lines  bool  // whole rows, picked from the keyboard
	anchor point // where it started
	cursor point // where it ends, the part that moves
}

// span returns the first and last cell of the selection in reading order.
func (s selection) span() (first, last point) {
	first, last = s.anchor, s.cursor
	if last.row < first.row || last.row == first.row && last.col < first.col {
		first, last = last, first
	}
	return first, last
}

// cols returns the selected columns [from, to) of a row that is width wide.
func (s selection) cols(row, width int) (from, to int) {
	first, last := s.span()
	from, to = 0, width
	if s.lines {
		return from, to
	}
	if row == first.row {
		from = first.col
	}
	if row == last.row {
		to = last.col + 1
	}
	return min(from, width), min(to, width)
}

// paint restyles the columns [from, to) of a transcript row.
func paint(row string, from, to int, style lipgloss.Style) string {
	if to <= from {
		// an empty row still shows that it is part of the selection
		return row + style.Render(" ")
	}
	width := ansi.StringWidth(row)
	return ansi.Cut(row, 0, from) + style.Render(ansi.Strip(ansi.Cut(row, from, to))) + ansi.Cut(row, to, width)
}

// decorated returns the transcript rows with the selection, or the flash of
// what was just copied, drawn over them.
func (t *tui) decorated() []string {
	if !t.sel.active && !t.flashing {
		return t.rows
	}

	rows := append([]string(nil), t.rows...)
	sel, style := t.sel, styleSelection
	if t.flashing {
		sel, style = t.flash, styleFlash
	}

	first, last := sel.span()
	for r := max(first.row, 0); r <= last.row && r < len(rows); r++ {
		from, to := sel.cols(r, ansi.StringWidth(rows[r]))
		rows[r] = paint(rows[r], from, to, style)
	}
	return rows
}

// redraw hands the transcript to the viewport again, keeping its position.
func (t *tui) redraw() {
	rows := t.decorated()
	if t.running && t.activeRow >= 0 && t.activeRow < len(rows) {
		rows = append([]string(nil), rows...)
		gap := " "
		if rows[t.activeRow] == "" {
			gap = ""
		}
		rows[t.activeRow] += gap + styleCommand.Render(t.spinner.View())
	}
	t.viewport.SetContentLines(rows)
}

// selectedText returns the text of the selection. Whole lines lose the guide
// and their shared indentation, so a command copied out of a message pastes
// as the command and nothing else.
func (t *tui) selectedText() string {
	first, last := t.sel.span()

	var lines []string
	for r := max(first.row, 0); r <= last.row && r < len(t.rows); r++ {
		plain := ansi.Strip(t.rows[r])
		from, to := t.sel.cols(r, ansi.StringWidth(plain))
		line := strings.TrimRight(ansi.Cut(plain, from, to), " ")
		if t.sel.lines {
			line = strings.TrimPrefix(line, "  ┊ ")
		}
		lines = append(lines, line)
	}

	if t.sel.lines {
		lines = dedent(lines)
	}
	return strings.Join(lines, "\n")
}

// dedent removes the leading spaces that all non-blank lines share.
func dedent(lines []string) []string {
	shared := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if shared < 0 || indent < shared {
			shared = indent
		}
	}

	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = line[min(max(shared, 0), len(line)):]
	}
	return out
}

// copySelection puts the selection on the clipboard and says so.
func (t *tui) copySelection() tea.Cmd {
	text := t.selectedText()
	copied := t.sel
	t.sel = selection{}
	if strings.TrimSpace(text) == "" {
		t.redraw()
		return nil
	}

	// the system clipboard when there is one, and the terminal's own way,
	// which also works over ssh
	clipboard.WriteAll(text)

	t.flash, t.flashing = copied, true
	t.notice = "Yanked: " + ansi.Truncate(strings.TrimSpace(text), noticeMax, "…")
	if n := strings.Count(text, "\n") + 1; n > 1 {
		t.notice = fmt.Sprintf("Yanked %d lines", n)
	}
	t.redraw()

	t.copies++
	id := t.copies
	return tea.Batch(
		tea.SetClipboard(text),
		tea.Tick(flashTime, func(time.Time) tea.Msg { return flashDoneMsg(id) }),
		tea.Tick(noticeTime, func(time.Time) tea.Msg { return noticeDoneMsg(id) }),
	)
}

// cancelSelection drops the selection without copying it.
func (t *tui) cancelSelection() {
	if t.sel.active {
		t.sel = selection{}
		t.redraw()
	}
}

// cellAt returns the transcript cell under a screen position.
func (t *tui) cellAt(x, y int) (point, bool) {
	if len(t.rows) == 0 || y < 0 || y >= t.viewport.Height() || x < 0 || x >= t.viewport.Width() {
		return point{}, false
	}
	return point{min(t.viewport.YOffset()+y, len(t.rows)-1), x}, true
}

// mouse handles the buttons: dragging selects, a right click copies what is
// selected or pastes when nothing is.
func (t *tui) mouse(msg tea.MouseMsg) tea.Cmd {
	m := msg.Mouse()

	switch msg.(type) {
	case tea.MouseClickMsg:
		switch m.Button {
		case tea.MouseLeft:
			// a click on its own selects nothing, and ends what was selected
			t.cancelSelection()
			if p, ok := t.cellAt(m.X, m.Y); ok {
				t.sel = selection{anchor: p, cursor: p}
				t.dragging = true
			}

		case tea.MouseRight:
			if t.sel.active {
				return t.copySelection()
			}
			return t.paste()
		}

	case tea.MouseMotionMsg:
		if !t.dragging {
			return nil
		}
		// dragging past the edge scrolls
		if m.Y <= 0 {
			t.viewport.ScrollUp(1)
		} else if m.Y >= t.viewport.Height()-1 {
			t.viewport.ScrollDown(1)
		}

		x := min(max(m.X, 0), t.viewport.Width()-1)
		y := min(max(m.Y, 0), t.viewport.Height()-1)
		if p, ok := t.cellAt(x, y); ok && (t.sel.active || p != t.sel.anchor) {
			t.sel.cursor = p
			t.sel.active = true
			t.redraw()
		}

	case tea.MouseReleaseMsg:
		t.dragging = false
	}
	return nil
}

// paste types the first line of the clipboard into the prompt.
func (t *tui) paste() tea.Cmd {
	text, err := clipboard.ReadAll()
	if err != nil {
		return nil
	}
	text, _, _ = strings.Cut(strings.TrimSpace(text), "\n")

	var cmd tea.Cmd
	t.input, cmd = t.input.Update(tea.PasteMsg{Content: strings.TrimSpace(text)})
	t.edited()
	return cmd
}

// selectLines starts a selection of whole lines at the last one in view.
func (t *tui) selectLines() {
	if len(t.rows) == 0 {
		return
	}
	row := min(t.viewport.YOffset()+t.viewport.Height(), len(t.rows)) - 1
	t.sel = selection{active: true, lines: true, anchor: point{row: row}, cursor: point{row: row}}
	t.redraw()
}

// extendLines moves the end of a selection of whole lines up or down.
func (t *tui) extendLines(step int) {
	row := min(max(t.sel.cursor.row+step, 0), len(t.rows)-1)
	t.sel.cursor.row = row

	// keep the moving end in view
	if row < t.viewport.YOffset() {
		t.viewport.SetYOffset(row)
	} else if bottom := t.viewport.YOffset() + t.viewport.Height() - 1; row > bottom {
		t.viewport.SetYOffset(row - t.viewport.Height() + 1)
	}
	t.redraw()
}

// selectionKey handles a key press while something is selected. It reports
// false for keys that should go on to the prompt, which end the selection.
func (t *tui) selectionKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch t.presentation.mapKey("selection", msg.String()) {
	case "y", "enter", "ctrl+c":
		return t.copySelection(), true

	case "esc":
		t.cancelSelection()
		return nil, true

	case "up", "shift+up":
		if t.sel.lines {
			t.extendLines(-1)
			return nil, true
		}

	case "down", "shift+down":
		if t.sel.lines {
			t.extendLines(1)
			return nil, true
		}

	case "pgup":
		t.viewport.PageUp()
		return nil, true

	case "pgdown":
		t.viewport.PageDown()
		return nil, true

	case "ctrl+q":
		return tea.Quit, true
	}

	t.cancelSelection()
	return nil, false
}
