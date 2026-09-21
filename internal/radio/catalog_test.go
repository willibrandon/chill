package radio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := NewClient()
	c.BaseURL = server.URL
	return c
}

// TestStationsFiltersAndNormalizes covers request filters and unsafe entries.
func TestStationsFiltersAndNormalizes(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("hidebroken") != "true" || r.URL.Query().Get("tagExact") != "true" || r.URL.Query().Get("order") != SortTrending {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[
          {"stationuuid":"one","name":" One ","url_resolved":"https://radio.example/live","countrycode":"us","bitrate":128,"favicon":"file:///tmp/private"},
          {"stationuuid":"dupe","name":"Duplicate","url_resolved":"https://radio.example/live"},
          {"name":"Broken","url_resolved":"file:///tmp/a"},
          {"name":"Fallback","url_resolved":"","url":"http://fallback.example/stream"}
        ]`))
	})
	stations, err := c.Stations(context.Background(), Query{Tag: "jazz", Sort: "trending"})
	if err != nil {
		t.Fatal(err)
	}
	if len(stations) != 2 || stations[0].Name != "One" || stations[0].CountryCode != "US" || stations[1].URL != "http://fallback.example/stream" {
		t.Fatalf("stations = %#v", stations)
	}
	if stations[0].Favicon != "" {
		t.Fatalf("unsafe favicon = %q", stations[0].Favicon)
	}
}

// TestQueryValidation rejects invalid pagination, location, and sort values.
func TestQueryValidation(t *testing.T) {
	c := NewClient()
	for _, q := range []Query{{Limit: 201}, {Offset: -1}, {CountryCode: "USA"}, {Sort: "newest"}} {
		if _, err := c.Stations(context.Background(), q); err == nil {
			t.Fatalf("query %#v unexpectedly succeeded", q)
		}
	}
}

// TestCountriesMergeCodes folds case-only country duplicates.
func TestCountriesMergeCodes(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
          {"name":"United States","iso_3166_1":"US","stationcount":10},
          {"name":"USA","iso_3166_1":"us","stationcount":2},
          {"name":"Germany","iso_3166_1":"DE","stationcount":20}
        ]`))
	})
	places, err := c.Countries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(places) != 2 || places[0].Code != "DE" || places[1].StationCount != 12 {
		t.Fatalf("places = %#v", places)
	}
}

// TestStatesUsesCountryName verifies the directory's country-name endpoint
// while retaining the ISO code needed for subsequent station searches.
func TestStatesUsesCountryName(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/states/United%20States" || r.URL.Query().Get("hidebroken") != "true" {
			t.Errorf("request = %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`[{"name":"California","country":"United States","stationcount":12}]`))
	})
	states, err := c.States(context.Background(), "United States", "us")
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Code != "US" || states[0].Name != "California" {
		t.Fatalf("states = %#v", states)
	}
}

// TestCatalogFailoverPromotesAHealthyServer verifies discovered mirror retries.
func TestCatalogFailoverPromotesAHealthyServer(t *testing.T) {
	failedRequests := 0
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		failedRequests++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer failed.Close()
	healthyRequests := 0
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		healthyRequests++
		_, _ = w.Write([]byte(`[{"stationuuid":"one","name":"One","url_resolved":"https://example.com/live"}]`))
	}))
	defer healthy.Close()
	c := NewClient()
	c.resolver = func(context.Context) ([]string, error) { return []string{failed.URL, healthy.URL}, nil }
	for range 2 {
		stations, err := c.Stations(context.Background(), Query{Limit: 1})
		if err != nil || len(stations) != 1 {
			t.Fatalf("stations = %#v, %v", stations, err)
		}
	}
	if failedRequests != 1 || healthyRequests != 2 || len(c.servers) != 2 || c.servers[0] != healthy.URL {
		t.Fatalf("servers = %#v, requests = %d failed / %d healthy", c.servers, failedRequests, healthyRequests)
	}
}
