package podcast

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestDiscoveryPreservesRankAndDeduplicatesFeeds checks chart order and search parameters.
func TestDiscoveryPreservesRankAndDeduplicatesFeeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/us/podcasts/top/100/podcasts.json":
			fmt.Fprint(w, `{"feed":{"results":[{"id":"2"},{"id":"1"},{"id":"3"},{"id":"4"}]}}`)
		case "/lookup":
			if r.URL.Query().Get("id") != "2,1,3,4" {
				t.Errorf("lookup IDs: %v", r.URL.Query())
			}
			fmt.Fprint(w, `{"results":[{"collectionId":1,"collectionName":"first","feedUrl":"https://example.com/a"},{"collectionId":2,"collectionName":"second","feedUrl":"https://example.com/b"},{"collectionId":3,"feedUrl":"https://example.com/b"},{"collectionId":4,"feedUrl":"file:///tmp/a"}]}`)
		case "/search":
			if r.URL.Query().Get("genreId") != "1318" || r.URL.Query().Get("term") != "Technology" || r.URL.Query().Get("media") != "podcast" {
				t.Errorf("search parameters: %v", r.URL.Query())
			}
			fmt.Fprint(w, `{"results":[]}`)
		}
	}))
	defer server.Close()
	c := NewClient()
	c.DirectoryURL, c.ChartsURL = server.URL, server.URL
	shows, err := c.Top(context.Background(), "US")
	if err != nil || len(shows) != 2 || shows[0].ID != "2" || shows[1].ID != "1" {
		t.Fatalf("ranked shows: %+v, %v", shows, err)
	}
	if _, err := c.Search(context.Background(), "Technology", "1318"); err != nil {
		t.Fatal(err)
	}
}

// TestFeedReadsMetadataAndSkipsNonAudio checks episode metadata and stable identity.
func TestFeedReadsMetadataAndSkipsNonAudio(t *testing.T) {
	body := `<?xml version="1.0"?><rss xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel><title>Show &amp; Tell</title><itunes:author>Host</itunes:author>
<item><title>Episode &amp; one</title><guid>episode-1</guid><pubDate>Sun, 20 Sep 2026 12:00:00 +0000</pubDate><itunes:duration>1:02:03</itunes:duration><description><![CDATA[<p>A story</p>]]></description><enclosure url="/audio?id=1" type="audio/mpeg"/></item>
<item><title>Video</title><enclosure url="https://example.com/video.mp4" type="video/mp4"/></item>
<item><title>No GUID</title><enclosure url="https://example.com/file.mp3"/></item>
<item><title>Duplicate</title><guid>episode-1</guid><enclosure url="https://example.com/other.mp3"/></item>
</channel></rss>`
	base, _ := url.Parse("https://example.com/feed")
	f, err := parseFeed(context.Background(), strings.NewReader(body), base.String(), base)
	if err != nil {
		t.Fatal(err)
	}
	if f.Show.Title != "Show & Tell" || f.Show.Author != "Host" || len(f.Episodes) != 2 {
		t.Fatalf("feed: %+v", f)
	}
	e := f.Episodes[0]
	if e.Duration != 3723 || e.URL != "https://example.com/audio?id=1" || e.Description != "A story" || e.Published.IsZero() {
		t.Fatalf("episode: %+v", e)
	}
	old := e.Key()
	e.URL = "https://example.com/refreshed"
	if old != e.Key() {
		t.Fatal("signed URL change lost identity")
	}
	e.FeedURL = "https://example.com/another-feed"
	if old == e.Key() {
		t.Fatal("GUIDs crossed feeds")
	}
}

// TestFeedAtomLimitsErrorsAndCancellation checks feed formats and bounded parsing.
func TestFeedAtomLimitsErrorsAndCancellation(t *testing.T) {
	base, _ := url.Parse("https://example.com/rss")
	atom := `<feed xmlns="http://www.w3.org/2005/Atom"><title>Atom Show</title><entry><id>a</id><title>A</title><published>2026-09-20T12:00:00Z</published><link rel="enclosure" href="https://example.com/a" type="audio/ogg"/></entry></feed>`
	if f, err := parseFeed(context.Background(), strings.NewReader(atom), base.String(), base); err != nil || len(f.Episodes) != 1 {
		t.Fatalf("Atom: %+v %v", f, err)
	}
	for _, body := range []string{`<html>not a feed</html>`, `<rss><channel>`, `<rss><channel/></rss>`} {
		if _, err := parseFeed(context.Background(), strings.NewReader(body), base.String(), base); err == nil {
			t.Fatal("accepted invalid/empty feed", body)
		}
	}
	var b strings.Builder
	b.WriteString("<rss><channel>")
	for i := range 350 {
		fmt.Fprintf(&b, `<item><guid>%d</guid><enclosure type="audio/mpeg" url="https://example.com/%d"/></item>`, i, i)
	}
	b.WriteString("</channel></rss>")
	if f, err := parseFeed(context.Background(), strings.NewReader(b.String()), base.String(), base); err != nil || len(f.Episodes) != 300 {
		t.Fatalf("feed limit: %d %v", len(f.Episodes), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parseFeed(ctx, strings.NewReader(atom), base.String(), base); err == nil {
		t.Fatal("ignored cancellation")
	}
	for _, s := range []string{"NaN", "Inf", "-1", "1:2:3:4"} {
		if Duration(s) != 0 {
			t.Fatal("invalid duration", s)
		}
	}
}

// TestDirectoryErrorsAndCancellation checks HTTP failures and cancelled searches.
func TestDirectoryErrorsAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "try later", http.StatusTooManyRequests) }))
	defer server.Close()
	c := NewClient()
	c.DirectoryURL = server.URL
	if _, err := c.Search(context.Background(), "test", ""); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatal("HTTP error lost", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Search(ctx, "test", ""); err == nil {
		t.Fatal("request ignored cancellation")
	}
}
