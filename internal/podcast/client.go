package podcast

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client retrieves podcast discovery data and RSS feeds with bounded requests.
type Client struct {
	// HTTP supplies request deadlines and redirect policy.
	HTTP *http.Client
	// DirectoryURL is the search and lookup service base URL.
	DirectoryURL string
	// ChartsURL is the chart service base URL.
	ChartsURL string
}

// NewClient configures public discovery endpoints and a 30-second timeout.
func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if !ValidURL(req.URL.String()) {
			return fmt.Errorf("redirect is not an HTTP(S) URL")
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}}, DirectoryURL: "https://itunes.apple.com", ChartsURL: "https://rss.marketingtools.apple.com/api/v2"}
}

func (c *Client) get(ctx context.Context, raw string) (*http.Response, error) {
	if !ValidURL(raw) {
		return nil, fmt.Errorf("enter an HTTP(S) feed URL")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "chill/1.0")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("server returned %s", resp.Status)
	}
	return resp, nil
}

func (c *Client) json(ctx context.Context, raw string, result any) error {
	resp, err := c.get(ctx, raw)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return fmt.Errorf("directory response is too large")
	}
	return json.Unmarshal(data, result)
}

func (c *Client) shows(ctx context.Context, endpoint string, q url.Values) ([]Show, error) {
	var response struct {
		Results []struct {
			ID      json.Number `json:"collectionId"`
			Title   string      `json:"collectionName"`
			Author  string      `json:"artistName"`
			FeedURL string      `json:"feedUrl"`
			Artwork string      `json:"artworkUrl600"`
		} `json:"results"`
	}
	if err := c.json(ctx, c.DirectoryURL+"/"+endpoint+"?"+q.Encode(), &response); err != nil {
		return nil, err
	}
	if response.Results == nil {
		return nil, fmt.Errorf("directory returned no results array")
	}
	shows := make([]Show, 0, len(response.Results))
	for _, r := range response.Results {
		if !ValidURL(r.FeedURL) {
			continue
		}
		title := Text(r.Title)
		if title == "" {
			title = "Untitled show"
		}
		shows = append(shows, Show{ID: r.ID.String(), Title: title, Author: Text(r.Author), FeedURL: r.FeedURL, Artwork: r.Artwork})
	}
	return shows, nil
}

func unique(shows []Show) []Show {
	seen := map[string]bool{}
	out := make([]Show, 0, len(shows))
	for _, s := range shows {
		if !seen[s.FeedURL] {
			seen[s.FeedURL] = true
			out = append(out, s)
		}
	}
	return out
}

// Search finds up to 100 shows, optionally restricted to a genre ID.
func (c *Client) Search(ctx context.Context, query, genre string) ([]Show, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	q := url.Values{"term": {query}, "media": {"podcast"}, "entity": {"podcast"}, "limit": {"100"}}
	if genre != "" {
		q.Set("genreId", genre)
	}
	shows, err := c.shows(ctx, "search", q)
	return unique(shows), err
}

// Top resolves a country's top 100 chart to unique feeds in ranking order.
func (c *Client) Top(ctx context.Context, country string) ([]Show, error) {
	country, err := Country(country)
	if err != nil {
		return nil, err
	}
	var chart struct {
		Feed struct {
			Results []struct {
				ID string `json:"id"`
			} `json:"results"`
		} `json:"feed"`
	}
	if err := c.json(ctx, c.ChartsURL+"/"+country+"/podcasts/top/100/podcasts.json", &chart); err != nil {
		return nil, err
	}
	if chart.Feed.Results == nil {
		return nil, fmt.Errorf("chart returned no results array")
	}
	var ids []string
	for _, r := range chart.Feed.Results {
		if r.ID != "" {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	shows, err := c.shows(ctx, "lookup", url.Values{"id": {strings.Join(ids, ",")}, "entity": {"podcast"}})
	if err != nil {
		return nil, err
	}
	byID := map[string]Show{}
	for _, s := range shows {
		byID[s.ID] = s
	}
	var ranked []Show
	for _, id := range ids {
		if s, ok := byID[id]; ok {
			ranked = append(ranked, s)
		}
	}
	return unique(ranked), nil
}
