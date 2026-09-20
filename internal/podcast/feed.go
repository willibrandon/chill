package podcast

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"mime"
	"net/mail"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
)

const maxEpisodes = 300

type enclosure struct {
	// URL is the RSS attachment address.
	URL string `xml:"url,attr"`
	// Type is the attachment's declared media type.
	Type string `xml:"type,attr"`
}
type feedItem struct {
	// Title is the publisher's episode name.
	Title string `xml:"title"`
	// GUID is the RSS episode identifier.
	GUID string `xml:"guid"`
	// ID is the Atom episode identifier.
	ID string `xml:"id"`
	// Date is the RSS publication timestamp.
	Date string `xml:"pubDate"`
	// Published is the Atom publication timestamp.
	Published string `xml:"published"`
	// Description is the RSS episode summary.
	Description string `xml:"description"`
	// Summary is the Atom episode summary.
	Summary string `xml:"summary"`
	// Duration is the iTunes duration tag.
	Duration string `xml:"http://www.itunes.com/dtds/podcast-1.0.dtd duration"`
	// Enclosures contains RSS media attachments.
	Enclosures []enclosure `xml:"enclosure"`
	// Links contains Atom relationships, including media enclosures.
	Links []struct {
		// Rel identifies the relationship, such as enclosure.
		Rel string `xml:"rel,attr"`
		// Href is the target address.
		Href string `xml:"href,attr"`
		// Type is the target's media type.
		Type string `xml:"type,attr"`
	} `xml:"link"`
}

// Duration parses seconds, MM:SS or HH:MM:SS; invalid values return zero.
func Duration(s string) float64 {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) > 3 {
		return 0
	}
	seconds := 0.0
	for _, p := range parts {
		n, err := strconv.ParseFloat(p, 64)
		if err != nil || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0
		}
		seconds = seconds*60 + n
	}
	if seconds > 365*24*3600 {
		return 0
	}
	return seconds
}

func publication(s string) time.Time {
	if date, err := mail.ParseDate(strings.TrimSpace(s)); err == nil {
		return date
	}
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339, "Mon, 2 Jan 2006 15:04:05 -0700", time.DateOnly} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func audioURL(raw, typ string, base *url.URL) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || raw == "" {
		return ""
	}
	u = base.ResolveReference(u)
	if !ValidURL(u.String()) {
		return ""
	}
	media, _, err := mime.ParseMediaType(typ)
	if typ != "" && err != nil {
		return ""
	}
	if strings.HasPrefix(media, "audio/") || media == "application/ogg" {
		return u.String()
	}
	if media == "" || media == "application/octet-stream" || media == "binary/octet-stream" {
		switch strings.ToLower(path.Ext(u.Path)) {
		case ".mp3", ".m4a", ".aac", ".ogg", ".opus", ".flac", ".wav", ".mp4", ".webm":
			return u.String()
		}
	}
	return ""
}

// Feed reads up to 300 playable RSS or Atom episodes from a publisher URL.
func (c *Client) Feed(ctx context.Context, raw string) (Feed, error) {
	resp, err := c.get(ctx, raw)
	if err != nil {
		return Feed{}, err
	}
	defer resp.Body.Close()
	return parseFeed(ctx, resp.Body, raw, resp.Request.URL)
}

func parseFeed(ctx context.Context, reader io.Reader, original string, base *url.URL) (Feed, error) {
	limited := &io.LimitedReader{R: reader, N: 32 << 20}
	dec := xml.NewDecoder(limited)
	dec.CharsetReader = charset.NewReaderLabel
	f := Feed{Show: Show{FeedURL: original}}
	var stack []string
	seen := map[string]bool{}
	valid := false
	for len(f.Episodes) < maxEpisodes {
		if err := ctx.Err(); err != nil {
			return Feed{}, err
		}
		token, err := dec.Token()
		if err == io.EOF {
			if limited.N == 0 {
				return Feed{}, fmt.Errorf("feed exceeds 32 MiB")
			}
			break
		}
		if err != nil {
			return Feed{}, fmt.Errorf("read feed: %w", err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			if len(stack) == 0 && t.Name.Local != "rss" && t.Name.Local != "feed" {
				return Feed{}, fmt.Errorf("URL did not return an RSS or Atom feed")
			}
			parent := ""
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			if (parent == "channel" && t.Name.Local == "item") || (parent == "feed" && t.Name.Local == "entry") {
				var item feedItem
				if err := dec.DecodeElement(&item, &t); err != nil {
					return Feed{}, fmt.Errorf("read episode: %w", err)
				}
				for _, l := range item.Links {
					if l.Rel == "enclosure" {
						item.Enclosures = append(item.Enclosures, enclosure{l.Href, l.Type})
					}
				}
				for _, enc := range item.Enclosures {
					audio := audioURL(enc.URL, enc.Type, base)
					if audio == "" {
						continue
					}
					guid := strings.TrimSpace(item.GUID)
					if guid == "" {
						guid = strings.TrimSpace(item.ID)
					}
					date := item.Date
					if date == "" {
						date = item.Published
					}
					title := Text(item.Title)
					if title == "" {
						title = "Untitled episode"
					}
					desc := item.Description
					if desc == "" {
						desc = item.Summary
					}
					e := Episode{FeedURL: original, GUID: guid, Title: title, URL: audio, Published: publication(date), Duration: Duration(item.Duration), Description: Text(desc)}
					if !seen[e.Key()] {
						f.Episodes = append(f.Episodes, e)
						seen[e.Key()] = true
					}
					break
				}
				continue
			}
			if parent == "channel" || parent == "feed" {
				switch t.Name.Local {
				case "title", "author", "image":
					var value struct {
						Text string `xml:",chardata"`
						Name string `xml:"name"`
						Href string `xml:"href,attr"`
						URL  string `xml:"url"`
					}
					if err := dec.DecodeElement(&value, &t); err != nil {
						return Feed{}, err
					}
					if t.Name.Local == "title" {
						f.Show.Title = Text(value.Text)
					}
					if t.Name.Local == "author" {
						f.Show.Author = Text(value.Text + " " + value.Name)
					}
					if t.Name.Local == "image" {
						art := value.Href
						if art == "" {
							art = value.URL
						}
						if ValidURL(art) {
							f.Show.Artwork = art
						}
					}
					continue
				}
			}
			if t.Name.Local == "channel" && parent == "rss" || t.Name.Local == "feed" && len(stack) == 0 {
				valid = true
			}
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if !valid {
		return Feed{}, fmt.Errorf("URL did not return a podcast feed")
	}
	if f.Show.Title == "" {
		f.Show.Title = original
	}
	for i := range f.Episodes {
		f.Episodes[i].Show = f.Show.Title
	}
	if len(f.Episodes) == 0 {
		return Feed{}, fmt.Errorf("feed contains no playable audio episodes")
	}
	return f, nil
}
