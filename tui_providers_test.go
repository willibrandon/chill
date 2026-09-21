package main

import (
	"context"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type providerSearchStub struct{}

func (providerSearchStub) key() string                    { return "youtube" }
func (providerSearchStub) name() string                   { return "YouTube" }
func (providerSearchStub) capabilities() []string         { return []string{"search"} }
func (providerSearchStub) validate(context.Context) error { return nil }
func (providerSearchStub) search(_ context.Context, query string, _ int) ([]MediaItem, error) {
	return []MediaItem{providerMediaItem("youtube", "result", query, "Artist", "", "", "https://youtube.com/watch?v=result", 60)}, nil
}
func (providerSearchStub) browse(context.Context, providerBrowseRequest) (providerPage, error) {
	return providerPage{
		Provider: "youtube",
		Title:    "YouTube",
		Items:    []MediaItem{providerMediaItem("youtube", "result", "Result", "Artist", "", "", "https://youtube.com/watch?v=result", 60)},
	}, nil
}

func useProviderSearchStub(t *testing.T) {
	providerRegistryState.Lock()
	previous := providerRegistryState.registry
	providerRegistryState.registry = &providerRegistry{
		providers: map[string]mediaProvider{"youtube": providerSearchStub{}},
		order:     []string{"youtube"},
		recent:    map[string]MediaItem{},
	}
	providerRegistryState.Unlock()
	t.Cleanup(func() {
		providerRegistryState.Lock()
		providerRegistryState.registry = previous
		providerRegistryState.Unlock()
	})
}

func providerSearchTestModel() *tui {
	model := newTUI()
	model.providersUI = providerBrowser{
		open: true,
		page: "home",
		infos: []providerInfo{
			{Key: "mixcloud", Name: "Mixcloud"},
			{Key: "soundcloud", Name: "SoundCloud"},
			{Key: "youtube", Name: "YouTube"},
			{Key: "ytmusic", Name: "YouTube Music"},
		},
		input: textinput.New(),
	}
	return model
}

// TestProviderSearchIndicatorNamesItsScope checks animated search feedback.
func TestProviderSearchIndicatorNamesItsScope(t *testing.T) {
	model := providerSearchTestModel()
	browser := &model.providersUI
	browser.spinner = spinner.New(spinner.WithSpinner(spinner.Points))
	browser.loading, browser.provider = true, "youtube"
	browser.searchQuery = "ambient"
	browser.results = make(<-chan providerSearchBatch)
	browser.pending, browser.searchTotal = 1, 1
	if indicator := browser.loadingText(); !strings.Contains(indicator, "YouTube") || !strings.Contains(indicator, `"ambient"`) || strings.Contains(indicator, "1 of 1 complete") {
		t.Fatalf("single-provider indicator = %q", indicator)
	}
	frame := browser.spinner.View()
	if command := model.update(browser.spinner.Tick()); command == nil || browser.spinner.View() == frame {
		t.Fatal("provider search indicator did not animate")
	}

	browser.provider, browser.pending, browser.searchTotal = "all", 2, 4
	if indicator := browser.loadingText(); !strings.Contains(indicator, "All providers") || !strings.Contains(indicator, "2 of 4 complete") {
		t.Fatalf("global indicator = %q", indicator)
	}
}

// TestProviderListResultFinishesLoading guards the F8 loading transition.
func TestProviderListResultFinishesLoading(t *testing.T) {
	model := providerSearchTestModel()
	model.providersUI.loading = true
	model.providersUI.id = 7
	model.providerResult(providerResultMsg{id: 7, infos: []providerInfo{{Key: "youtube", Name: "YouTube"}}})
	if model.providersUI.loading {
		t.Fatal("provider list remained in its loading state")
	}
}

// TestProviderBrowseSelectsFirstResult changes selection with the result page.
func TestProviderBrowseSelectsFirstResult(t *testing.T) {
	useProviderSearchStub(t)
	model := providerSearchTestModel()
	model.providersUI.selected = 2
	command := model.providerBrowse("youtube", providerBrowseRequest{Kind: "music", Limit: 30}, true)
	if command == nil {
		t.Fatal("provider browse did not start")
	}
	if model.providersUI.selected != 2 {
		t.Fatalf("selection moved before results loaded: %d", model.providersUI.selected)
	}
	batch, ok := command().(tea.BatchMsg)
	if !ok {
		t.Fatal("provider browse did not return its batched request")
	}
	for _, command := range batch {
		if result, ok := command().(providerResultMsg); ok {
			model.update(result)
		}
	}
	if model.providersUI.page != "results" || model.providersUI.selected != 0 {
		t.Fatalf("loaded provider page = %q, selection = %d", model.providersUI.page, model.providersUI.selected)
	}
}

// TestProviderSearchUsesSelectedHomeProvider guards scoped F8 searches.
func TestProviderSearchUsesSelectedHomeProvider(t *testing.T) {
	model := providerSearchTestModel()
	for range 3 {
		model.providerKey(vizKey("down"))
	}
	if command := model.providerKey(vizKey("/")); command == nil {
		t.Fatal("provider search did not focus its input")
	}
	browser := model.providersUI
	if !browser.editing || !browser.searching || browser.searchProvider != "youtube" || browser.selected != 3 {
		t.Fatalf("provider search state = %+v", browser)
	}
	if browser.input.Prompt != "Search YouTube: " {
		t.Fatalf("search prompt = %q", browser.input.Prompt)
	}
}

// TestProviderSearchKeepsGlobalAndBrowseScopesExplicit checks both entry paths.
func TestProviderSearchKeepsGlobalAndBrowseScopesExplicit(t *testing.T) {
	model := providerSearchTestModel()
	model.providerKey(vizKey("/"))
	if model.providersUI.searchProvider != "all" || model.providersUI.input.Prompt != "Search all providers: " {
		t.Fatalf("global search state = %+v", model.providersUI)
	}

	model = providerSearchTestModel()
	model.providersUI.page, model.providersUI.provider = "results", "youtube"
	model.providerKey(vizKey("ctrl+f"))
	if model.providersUI.searchProvider != "youtube" || model.providersUI.input.Prompt != "Search YouTube: " {
		t.Fatalf("browse search state = %+v", model.providersUI)
	}

	model.providersUI.editing = false
	model.providerKey(vizKey("/"))
	if model.providersUI.searching || model.providersUI.input.Prompt != "/ " {
		t.Fatalf("result filter state = %+v", model.providersUI)
	}
}

// TestProviderSearchSubmissionDeliversScopedResults covers the complete F8 search path.
func TestProviderSearchSubmissionDeliversScopedResults(t *testing.T) {
	useProviderSearchStub(t)
	model := providerSearchTestModel()
	for range 3 {
		model.providerKey(vizKey("down"))
	}
	model.providerKey(vizKey("/"))
	model.providersUI.input.SetValue("ambient")

	command := model.providerKey(vizKey("enter"))
	if command == nil {
		t.Fatal("provider search did not start")
	}
	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		t.Fatalf("provider search command returned %T", message)
	}
	for _, command := range batch {
		message := command()
		if result, ok := message.(providerResultMsg); ok {
			for {
				next := model.update(result)
				if next == nil {
					break
				}
				message = next()
				var valid bool
				result, valid = message.(providerResultMsg)
				if !valid {
					break
				}
			}
		}
	}

	browser := model.providersUI
	if browser.loading || browser.provider != "youtube" || len(browser.items) != 1 {
		t.Fatalf("completed provider search state = %+v", browser)
	}
	if browser.items[0].Title != "ambient" {
		t.Fatalf("provider search result = %+v", browser.items[0])
	}
}
