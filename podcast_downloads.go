package main

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
	"sort"
	"strings"
	"time"

	"github.com/willibrandon/chill/internal/podcast"
)

func podcastDownloadDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "chill", "podcasts")
}

func episodeDownloadPath(e podcast.Episode) string {
	ext := strings.ToLower(filepath.Ext(func() string {
		u, _ := url.Parse(e.URL)
		if u != nil {
			return u.Path
		}
		return ""
	}()))
	if !audioExtensions[ext] {
		ext = ".audio"
	}
	return filepath.Join(podcastDownloadDir(), e.Key()+ext)
}

func validEpisodeDownload(download episodeDownload) (bool, error) {
	expected := episodeDownloadPath(download.Episode)
	if expected == "" || filepath.Clean(download.Path) != filepath.Clean(expected) {
		return false, fmt.Errorf("download path is outside the managed podcast cache")
	}
	file, err := os.Open(download.Path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != download.Bytes {
		return false, fmt.Errorf("download file is missing or incomplete")
	}
	if download.SHA256 == "" {
		return false, fmt.Errorf("download has no integrity digest")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), download.SHA256), nil
}

func invalidEpisodeDownload(download episodeDownload, reason string) episodeDownload {
	if download.Path != "" && filepath.Clean(download.Path) == filepath.Clean(episodeDownloadPath(download.Episode)) {
		_ = os.Remove(download.Path)
	}
	download.State, download.Error, download.Updated = "error", reason, time.Now().UTC()
	download.Path, download.SHA256 = "", ""
	download.Bytes, download.Total = 0, 0
	return download
}

func (d *Daemon) podcastDownloadCommand(action, arg string) string {
	library, err := d.podcastLibrary()
	if err != nil {
		return fail(err.Error())
	}
	switch action {
	case "podcast-download":
		var episode podcast.Episode
		if json.Unmarshal([]byte(arg), &episode) != nil || !podcast.ValidURL(episode.URL) || !podcast.ValidURL(episode.FeedURL) {
			return fail("invalid episode")
		}
		key := episode.Key()
		record := library.Downloads[key]
		if record.State == "ready" {
			if valid, _ := validEpisodeDownload(record); valid {
				return ok("already downloaded: " + episode.Title)
			}
			if record.Path != "" && filepath.Clean(record.Path) == filepath.Clean(episodeDownloadPath(record.Episode)) {
				_ = os.Remove(record.Path)
			}
		}
		if record.State == "queued" || record.State == "downloading" {
			return ok("download already queued: " + episode.Title)
		}
		if retry := d.downloadRetries[key]; retry != nil {
			retry.Stop()
			delete(d.downloadRetries, key)
		}
		record.Episode = episode
		record.State = "queued"
		record.Path, record.SHA256 = "", ""
		record.Bytes, record.Total = 0, 0
		record.Error = ""
		record.Attempts = 0
		record.RetryAt = time.Time{}
		record.Updated = time.Now().UTC()
		library.Downloads[key] = record
		if err := library.commit(*library); err != nil {
			return fail(err.Error())
		}
		d.scheduleDownloadsLocked()
		return ok("download queued: " + episode.Title)
	case "podcast-download-remove":
		key := strings.TrimSpace(arg)
		record, exists := library.Downloads[key]
		if !exists {
			return fail("download not found")
		}
		if cancel := d.downloadCancels[key]; cancel != nil {
			cancel()
			delete(d.downloadCancels, key)
		}
		if retry := d.downloadRetries[key]; retry != nil {
			retry.Stop()
			delete(d.downloadRetries, key)
		}
		if record.Path != "" && filepath.Clean(record.Path) == filepath.Clean(episodeDownloadPath(record.Episode)) {
			_ = os.Remove(record.Path)
		}
		_ = os.Remove(episodeDownloadPath(record.Episode) + ".part")
		delete(library.Downloads, key)
		if err := library.commit(*library); err != nil {
			return fail(err.Error())
		}
		return ok("download removed")
	case "podcast-download-pin":
		key := strings.TrimSpace(arg)
		record, exists := library.Downloads[key]
		if !exists {
			return fail("download not found")
		}
		record.Pinned = !record.Pinned
		record.Updated = time.Now().UTC()
		library.Downloads[key] = record
		if err := library.commit(*library); err != nil {
			return fail(err.Error())
		}
		return ok(fmt.Sprintf("download pinned: %t", record.Pinned))
	case "podcast-download-settings":
		var settings downloadPreferences
		if err := json.Unmarshal([]byte(arg), &settings); err != nil {
			return fail("invalid download settings")
		}
		if settings.Latest < 1 || settings.Latest > 20 || settings.Concurrency < 1 || settings.Concurrency > 8 || settings.MaxBytes < 100<<20 || settings.RetainPlayedDays < 0 || settings.RetainPlayedDays > 3650 {
			return fail("invalid download settings")
		}
		library.DownloadSettings = settings
		if err := library.commit(*library); err != nil {
			return fail(err.Error())
		}
		d.scheduleDownloadsLocked()
		return ok("download settings saved")
	case "podcast-download-retry":
		key := strings.TrimSpace(arg)
		record, exists := library.Downloads[key]
		if !exists {
			return fail("download not found")
		}
		if record.State != "error" && record.State != "retrying" && record.State != "evicted" {
			return fail("download is not waiting for retry")
		}
		record.State = "queued"
		record.Error = ""
		record.Attempts = 0
		record.RetryAt = time.Time{}
		if retry := d.downloadRetries[key]; retry != nil {
			retry.Stop()
			delete(d.downloadRetries, key)
		}
		record.Updated = time.Now().UTC()
		library.Downloads[key] = record
		if err := library.commit(*library); err != nil {
			return fail(err.Error())
		}
		d.scheduleDownloadsLocked()
		return ok("download queued: " + record.Episode.Title)
	case "podcast-sync":
		if library.Syncing {
			return ok("podcast sync already running")
		}
		library.Syncing, library.SyncError = true, ""
		if err := library.commit(*library); err != nil {
			return fail(err.Error())
		}
		go d.syncPodcastInbox()
		return ok("podcast sync started")
	}
	return fail("unknown podcast download command")
}

