package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var audioExtensions = map[string]bool{
	".aac": true, ".aiff": true, ".alac": true, ".flac": true, ".m4a": true,
	".mp3": true, ".mp4": true, ".oga": true, ".ogg": true, ".opus": true,
	".wav": true, ".webm": true, ".wma": true,
}

func isMediaInput(value string) bool {
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") || isPlaylistPath(value) || isAudioPath(value) {
		return true
	}
	info, err := os.Stat(value)
	return err == nil && (info.IsDir() || !info.IsDir() && isAudioPath(value))
}

func isAudioPath(path string) bool { return audioExtensions[strings.ToLower(filepath.Ext(path))] }

func loadMediaInputs(ctx context.Context, inputs []string) ([]MediaItem, error) {
	var sources []string
	var items []MediaItem
	flush := func() error {
		if len(sources) == 0 {
			return nil
		}
		loaded, err := probeSources(ctx, sources)
		if err != nil {
			return err
		}
		items = append(items, loaded...)
		sources = nil
		return nil
	}
	for _, input := range inputs {
		if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
			if isPlaylistPath(input) {
				if err := flush(); err != nil {
					return nil, err
				}
				loaded, err := readPlaylist(ctx, input)
				if err != nil {
					return nil, err
				}
				items = append(items, loaded...)
				continue
			}
			sources = append(sources, input)
			continue
		}
		if _, statErr := os.Stat(input); os.IsNotExist(statErr) {
			if station := findStation(input); station != nil {
				if err := flush(); err != nil {
					return nil, err
				}
				items = append(items, itemFromStation(*station))
				continue
			}
		}
		path, err := filepath.Abs(input)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			err = filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if !entry.IsDir() && isAudioPath(candidate) {
					sources = append(sources, candidate)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			continue
		}
		if isPlaylistPath(path) {
			if err := flush(); err != nil {
				return nil, err
			}
			loaded, err := readPlaylist(ctx, path)
			if err != nil {
				return nil, err
			}
			items = append(items, loaded...)
			continue
		}
		if !isAudioPath(path) {
			return nil, fmt.Errorf("unsupported audio file: %s", input)
		}
		sources = append(sources, path)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return items, nil
}

func probeSources(ctx context.Context, sources []string) ([]MediaItem, error) {
	for _, source := range sources {
		if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
			if err := checkLocalMediaRequirements(); err != nil {
				return nil, err
			}
			break
		}
	}
	items := make([]MediaItem, len(sources))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	var firstErr error
	var errorMu sync.Mutex
	for i, source := range sources {
		i, source := i, source
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var item MediaItem
			var err error
			if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
				item, err = itemFromURL(source)
			} else {
				item, err = probeLocalMedia(ctx, source)
			}
			if err != nil {
				errorMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errorMu.Unlock()
				return
			}
			items[i] = item
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return items, nil
}

type probeDocument struct {
	// Format contains container duration and tags.
	Format struct {
		Duration string            `json:"duration"` // Duration is reported in seconds.
		Tags     map[string]string `json:"tags"`     // Tags contains normalized container metadata.
	} `json:"format"`
	// Streams identifies attached artwork.
	Streams []struct {
		CodecType   string            `json:"codec_type"`  // CodecType distinguishes audio and video.
		Disposition map[string]int    `json:"disposition"` // Disposition marks attached pictures.
		Tags        map[string]string `json:"tags"`        // Tags carries stream-level metadata such as embedded lyrics.
	} `json:"streams"`
}

func tag(tags map[string]string, names ...string) string {
	for _, name := range names {
		for key, value := range tags {
			if strings.EqualFold(key, name) && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func probeLocalMedia(ctx context.Context, path string) (MediaItem, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return MediaItem{}, err
	}
	item := MediaItem{Kind: MediaTrack, Source: filepath.Clean(absolute), Title: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), AddedAt: time.Now().UTC()}
	item.ID = mediaID(item.Kind, item.Source)
	out, _, err := diagnosticCommandContext(ctx, "ffprobe", 15*time.Second, "-v", "error", "-show_format", "-show_streams", "-of", "json", "--", item.Source)
	if err != nil {
		return MediaItem{}, fmt.Errorf("read metadata for %s: %w", path, err)
	}
	var doc probeDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return MediaItem{}, fmt.Errorf("read metadata for %s: %w", path, err)
	}
	mediaTag := func(names ...string) string {
		if value := tag(doc.Format.Tags, names...); value != "" {
			return value
		}
		for _, stream := range doc.Streams {
			if value := tag(stream.Tags, names...); value != "" {
				return value
			}
		}
		return ""
	}
	item.Title = firstNonempty(mediaTag("title"), item.Title)
	item.Artist = mediaTag("artist", "album_artist")
	item.Album = mediaTag("album")
	item.Genre = mediaTag("genre")
	item.EmbeddedLyrics = mediaTag("lyrics", "unsyncedlyrics", "syncedlyrics")
	item.Duration, _ = strconv.ParseFloat(doc.Format.Duration, 64)
	for _, stream := range doc.Streams {
		if stream.CodecType == "video" && stream.Disposition["attached_pic"] == 1 {
			item.Artwork = cachedArtworkPath(item)
			if item.Artwork == "" {
				break
			}
			if _, err := os.Stat(item.Artwork); os.IsNotExist(err) {
				_, _, extractErr := diagnosticCommandContext(ctx, "ffmpeg", 15*time.Second, "-nostdin", "-v", "error", "-i", item.Source, "-map", "0:v:0", "-frames:v", "1", "-y", item.Artwork)
				if extractErr != nil {
					item.Artwork = ""
				}
			}
			break
		}
	}
	return item, nil
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func cachedArtworkPath(item MediaItem) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	dir = filepath.Join(dir, "chill", "artwork")
	_ = os.MkdirAll(dir, 0700)
	return filepath.Join(dir, item.ID+".jpg")
}

