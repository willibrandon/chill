package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestSpanOrdersBackwardsSelection checks reverse drags produce ordered bounds.
func TestSpanOrdersBackwardsSelection(t *testing.T) {
	sel := selection{anchor: point{5, 3}, cursor: point{2, 7}}
	first, last := sel.span()
	if first != (point{2, 7}) || last != (point{5, 3}) {
		t.Errorf("span() = %v, %v; want {2,7}, {5,3}", first, last)
	}
}

// TestCols checks selected column ranges across transcript rows.
func TestCols(t *testing.T) {
	// a drag from row 1 col 4 to row 3 col 6
	sel := selection{anchor: point{1, 4}, cursor: point{3, 6}}

	tests := []struct {
		row      int
		width    int
		from, to int
	}{
		{0, 10, 0, 10}, // before the selection: untouched by span logic
		{1, 10, 4, 10}, // first row: from the anchor to the end
		{2, 10, 0, 10}, // middle row: everything
		{3, 10, 0, 7},  // last row: to the cursor, inclusive
		{3, 5, 0, 5},   // clamped to a shorter row
	}
	for _, tt := range tests {
		from, to := sel.cols(tt.row, tt.width)
		if from != tt.from || to != tt.to {
			t.Errorf("cols(%d, %d) = %d, %d; want %d, %d", tt.row, tt.width, from, to, tt.from, tt.to)
		}
	}
}

// TestColsWholeLines checks line selection spans complete rows.
func TestColsWholeLines(t *testing.T) {
	sel := selection{lines: true, anchor: point{1, 2}, cursor: point{3, 9}}
	from, to := sel.cols(2, 42)
	if from != 0 || to != 42 {
		t.Errorf("cols() = %d, %d; want whole row 0, 42", from, to)
	}
}

// TestPaint checks highlighting preserves the selected text.
func TestPaint(t *testing.T) {
	got := paint("abcdef", 2, 4, styleSelection)
	if ansi.Strip(got) != "abcdef" {
		t.Errorf("paint changed the text: %q", got)
	}
	if got == "abcdef" {
		t.Error("paint applied no styling")
	}

	// an empty span still shows it belongs to the selection
	got = paint("abc", 1, 1, styleSelection)
	if ansi.Strip(got) != "abc " {
		t.Errorf("paint() = %q, want the row with a styled space", got)
	}
}

// TestDedent checks common indentation is removed from copied text.
func TestDedent(t *testing.T) {
	lines := []string{"    one", "", "  two", "    three"}
	got := dedent(lines)
	want := []string{"  one", "", "two", "  three"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("dedent() = %q, want %q", got, want)
	}
}

// TestDedentAllBlank checks copying blank rows is well-defined.
func TestDedentAllBlank(t *testing.T) {
	// with no indented text the shared indent is unknown, lines stay as they are
	got := dedent([]string{"   ", " "})
	if got[0] != "   " || got[1] != " " {
		t.Errorf("dedent() = %q, want the lines untouched", got)
	}
}
