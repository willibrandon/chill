package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/willibrandon/chill/internal/podcast"
)

type episodeProgress struct {
	// Position is the saved playhead in seconds.
	Position float64 `json:"position"`
	// Duration is the last known episode length in seconds.
	Duration float64 `json:"duration,omitempty"`
	// Played marks a completed or nearly completed episode.
	Played bool `json:"played"`
	// Updated determines retention order when pruning old records.
	Updated time.Time `json:"updated"`
}

// Only the daemon writes this file. REPLs may read it while the daemon is off.
type podcastLibrary struct {
	// Speed is the preferred podcast playback rate, from 0.5 to 3.
	Speed float64 `json:"speed"`
	// Version identifies the persistent file format.
	Version int `json:"version"`
	// Country selects the two-letter Apple chart country.
	Country string `json:"country"`
	// Subscriptions preserves saved shows in subscription order.
	Subscriptions []podcast.Show `json:"subscriptions"`
	// Progress maps stable episode keys to listening records.
	Progress map[string]episodeProgress `json:"progress"`
}

func podcastPath() string {
	if p := configPath(); p != "" {
		return filepath.Join(filepath.Dir(p), "podcasts.json")
	}
	return ""
}

func loadPodcastLibrary() (*podcastLibrary, error) {
	l := &podcastLibrary{Version: 1, Country: "us", Speed: 1, Subscriptions: []podcast.Show{}, Progress: map[string]episodeProgress{}}
	data, err := os.ReadFile(podcastPath())
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read podcasts: %w", err)
	}
	if err := json.Unmarshal(data, l); err != nil {
		return nil, fmt.Errorf("read podcasts: %w", err)
	}
	if l.Version != 1 {
		return nil, fmt.Errorf("unsupported podcasts.json version %d", l.Version)
	}
	if _, err := podcast.Country(l.Country); err != nil {
		return nil, err
	}
	if l.Progress == nil {
		l.Progress = map[string]episodeProgress{}
	}
	for _, show := range l.Subscriptions {
		if !podcast.ValidURL(show.FeedURL) {
			return nil, fmt.Errorf("invalid subscription URL in podcasts.json")
		}
	}
	for _, p := range l.Progress {
		if p.Position < 0 || p.Duration < 0 || p.Position > 365*24*3600 || p.Duration > 365*24*3600 || math.IsNaN(p.Position) || math.IsNaN(p.Duration) {
			return nil, fmt.Errorf("invalid listening progress in podcasts.json")
		}
	}
	if l.Speed < 0.5 || l.Speed > 3 {
		return nil, fmt.Errorf("invalid podcast speed in podcasts.json")
	}
	return l, nil
}

func (d *Daemon) podcastLibrary() (*podcastLibrary, error) {
	if d.podcasts == nil {
		l, err := loadPodcastLibrary()
		if err != nil {
			return nil, err
		}
		d.podcasts = l
		if d.speed == 0 {
			d.speed = l.Speed
		}
	}
	return d.podcasts, nil
}

func (l *podcastLibrary) commit(next podcastLibrary) error {
	if err := writeJSON(podcastPath(), next); err != nil {
		return err
	}
	*l = next
	return nil
}

func (d *Daemon) podcastSettings(action, arg string) string {
	l, err := d.podcastLibrary()
	if err != nil {
		return fail(err.Error())
	}
	next := *l
	switch action {
	case "podcasts":
		b, _ := json.Marshal(l)
		return ok(string(b))
	case "podcast-country":
		next.Country, err = podcast.Country(arg)
	case "podcast-subscribe", "podcast-unsubscribe":
		var show podcast.Show
		if err = json.Unmarshal([]byte(arg), &show); err != nil {
			return fail("invalid show")
		}
		if !podcast.ValidURL(show.FeedURL) {
			return fail("invalid feed URL")
		}
		show.Title, show.Author = podcast.Text(show.Title), podcast.Text(show.Author)
		next.Subscriptions = slices.DeleteFunc(slices.Clone(l.Subscriptions), func(s podcast.Show) bool { return s.FeedURL == show.FeedURL })
		if action == "podcast-subscribe" {
			next.Subscriptions = append(next.Subscriptions, show)
		}
	}
	if err == nil {
		err = l.commit(next)
	}
	if err != nil {
		return fail(err.Error())
	}
	return ok("podcasts saved")
}

func (l *podcastLibrary) record(e podcast.Episode, position, duration float64, ended bool) error {
	next := *l
	next.Progress = maps.Clone(l.Progress)
	threshold := duration - 60
	if duration < 120 {
		threshold = duration / 2
	}
	played := ended || duration > 0 && position >= threshold
	next.Progress[e.Key()] = episodeProgress{Position: max(0, position), Duration: duration, Played: played, Updated: time.Now()}
	if len(next.Progress) > 2000 {
		keys := slices.Collect(maps.Keys(next.Progress))
		slices.SortFunc(keys, func(a, b string) int { return next.Progress[a].Updated.Compare(next.Progress[b].Updated) })
		for _, key := range keys[:len(keys)-2000] {
			delete(next.Progress, key)
		}
	}
	return l.commit(next)
}

func (l *podcastLibrary) resume(e podcast.Episode) time.Duration {
	s := l.Progress[e.Key()]
	if s.Played || s.Position < 15 {
		return 0
	}
	return time.Duration(max(0, s.Position-5) * float64(time.Second))
}