func (d *Daemon) scheduleDownloadsLocked() {
	if d.podcasts == nil {
		return
	}
	if d.downloadCancels == nil {
		d.downloadCancels = map[string]context.CancelFunc{}
	}
	if d.downloadRetries == nil {
		d.downloadRetries = map[string]*time.Timer{}
	}
	limit := d.podcasts.DownloadSettings.Concurrency
	for key, record := range d.podcasts.Downloads {
		if record.State == "retrying" {
			d.scheduleDownloadRetryLocked(key, record)
			continue
		}
		if len(d.downloadCancels) >= limit {
			return
		}
		if record.State != "queued" {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		d.downloadCancels[key] = cancel
		record.State = "downloading"
		record.Updated = time.Now().UTC()
		d.podcasts.Downloads[key] = record
		_ = d.podcasts.commit(*d.podcasts)
		go d.downloadEpisode(ctx, key, record.Episode)
	}
}

func (d *Daemon) scheduleDownloadRetryLocked(key string, record episodeDownload) {
	if d.downloadRetries[key] != nil {
		return
	}
	delay := max(time.Duration(0), time.Until(record.RetryAt))
	d.downloadRetries[key] = time.AfterFunc(delay, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		delete(d.downloadRetries, key)
		current, ok := d.podcasts.Downloads[key]
		if !ok || current.State != "retrying" || current.RetryAt != record.RetryAt {
			return
		}
		current.State, current.RetryAt = "queued", time.Time{}
		d.podcasts.Downloads[key] = current
		_ = d.podcasts.commit(*d.podcasts)
		d.scheduleDownloadsLocked()
	})
}

