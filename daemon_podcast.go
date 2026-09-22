package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/willibrandon/chill/internal/episode"
	"github.com/willibrandon/chill/internal/podcast"
)

type episodeRequest struct {
	// Episode identifies the requested podcast media.
	Episode podcast.Episode `json:"episode"`
	// Restart bypasses any saved listening position.
	Restart bool `json:"restart,omitempty"`
}

func (d *Daemon) playEpisode(arg string) string {
	var req episodeRequest
	if err := json.Unmarshal([]byte(arg), &req); err != nil {
		return fail("invalid episode")
	}
	e := req.Episode
	if !podcast.ValidURL(e.FeedURL) || !podcast.ValidURL(e.URL) {
		return fail("episode needs HTTP(S) feed and audio URLs")
	}
	if math.IsNaN(e.Duration) || math.IsInf(e.Duration, 0) || e.Duration < 0 || e.Duration > 365*24*3600 {
		return fail("invalid episode duration")
	}
	e.Show, e.Title, e.Description = podcast.Text(e.Show), podcast.Text(e.Title), podcast.Text(e.Description)
	if !req.Restart && d.episode != nil && e.Key() == d.episode.Key() && (d.state == "playing" || d.state == "paused") {
		return d.resume()
	}
	return d.playMediaItem(itemFromEpisode(e), req.Restart, true)
}

func (d *Daemon) startEpisodeCache() error {
	d.episodeLocal = ""
	if library, err := d.podcastLibrary(); err == nil {
		if download, ok := library.Downloads[d.episode.Key()]; ok && download.State == "ready" && d.offlineFailed != d.episode.Key() {
			valid, validationErr := validEpisodeDownload(download)
			if valid {
				d.episodeLocal = download.Path
				d.probeEpisodeDuration(download.Path, nil)
				return nil
			}
			reason := "download integrity check failed"
			if validationErr != nil {
				reason = validationErr.Error()
			}
			download = invalidEpisodeDownload(download, reason)
			library.Downloads[d.episode.Key()] = download
			_ = library.commit(*library)
		}
	}
	cache, err := episode.Open(d.episode.URL)
	if err != nil {
		return err
	}
	d.episodeCache = cache
	d.probeEpisodeDuration("", cache)
	return nil
}

