// Package radio provides discovery for public internet radio stations.
package radio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const fallbackBaseURL = "https://all.api.radio-browser.info/json"

// Station is one playable directory entry.
type Station struct {
	// ID is the directory's stable station identifier.
	ID string `json:"id"`
	// Name is the station's display name.
	Name string `json:"name"`
	// URL is the resolved playback address.
	URL string `json:"url"`
	// Country is the directory's display country.
	Country string `json:"country,omitempty"`
	// CountryCode is the ISO 3166-1 alpha-2 country code.
	CountryCode string `json:"country_code,omitempty"`
	// State is the optional region within the country.
	State string `json:"state,omitempty"`
	// Language is the station's language description.
	Language string `json:"language,omitempty"`
	// Tags is the directory's comma-separated tag list.
	Tags string `json:"tags,omitempty"`
	// Codec is the advertised audio codec.
	Codec string `json:"codec,omitempty"`
	// Bitrate is the advertised kilobits per second.
	Bitrate int `json:"bitrate,omitempty"`
	// Votes is the directory's community vote count.
	Votes int `json:"votes,omitempty"`
	// Homepage is the station's public website.
	Homepage string `json:"homepage,omitempty"`
	// Favicon is the station's artwork URL.
	Favicon string `json:"favicon,omitempty"`
}

// Place is a country or region with its number of catalog stations.
type Place struct {
	// Name is the country or region display name.
	Name string `json:"name"`
	// Code is the ISO country code.
	Code string `json:"code,omitempty"`
	// Country identifies the parent country for a region.
	Country string `json:"country,omitempty"`
	// StationCount is the number of directory entries in this place.
	StationCount int `json:"station_count"`
}

// Tag is a community-maintained genre, format, language, or descriptor.
type Tag struct {
	// Name is the directory tag.
	Name string `json:"name"`
	// StationCount is the number of stations carrying the tag.
	StationCount int `json:"station_count"`
}

// Query describes a station search. The zero value returns the most-voted stations.
type Query struct {
	// Name restricts results by station name.
	Name string
	// Tag restricts results to an exact directory tag.
	Tag string
	// Language restricts results by language.
	Language string
	// CountryCode restricts results to an ISO country code.
	CountryCode string
	// State restricts results to an exact region.
	State string
	// Sort chooses votes, listens, trending, name, or random order.
	Sort string
	// Offset skips matching results.
	Offset int
	// Limit caps results between one and 200.
	Limit int
}

const (
	// SortVotes orders by community votes.
	SortVotes = "votes"
	// SortListens orders by total directory clicks.
	SortListens = "clickcount"
	// SortTrending orders by recent click growth.
	SortTrending = "clicktrend"
	// SortName orders alphabetically.
	SortName = "name"
	// SortRandom asks the directory for a random order.
	SortRandom = "random"
)

// Client retrieves catalog data with bounded requests.
type Client struct {
	// HTTP performs bounded directory requests.
	HTTP *http.Client
	// BaseURL can be replaced for mirrors and tests.
	BaseURL  string
	resolver func(context.Context) ([]string, error)
	mu       sync.Mutex
	servers  []string
}

// NewClient constructs a catalog client suitable for CLI and interactive use.
func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
					return fmt.Errorf("directory redirected to an unsupported URL")
				}
				return nil
			},
		},
		resolver: discoverServers,
	}
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	servers, err := c.baseURLs(ctx)
	if err != nil {
		return err
	}
	var lastErr error
	for _, base := range servers {
		data, requestErr := c.request(ctx, strings.TrimRight(base, "/")+path)
		if requestErr != nil {
			lastErr = requestErr
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if err := json.Unmarshal(data, out); err != nil {
			lastErr = fmt.Errorf("radio directory response: %w", err)
			continue
		}
		c.promote(base)
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("radio directory has no available servers")
	}
	return lastErr
}

func (c *Client) request(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "chill/1.0")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("radio directory: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("radio directory returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, fmt.Errorf("radio directory response is too large")
	}
	return data, nil
}

func (c *Client) baseURLs(ctx context.Context) ([]string, error) {
	if base := strings.TrimSpace(c.BaseURL); base != "" {
		return []string{base}, nil
	}
	c.mu.Lock()
	if len(c.servers) > 0 {
		servers := append([]string(nil), c.servers...)
		c.mu.Unlock()
		return servers, nil
	}
	c.mu.Unlock()
	resolver := c.resolver
	if resolver == nil {
		resolver = discoverServers
	}
	servers, err := resolver(ctx)
	if err != nil || len(servers) == 0 {
		servers = []string{fallbackBaseURL}
	}
	c.mu.Lock()
	if len(c.servers) == 0 {
		c.servers = append([]string(nil), servers...)
	}
	servers = append([]string(nil), c.servers...)
	c.mu.Unlock()
	return servers, nil
}

