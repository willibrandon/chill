package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/willibrandon/chill/internal/notify"
)

type queueRequest struct {
	Items []MediaItem `json:"items"`           // Items contains queue additions.
	Index int         `json:"index,omitempty"` // Index is the one-based source position.
	To    int         `json:"to,omitempty"`    // To is the one-based destination position.
}

func (d *Daemon) mediaLibrary() (*libraryState, error) {
	if d.library == nil {
		library, err := loadLibrary()
		if err != nil {
			return nil, err
		}
		d.library = library
	}
	return d.library, nil
}

func (d *Daemon) commitLibrary() error {
	if d.library == nil {
		return nil
	}
	return d.library.commit()
}

func (d *Daemon) playMediaItem(item MediaItem, restart, remember bool) string {
	normalized, err := item.normalized()
	if err != nil {
		return fail(err.Error())
	}
	item = normalized
	if d.current != nil && d.current.ID == item.ID && (d.state == "playing" || d.state == "paused") && !restart {
		return d.resume()
	}
	library, err := d.mediaLibrary()
	if err != nil {
		return fail(err.Error())
	}
	position := time.Duration(0)
	if item.Kind == MediaPodcast {
		podcasts, err := d.podcastLibrary()
		if err != nil {
			return fail(err.Error())
		}
		if !restart {
			position = podcasts.resume(*item.Episode)
		}
	} else if item.Kind == MediaTrack || item.Kind == MediaURL || item.Kind == MediaProvider {
		if point := library.Resume[item.ID]; !restart && !point.Played && point.Position >= 15 {
			position = time.Duration(max(0, point.Position-5) * float64(time.Second))
		}
	}
	if remember && d.current != nil && d.current.ID != item.ID {
		d.itemHistory = append(d.itemHistory, *d.current)
		if len(d.itemHistory) > 100 {
			d.itemHistory = d.itemHistory[len(d.itemHistory)-100:]
		}
	}
	previousHistory := cloneItems(d.itemHistory)
	d.kill()
	d.itemHistory = previousHistory
	d.current = &item
	d.station, d.episode = item.Station, item.Episode
	d.rate = 1
	if item.Kind == MediaPodcast {
		d.rate = d.speed
		if d.rate == 0 {
			d.rate = 1
		}
	}
	d.episodeOffset = position
	d.episodeDuration = time.Duration(item.Duration * float64(time.Second))
	if item.Kind == MediaPodcast {
		if err := d.startEpisodeCache(); err != nil {
			d.state, d.lastError = "failed", err.Error()
			return fail(err.Error())
		}
	}
	if err := d.startPlayback(); err != nil {
		d.state, d.lastError = "failed", err.Error()
		d.closeEpisodeCache()
		return fail(err.Error())
	}
	if item.finite() {
		d.scheduleProgress()
	}
	return ok("loading: " + item.display())
}

func (d *Daemon) notifyCurrentItem() {
	if !d.notifications || d.current == nil || d.current.Kind == MediaStation || d.notifiedGeneration == d.generation {
		return
	}
	d.notifiedGeneration = d.generation
	item := *d.current
	title, body := item.Title, item.Artist
	if title == "" {
		title = item.display()
	}
	if item.Album != "" {
		if body == "" {
			body = item.Album
		} else {
			body += " · " + item.Album
		}
	}
	go func() { _ = notify.Show(title, body, item.Artwork) }()
}

func (d *Daemon) playItemRequest(arg string) string {
	var request struct {
		Item    MediaItem `json:"item"`
		Restart bool      `json:"restart,omitempty"`
	}
	if err := json.Unmarshal([]byte(arg), &request); err != nil {
		return fail("invalid media item")
	}
	return d.playMediaItem(request.Item, request.Restart, true)
}

func (d *Daemon) playSessionRequest(arg string) string {
	var request queueRequest
	if err := json.Unmarshal([]byte(arg), &request); err != nil || len(request.Items) == 0 {
		return fail("media session needs at least one item")
	}
	if len(request.Items) > 5000 {
		return fail("media session is limited to 5000 items")
	}
	for i := range request.Items {
		item, err := request.Items[i].normalized()
		if err != nil {
			return fail(err.Error())
		}
		request.Items[i] = item
	}
	library, err := d.mediaLibrary()
	if err != nil {
		return fail(err.Error())
	}
	library.rememberQueue()
	library.Queue = cloneItems(request.Items[1:])
	library.PlayNext = nil
	library.Cycle = cloneItems(request.Items)
	if err := library.commit(); err != nil {
		return fail(err.Error())
	}
	return d.playMediaItem(request.Items[0], false, true)
}