func (d *Daemon) probeEpisodeDuration(localPath string, cache *episode.Cache) {
	go func() {
		path := localPath
		if cache != nil {
			<-cache.Done()
			var err error
			path, err = cache.CompletedPath()
			if err != nil {
				return
			}
		}
		probeContext := context.Background()
		if cache != nil {
			probeContext = cache.Context()
		}
		item, err := probeLocalMedia(probeContext, path)
		seconds := item.Duration
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 365*24*3600 {
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if cache == nil && d.episodeLocal == path || cache != nil && d.episodeCache == cache {
			d.episodeDuration = time.Duration(seconds * float64(time.Second))
		}
	}()
}

func (d *Daemon) closeEpisodeCache() {
	if d.episodeCache != nil {
		d.episodeCache.Close()
		d.episodeCache = nil
	}
}

func (d *Daemon) episodePosition() time.Duration {
	if p, ok := d.player.(interface{ position() time.Duration }); ok {
		return p.position()
	}
	return d.episodeOffset
}

func (d *Daemon) saveEpisode(ended bool) {
	if d.episode == nil || d.podcasts == nil {
		return
	}
	key := d.episode.Key()
	wasPlayed := d.podcasts.Progress[key].Played
	if err := d.podcasts.record(*d.episode, d.episodePosition().Seconds(), d.episodeDuration.Seconds(), ended || d.state == "ended"); err != nil {
		d.storageError = "could not save podcast progress: " + err.Error()
	} else {
		d.storageError = ""
		if !wasPlayed && d.podcasts.Progress[key].Played {
			d.enforceDownloadRetentionLocked()
			if err := d.podcasts.commit(*d.podcasts); err != nil {
				d.storageError = "could not apply download retention: " + err.Error()
			}
		}
	}
}

func (d *Daemon) saveCurrentProgress(ended bool, lifecycle ...string) {
	if d.current == nil || !d.current.finite() {
		return
	}
	if ended && d.current.Kind == MediaProvider {
		d.providerCompleted = true
	}
	if d.episode != nil {
		d.saveEpisode(ended)
		return
	}
	library, err := d.mediaLibrary()
	if err != nil {
		d.storageError = "could not save listening progress: " + err.Error()
		return
	}
	position, duration := d.episodePosition().Seconds(), d.episodeDuration.Seconds()
	threshold := duration - 60
	if duration < 120 {
		threshold = duration / 2
	}
	played := ended || duration > 0 && position >= threshold
	library.Resume[d.current.ID] = resumePoint{Position: max(0, position), Duration: duration, Played: played, Updated: time.Now().UTC()}
	library.recordRecent(*d.current)
	if err := library.commit(); err != nil {
		d.storageError = "could not save listening progress: " + err.Error()
	} else {
		d.storageError = ""
	}
	item := *d.current
	state := "playing"
	if len(lifecycle) > 0 {
		state = lifecycle[0]
	} else if ended {
		state = "finished"
	} else if d.paused || d.state == "paused" {
		state = "paused"
	}
	d.syncProviderProgress(item, time.Duration(position*float64(time.Second)), time.Duration(duration*float64(time.Second)), state)
}

func (d *Daemon) syncProviderProgress(item MediaItem, position, duration time.Duration, state string) {
	if item.Kind != MediaProvider {
		return
	}
	scrobble := state == "finished" && !d.providerScrobbled
	if scrobble {
		d.providerScrobbled = true
	}
	update := providerProgressUpdate{item: item, position: position, duration: duration, state: state, completed: d.providerCompleted || state == "finished", scrobble: scrobble}
	d.providerProgress.submit(update, func(err error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if err != nil {
			d.storageError = "could not synchronize provider progress: " + err.Error()
		} else if strings.HasPrefix(d.storageError, "could not synchronize provider progress:") {
			d.storageError = ""
		}
	})
}

func (d *Daemon) scheduleProgress() {
	if d.progressTimer != nil {
		d.progressTimer.Stop()
	}
	if d.current == nil || !d.current.finite() || d.player == nil {
		return
	}
	generation := d.generation
	d.progressTimer = time.AfterFunc(15*time.Second, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.generation != generation {
			return
		}
		if d.state == "playing" {
			d.saveCurrentProgress(false)
		}
		d.scheduleProgress()
	})
}

func (d *Daemon) finishFiniteItem() {
	d.saveCurrentProgress(true)
	d.episodeOffset = d.episodePosition()
	if d.progressTimer != nil {
		d.progressTimer.Stop()
		d.progressTimer = nil
	}
	if d.library != nil && d.library.Repeat == "one" && d.current != nil {
		d.closePlayer()
		d.closeEpisodeCache()
		d.state, d.lastError, d.paused = "ended", "", false
		item := *d.current
		d.playMediaItem(item, true, false)
		return
	}
	if item, ok := d.nextQueued(); ok {
		if d.advanceGapless(item) {
			return
		}
		d.closePlayer()
		d.closeEpisodeCache()
		d.state, d.lastError, d.paused = "ended", "", false
		d.playMediaItem(item, false, true)
		return
	}
	d.closePlayer()
	d.closeEpisodeCache()
	d.state, d.lastError, d.paused = "ended", "", false
	if d.library != nil && d.library.Repeat == "all" && len(d.library.Cycle) > 0 {
		d.playNextItem()
	}
}

func (d *Daemon) playbackSpeed() float64 {
	if d.rate == 0 {
		return 1
	}
	return d.rate
}

