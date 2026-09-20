package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// every description in the help listing starts in the same column
func TestHelpAlignment(t *testing.T) {
	descs := append([]string(nil), func() []string {
		var d []string
		for _, c := range replCommands {
			d = append(d, c.desc)
		}
		return append(d, "same as play <station>")
	}()...)

	want := -1
	for i, line := range strings.Split(replHelp(), "\n") {
		plain := ansi.Strip(line)
		at := strings.Index(plain, descs[i])
		if at < 0 {
			t.Fatalf("row %q has no description %q", plain, descs[i])
		}
		if want < 0 {
			want = at
		} else if at != want {
			t.Errorf("row %q starts its description at %d, want %d", plain, at, want)
		}
	}
	if want != helpWidth()+2 {
		t.Errorf("descriptions start at %d, want %d (just past the longest name)", want, helpWidth()+2)
	}
}