func (d *Daemon) downloadEpisode(ctx context.Context, key string, episode podcast.Episode) {
	d.mu.Lock()
	maxBytes := d.podcasts.DownloadSettings.MaxBytes
	d.mu.Unlock()
	path := episodeDownloadPath(episode)
	part := path + ".part"
	err := os.MkdirAll(filepath.Dir(path), 0700)
	var written, total int64
	if err == nil {
		written, total, err = resumeDownload(ctx, episode.URL, part, maxBytes, func(done, size int64) {
			d.mu.Lock()
			if record, ok := d.podcasts.Downloads[key]; ok {
				record.Bytes, record.Total = done, size
				d.podcasts.Downloads[key] = record
			}
			d.mu.Unlock()
		})
	}
	hash := ""
	if err == nil {
		file, openErr := os.Open(part)
		if openErr != nil {
			err = openErr
		} else {
			digest := sha256.New()
			_, err = io.Copy(digest, file)
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				hash = hex.EncodeToString(digest.Sum(nil))
				if syncErr := syncFile(part); syncErr != nil {
					err = syncErr
				} else {
					err = os.Rename(part, path)
					if err != nil {
						_ = os.Remove(path)
						err = os.Rename(part, path)
					}
				}
			}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.downloadCancels, key)
	record, ok := d.podcasts.Downloads[key]
	if !ok {
		d.mu.Unlock()
		_ = os.Remove(part)
		d.mu.Lock()
	}
	if ok {
		record.Bytes, record.Total, record.Updated = written, total, time.Now().UTC()
		if err != nil {
			record.Attempts++
			record.State = "error"
			record.Error = err.Error()
			if ctx.Err() == nil && record.Attempts < 5 {
				record.State = "retrying"
				record.RetryAt = time.Now().Add(retryDelay(record.Attempts - 1))
			}
		} else {
			record.State = "ready"
			record.Path = path
			record.SHA256 = hash
			record.Error = ""
			record.Attempts = 0
			record.RetryAt = time.Time{}
		}
		d.podcasts.Downloads[key] = record
		d.enforceDownloadRetentionLocked()
		if commitErr := d.podcasts.commit(*d.podcasts); commitErr != nil {
			d.storageError = "could not save downloads: " + commitErr.Error()
		}
	}
	d.scheduleDownloadsLocked()
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func resumeDownload(ctx context.Context, raw, path string, maxBytes int64, progress func(int64, int64)) (int64, int64, error) {
	offset := int64(0)
	if info, err := os.Stat(path); err == nil {
		offset = info.Size()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return offset, 0, err
	}
	req.Header.Set("User-Agent", "chill/1.0")
	req.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 20 * time.Second}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.User != nil || req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("invalid download redirect")
		}
		return nil
	}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return offset, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return offset, 0, fmt.Errorf("download server returned %s", resp.Status)
	}
	if resp.StatusCode == http.StatusPartialContent && offset > 0 {
		expected := fmt.Sprintf("bytes %d-", offset)
		if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Range")), expected) {
			return offset, 0, fmt.Errorf("download server returned an invalid content range")
		}
	}
	flags := os.O_CREATE | os.O_WRONLY
	if resp.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		offset = 0
	}
	file, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return offset, 0, err
	}
	defer file.Close()
	total := resp.ContentLength
	if total >= 0 {
		total += offset
		if total > maxBytes {
			return offset, total, fmt.Errorf("episode exceeds the download quota")
		}
	}
	buffer := make([]byte, 64<<10)
	last := time.Now()
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if offset+int64(n) > maxBytes {
				return offset, total, fmt.Errorf("episode exceeds the download quota")
			}
			if _, err = file.Write(buffer[:n]); err != nil {
				return offset, total, err
			}
			offset += int64(n)
			if time.Since(last) >= 250*time.Millisecond {
				progress(offset, total)
				last = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return offset, total, readErr
		}
	}
	if total >= 0 && offset != total {
		return offset, total, io.ErrUnexpectedEOF
	}
	progress(offset, total)
	return offset, total, nil
}

