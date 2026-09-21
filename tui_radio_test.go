package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/willibrandon/chill/internal/radio"
)

// TestRadioNearbyConsentIsAsyncAndDurable guards the nearby consent transition.
func TestRadioNearbyConsentIsAsyncAndDurable(t *testing.T) {
	store := &radio.Store{Path: filepath.Join(t.TempDir(), "radio.json")}
	detected := false
	model := &tui{radio: radioBrowser{
		open: true, page: radioPage{kind: "home"}, store: store, consent: true,
		detectCountry: func() string { detected = true; return "US" },
	}}
	command := model.radioKey(vizKey("y"))
	if command == nil {
		t.Fatal("accepting nearby consent returned no command")
	}
	if detected {
		t.Fatal("country detection ran synchronously")
	}
	if model.radio.consent {
		t.Fatal("consent prompt remained active after acceptance")
	}
	message, ok := command().(radioResultMsg)
	if !ok {
		t.Fatalf("message = %T", command())
	}
	model.radioResult(message)
	if !detected || model.radio.library.Country != "US" {
		t.Fatalf("nearby country = %q, detected = %v", model.radio.library.Country, detected)
	}
	model.radioKey(vizKey("esc"))
	library, err := store.Load()
	if err != nil || library.Country != "US" {
		t.Fatalf("stored nearby country = %q, err = %v", library.Country, err)
	}
}

// TestRadioNearbyDisabledRemainsAvailable guards recovery from an opt-out.
func TestRadioNearbyDisabledRemainsAvailable(t *testing.T) {
	browser := radioBrowser{page: radioPage{kind: "home"}, library: radio.Library{Country: "NONE"}}
	for _, row := range browser.homeRows() {
		if row.kind == "consent" {
			return
		}
	}
	t.Fatal("disabled nearby entry disappeared from radio home")
}

// TestRadioNearbyOpensStationsDirectly guards the nearby suggestion path.
func TestRadioNearbyOpensStationsDirectly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stations/search" || r.URL.Query().Get("countrycode") != "US" {
			t.Errorf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"stationuuid":"1","name":"Nearby FM","url_resolved":"https://example.com/live","countrycode":"US"}]`))
	}))
	defer server.Close()
	client := radio.NewClient()
	client.BaseURL = server.URL
	model := &tui{radio: radioBrowser{
		open: true, client: client, page: radioPage{kind: "home"}, library: radio.Library{Country: "US"},
	}}
	nearby := -1
	for index, row := range model.radio.homeRows() {
		if row.kind == "nearby" {
			nearby = index
			break
		}
	}
	if nearby < 0 {
		t.Fatal("nearby entry is missing from radio home")
	}
	command := model.radioSelect(nearby)
	message, ok := command().(radioResultMsg)
	if !ok {
		t.Fatalf("message = %T", command())
	}
	model.radioResult(message)
	if model.radio.page.kind != "stations" || model.radio.page.query.CountryCode != "US" || len(model.radio.page.stations) != 1 {
		t.Fatalf("nearby page = %+v", model.radio.page)
	}
}

// TestRadioNearbyResolvesCodeBeforeLoadingRegions guards country-code navigation.
func TestRadioNearbyResolvesCodeBeforeLoadingRegions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/countries":
			_, _ = w.Write([]byte(`[{"name":"United States","iso_3166_1":"US","stationcount":120}]`))
		case "/states/United States":
			_, _ = w.Write([]byte(`[{"name":"California","country":"United States","stationcount":20}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := radio.NewClient()
	client.BaseURL = server.URL
	model := &tui{radio: radioBrowser{open: true, client: client}}
	command := model.radioOpenCountry("US", "", 0)
	message, ok := command().(radioResultMsg)
	if !ok {
		t.Fatalf("message = %T", command())
	}
	model.radioResult(message)
	if model.radio.note != "" || model.radio.page.title != "United States" || len(model.radio.page.places) != 2 || model.radio.page.places[0].StationCount != 120 {
		t.Fatalf("country page = %+v, note = %q", model.radio.page, model.radio.note)
	}
}

// TestFeatureKeysSwitchPanelsAndFailedLyricsDoNotRetry guards global TUI routing.
func TestFeatureKeysSwitchPanelsAndFailedLyricsDoNotRetry(t *testing.T) {
	model := newTUI()
	model.radio.open = true
	if command := model.update(vizKey("f6")); command == nil || model.radio.open || !model.lyrics.open {
		t.Fatalf("F6 did not switch panels: radio=%v lyrics=%v", model.radio.open, model.lyrics.open)
	}
	model.lyrics.id = 7
	model.lyricsResult(lyricsResultMsg{id: 7, raw: "Artist - Missing", err: errors.New("not found")})
	if model.lyrics.raw != "Artist - Missing" || model.lyrics.loading {
		t.Fatalf("failed lookup state = %+v", model.lyrics)
	}
	model.update(vizKey("f5"))
	if model.lyrics.open || !model.radio.open {
		t.Fatalf("F5 did not switch panels: radio=%v lyrics=%v", model.radio.open, model.lyrics.open)
	}
	model.shutdown()
}