func (c *Client) promote(base string) {
	if strings.TrimSpace(c.BaseURL) != "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	index := slices.Index(c.servers, base)
	if index > 0 {
		copy(c.servers[1:index+1], c.servers[:index])
		c.servers[0] = base
	}
}

func discoverServers(ctx context.Context) ([]string, error) {
	lookupContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, records, err := net.DefaultResolver.LookupSRV(lookupContext, "api", "tcp", "radio-browser.info")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	servers := make([]string, 0, len(records))
	for _, record := range records {
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(record.Target)), ".")
		if host == "" || !strings.HasSuffix(host, ".api.radio-browser.info") {
			continue
		}
		address := host
		if record.Port != 0 && record.Port != 443 {
			address = net.JoinHostPort(host, strconv.Itoa(int(record.Port)))
		}
		base := "https://" + address + "/json"
		if !seen[base] {
			seen[base] = true
			servers = append(servers, base)
		}
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("radio directory DNS returned no servers")
	}
	if len(servers) > 1 {
		offset := int(time.Now().UnixNano() % int64(len(servers)))
		servers = append(servers[offset:], servers[:offset]...)
	}
	return servers, nil
}

func validHTTP(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func normalizedSort(sort string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(sort)) {
	case "", "top", "votes", "voted":
		return SortVotes, nil
	case "popular", "listens", "listened", "clicks", "clickcount":
		return SortListens, nil
	case "trending", "trend", "clicktrend":
		return SortTrending, nil
	case "name", "alphabetical":
		return SortName, nil
	case "random":
		return SortRandom, nil
	default:
		return "", fmt.Errorf("sort must be top, popular, trending, name, or random")
	}
}

func (q Query) values() (url.Values, error) {
	sort, err := normalizedSort(q.Sort)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("limit must be between 1 and 200")
	}
	if q.Offset < 0 || q.Offset > 100000 {
		return nil, fmt.Errorf("offset must be between 0 and 100000")
	}
	v := url.Values{
		"hidebroken": {"true"},
		"limit":      {strconv.Itoa(limit)},
		"offset":     {strconv.Itoa(q.Offset)},
		"order":      {sort},
	}
	if sort != SortName && sort != SortRandom {
		v.Set("reverse", "true")
	}
	if value := strings.TrimSpace(q.Name); value != "" {
		v.Set("name", value)
	}
	if value := strings.TrimSpace(q.Tag); value != "" {
		v.Set("tag", value)
		v.Set("tagExact", "true")
	}
	if value := strings.TrimSpace(q.Language); value != "" {
		v.Set("language", value)
	}
	if value := strings.ToUpper(strings.TrimSpace(q.CountryCode)); value != "" {
		if len(value) != 2 {
			return nil, fmt.Errorf("country must be a two-letter code")
		}
		v.Set("countrycode", value)
	}
	if value := strings.TrimSpace(q.State); value != "" {
		v.Set("state", value)
		v.Set("stateExact", "true")
	}
	return v, nil
}