func isPlaylistPath(value string) bool {
	u, err := url.Parse(value)
	path := value
	if err == nil && u.Path != "" {
		path = u.Path
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m3u", ".m3u8", ".pls":
		return true
	}
	return false
}

func readPlaylist(ctx context.Context, source string) ([]MediaItem, error) {
	body, base, err := readPlaylistSource(ctx, source)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(filepath.Ext(base), ".pls") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(body)), "[playlist]") {
		return parsePLS(ctx, body, base)
	}
	return parseM3U(ctx, body, base)
}

func readPlaylistSource(ctx context.Context, source string) (string, string, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		req, err := newMediaRequest(ctx, source)
		if err != nil {
			return "", "", err
		}
		data, finalURL, err := fetchLimited(req, 8<<20)
		return string(data), finalURL, err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", "", err
	}
	return strings.TrimPrefix(string(data), "\ufeff"), source, nil
}

func newMediaRequest(ctx context.Context, raw string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err == nil {
		req.Header.Set("User-Agent", "chill/1.0")
		req.Header.Set("Accept-Encoding", "identity")
	}
	return req, err
}

func fetchLimited(req *http.Request, limit int64) ([]byte, string, error) {
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 || next.URL.User != nil || next.URL.Scheme != "http" && next.URL.Scheme != "https" {
			return fmt.Errorf("invalid media redirect")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("media server returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("playlist exceeds %d bytes", limit)
	}
	return data, resp.Request.URL.String(), err
}

func resolvePlaylistEntry(base, entry string) string {
	entry = strings.TrimSpace(entry)
	if strings.HasPrefix(base, "http://") || strings.HasPrefix(base, "https://") {
		baseURL, _ := url.Parse(base)
		entryURL, err := url.Parse(entry)
		if err == nil {
			return baseURL.ResolveReference(entryURL).String()
		}
	}
	if filepath.IsAbs(entry) {
		return filepath.Clean(entry)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(base), entry))
}

func parseM3U(ctx context.Context, body, base string) ([]MediaItem, error) {
	type entry struct {
		source, title string
		live          bool
	}
	var entries []entry
	pendingTitle := ""
	pendingLive := false
	scanner := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(body, "\ufeff")))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(strings.ToUpper(line), "#EXTINF:") {
			if comma := strings.Index(line, ","); comma >= 0 {
				durationText := line[:comma]
				if colon := strings.Index(durationText, ":"); colon >= 0 {
					durationText = durationText[colon+1:]
				}
				duration, _ := strconv.Atoi(strings.TrimSpace(durationText))
				pendingLive = duration < 0
				pendingTitle = strings.TrimSpace(line[comma+1:])
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries = append(entries, entry{source: resolvePlaylistEntry(base, line), title: pendingTitle, live: pendingLive})
		pendingTitle, pendingLive = "", false
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sources := make([]string, len(entries))
	for i := range entries {
		sources[i] = entries[i].source
	}
	items, err := probeSources(ctx, sources)
	if err != nil {
		return nil, err
	}
	for i, entry := range entries {
		if entry.title != "" {
			items[i].Title = entry.title
		}
		if entry.live && items[i].Kind == MediaURL {
			station := Station{Name: entry.title, Desc: entry.title, URL: items[i].Source}
			if station.Name == "" {
				station.Name = items[i].Title
				station.Desc = items[i].Title
			}
			items[i] = itemFromStation(station)
		}
	}
	return items, nil
}

func parsePLS(ctx context.Context, body, base string) ([]MediaItem, error) {
	files, titles, lengths := map[int]string{}, map[int]string{}, map[int]string{}
	scanner := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(body, "\ufeff")))
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok {
			continue
		}
		lower := strings.ToLower(key)
		var prefix string
		switch {
		case strings.HasPrefix(lower, "file"):
			prefix = "file"
		case strings.HasPrefix(lower, "title"):
			prefix = "title"
		case strings.HasPrefix(lower, "length"):
			prefix = "length"
		default:
			continue
		}
		i, err := strconv.Atoi(strings.TrimPrefix(lower, prefix))
		if err != nil || i < 1 {
			continue
		}
		if prefix == "file" {
			files[i] = value
		} else if prefix == "title" {
			titles[i] = value
		} else {
			lengths[i] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	indexes := make([]int, 0, len(files))
	for i := range files {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	sources := make([]string, len(indexes))
	for position, i := range indexes {
		sources[position] = resolvePlaylistEntry(base, files[i])
	}
	items, err := probeSources(ctx, sources)
	if err != nil {
		return nil, err
	}
	for position, i := range indexes {
		if titles[i] != "" {
			items[position].Title = strings.TrimSpace(titles[i])
		}
		if length, _ := strconv.Atoi(strings.TrimSpace(lengths[i])); length < 0 && items[position].Kind == MediaURL {
			station := Station{Name: items[position].Title, Desc: items[position].Title, URL: items[position].Source}
			items[position] = itemFromStation(station)
		}
	}
	return items, nil
}