func (d *Daemon) queueItems() []MediaItem {
	if d.library == nil {
		return nil
	}
	return append(cloneItems(d.library.PlayNext), d.library.Queue...)
}

func (d *Daemon) nextLocalPreload() (MediaItem, time.Duration, bool) {
	if d.current == nil || d.current.Kind != MediaTrack || d.library == nil || d.library.Shuffle || d.library.Repeat == "one" {
		return MediaItem{}, 0, false
	}
	items := d.queueItems()
	if len(items) == 0 || items[0].Kind != MediaTrack {
		return MediaItem{}, 0, false
	}
	item := items[0]
	offset := time.Duration(0)
	if point := d.library.Resume[item.ID]; !point.Played && point.Position >= 15 {
		offset = time.Duration(max(0, point.Position-5) * float64(time.Second))
	}
	return item, offset, true
}

func (d *Daemon) preloadNextLocal() {
	player, ok := d.player.(*pcmPlayer)
	if !ok {
		return
	}
	if item, offset, ok := d.nextLocalPreload(); ok {
		player.preload(item.Source, offset, true)
	} else {
		player.cancelPreload()
	}
}

func (d *Daemon) advanceGapless(item MediaItem) bool {
	player, ok := d.player.(*pcmPlayer)
	if !ok || d.current == nil || d.current.Kind != MediaTrack || item.Kind != MediaTrack {
		return false
	}
	normalized, err := item.normalized()
	if err != nil {
		return false
	}
	item = normalized
	offset := time.Duration(0)
	if point := d.library.Resume[item.ID]; !point.Played && point.Position >= 15 {
		offset = time.Duration(max(0, point.Position-5) * float64(time.Second))
	}
	if !player.transition(item.Source, offset, true) {
		return false
	}
	d.itemHistory = append(d.itemHistory, *d.current)
	if len(d.itemHistory) > 100 {
		d.itemHistory = d.itemHistory[len(d.itemHistory)-100:]
	}
	d.generation++
	d.current, d.station, d.episode = &item, nil, nil
	d.episodeOffset = offset
	d.episodeDuration = time.Duration(item.Duration * float64(time.Second))
	d.paused = false
	d.state, d.lastError = "loading", ""
	d.startedAt = time.Time{}
	d.retries, d.retryAt = 0, time.Time{}
	d.scheduleProgress()
	d.preloadNextLocal()
	generation := d.generation
	d.loadTimer = time.AfterFunc(45*time.Second, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.generation == generation && d.player == player && d.state == "loading" {
			d.playbackFailed("track took too long to load")
		}
	})
	return true
}

func (d *Daemon) nextQueued() (MediaItem, bool) {
	library, err := d.mediaLibrary()
	if err != nil {
		d.storageError = err.Error()
		return MediaItem{}, false
	}
	var item MediaItem
	if len(library.PlayNext) > 0 {
		item = library.PlayNext[0]
		library.PlayNext = library.PlayNext[1:]
	} else if len(library.Queue) > 0 {
		index := 0
		if library.Shuffle && len(library.Queue) > 1 {
			index = rand.Intn(len(library.Queue))
		}
		item = library.Queue[index]
		library.Queue = slices.Delete(library.Queue, index, index+1)
	} else {
		return MediaItem{}, false
	}
	if err := library.commit(); err != nil {
		d.storageError = "could not save queue: " + err.Error()
	}
	return item, true
}

func (d *Daemon) playNextItem() string {
	if item, ok := d.nextQueued(); ok {
		return d.playMediaItem(item, false, true)
	}
	if d.library != nil && d.library.Repeat == "all" && len(d.library.Cycle) > 0 {
		d.library.Queue = cloneItems(d.library.Cycle)
		if err := d.library.commit(); err != nil {
			return fail(err.Error())
		}
		if item, ok := d.nextQueued(); ok {
			return d.playMediaItem(item, false, true)
		}
	}
	return fail("queue is empty")
}

