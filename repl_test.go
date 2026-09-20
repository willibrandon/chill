package main

import (
	"strings"
	"testing"
)

func TestSuggestEmpty(t *testing.T) {
	if got := suggest(""); got != nil {
		t.Errorf("suggest(\"\") = %v, want nil", got)
	}
}

func TestSuggestFirstWord(t *testing.T) {
	matches := suggest("pl")

	// "play" the command, and nothing else starts with "pl"
	if len(matches) != 1 || matches[0].text != "play" {
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.text
		}
		t.Errorf("suggest(\"pl\") = %v, want [play]", names)
	}
	if !matches[0].takes {
		t.Error("play takes an argument, so accepting it should add a space")
	}
}

func TestSuggestStationNames(t *testing.T) {
	matches := suggest("ch")

	var stations []string
	for _, m := range matches {
		if m.station {
			stations = append(stations, m.text)
		}
	}
	if len(stations) != 2 || stations[0] != "chillhop" || stations[1] != "chillout" {
		t.Errorf("suggest(\"ch\") stations = %v, want [chillhop chillout]", stations)
	}
}

func TestSuggestPlayArgument(t *testing.T) {
	// after "play " only stations are offered
	matches := suggest("play ")
	if len(matches) == 0 {
		t.Fatal("suggest(\"play \") offered nothing")
	}
	for _, m := range matches {
		if !m.station {
			t.Errorf("suggest(\"play \") offered %q, which is not a station", m.text)
		}
	}

	// the prefix narrows them
	matches = suggest("play sl")
	if len(matches) != 1 || matches[0].text != "sleep" {
		t.Errorf("suggest(\"play sl\") = %v, want [sleep]", matches)
	}
}

func TestSuggestVolArgument(t *testing.T) {
	matches := suggest("vol ")
	if len(matches) != 2 || matches[0].text != "up" || matches[1].text != "down" {
		t.Errorf("suggest(\"vol \") = %v, want [up down]", matches)
	}

	matches = suggest("vol u")
	if len(matches) != 1 || matches[0].text != "up" {
		t.Errorf("suggest(\"vol u\") = %v, want [up]", matches)
	}
}

func TestSuggestExactMatchCompletesNothing(t *testing.T) {
	// typing out a full word leaves nothing to complete
	if got := suggest("skip"); got != nil {
		t.Errorf("suggest(\"skip\") = %v, want nil", got)
	}
}

func TestSuggestStopsAfterArgument(t *testing.T) {
	if got := suggest("play sleep "); got != nil {
		t.Errorf("suggest(\"play sleep \") = %v, want nil", got)
	}
}

func TestAcceptSuggestion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		sug   suggestion
		want  string
	}{
		{"first word", "pl", suggestion{text: "play"}, "play"},
		{"argument", "play sl", suggestion{text: "sleep"}, "play sleep"},
		{"takes argument", "pl", suggestion{text: "play", takes: true}, "play "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := acceptSuggestion(tt.input, tt.sug); got != tt.want {
				t.Errorf("acceptSuggestion(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExecuteUnknown(t *testing.T) {
	if _, err := execute("bogus"); err == nil {
		t.Error("execute(\"bogus\") should fail")
	}
	if _, err := execute("play bogus"); err == nil {
		t.Error("execute(\"play bogus\") should fail")
	}
}

func TestExecuteKeepsArgumentCase(t *testing.T) {
	// descriptions in "add" keep their case even though the command is lowered
	cmd := "ADD My-Mix https://example.com Chill Beats"
	parts := strings.Fields(cmd)
	arg := ""
	if i := strings.IndexByte(cmd, ' '); i >= 0 {
		arg = strings.TrimSpace(cmd[i+1:])
	}
	if parts[0] != "ADD" {
		t.Fatalf("parts[0] = %q", parts[0])
	}
	if arg != "My-Mix https://example.com Chill Beats" {
		t.Errorf("arg = %q", arg)
	}
}

func TestHistory(t *testing.T) {
	h := &history{} // no path, memory only

	h.add("play")
	h.add("skip")
	h.add("play")
	h.add("play") // a repeat of the last line is skipped

	want := []string{"play", "skip", "play"}
	if strings.Join(h.lines, " ") != strings.Join(want, " ") {
		t.Errorf("lines = %v, want %v", h.lines, want)
	}
}
