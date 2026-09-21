package main

import (
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
	l, err := d.podcastLibrary()
	if err != nil {
		return fail(err.Error())
	}
	offset := l.resume(e)
	if req.Restart {
		offset = 0
	}
	queue, history := d.episodeQueue, d.episodeHistory
	if d.episode != nil {
		history = append(history, *d.episode)
		if len(history) > 100 {
			history = history[len(history)-100:]
		}
	}
	d.kill()
	d.episodeQueue, d.episodeHistory = queue, history
	d.episode = &e
	d.episodeOffset, d.episodeDuration = offset, time.Duration(e.Duration*float64(time.Second))
	if err := d.startEpisodeCache(); err != nil {
		d.state, d.lastError = "failed", err.Error()
		return fail(err.Error())
	}
	if err := d.startPlayback(); err != nil {
		d.state, d.lastError = "failed", err.Error()
		d.closeEpisodeCache()
		return fail(err.Error())
	}
	d.scheduleProgress()
	return ok("loading: " + e.Show + " — " + e.Title)
}

func (d *Daemon) startEpisodeCache() error {
	cache, err := episode.Open(d.episode.URL)
	if err != nil {
		return err
	}
	d.episodeCache = cache
	go func() {
		<-cache.Done()
		path, err := cache.CompletedPath()
		if err != nil {
			return
		}
		out, _, err := diagnosticCommandContext(cache.Context(), "ffprobe", 10*time.Second, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path)
		seconds, parseErr := strconv.ParseFloat(strings.TrimSpace(out), 64)
		if err != nil || parseErr != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 365*24*3600 {
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.episodeCache == cache {
			d.episodeDuration = time.Duration(seconds * float64(time.Second))
		}
	}()
	return nil
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
	if err := d.podcasts.record(*d.episode, d.episodePosition().Seconds(), d.episodeDuration.Seconds(), ended || d.state == "ended"); err != nil {
		d.storageError = "could not save podcast progress: " + err.Error()
	} else {
		d.storageError = ""
	}
}

func (d *Daemon) scheduleProgress() {
	if d.progressTimer != nil {
		d.progressTimer.Stop()
	}
	if d.episode == nil || d.player == nil {
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
			d.saveEpisode(false)
		}
		d.scheduleProgress()
	})
}

func (d *Daemon) finishEpisode() {
	d.saveEpisode(true)
	d.episodeOffset = d.episodePosition()
	d.closePlayer()
	d.closeEpisodeCache()
	if d.progressTimer != nil {
		d.progressTimer.Stop()
		d.progressTimer = nil
	}
	d.state, d.lastError, d.paused = "ended", "", false
	if len(d.episodeQueue) > 0 {
		d.podcastQueueCommand("next", "")
	}
}

func (d *Daemon) playbackSpeed() float64 {
	if d.speed == 0 {
		return 1
	}
	return d.speed
}

func (d *Daemon) episodeSpeed(arg string) string {
	l, err := d.podcastLibrary()
	if err != nil {
		return fail(err.Error())
	}
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
	if d.episode != nil && d.player != nil {
		if err := d.player.command("set_property", "speed", v); err != nil {
			return fail(err.Error())
		}
	}
	next := *l
	next.Speed = v
	if err := l.commit(next); err != nil {
		if d.episode != nil && d.player != nil {
			_ = d.player.command("set_property", "speed", d.playbackSpeed())
		}
		return fail(err.Error())
	}
	d.speed = v
	return ok(fmt.Sprintf("podcast speed: %.2fx", v))
}

func (d *Daemon) podcastQueueCommand(action, arg string) string {
	switch action {
	case "queue":
		b, _ := json.Marshal(d.episodeQueue)
		return ok(string(b))
	case "queue-clear":
		d.episodeQueue = nil
		return ok("queue cleared")
	case "podcast-queue":
		var e podcast.Episode
		if json.Unmarshal([]byte(arg), &e) != nil || !podcast.ValidURL(e.URL) || !podcast.ValidURL(e.FeedURL) {
			return fail("invalid episode")
		}
		if len(d.episodeQueue) >= 1000 {
			return fail("queue is full")
		}
		d.episodeQueue = append(d.episodeQueue, e)
		if d.player == nil && d.retryTimer == nil {
			return d.podcastQueueCommand("next", "")
		}
		return ok("queued: " + podcast.Text(e.Title))
	case "next":
		if len(d.episodeQueue) == 0 {
			return fail("podcast queue is empty")
		}
		e := d.episodeQueue[0]
		d.episodeQueue = d.episodeQueue[1:]
		b, _ := json.Marshal(episodeRequest{Episode: e})
		return d.playEpisode(string(b))
	case "prev":
		if d.episode == nil {
			return fail("no podcast playing")
		}
		if d.episodePosition() > 3*time.Second || len(d.episodeHistory) == 0 {
			return d.seekEpisode(strconv.FormatFloat(-d.episodePosition().Seconds(), 'f', 6, 64))
		}
		history := d.episodeHistory[:len(d.episodeHistory)-1]
		e := d.episodeHistory[len(d.episodeHistory)-1]
		d.episodeQueue = append([]podcast.Episode{*d.episode}, d.episodeQueue...)
		b, _ := json.Marshal(episodeRequest{Episode: e, Restart: true})
		result := d.playEpisode(string(b))
		d.episodeHistory = history
		return result
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
	if d.episode == nil {
		return fail("seeking is available while playing a podcast")
	}
	delta, err := seekDelta(arg)
	if err != nil {
		return fail(err.Error())
	}
	target := max(0, d.episodePosition()+delta)
	if d.episodeDuration > 0 {
		target = min(target, max(0, d.episodeDuration-time.Second))
	}
	d.saveEpisode(false)
	d.closePlayer()
	d.generation++ // invalidate a reconnect, old frames, and progress timers
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
	if d.episodeCache == nil {
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