func (d *Daemon) previousItem() string {
	if d.current != nil && d.current.finite() && d.episodePosition() > 3*time.Second {
		return d.seekEpisode(strconv.FormatFloat(-d.episodePosition().Seconds(), 'f', 6, 64))
	}
	if len(d.itemHistory) == 0 {
		return fail("no previous item")
	}
	item := d.itemHistory[len(d.itemHistory)-1]
	history := cloneItems(d.itemHistory[:len(d.itemHistory)-1])
	if d.current != nil {
		d.library.PlayNext = append([]MediaItem{*d.current}, d.library.PlayNext...)
	}
	result := d.playMediaItem(item, true, false)
	d.itemHistory = history
	_ = d.commitLibrary()
	return result
}

func (d *Daemon) queueCommand(action, arg string) string {
	defer d.preloadNextLocal()
	library, err := d.mediaLibrary()
	if err != nil {
		return fail(err.Error())
	}
	save := func(message string) string {
		if len(library.Queue)+len(library.PlayNext) > 5000 {
			return fail("queue is full")
		}
		if err := library.commit(); err != nil {
			return fail(err.Error())
		}
		return ok(message)
	}
	switch action {
	case "queue-list":
		data, _ := json.Marshal(struct {
			Queue    []MediaItem `json:"queue"`
			PlayNext []MediaItem `json:"play_next"`
			Shuffle  bool        `json:"shuffle"`
			Repeat   string      `json:"repeat"`
		}{cloneItems(library.Queue), cloneItems(library.PlayNext), library.Shuffle, library.Repeat})
		return ok(string(data))
	case "queue-append", "queue-replace", "queue-next":
		var request queueRequest
		if err := json.Unmarshal([]byte(arg), &request); err != nil || len(request.Items) == 0 && action != "queue-replace" {
			return fail("queue command needs at least one media item")
		}
		for i := range request.Items {
			request.Items[i], err = request.Items[i].normalized()
			if err != nil {
				return fail(err.Error())
			}
		}
		resulting := len(request.Items)
		if action != "queue-replace" {
			resulting += len(library.Queue) + len(library.PlayNext)
		}
		if resulting > 5000 {
			return fail("queue is full")
		}
		library.rememberQueue()
		if action != "queue-replace" && len(library.Cycle) == 0 && d.current != nil {
			library.Cycle = []MediaItem{*d.current}
		}
		if action == "queue-replace" {
			library.Queue, library.PlayNext = cloneItems(request.Items), nil
			library.Cycle = cloneItems(request.Items)
		} else if action == "queue-next" {
			library.PlayNext = append(cloneItems(request.Items), library.PlayNext...)
			library.Cycle = append(library.Cycle, request.Items...)
		} else {
			library.Queue = append(library.Queue, request.Items...)
			library.Cycle = append(library.Cycle, request.Items...)
		}
		message := fmt.Sprintf("queued %d item(s)", len(request.Items))
		if d.current == nil && action != "queue-next" && len(library.Queue) > 0 {
			first, rest := library.Queue[0], cloneItems(library.Queue[1:])
			library.Queue = rest
			if result := save(message); strings.Contains(result, `"ok":false`) {
				return result
			}
			return d.playMediaItem(first, false, true)
		}
		return save(message)
	case "queue-clear":
		library.rememberQueue()
		library.Queue, library.PlayNext, library.Cycle = nil, nil, nil
		return save("queue cleared")
	case "queue-remove":
		index, parseErr := strconv.Atoi(strings.TrimSpace(arg))
		items := d.queueItems()
		if parseErr != nil || index < 1 || index > len(items) {
			return fail("queue index is out of range")
		}
		library.rememberQueue()
		if index <= len(library.PlayNext) {
			library.PlayNext = slices.Delete(library.PlayNext, index-1, index)
		} else {
			i := index - len(library.PlayNext) - 1
			library.Queue = slices.Delete(library.Queue, i, i+1)
		}
		library.Cycle = appendCurrentAndPending(d.current, d.queueItems())
		return save("queue item removed")
	case "queue-play":
		index, parseErr := strconv.Atoi(strings.TrimSpace(arg))
		items := d.queueItems()
		if parseErr != nil || index < 1 || index > len(items) {
			return fail("queue index is out of range")
		}
		library.rememberQueue()
		item := items[index-1]
		if index <= len(library.PlayNext) {
			library.PlayNext = slices.Delete(library.PlayNext, index-1, index)
		} else {
			i := index - len(library.PlayNext) - 1
			library.Queue = slices.Delete(library.Queue, i, i+1)
		}
		if result := save("queue item selected"); strings.Contains(result, `"ok":false`) {
			return result
		}
		return d.playMediaItem(item, false, true)
	case "queue-move":
		var request queueRequest
		if json.Unmarshal([]byte(arg), &request) != nil {
			return fail("invalid queue move")
		}
		items := d.queueItems()
		if request.Index < 1 || request.Index > len(items) || request.To < 1 || request.To > len(items) {
			return fail("queue index is out of range")
		}
		library.rememberQueue()
		item := items[request.Index-1]
		items = slices.Delete(items, request.Index-1, request.Index)
		items = slices.Insert(items, request.To-1, item)
		library.PlayNext = nil
		library.Queue = items
		library.Cycle = appendCurrentAndPending(d.current, items)
		return save("queue reordered")
	case "queue-undo":
		if len(library.Undo) == 0 {
			return fail("nothing to undo")
		}
		snapshot := library.Undo[len(library.Undo)-1]
		library.Undo = library.Undo[:len(library.Undo)-1]
		library.Queue, library.PlayNext, library.Cycle = snapshot.Queue, snapshot.PlayNext, snapshot.Cycle
		return save("queue restored")
	case "shuffle":
		value := strings.ToLower(strings.TrimSpace(arg))
		if value == "" || value == "toggle" {
			library.Shuffle = !library.Shuffle
		} else if value == "on" {
			library.Shuffle = true
		} else if value == "off" {
			library.Shuffle = false
		} else {
			return fail("shuffle must be on, off, or toggle")
		}
		return save(fmt.Sprintf("shuffle: %t", library.Shuffle))
	case "repeat":
		value := strings.ToLower(strings.TrimSpace(arg))
		if value == "" || value == "cycle" {
			value = map[string]string{"off": "all", "all": "one", "one": "off"}[library.Repeat]
		}
		if value != "off" && value != "all" && value != "one" {
			return fail("repeat must be off, all, one, or cycle")
		}
		library.Repeat = value
		return save("repeat: " + value)
	case "favorite", "bookmark", "favorite-set":
		item := d.current
		bookmark := action == "bookmark"
		var desired *bool
		if action == "favorite-set" {
			var request favoriteSetRequest
			if json.Unmarshal([]byte(arg), &request) != nil {
				return fail("invalid favorite request")
			}
			normalized, normalizeErr := request.Item.normalized()
			if normalizeErr != nil {
				return fail(normalizeErr.Error())
			}
			item = &normalized
			desired = &request.Favorite
		} else if strings.TrimSpace(arg) != "" {
			var requested MediaItem
			if json.Unmarshal([]byte(arg), &requested) != nil {
				return fail("invalid library item")
			}
			normalized, normalizeErr := requested.normalized()
			if normalizeErr != nil {
				return fail(normalizeErr.Error())
			}
			item = &normalized
		}
		if item == nil {
			return fail("nothing playing")
		}
		marked, markErr := library.setMarked(*item, bookmark, desired)
		if markErr != nil {
			return fail(markErr.Error())
		}
		label := "favorite"
		if bookmark {
			label = "bookmark"
		}
		result := save(fmt.Sprintf("%s: %t", label, marked))
		if commandSucceeded(result) && !bookmark {
			selected := *item
			go func() {
				if err := setProviderFavorite(selected, marked); err != nil {
					d.mu.Lock()
					d.storageError = "could not synchronize provider favorite: " + err.Error()
					d.mu.Unlock()
				}
			}()
		}
		return result
	}
	return fail("unknown queue command")
}

func appendCurrentAndPending(current *MediaItem, pending []MediaItem) []MediaItem {
	items := make([]MediaItem, 0, len(pending)+1)
	if current != nil {
		items = append(items, *current)
	}
	return append(items, pending...)
}