// Stations retrieves, validates, and deduplicates matching stations.
func (c *Client) Stations(ctx context.Context, q Query) ([]Station, error) {
	v, err := q.values()
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID          string `json:"stationuuid"`
		Name        string `json:"name"`
		URL         string `json:"url_resolved"`
		FallbackURL string `json:"url"`
		Country     string `json:"country"`
		CountryCode string `json:"countrycode"`
		State       string `json:"state"`
		Language    string `json:"language"`
		Tags        string `json:"tags"`
		Codec       string `json:"codec"`
		Bitrate     int    `json:"bitrate"`
		Votes       int    `json:"votes"`
		Homepage    string `json:"homepage"`
		Favicon     string `json:"favicon"`
	}
	if err := c.get(ctx, "/stations/search?"+v.Encode(), &raw); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(raw))
	stations := make([]Station, 0, len(raw))
	for _, r := range raw {
		streamURL := strings.TrimSpace(r.URL)
		if !validHTTP(streamURL) {
			streamURL = strings.TrimSpace(r.FallbackURL)
		}
		name := strings.TrimSpace(r.Name)
		if name == "" || !validHTTP(streamURL) || seen[streamURL] {
			continue
		}
		seen[streamURL] = true
		id := strings.TrimSpace(r.ID)
		if id == "" {
			id = streamURL
		}
		homepage, favicon := strings.TrimSpace(r.Homepage), strings.TrimSpace(r.Favicon)
		if !validHTTP(homepage) {
			homepage = ""
		}
		if !validHTTP(favicon) {
			favicon = ""
		}
		stations = append(stations, Station{
			ID: id, Name: name, URL: streamURL, Country: strings.TrimSpace(r.Country),
			CountryCode: strings.ToUpper(strings.TrimSpace(r.CountryCode)), State: strings.TrimSpace(r.State),
			Language: strings.TrimSpace(r.Language), Tags: strings.TrimSpace(r.Tags), Codec: strings.ToUpper(strings.TrimSpace(r.Codec)),
			Bitrate: max(0, r.Bitrate), Votes: max(0, r.Votes), Homepage: homepage, Favicon: favicon,
		})
	}
	return stations, nil
}

// Countries returns the catalog's country index with duplicate codes merged.
func (c *Client) Countries(ctx context.Context) ([]Place, error) {
	var raw []struct {
		Name  string `json:"name"`
		Code  string `json:"iso_3166_1"`
		Count int    `json:"stationcount"`
	}
	if err := c.get(ctx, "/countries", &raw); err != nil {
		return nil, err
	}
	byCode := make(map[string]int)
	var places []Place
	for _, r := range raw {
		code, name := strings.ToUpper(strings.TrimSpace(r.Code)), strings.TrimSpace(r.Name)
		if len(code) != 2 || name == "" || r.Count <= 0 {
			continue
		}
		if i, ok := byCode[code]; ok {
			places[i].StationCount += r.Count
			continue
		}
		byCode[code] = len(places)
		places = append(places, Place{Name: name, Code: code, StationCount: r.Count})
	}
	slices.SortFunc(places, func(a, b Place) int {
		if a.StationCount != b.StationCount {
			return b.StationCount - a.StationCount
		}
		return strings.Compare(a.Name, b.Name)
	})
	return places, nil
}

// States returns known regions within a country.
func (c *Client) States(ctx context.Context, country, code string) ([]Place, error) {
	country = strings.TrimSpace(country)
	code = strings.ToUpper(strings.TrimSpace(code))
	if country == "" || len(code) != 2 {
		return nil, fmt.Errorf("country needs a name and two-letter code")
	}
	var raw []struct {
		Name    string `json:"name"`
		Country string `json:"country"`
		Count   int    `json:"stationcount"`
	}
	if err := c.get(ctx, "/states/"+url.PathEscape(country)+"?hidebroken=true", &raw); err != nil {
		return nil, err
	}
	var places []Place
	for _, r := range raw {
		if name := strings.TrimSpace(r.Name); name != "" && r.Count > 0 {
			places = append(places, Place{Name: name, Code: code, Country: strings.TrimSpace(r.Country), StationCount: r.Count})
		}
	}
	slices.SortFunc(places, func(a, b Place) int {
		if a.StationCount != b.StationCount {
			return b.StationCount - a.StationCount
		}
		return strings.Compare(a.Name, b.Name)
	})
	return places, nil
}

// Tags returns useful directory tags, highest station count first.
func (c *Client) Tags(ctx context.Context) ([]Tag, error) {
	var raw []struct {
		Name  string `json:"name"`
		Count int    `json:"stationcount"`
	}
	if err := c.get(ctx, "/tags?order=stationcount&reverse=true&hidebroken=true", &raw); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var tags []Tag
	for _, r := range raw {
		name := strings.TrimSpace(r.Name)
		key := strings.ToLower(name)
		if name == "" || r.Count <= 0 || seen[key] {
			continue
		}
		seen[key] = true
		tags = append(tags, Tag{Name: name, StationCount: r.Count})
	}
	return tags, nil
}

// RecordClick lets the community directory count a successful play. Failure is non-fatal to playback.
func (c *Client) RecordClick(ctx context.Context, stationID string) error {
	stationID = strings.TrimSpace(stationID)
	if stationID == "" || strings.Contains(stationID, "/") {
		return nil
	}
	var ignored any
	return c.get(ctx, "/url/"+url.PathEscape(stationID), &ignored)
}