func (d *Daemon) enforceDownloadRetentionLocked() {
	settings := d.podcasts.DownloadSettings
	now := time.Now()
	type candidate struct {
		key    string
		record episodeDownload
	}
	var candidates []candidate
	var total int64
	for key, record := range d.podcasts.Downloads {
		if record.State != "ready" {
			continue
		}
		if expected := episodeDownloadPath(record.Episode); expected == "" || filepath.Clean(record.Path) != filepath.Clean(expected) {
			d.podcasts.Downloads[key] = invalidEpisodeDownload(record, "download path is outside the managed podcast cache")
			continue
		}
		info, err := os.Stat(record.Path)
		if err != nil {
			d.podcasts.Downloads[key] = invalidEpisodeDownload(record, "download file is missing")
			continue
		}
		record.Bytes = info.Size()
		d.podcasts.Downloads[key] = record
		total += info.Size()
		if !record.Pinned && d.currentDownloadKey() != key {
			candidates = append(candidates, candidate{key, record})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].record.Updated.Before(candidates[j].record.Updated) })
	for _, candidate := range candidates {
		progress := d.podcasts.Progress[candidate.key]
		expired := settings.RetainPlayedDays > 0 && progress.Played && now.Sub(progress.Updated) > time.Duration(settings.RetainPlayedDays)*24*time.Hour
		if !expired && total <= settings.MaxBytes {
			continue
		}
		_ = os.Remove(candidate.record.Path)
		total -= candidate.record.Bytes
		record := candidate.record
		record.State, record.Path, record.SHA256 = "evicted", "", ""
		record.Bytes, record.Total, record.Updated = 0, 0, time.Now().UTC()
		record.Error = "removed by retention policy"
		d.podcasts.Downloads[candidate.key] = record
	}
}

func (d *Daemon) currentDownloadKey() string {
	if d.episode == nil {
		return ""
	}
	return d.episode.Key()
}

func (d *Daemon) syncPodcastInbox() {
	d.mu.Lock()
	library, err := d.podcastLibrary()
	if err != nil {
		d.mu.Unlock()
		return
	}
	subscriptions := append([]podcast.Show(nil), library.Subscriptions...)
	settings := library.DownloadSettings
	d.mu.Unlock()
	client := podcast.NewClient()
	sem := make(chan struct{}, max(1, settings.Concurrency))
	var syncErrors int
	for _, show := range subscriptions {
		show := show
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			feed, err := client.Feed(ctx, show.FeedURL)
			if err != nil {
				d.mu.Lock()
				syncErrors++
				d.mu.Unlock()
				return
			}
			sort.SliceStable(feed.Episodes, func(i, j int) bool { return feed.Episodes[i].Published.After(feed.Episodes[j].Published) })
			limit := min(settings.Latest, len(feed.Episodes))
			d.mu.Lock()
			defer d.mu.Unlock()
			for _, episode := range feed.Episodes {
				seen := false
				for _, existing := range d.podcasts.Inbox {
					if existing.Key() == episode.Key() {
						seen = true
						break
					}
				}
				if !seen {
					d.podcasts.Inbox = append(d.podcasts.Inbox, episode)
				}
			}
			sort.SliceStable(d.podcasts.Inbox, func(i, j int) bool { return d.podcasts.Inbox[i].Published.After(d.podcasts.Inbox[j].Published) })
			if len(d.podcasts.Inbox) > 1000 {
				d.podcasts.Inbox = d.podcasts.Inbox[:1000]
			}
			for _, episode := range feed.Episodes[:limit] {
				if !settings.Auto {
					continue
				}
				key := episode.Key()
				if d.podcasts.Progress[key].Played {
					continue
				}
				if existing, ok := d.podcasts.Downloads[key]; ok && (existing.State == "ready" || existing.State == "downloading" || existing.State == "queued" || existing.State == "retrying" || existing.State == "evicted") {
					continue
				}
				d.podcasts.Downloads[key] = episodeDownload{Episode: episode, State: "queued", Updated: time.Now().UTC()}
			}
			_ = d.podcasts.commit(*d.podcasts)
			d.scheduleDownloadsLocked()
		}()
	}
	for range cap(sem) {
		sem <- struct{}{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.podcasts != nil {
		d.podcasts.Syncing = false
		d.podcasts.LastSync = time.Now().UTC()
		d.podcasts.SyncError = ""
		if syncErrors > 0 {
			d.podcasts.SyncError = fmt.Sprintf("%d subscription feed(s) could not be refreshed", syncErrors)
		}
		_ = d.podcasts.commit(*d.podcasts)
	}
}
