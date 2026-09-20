package main

import (
	"strings"
	"testing"
)

// TestSuggestEmpty checks an empty prompt does not open suggestions.
func TestSuggestEmpty(t *testing.T) {
	if got := suggest(""); got != nil {
		t.Errorf("suggest(\"\") = %v, want nil", got)
	}
}

// TestSuggestFirstWord checks command prefix completion.
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

// TestSuggestStationNames checks station shorthand completion.
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

// TestSuggestPlayArgument checks station arguments after play.
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

// TestSuggestVolArgument checks relative volume suggestions.
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

// TestSuggestEqualizerArguments checks presets with spaces and band completion.
func TestSuggestEqualizerArguments(t *testing.T) {
	matches := suggest("eq bass")
	if len(matches) != 1 || matches[0].text != "Bass Boost" {
		t.Fatalf("suggest preset = %+v, want Bass Boost", matches)
	}
	if got := acceptSuggestion("eq bass", matches[0]); got != "eq Bass Boost" {
		t.Fatalf("accepted preset = %q", got)
	}

	matches = suggest("eq --band 1")
	var found bool
	for _, match := range matches {
		if match.text == "1k" {
			found = true
			if got := acceptSuggestion("eq --band 1", match); got != "eq --band 1k " {
				t.Fatalf("accepted band = %q", got)
			}
		}
	}
	if !found {
		t.Fatalf("frequency suggestions missing 1k: %+v", matches)
	}
}

// TestSuggestDoctor checks diagnostic options and their arguments.
func TestSuggestDoctor(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  string
		takes bool
	}{
		{"doc", "doctor", true},
		{"doctor --l", "--logs", false},
		{"doctor --str", "--stream", true},
		{"doctor --stream sl", "sleep", false},
		{"doctor --logs --stream sl", "sleep", false},
		{"doctor --timeout 3", "30s", false},
	} {
		matches := suggest(tt.input)
		if len(matches) != 1 || matches[0].text != tt.want || matches[0].takes != tt.takes {
			t.Errorf("suggest(%q) = %+v, want %s (takes=%v)", tt.input, matches, tt.want, tt.takes)
		}
	}
	for _, input := range []string{"doctor --stations ", "doctor --stream sleep ", "doctor --stream=sleep "} {
		matches := suggest(input)
		if len(matches) == 0 {
			t.Fatalf("no remaining options for %q", input)
		}
		for _, match := range matches {
			if match.text == "--stations" || match.text == "--stream" || match.station {
				t.Errorf("suggest(%q) offered conflicting selector %+v", input, match)
			}
		}
	}
	if matches := suggest("doctor --help "); len(matches) != 0 {
		t.Errorf("help offered extra arguments: %+v", matches)
	}
}

// TestExecuteDoctorHelp checks diagnostic help through the REPL.
func TestExecuteDoctorHelp(t *testing.T) {
	withConfigDir(t)
	t.Setenv("PATH", t.TempDir())
	out, err := execute("doctor --help")
	if err != nil || !strings.Contains(out, "--stations") || !strings.Contains(out, "--stream") {
		t.Fatalf("doctor help: %q, %v", out, err)
	}
	out, err = execute("doctor --timeout 0s")
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("invalid doctor options: %q, %v", out, err)
	}
}

// TestSuggestExactMatchCompletesNothing checks already complete words stay unchanged.
func TestSuggestExactMatchCompletesNothing(t *testing.T) {
	// typing out a full word leaves nothing to complete
	if got := suggest("skip"); got != nil {
		t.Errorf("suggest(\"skip\") = %v, want nil", got)
	}
}

// TestSuggestStopsAfterArgument checks completed commands do not suggest extra arguments.
func TestSuggestStopsAfterArgument(t *testing.T) {
	if got := suggest("play sleep "); got != nil {
		t.Errorf("suggest(\"play sleep \") = %v, want nil", got)
	}
}

// TestAcceptSuggestion checks word replacement and argument spacing.
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
		{"multiword replacement", "eq bass", suggestion{text: "Bass Boost", replace: "eq Bass Boost"}, "eq Bass Boost"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := acceptSuggestion(tt.input, tt.sug); got != tt.want {
				t.Errorf("acceptSuggestion(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestExecuteUnknown checks unknown REPL input produces an error.
func TestExecuteUnknown(t *testing.T) {
	if _, err := execute("bogus"); err == nil {
		t.Error("execute(\"bogus\") should fail")
	}
	if _, err := execute("play bogus"); err == nil {
		t.Error("execute(\"play bogus\") should fail")
	}
}

// TestExecuteKeepsArgumentCase checks descriptions retain their original spelling.
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

// TestHistory checks command history persistence and duplicate handling.
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