func (d *Daemon) episodeSpeed(arg string) string {
	if arg == "" {
		return ok(fmt.Sprintf("speed: %.2fx", d.playbackSpeed()))
	}
	v, err := strconv.ParseFloat(arg, 64)
	if strings.HasPrefix(arg, "+") || strings.HasPrefix(arg, "-") {
		v += d.playbackSpeed()
	}
	if err != nil || math.IsNaN(v) || v < 0.5 || v > 3 {
		return fail("speed must be between 0.5 and 3")
	}
	if d.current != nil && d.current.finite() && d.player != nil {
		if err := d.player.setSpeed(v); err != nil {
			return fail(err.Error())
		}
	}
	previous := d.playbackSpeed()
	if d.episode != nil || d.current == nil {
		l, err := d.podcastLibrary()
		if err != nil {
			return fail(err.Error())
		}
		next := *l
		next.Speed = v
		if err := l.commit(next); err != nil {
			if d.current != nil && d.current.finite() && d.player != nil {
				_ = d.player.setSpeed(previous)
			}
			return fail(err.Error())
		}
		d.speed = v
	}
	d.rate = v
	return ok(fmt.Sprintf("speed: %.2fx", v))
}

func (d *Daemon) podcastQueueCommand(action, arg string) string {
	switch action {
	case "queue":
		var episodes []podcast.Episode
		for _, item := range d.queueItems() {
			if item.Episode != nil {
				episodes = append(episodes, *item.Episode)
			}
		}
		b, _ := json.Marshal(episodes)
		return ok(string(b))
	case "queue-clear":
		return d.queueCommand("queue-clear", "")
	case "podcast-queue":
		var e podcast.Episode
		if json.Unmarshal([]byte(arg), &e) != nil || !podcast.ValidURL(e.URL) || !podcast.ValidURL(e.FeedURL) {
			return fail("invalid episode")
		}
		item := itemFromEpisode(e)
		data, _ := json.Marshal(queueRequest{Items: []MediaItem{item}})
		return d.queueCommand("queue-append", string(data))
	case "next":
		return d.playNextItem()
	case "prev":
		return d.previousItem()
	}
	return fail("unknown queue command")
}

func seekDelta(arg string) (time.Duration, error) {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(arg), 64)
	if err != nil {
		d, parseErr := time.ParseDuration(arg)
		if parseErr == nil {
			seconds, err = d.Seconds(), nil
		}
	}
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || math.Abs(seconds) > 365*24*3600 {
		return 0, fmt.Errorf("seek needs seconds or a duration, such as -30, +30, or 2m")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func (d *Daemon) seekEpisode(arg string) string {
	if d.current == nil || !d.current.finite() {
		return fail("seeking is available while playing finite media")
	}
	delta, err := seekDelta(arg)
	if err != nil {
		return fail(err.Error())
	}
	target := max(0, d.episodePosition()+delta)
	if d.episodeDuration > 0 {
		target = min(target, max(0, d.episodeDuration-time.Second))
	}
	d.saveCurrentProgress(false)
	d.closePlayer()
	d.generation++ // invalidate a reconnect, old frames, and progress timers
	d.providerCompleted = false
	d.providerScrobbled = false
	if d.resolveCancel != nil {
		d.resolveCancel()
		d.resolveCancel = nil
	}
	if d.retryTimer != nil {
		d.retryTimer.Stop()
		d.retryTimer = nil
	}
	d.retries, d.retryAt = 0, time.Time{}
	d.episodeOffset = target
	if d.episode != nil && d.episodeCache == nil && d.episodeLocal == "" {
		if err := d.startEpisodeCache(); err != nil {
			d.state, d.lastError = "failed", err.Error()
			return fail(err.Error())
		}
	}
	if err := d.startPlayback(); err != nil {
		d.state, d.lastError = "failed", err.Error()
		return fail(err.Error())
	}
	d.scheduleProgress()
	if d.media != nil {
		d.media.Seeked(target)
	}
	return ok("seeking to " + clock(target.Seconds()))
}

func clock(seconds float64) string {
	s := int(max(0, seconds))
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
