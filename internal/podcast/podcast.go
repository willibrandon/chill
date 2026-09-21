// Package podcast reads public podcast directories and publisher feeds.
package podcast

import (
	"crypto/sha256"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Show is a publisher's podcast and the feed used to retrieve its episodes.
type Show struct {
	// ID is the directory identifier, empty for a directly opened feed.
	ID string `json:"id,omitempty"`
	// Title is the display name of the show.
	Title string `json:"title"`
	// Author identifies the publisher or host.
	Author string `json:"author,omitempty"`
	// FeedURL is the stable subscription address.
	FeedURL string `json:"feed_url"`
	// Artwork is the optional publisher image URL.
	Artwork string `json:"artwork,omitempty"`
}

// Episode identifies a finite audio item independently of the playback process.
type Episode struct {
	// FeedURL scopes episode identifiers to their originating show.
	FeedURL string `json:"feed_url"`
	// GUID is the publisher identifier, if provided.
	GUID string `json:"guid,omitempty"`
	// Title is the episode's plain-text name.
	Title string `json:"title"`
	// Show is the display name of the parent podcast.
	Show string `json:"show"`
	// Author identifies the podcast publisher or host.
	Author string `json:"author,omitempty"`
	// Artwork is the parent podcast's image URL.
	Artwork string `json:"artwork,omitempty"`
	// URL points to the encoded audio enclosure.
	URL string `json:"url"`
	// Published is the publication date, or zero when unknown.
	Published time.Time `json:"published,omitzero"`
	// Duration is the publisher's length estimate in seconds.
	Duration float64 `json:"duration,omitempty"`
	// Description is a plain-text episode summary.
	Description string `json:"description,omitempty"`
}

// Feed contains a show's metadata and playable episodes in publisher order.
type Feed struct {
	// Show describes the feed's publisher metadata.
	Show Show `json:"show"`
	// Episodes contains at most 300 playable items.
	Episodes []Episode `json:"episodes"`
}

// Key survives publisher changes to signed media URLs whenever the feed has
// a GUID, or a title and publication date. Feed identity scopes publisher IDs.
func (e Episode) Key() string {
	id := "guid:" + e.GUID
	if e.GUID == "" {
		id = "url:" + e.URL
		if e.Title != "" && !e.Published.IsZero() {
			id = "published:" + e.Published.UTC().Format(time.RFC3339) + ":" + e.Title
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(e.FeedURL+"\x00"+id)))
}

// ValidURL accepts absolute HTTP(S) addresses without embedded credentials.
func ValidURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && !strings.ContainsAny(raw, "\r\n")
}

// Country validates and lowercases a two-letter chart country code.
func Country(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 2 || s[0] < 'a' || s[0] > 'z' || s[1] < 'a' || s[1] > 'z' {
		return "", fmt.Errorf("country must be a two-letter code, such as us or gb")
	}
	return s, nil
}

var tags = regexp.MustCompile(`<[^>]*>`)

// Text strips markup and terminal control characters from publisher metadata.
func Text(s string) string {
	s = html.UnescapeString(tags.ReplaceAllString(s, " "))
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > 4096 {
		s = string(runes[:4096])
	}
	return s
}

// Category names a supported podcast directory genre.
type Category struct {
	// ID is Apple's genre identifier.
	ID string `json:"id"`
	// Name is the display name and discovery search term.
	Name string `json:"name"`
}

// Categories lists the 19 top-level genres in display order.
var Categories = []Category{
	{"1301", "Arts"}, {"1321", "Business"}, {"1303", "Comedy"}, {"1304", "Education"},
	{"1483", "Fiction"}, {"1511", "Government"}, {"1512", "Health & Fitness"}, {"1487", "History"},
	{"1305", "Kids & Family"}, {"1502", "Leisure"}, {"1310", "Music"}, {"1489", "News"},
	{"1314", "Religion & Spirituality"}, {"1533", "Science"}, {"1324", "Society & Culture"},
	{"1545", "Sports"}, {"1318", "Technology"}, {"1488", "True Crime"}, {"1309", "TV & Film"},
}
