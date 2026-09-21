// Package lyrics retrieves song lyrics for recognized now-playing metadata.
package lyrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultBaseURL = "https://lrclib.net/api"

// Result is one lyrics match.
type Result struct {
	// Track is the matched track title.
	Track string `json:"track"`
	// Artist is the matched artist.
	Artist string `json:"artist"`
	// Album is the matched album when available.
	Album string `json:"album,omitempty"`
	// Plain contains untimed lyrics.
	Plain string `json:"plain,omitempty"`
	// Synced contains LRC timestamped lyrics.
	Synced string `json:"synced,omitempty"`
	// Instrumental reports a match intentionally containing no lyrics.
	Instrumental bool `json:"instrumental,omitempty"`
}

// Client retrieves lyrics with a bounded request and an on-disk cache.
type Client struct {
	// HTTP performs lyrics requests.
	HTTP *http.Client
	// BaseURL can be replaced for tests.
	BaseURL string
	// CacheDir stores successful responses; empty disables caching.
	CacheDir string
}

// NewClient creates the default public lyrics client.
func NewClient() *Client {
	cache := ""
	if dir, err := os.UserCacheDir(); err == nil {
		cache = filepath.Join(dir, "chill", "lyrics")
	}
	return &Client{HTTP: &http.Client{Timeout: 15 * time.Second}, BaseURL: defaultBaseURL, CacheDir: cache}
}

func cacheKey(artist, title string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(artist)) + "\x00" + strings.ToLower(strings.TrimSpace(title))))
	return hex.EncodeToString(sum[:])
}

func (c *Client) cached(key string) (Result, bool) {
	if c.CacheDir == "" {
		return Result{}, false
	}
	data, err := os.ReadFile(filepath.Join(c.CacheDir, key+".json"))
	if err != nil {
		return Result{}, false
	}
	var result Result
	if json.Unmarshal(data, &result) != nil {
		return Result{}, false
	}
	return result, true
}

func (c *Client) cache(key string, result Result) {
	if c.CacheDir == "" {
		return
	}
	if err := os.MkdirAll(c.CacheDir, 0700); err != nil {
		return
	}
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	path := filepath.Join(c.CacheDir, key+".json")
	f, err := os.CreateTemp(c.CacheDir, ".lyrics-*")
	if err != nil {
		return
	}
	temp := f.Name()
	defer os.Remove(temp)
	if f.Chmod(0600) != nil {
		f.Close()
		return
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return
	}
	if f.Close() == nil {
		_ = os.Rename(temp, path)
	}
}

type wireResult struct {
	// Track is the service's trackName field.
	Track string `json:"trackName"`
	// Artist is the service's artistName field.
	Artist string `json:"artistName"`
	// Album is the service's albumName field.
	Album string `json:"albumName"`
	// Plain is the service's plainLyrics field.
	Plain string `json:"plainLyrics"`
	// Synced is the service's syncedLyrics field.
	Synced string `json:"syncedLyrics"`
	// Instrumental is true when the match intentionally has no words.
	Instrumental bool `json:"instrumental"`
}

func fromWire(w wireResult) Result {
	return Result{Track: strings.TrimSpace(w.Track), Artist: strings.TrimSpace(w.Artist), Album: strings.TrimSpace(w.Album), Plain: strings.TrimSpace(w.Plain), Synced: strings.TrimSpace(w.Synced), Instrumental: w.Instrumental}
}

func (c *Client) request(ctx context.Context, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "chill/1.0")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("lyrics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("lyrics service returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil {
		return resp.StatusCode, err
	}
	if len(data) > 2<<20 {
		return resp.StatusCode, fmt.Errorf("lyrics response is too large")
	}
	return resp.StatusCode, json.Unmarshal(data, out)
}

// Get returns the closest lyrics match for an artist and title.
func (c *Client) Get(ctx context.Context, artist, title string) (Result, error) {
	artist, title = strings.TrimSpace(artist), strings.TrimSpace(title)
	if title == "" {
		return Result{}, fmt.Errorf("no recognized track title")
	}
	key := cacheKey(artist, title)
	if result, ok := c.cached(key); ok {
		return result, nil
	}
	values := url.Values{"track_name": {title}}
	if artist != "" {
		values.Set("artist_name", artist)
	}
	if artist != "" {
		var exact wireResult
		status, err := c.request(ctx, "/get?"+values.Encode(), &exact)
		if err != nil {
			return Result{}, err
		}
		if status == http.StatusOK {
			result := fromWire(exact)
			if result.Plain != "" || result.Synced != "" || result.Instrumental {
				c.cache(key, result)
				return result, nil
			}
		}
	}
	var matches []wireResult
	_, err := c.request(ctx, "/search?"+values.Encode(), &matches)
	if err != nil {
		return Result{}, err
	}
	for _, match := range matches {
		result := fromWire(match)
		if result.Plain != "" || result.Synced != "" || result.Instrumental {
			c.cache(key, result)
			return result, nil
		}
	}
	return Result{}, fmt.Errorf("lyrics not found for %s", title)
}

// Lines returns displayable text, stripping LRC timestamps for live streams.
func (r Result) Lines() []string {
	text := r.Plain
	if text == "" {
		text = r.Synced
	}
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		for strings.HasPrefix(line, "[") {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				break
			}
			line = strings.TrimSpace(line[end+1:])
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
