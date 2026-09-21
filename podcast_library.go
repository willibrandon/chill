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

type downloadPreferences struct {
	Auto             bool  `json:"auto"`               // Auto downloads new subscription episodes during sync.
	Latest           int   `json:"latest"`             // Latest limits automatic downloads per show.
	Concurrency      int   `json:"concurrency"`        // Concurrency bounds simultaneous transfers.
	MaxBytes         int64 `json:"max_bytes"`          // MaxBytes caps managed on-disk media.
	RetainPlayedDays int   `json:"retain_played_days"` // RetainPlayedDays removes old played downloads.
}

type episodeDownload struct {
	Episode  podcast.Episode `json:"episode"`            // Episode retains stable feed identity.
	State    string          `json:"state"`              // State tracks queued, active, ready, retry, error, or eviction.
	Path     string          `json:"path,omitempty"`     // Path is the completed local media file.
	Bytes    int64           `json:"bytes,omitempty"`    // Bytes is the amount downloaded.
	Total    int64           `json:"total,omitempty"`    // Total is the expected size when known.
	SHA256   string          `json:"sha256,omitempty"`   // SHA256 verifies the completed file.
	Error    string          `json:"error,omitempty"`    // Error describes the latest failure.
	Pinned   bool            `json:"pinned"`             // Pinned excludes the download from automatic cleanup.
	Updated  time.Time       `json:"updated"`            // Updated drives cleanup and display ordering.
	Attempts int             `json:"attempts,omitempty"` // Attempts counts automatic transfer retries.
	RetryAt  time.Time       `json:"retry_at,omitzero"`  // RetryAt is the next automatic attempt.
}

// Only the daemon writes this file. REPLs may read it while the daemon is off.
type podcastLibrary struct {
	change uint64 `json:"-"` // change invalidates in-process remote snapshots.
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
	// DownloadSettings controls automatic downloads and retention.
	DownloadSettings downloadPreferences `json:"download_settings"`
	// Downloads maps episode keys to managed offline files.
	Downloads map[string]episodeDownload `json:"downloads"`
	// Inbox contains the newest known episodes across subscriptions.
	Inbox []podcast.Episode `json:"inbox"`
	// Syncing reports an active subscription refresh.
	Syncing bool `json:"syncing"`
	// LastSync is the last completed inbox refresh.
	LastSync time.Time `json:"last_sync,omitzero"`
	// SyncError summarizes feeds that could not be refreshed.
	SyncError string `json:"sync_error,omitempty"`
}

func podcastPath() string {
	if p := configPath(); p != "" {
		return filepath.Join(filepath.Dir(p), "podcasts.json")
	}
	return ""
}

func loadPodcastLibrary() (*podcastLibrary, error) {
	l := &podcastLibrary{change: 1, Version: 1, Country: "us", Speed: 1, Subscriptions: []podcast.Show{}, Progress: map[string]episodeProgress{},
		DownloadSettings: downloadPreferences{Latest: 1, Concurrency: 2, MaxBytes: 10 << 30, RetainPlayedDays: 30}, Downloads: map[string]episodeDownload{}, Inbox: []podcast.Episode{}}
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
	// An interrupted daemon cannot still own a synchronization job.
	l.Syncing = false
	if _, err := podcast.Country(l.Country); err != nil {
		return nil, err
	}
	if l.Progress == nil {
		l.Progress = map[string]episodeProgress{}
	}
	if l.Downloads == nil {
		l.Downloads = map[string]episodeDownload{}
	}
	if l.Inbox == nil {
		l.Inbox = []podcast.Episode{}
	}
	for key, download := range l.Downloads {
		if download.Episode.Key() != key || !podcast.ValidURL(download.Episode.URL) || !podcast.ValidURL(download.Episode.FeedURL) {
			return nil, fmt.Errorf("invalid podcast download in podcasts.json")
		}
		if download.State != "queued" && download.State != "downloading" && download.State != "retrying" && download.State != "ready" && download.State != "error" && download.State != "evicted" {
			return nil, fmt.Errorf("invalid podcast download state in podcasts.json")
		}
		if download.State == "downloading" {
			download.State = "queued"
			download.Updated = time.Now().UTC()
			l.Downloads[key] = download
		}
	}
	if l.DownloadSettings.Latest < 1 || l.DownloadSettings.Latest > 20 || l.DownloadSettings.Concurrency < 1 || l.DownloadSettings.Concurrency > 8 || l.DownloadSettings.MaxBytes < 100<<20 || l.DownloadSettings.RetainPlayedDays < 0 || l.DownloadSettings.RetainPlayedDays > 3650 {
		return nil, fmt.Errorf("invalid podcast download settings")
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
	next.change = l.change + 1
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
