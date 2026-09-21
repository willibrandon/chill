package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/willibrandon/chill/internal/audio"
)

const (
	remoteVersion     = 2
	remoteJobLimit    = 256
	remoteJobLifetime = 15 * time.Minute
	remoteWriteLimit  = 5 * time.Second
)

type remoteRequest struct {
	Version   int            `json:"version"`             // Version selects the wire protocol.
	ID        string         `json:"id,omitempty"`        // ID correlates the request and response.
	Method    string         `json:"method"`              // Method selects the control-plane action.
	Operation string         `json:"operation,omitempty"` // Operation selects a submitted runtime action.
	Params    map[string]any `json:"params,omitempty"`    // Params carries operation-specific values.
	JobID     string         `json:"job_id,omitempty"`    // JobID selects an existing asynchronous job.
	Topics    []string       `json:"topics,omitempty"`    // Topics selects exact subscription topics.
}

type remoteResponse struct {
	Version      int                `json:"version"`                // Version identifies the response protocol.
	ID           string             `json:"id,omitempty"`           // ID matches the originating request.
	OK           bool               `json:"ok"`                     // OK reports whether the request succeeded.
	Error        *remoteError       `json:"error,omitempty"`        // Error contains a stable failure code.
	Snapshot     *remoteSnapshot    `json:"snapshot,omitempty"`     // Snapshot contains current runtime state.
	Spectrum     *visualizerPacket  `json:"spectrum,omitempty"`     // Spectrum contains the current analysis frame.
	Capabilities []remoteCapability `json:"capabilities,omitempty"` // Capabilities describes supported operations.
	Topics       []string           `json:"topics,omitempty"`       // Topics lists subscribable event channels.
	Job          *remoteJob         `json:"job,omitempty"`          // Job contains asynchronous operation state.
}

type remoteError struct {
	Code    string `json:"code"`             // Code is stable for programmatic handling.
	Message string `json:"message"`          // Message is a concise user-facing explanation.
	Detail  string `json:"detail,omitempty"` // Detail carries optional diagnostics.
}

type remoteCapability struct {
	Name        string   `json:"name"`                 // Name identifies an operation.
	Description string   `json:"description"`          // Description explains the operation.
	Parameters  []string `json:"parameters,omitempty"` // Parameters lists accepted keys.
}

type remoteJob struct {
	ID         string       `json:"id"`                   // ID uniquely identifies the job.
	Operation  string       `json:"operation"`            // Operation names the requested action.
	State      string       `json:"state"`                // State is queued, running, succeeded, failed, or canceled.
	Progress   float64      `json:"progress,omitzero"`    // Progress ranges from zero through one.
	Message    string       `json:"message,omitempty"`    // Message describes current work.
	Result     any          `json:"result,omitempty"`     // Result contains successful output.
	Error      *remoteError `json:"error,omitempty"`      // Error contains final failure information.
	CreatedAt  time.Time    `json:"created_at"`           // CreatedAt records submission time.
	UpdatedAt  time.Time    `json:"updated_at"`           // UpdatedAt records the latest transition.
	FinishedAt time.Time    `json:"finished_at,omitzero"` // FinishedAt records terminal completion.
}

type remoteEvent struct {
	Version        int               `json:"version"`                  // Version identifies the event protocol.
	Event          string            `json:"event"`                    // Event names the subscribed topic.
	At             time.Time         `json:"at"`                       // At records publication time.
	Snapshot       *remoteSnapshot   `json:"snapshot,omitempty"`       // Snapshot contains retained runtime state.
	Job            *remoteJob        `json:"job,omitempty"`            // Job contains a job transition.
	Spectrum       *visualizerPacket `json:"spectrum,omitempty"`       // Spectrum contains one current analysis frame.
	ResyncRequired bool              `json:"resync_required,omitzero"` // ResyncRequired tells a slow client to reload state.
}

type remoteJobEntry struct {
	job    remoteJob
	cancel context.CancelCauseFunc
}

type remoteSubscription struct {
	topics   map[string]bool
	events   chan remoteEvent
	overflow chan struct{}
}

type remoteService struct {
	daemon                *Daemon
	mu                    sync.Mutex
	jobs                  map[string]*remoteJobEntry
	subscribers           map[string]*remoteSubscription
	lastPlayback          string
	lastQueue             string
	lastSettings          string
	lastMetadata          string
	libraryChange         uint64
	podcastChange         uint64
	libraryCache          *libraryState
	podcastCache          *podcastLibrary
	observedLibraryChange uint64
	observedPodcastChange uint64
}

type remoteSnapshot struct {
	Playback      Status          `json:"playback"`  // Playback contains current transport, queue, metadata, and settings.
	Library       *libraryState   `json:"library"`   // Library contains durable queues, playlists, favorites, history, and resume points.
	Podcasts      *podcastLibrary `json:"podcasts"`  // Podcasts contains subscriptions, inbox, progress, downloads, and policies.
	Providers     []providerInfo  `json:"providers"` // Providers describes enabled catalog capabilities.
	libraryChange uint64
	podcastChange uint64
}

var remoteCapabilities = []remoteCapability{
	{Name: "playback.play", Description: "play a configured station", Parameters: []string{"name"}},
	{Name: "playback.pause", Description: "pause playback"},
	{Name: "playback.resume", Description: "resume playback"},
	{Name: "playback.toggle", Description: "toggle pause or resume"},
	{Name: "playback.stop", Description: "stop playback without stopping the daemon"},
	{Name: "playback.next", Description: "play the next queue item"},
	{Name: "playback.previous", Description: "play the previous queue item"},
	{Name: "playback.seek", Description: "seek relative to the current position", Parameters: []string{"value"}},
	{Name: "playback.speed", Description: "set finite-media playback speed", Parameters: []string{"value"}},
	{Name: "playback.volume", Description: "set or adjust volume", Parameters: []string{"value"}},
	{Name: "playback.mute", Description: "toggle mute"},
	{Name: "playback.sleep", Description: "set or cancel the sleep timer", Parameters: []string{"value"}},
	{Name: "track.play", Description: "play a source-neutral media item", Parameters: []string{"item", "restart"}},
	{Name: "queue.list", Description: "read the universal queue"},
	{Name: "queue.append", Description: "append media items", Parameters: []string{"items", "if_revision"}},
	{Name: "queue.next", Description: "put media items in the play-next lane", Parameters: []string{"items", "if_revision"}},
	{Name: "queue.replace", Description: "replace the pending queue", Parameters: []string{"items", "if_revision"}},
	{Name: "queue.remove", Description: "remove one pending item", Parameters: []string{"index", "if_revision"}},
	{Name: "queue.move", Description: "move one pending item", Parameters: []string{"index", "to", "if_revision"}},
	{Name: "queue.play", Description: "play one pending item", Parameters: []string{"index", "if_revision"}},
	{Name: "queue.clear", Description: "clear the pending queue", Parameters: []string{"if_revision"}},
	{Name: "queue.undo", Description: "undo the latest queue edit", Parameters: []string{"if_revision"}},
	{Name: "settings.shuffle", Description: "set queue shuffle", Parameters: []string{"value"}},
	{Name: "settings.repeat", Description: "set queue repeat mode", Parameters: []string{"value"}},
	{Name: "settings.equalizer", Description: "show or change the equalizer", Parameters: []string{"value"}},
	{Name: "settings.notifications", Description: "show or change notifications", Parameters: []string{"value"}},
	{Name: "settings.audio", Description: "show or change audio output settings", Parameters: []string{"profile", "device", "sample_rate", "buffer_ms", "resample_quality", "mono", "channels", "exclusive"}},
	{Name: "device.list", Description: "list audio output devices"},
	{Name: "device.set", Description: "switch audio output device", Parameters: []string{"id"}},
	{Name: "provider.list", Description: "list configured providers", Parameters: []string{"validate"}},
	{Name: "provider.search", Description: "search one or every provider", Parameters: []string{"query", "provider", "limit"}},
	{Name: "provider.browse", Description: "browse provider collections", Parameters: []string{"provider", "kind", "id", "offset", "limit"}},
	{Name: "provider.play", Description: "play a provider result", Parameters: []string{"item", "provider", "id"}},
	{Name: "provider.queue", Description: "queue a provider result", Parameters: []string{"item", "provider", "id", "next"}},
	{Name: "library.state", Description: "read playlists, favorites, history, bookmarks, and resume points"},
	{Name: "library.scan", Description: "scan local files, folders, or playlists", Parameters: []string{"paths", "queue", "next"}},
	{Name: "library.favorite", Description: "set an item's favorite state", Parameters: []string{"item", "favorite"}},
	{Name: "library.bookmark", Description: "toggle an item's bookmark state", Parameters: []string{"item"}},
	{Name: "playlist.list", Description: "list saved playlists"},
	{Name: "playlist.get", Description: "read a saved playlist", Parameters: []string{"name"}},
	{Name: "playlist.save", Description: "create or replace a saved playlist", Parameters: []string{"name", "items"}},
	{Name: "playlist.delete", Description: "delete a saved playlist", Parameters: []string{"name"}},
	{Name: "playlist.rename", Description: "rename a saved playlist", Parameters: []string{"name", "new_name"}},
	{Name: "podcast.state", Description: "read subscriptions, inbox, progress, downloads, and policies"},
	{Name: "podcast.sync", Description: "refresh the subscription inbox"},
	{Name: "download.list", Description: "list offline podcast downloads"},
	{Name: "download.add", Description: "queue an episode download", Parameters: []string{"episode"}},
	{Name: "download.remove", Description: "remove a managed download", Parameters: []string{"key"}},
	{Name: "download.retry", Description: "retry a failed download", Parameters: []string{"key"}},
	{Name: "download.pin", Description: "toggle download retention pinning", Parameters: []string{"key"}},
	{Name: "download.settings", Description: "change automatic download policy", Parameters: []string{"auto", "latest", "concurrency", "max_bytes", "retain_played_days"}},
}

var remoteTopics = []string{
	"runtime.state", "runtime.playback", "runtime.queue", "runtime.settings",
	"runtime.downloads", "runtime.metadata", "runtime.spectrum", "runtime.job",
}

func newRemoteService(daemon *Daemon) *remoteService {
	return &remoteService{daemon: daemon, jobs: map[string]*remoteJobEntry{}, subscribers: map[string]*remoteSubscription{}}
}

// handle serves one request. It reports whether the connection became a stream.
func (service *remoteService) handle(conn net.Conn, frame []byte) bool {
	var request remoteRequest
	if err := json.Unmarshal(frame, &request); err != nil {
		service.write(conn, remoteFailure("", "invalid_request", "invalid JSON request", err.Error()))
		return false
	}
	if request.Version != remoteVersion {
		service.write(conn, remoteFailure(request.ID, "invalid_version", fmt.Sprintf("remote API version %d is required", remoteVersion), ""))
		return false
	}
	if request.ID == "" {
		service.write(conn, remoteFailure("", "invalid_request", "request id is required", ""))
		return false
	}
	switch request.Method {
	case "capabilities":
		service.write(conn, remoteResponse{Version: remoteVersion, ID: request.ID, OK: true, Capabilities: slices.Clone(remoteCapabilities), Topics: slices.Clone(remoteTopics)})
	case "state.get":
		snapshot, err := service.snapshot()
		if err != nil {
			service.write(conn, remoteFailure(request.ID, "internal_error", "could not read runtime state", err.Error()))
		} else {
			service.write(conn, remoteResponse{Version: remoteVersion, ID: request.ID, OK: true, Snapshot: &snapshot})
		}
	case "spectrum.get":
		packet := service.spectrum()
		service.write(conn, remoteResponse{Version: remoteVersion, ID: request.ID, OK: true, Spectrum: &packet})
	case "operation.submit":
		job, err := service.submit(request.Operation, request.Params)
		if err != nil {
			service.write(conn, remoteFailure(request.ID, remoteErrorCode(err), err.Error(), ""))
		} else {
			service.write(conn, remoteResponse{Version: remoteVersion, ID: request.ID, OK: true, Job: &job})
		}
	case "job.get":
		job, ok := service.job(request.JobID)
		if !ok {
			service.write(conn, remoteFailure(request.ID, "not_found", "job not found", ""))
		} else {
			service.write(conn, remoteResponse{Version: remoteVersion, ID: request.ID, OK: true, Job: &job})
		}
	case "job.cancel":
		job, err := service.cancel(request.JobID)
		if err != nil {
			service.write(conn, remoteFailure(request.ID, remoteErrorCode(err), err.Error(), ""))
		} else {
			service.write(conn, remoteResponse{Version: remoteVersion, ID: request.ID, OK: true, Job: &job})
		}
	case "subscribe":
		if err := service.subscribe(conn, request); err != nil {
			service.write(conn, remoteFailure(request.ID, remoteErrorCode(err), err.Error(), ""))
			return false
		}
		return true
	default:
		service.write(conn, remoteFailure(request.ID, "invalid_request", "unknown method", request.Method))
	}
	return false
}

func (service *remoteService) write(conn net.Conn, response remoteResponse) bool {
	data, err := json.Marshal(response)
	if err != nil {
		return false
	}
	_ = conn.SetWriteDeadline(time.Now().Add(remoteWriteLimit))
	_, err = conn.Write(append(data, '\n'))
	return err == nil
}

func remoteFailure(id, code, message, detail string) remoteResponse {
	return remoteResponse{Version: remoteVersion, ID: id, OK: false, Error: &remoteError{Code: code, Message: message, Detail: detail}}
}

func (service *remoteService) snapshot() (remoteSnapshot, error) {
	service.daemon.mu.Lock()
	raw := service.daemon.status()
	var playback Status
	if err := json.Unmarshal([]byte(raw), &playback); err != nil {
		service.daemon.mu.Unlock()
		return remoteSnapshot{}, err
	}
	library, err := service.daemon.mediaLibrary()
	if err != nil {
		service.daemon.mu.Unlock()
		return remoteSnapshot{}, err
	}
	podcasts, err := service.daemon.podcastLibrary()
	if err != nil {
		service.daemon.mu.Unlock()
		return remoteSnapshot{}, err
	}
	libraryChange, podcastChange := library.change, podcasts.change
	service.mu.Lock()
	libraryCopy, podcastCopy := service.libraryCache, service.podcastCache
	cached := libraryCopy != nil && podcastCopy != nil && service.libraryChange == libraryChange && service.podcastChange == podcastChange
	service.mu.Unlock()
	if !cached {
		libraryCopy, podcastCopy = new(libraryState), new(podcastLibrary)
		libraryData, _ := json.Marshal(library)
		podcastData, _ := json.Marshal(podcasts)
		_ = json.Unmarshal(libraryData, libraryCopy)
		_ = json.Unmarshal(podcastData, podcastCopy)
	}
	service.daemon.mu.Unlock()
	if !cached {
		service.mu.Lock()
		service.libraryChange, service.podcastChange = libraryChange, podcastChange
		service.libraryCache, service.podcastCache = libraryCopy, podcastCopy
		service.mu.Unlock()
	}
	registry, err := providers()
	if err != nil {
		return remoteSnapshot{}, err
	}
	return remoteSnapshot{Playback: playback, Library: libraryCopy, Podcasts: podcastCopy, Providers: registry.list(context.Background(), false), libraryChange: libraryChange, podcastChange: podcastChange}, nil
}

func (service *remoteService) spectrum() visualizerPacket {
	service.daemon.mu.Lock()
	player, state, generation := service.daemon.player, service.daemon.state, service.daemon.generation
	service.daemon.mu.Unlock()
	packet := visualizerPacket{Version: 1, State: state, Generation: generation}
	if state == "playing" {
		if source, ok := player.(interface{ audioFrame() audio.Frame }); ok {
			packet.Frame = source.audioFrame()
		} else {
			packet.State = "unavailable"
		}
	}
	return packet
}

func (service *remoteService) submit(operation string, params map[string]any) (remoteJob, error) {
	if !slices.ContainsFunc(remoteCapabilities, func(capability remoteCapability) bool { return capability.Name == operation }) {
		return remoteJob{}, fmt.Errorf("unknown operation %q", operation)
	}
	now := time.Now().UTC()
	service.mu.Lock()
	service.pruneJobsLocked(now)
	service.evictTerminalJobsLocked(remoteJobLimit - 1)
	if len(service.jobs) >= remoteJobLimit {
		service.mu.Unlock()
		return remoteJob{}, errors.New("too many active jobs")
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	entry := &remoteJobEntry{job: remoteJob{ID: uuid.New().String(), Operation: operation, State: "queued", CreatedAt: now, UpdatedAt: now}, cancel: cancel}
	service.jobs[entry.job.ID] = entry
	job := entry.job
	service.mu.Unlock()
	service.publish(remoteEvent{Version: remoteVersion, Event: "runtime.job", At: now, Job: &job})
	go service.runJob(ctx, entry, params)
	return job, nil
}

func (service *remoteService) runJob(ctx context.Context, entry *remoteJobEntry, params map[string]any) {
	service.updateJob(entry.job.ID, func(job *remoteJob) { job.State = "running" })
	result, err := service.daemon.performRemoteOperation(ctx, entry.job.Operation, params, func(progress float64, message string) {
		service.updateJob(entry.job.ID, func(job *remoteJob) {
			job.Progress, job.Message = min(1, max(0, progress)), message
		})
	})
	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	service.updateJob(entry.job.ID, func(job *remoteJob) {
		job.FinishedAt = time.Now().UTC()
		job.Progress = 1
		if err == nil {
			job.State, job.Result = "succeeded", result
			return
		}
		job.State = "failed"
		if errors.Is(err, context.Canceled) {
			job.State = "canceled"
		}
		job.Error = &remoteError{Code: remoteErrorCode(err), Message: err.Error()}
	})
	service.observe()
}

func (service *remoteService) updateJob(id string, update func(*remoteJob)) {
	service.mu.Lock()
	entry := service.jobs[id]
	if entry == nil {
		service.mu.Unlock()
		return
	}
	update(&entry.job)
	entry.job.UpdatedAt = time.Now().UTC()
	job := entry.job
	service.mu.Unlock()
	service.publish(remoteEvent{Version: remoteVersion, Event: "runtime.job", At: job.UpdatedAt, Job: &job})
}

func (service *remoteService) job(id string) (remoteJob, bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.pruneJobsLocked(time.Now().UTC())
	entry, ok := service.jobs[id]
	if !ok {
		return remoteJob{}, false
	}
	return entry.job, true
}

func (service *remoteService) cancel(id string) (remoteJob, error) {
	service.mu.Lock()
	entry := service.jobs[id]
	if entry == nil {
		service.mu.Unlock()
		return remoteJob{}, errors.New("job not found")
	}
	if entry.job.State == "succeeded" || entry.job.State == "failed" || entry.job.State == "canceled" {
		job := entry.job
		service.mu.Unlock()
		return job, nil
	}
	entry.cancel(context.Canceled)
	job := entry.job
	service.mu.Unlock()
	return job, nil
}

func (service *remoteService) pruneJobsLocked(now time.Time) {
	for id, entry := range service.jobs {
		if !entry.job.FinishedAt.IsZero() && now.Sub(entry.job.FinishedAt) >= remoteJobLifetime {
			delete(service.jobs, id)
		}
	}
}

func (service *remoteService) evictTerminalJobsLocked(maximum int) {
	for len(service.jobs) > maximum {
		oldestID := ""
		var oldest time.Time
		for id, entry := range service.jobs {
			if entry.job.FinishedAt.IsZero() {
				continue
			}
			if oldestID == "" || entry.job.FinishedAt.Before(oldest) {
				oldestID, oldest = id, entry.job.FinishedAt
			}
		}
		if oldestID == "" {
			return
		}
		delete(service.jobs, oldestID)
	}
}

func (service *remoteService) subscribe(conn net.Conn, request remoteRequest) error {
	if len(request.Topics) == 0 {
		return errors.New("at least one subscription topic is required")
	}
	topics := make(map[string]bool, len(request.Topics))
	for _, topic := range request.Topics {
		if !slices.Contains(remoteTopics, topic) {
			return fmt.Errorf("unknown subscription topic %q", topic)
		}
		topics[topic] = true
	}
	id := uuid.New().String()
	subscription := &remoteSubscription{topics: topics, events: make(chan remoteEvent, 32), overflow: make(chan struct{}, 1)}
	service.mu.Lock()
	service.subscribers[id] = subscription
	service.mu.Unlock()
	defer func() {
		service.mu.Lock()
		delete(service.subscribers, id)
		service.mu.Unlock()
	}()
	if !service.write(conn, remoteResponse{Version: remoteVersion, ID: request.ID, OK: true}) {
		return nil
	}
	if topics["runtime.state"] {
		if snapshot, err := service.snapshot(); err == nil {
			if !service.writeEvent(conn, remoteEvent{Version: remoteVersion, Event: "runtime.state", At: time.Now().UTC(), Snapshot: &snapshot}) {
				return nil
			}
		}
	}
	keepaliveTicker := time.NewTicker(30 * time.Second)
	defer keepaliveTicker.Stop()
	keepalive := keepaliveTicker.C
	var spectrum <-chan time.Time
	var spectrumTicker *time.Ticker
	if topics["runtime.spectrum"] {
		spectrumTicker = time.NewTicker(visualizerInterval)
		defer spectrumTicker.Stop()
		spectrum = spectrumTicker.C
	}
	for {
		select {
		case event := <-subscription.events:
			if !service.writeEvent(conn, event) {
				return nil
			}
		case <-subscription.overflow:
			service.writeEvent(conn, remoteEvent{Version: remoteVersion, Event: "system.overflow", At: time.Now().UTC(), ResyncRequired: true})
			return nil
		case <-spectrum:
			packet := service.spectrum()
			if !service.writeEvent(conn, remoteEvent{Version: remoteVersion, Event: "runtime.spectrum", At: time.Now().UTC(), Spectrum: &packet}) {
				return nil
			}
		case <-keepalive:
			if !service.writeEvent(conn, remoteEvent{Version: remoteVersion, Event: "system.keepalive", At: time.Now().UTC()}) {
				return nil
			}
		}
	}
}

func (service *remoteService) writeEvent(conn net.Conn, event remoteEvent) bool {
	data, err := json.Marshal(event)
	if err != nil {
		return false
	}
	_ = conn.SetWriteDeadline(time.Now().Add(remoteWriteLimit))
	_, err = conn.Write(append(data, '\n'))
	return err == nil
}

func (service *remoteService) publish(event remoteEvent) {
	service.mu.Lock()
	defer service.mu.Unlock()
	for _, subscriber := range service.subscribers {
		if !subscriber.topics[event.Event] {
			continue
		}
		select {
		case subscriber.events <- event:
		default:
			select {
			case subscriber.overflow <- struct{}{}:
			default:
			}
		}
	}
}

func (service *remoteService) observe() {
	service.mu.Lock()
	hasSubscribers := false
	for _, subscriber := range service.subscribers {
		for topic := range subscriber.topics {
			if topic != "runtime.job" && topic != "runtime.spectrum" {
				hasSubscribers = true
				break
			}
		}
		if hasSubscribers {
			break
		}
	}
	service.mu.Unlock()
	if !hasSubscribers {
		return
	}
	snapshot, err := service.snapshot()
	if err != nil {
		return
	}
	queueCopy := struct {
		Revision uint64      `json:"revision"`
		Queue    []MediaItem `json:"queue"`
		PlayNext []MediaItem `json:"play_next"`
		Shuffle  bool        `json:"shuffle"`
		Repeat   string      `json:"repeat"`
	}{snapshot.Playback.QueueRevision, snapshot.Playback.Queue, snapshot.Playback.PlayNext, snapshot.Playback.Shuffle, snapshot.Playback.Repeat}
	playbackCopy := snapshot.Playback
	playbackCopy.Queue, playbackCopy.PlayNext = nil, nil
	settingsCopy := struct {
		Volume        int                  `json:"volume"`
		Muted         bool                 `json:"muted"`
		EQPreset      string               `json:"eq_preset"`
		EQBands       audio.EqualizerBands `json:"eq_bands"`
		Audio         AudioStatus          `json:"audio"`
		Shuffle       bool                 `json:"shuffle"`
		Repeat        string               `json:"repeat"`
		Notifications bool                 `json:"notifications"`
	}{snapshot.Playback.Volume, snapshot.Playback.Muted, snapshot.Playback.EQPreset, snapshot.Playback.EQBands,
		snapshot.Playback.Audio, snapshot.Playback.Shuffle, snapshot.Playback.Repeat, snapshot.Playback.Notifications}
	metadataCopy := struct {
		Item          *MediaItem     `json:"item"`
		NowPlaying    any            `json:"now_playing,omitempty"`
		Providers     []providerInfo `json:"providers"`
		LibraryChange uint64         `json:"library_change"`
		PodcastChange uint64         `json:"podcast_change"`
	}{snapshot.Playback.Item, snapshot.Playback.NowPlaying, snapshot.Providers, snapshot.libraryChange, snapshot.podcastChange}
	playbackBytes, _ := json.Marshal(playbackCopy, json.Deterministic(true))
	queueBytes, _ := json.Marshal(queueCopy, json.Deterministic(true))
	settingsBytes, _ := json.Marshal(settingsCopy, json.Deterministic(true))
	metadataBytes, _ := json.Marshal(metadataCopy, json.Deterministic(true))
	service.mu.Lock()
	playbackChanged := string(playbackBytes) != service.lastPlayback
	queueChanged := string(queueBytes) != service.lastQueue
	settingsChanged := string(settingsBytes) != service.lastSettings
	downloadsChanged := snapshot.podcastChange != service.observedPodcastChange
	metadataChanged := string(metadataBytes) != service.lastMetadata
	service.lastPlayback, service.lastQueue = string(playbackBytes), string(queueBytes)
	service.lastSettings, service.lastMetadata = string(settingsBytes), string(metadataBytes)
	service.observedLibraryChange, service.observedPodcastChange = snapshot.libraryChange, snapshot.podcastChange
	service.mu.Unlock()
	now := time.Now().UTC()
	stateChanged := playbackChanged || queueChanged || settingsChanged || downloadsChanged || metadataChanged
	if stateChanged || playbackChanged || queueChanged || settingsChanged || downloadsChanged || metadataChanged {
		service.publish(remoteEvent{Version: remoteVersion, Event: "runtime.state", At: now, Snapshot: &snapshot})
	}
	if playbackChanged {
		service.publish(remoteEvent{Version: remoteVersion, Event: "runtime.playback", At: now, Snapshot: &snapshot})
	}
	if settingsChanged {
		service.publish(remoteEvent{Version: remoteVersion, Event: "runtime.settings", At: now, Snapshot: &snapshot})
	}
	if metadataChanged {
		service.publish(remoteEvent{Version: remoteVersion, Event: "runtime.metadata", At: now, Snapshot: &snapshot})
	}
	if queueChanged {
		service.publish(remoteEvent{Version: remoteVersion, Event: "runtime.queue", At: now, Snapshot: &snapshot})
	}
	if downloadsChanged {
		service.publish(remoteEvent{Version: remoteVersion, Event: "runtime.downloads", At: now, Snapshot: &snapshot})
	}
}

type operationResult struct {
	Message string `json:"message,omitempty"` // Message contains human-readable output.
	Data    any    `json:"data,omitempty"`    // Data contains structured operation output.
}

func (d *Daemon) performRemoteOperation(ctx context.Context, operation string, params map[string]any, progress func(float64, string)) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if result, handled, err := d.performStructuredRemoteOperation(ctx, operation, params, progress); handled {
		return result, err
	}
	action, arg, expected, err := legacyRemoteOperation(operation, params)
	if err != nil {
		return nil, err
	}
	response := d.executeWithRevision(action, arg, expected)
	var result reply
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, fmt.Errorf("invalid operation response: %w", err)
	}
	if !result.OK {
		return nil, errors.New(result.Msg)
	}
	output := operationResult{Message: result.Msg}
	if strings.HasPrefix(strings.TrimSpace(result.Msg), "{") || strings.HasPrefix(strings.TrimSpace(result.Msg), "[") {
		var data any
		if json.Unmarshal([]byte(result.Msg), &data) == nil {
			output.Message, output.Data = "", data
		}
	}
	return output, nil
}

func (d *Daemon) performStructuredRemoteOperation(ctx context.Context, operation string, params map[string]any, progress func(float64, string)) (any, bool, error) {
	value := func(name string) string {
		raw, ok := params[name]
		if !ok || raw == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(raw))
	}
	limit := func(fallback int) int {
		if number, ok := numberToInt(params["limit"]); ok && number > 0 {
			return min(number, 100)
		}
		return fallback
	}
	switch operation {
	case "provider.list":
		registry, err := providers()
		if err != nil {
			return nil, true, err
		}
		validate, _ := params["validate"].(bool)
		return registry.list(ctx, validate), true, nil
	case "provider.search":
		registry, err := providers()
		if err != nil {
			return nil, true, err
		}
		query := value("query")
		items, err := registry.search(ctx, value("provider"), query, limit(20), func(provider string, complete, total int) {
			if progress != nil && total > 0 {
				progress(float64(complete)/float64(total), "searched "+provider)
			}
		})
		return items, true, err
	case "provider.browse":
		registry, err := providers()
		if err != nil {
			return nil, true, err
		}
		offset, _ := numberToInt(params["offset"])
		page, err := registry.browse(ctx, value("provider"), providerBrowseRequest{Kind: value("kind"), ID: value("id"), Offset: offset, Limit: limit(20)})
		return page, true, err
	case "provider.play", "provider.queue":
		item, err := remoteProviderItem(ctx, params)
		if err != nil {
			return nil, true, err
		}
		if err := providerPlaybackAvailable(item); err != nil {
			return nil, true, err
		}
		action := "media-play"
		payload := any(map[string]any{"item": item})
		if operation == "provider.queue" {
			action = "queue-append"
			if next, _ := params["next"].(bool); next {
				action = "queue-next"
			}
			payload = queueRequest{Items: []MediaItem{item}}
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, true, err
		}
		return d.remoteLegacyResult(action, string(data), nil)
	case "device.list":
		d.mu.Lock()
		selected := d.audio.Device
		d.mu.Unlock()
		devices, err := listAudioDevices(ctx, selected)
		return devices, true, err
	case "device.set":
		d.mu.Lock()
		selected := d.audio.Device
		d.mu.Unlock()
		device, err := resolveAudioDevice(ctx, value("id"), selected)
		if err != nil {
			return nil, true, err
		}
		return d.remoteLegacyResult("audio", "device "+device, nil)
	case "settings.audio":
		return d.remoteAudioSettings(ctx, params)
	case "library.state":
		library, err := d.remoteLibraryState()
		return library, true, err
	case "library.scan":
		rawPaths, ok := stringSliceParam(params["paths"])
		if !ok || len(rawPaths) == 0 || len(rawPaths) > 100 {
			return nil, true, errors.New("paths must be a non-empty array of at most 100 entries")
		}
		paths := make([]string, 0, len(rawPaths))
		for _, path := range rawPaths {
			if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\x00\r\n") {
				return nil, true, errors.New("paths must contain valid path strings")
			}
			paths = append(paths, path)
		}
		items, err := loadMediaInputs(ctx, paths)
		if err != nil {
			return nil, true, err
		}
		if queue, _ := params["queue"].(bool); queue {
			action := "queue-append"
			if next, _ := params["next"].(bool); next {
				action = "queue-next"
			}
			data, marshalErr := json.Marshal(queueRequest{Items: items})
			if marshalErr != nil {
				return nil, true, marshalErr
			}
			return d.remoteLegacyResult(action, string(data), nil)
		}
		return items, true, nil
	case "library.favorite", "library.bookmark":
		item, err := remoteProviderItem(ctx, params)
		if err != nil {
			return nil, true, err
		}
		action, payload := "bookmark", any(item)
		if operation == "library.favorite" {
			action = "favorite-set"
			favorite, ok := params["favorite"].(bool)
			if !ok {
				return nil, true, errors.New("favorite must be true or false")
			}
			payload = favoriteSetRequest{Item: item, Favorite: favorite}
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, true, err
		}
		return d.remoteLegacyResult(action, string(data), nil)
	case "playlist.list", "playlist.get":
		library, err := d.remoteLibraryState()
		if err != nil {
			return nil, true, err
		}
		if operation == "playlist.list" {
			return library.Playlists, true, nil
		}
		playlist, ok := library.Playlists[playlistKey(value("name"))]
		if !ok {
			return nil, true, errors.New("playlist not found")
		}
		return playlist, true, nil
	case "playlist.save", "playlist.delete", "playlist.rename":
		request := playlistMutation{Name: value("name"), NewName: value("new_name")}
		if operation == "playlist.save" {
			raw, ok := params["items"]
			if !ok {
				return nil, true, errors.New("items are required")
			}
			data, err := json.Marshal(raw)
			if err != nil || json.Unmarshal(data, &request.Items) != nil {
				return nil, true, errors.New("items must be media items")
			}
		}
		data, err := json.Marshal(request)
		if err != nil {
			return nil, true, err
		}
		action := map[string]string{"playlist.save": "playlist-put", "playlist.delete": "playlist-delete", "playlist.rename": "playlist-rename"}[operation]
		return d.remoteLegacyResult(action, string(data), nil)
	case "podcast.state", "download.list":
		podcasts, err := d.remotePodcastState()
		if err != nil {
			return nil, true, err
		}
		if operation == "podcast.state" {
			return podcasts, true, nil
		}
		downloads := make([]episodeDownload, 0, len(podcasts.Downloads))
		for _, download := range podcasts.Downloads {
			downloads = append(downloads, download)
		}
		slices.SortFunc(downloads, func(a, b episodeDownload) int { return b.Updated.Compare(a.Updated) })
		return downloads, true, nil
	case "podcast.sync":
		d.mu.Lock()
		podcasts, err := d.podcastLibrary()
		if err == nil && podcasts.Syncing {
			err = errors.New("podcast sync is already running")
		}
		if err == nil {
			podcasts.Syncing, podcasts.SyncError = true, ""
			err = podcasts.commit(*podcasts)
		}
		d.mu.Unlock()
		if err != nil {
			return nil, true, err
		}
		if err := d.syncPodcastInboxContext(ctx, progress); err != nil {
			return nil, true, err
		}
		return operationResult{Message: "podcast inbox synchronized"}, true, nil
	case "download.add":
		episode, ok := params["episode"]
		if !ok {
			return nil, true, errors.New("episode is required")
		}
		data, err := json.Marshal(episode)
		if err != nil {
			return nil, true, err
		}
		return d.remoteLegacyResult("podcast-download", string(data), nil)
	case "download.remove", "download.retry", "download.pin":
		key := value("key")
		if key == "" || strings.ContainsAny(key, "\x00\r\n") {
			return nil, true, errors.New("download key is required")
		}
		action := map[string]string{"download.remove": "podcast-download-remove", "download.retry": "podcast-download-retry", "download.pin": "podcast-download-pin"}[operation]
		return d.remoteLegacyResult(action, key, nil)
	case "download.settings":
		podcasts, err := d.remotePodcastState()
		if err != nil {
			return nil, true, err
		}
		settings := podcasts.DownloadSettings
		recognized := false
		if raw, ok := params["auto"]; ok {
			value, valid := raw.(bool)
			if !valid {
				return nil, true, errors.New("auto must be true or false")
			}
			settings.Auto, recognized = value, true
		}
		for _, field := range []struct {
			name   string
			target *int
		}{{"latest", &settings.Latest}, {"concurrency", &settings.Concurrency}, {"retain_played_days", &settings.RetainPlayedDays}} {
			if raw, ok := params[field.name]; ok {
				value, valid := numberToInt(raw)
				if !valid {
					return nil, true, fmt.Errorf("%s must be a non-negative integer", field.name)
				}
				*field.target, recognized = value, true
			}
		}
		if raw, ok := params["max_bytes"]; ok {
			value, valid := numberToInt64(raw)
			if !valid {
				return nil, true, errors.New("max_bytes must be a non-negative integer")
			}
			settings.MaxBytes, recognized = value, true
		}
		if !recognized {
			return nil, true, errors.New("at least one download setting is required")
		}
		data, err := json.Marshal(settings)
		if err != nil {
			return nil, true, err
		}
		return d.remoteLegacyResult("podcast-download-settings", string(data), nil)
	default:
		return nil, false, nil
	}
}

func (d *Daemon) remoteLibraryState() (*libraryState, error) {
	response := d.execute("library-state", "")
	var result reply
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, errors.New(result.Msg)
	}
	var library libraryState
	if err := json.Unmarshal([]byte(result.Msg), &library); err != nil {
		return nil, err
	}
	return &library, nil
}

func (d *Daemon) remotePodcastState() (*podcastLibrary, error) {
	response := d.execute("podcasts", "")
	var result reply
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, errors.New(result.Msg)
	}
	var podcasts podcastLibrary
	if err := json.Unmarshal([]byte(result.Msg), &podcasts); err != nil {
		return nil, err
	}
	return &podcasts, nil
}

func remoteProviderItem(ctx context.Context, params map[string]any) (MediaItem, error) {
	if raw, ok := params["item"]; ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return MediaItem{}, err
		}
		var item MediaItem
		if err := json.Unmarshal(data, &item); err != nil {
			return MediaItem{}, errors.New("item must be a media item")
		}
		return item.normalized()
	}
	provider, providerOK := params["provider"]
	id, idOK := params["id"]
	if !providerOK || !idOK {
		return MediaItem{}, errors.New("item or provider and id are required")
	}
	providerName, itemID := strings.ToLower(strings.TrimSpace(fmt.Sprint(provider))), strings.TrimSpace(fmt.Sprint(id))
	if providerName == "" || itemID == "" {
		return MediaItem{}, errors.New("item or provider and id are required")
	}
	registry, err := providers()
	if err != nil {
		return MediaItem{}, err
	}
	return registry.lookup(ctx, providerName, itemID)
}

func (d *Daemon) remoteAudioSettings(ctx context.Context, params map[string]any) (any, bool, error) {
	fields := []struct {
		parameter string
		command   string
	}{
		{"profile", "profile"}, {"device", "device"}, {"sample_rate", "sample-rate"}, {"buffer_ms", "buffer"},
		{"resample_quality", "resample-quality"}, {"mono", "mono"}, {"channels", "channels"}, {"exclusive", "exclusive"},
	}
	type audioMutation struct {
		command string
		value   string
	}
	mutations := make([]audioMutation, 0, len(fields))
	changed := false
	for _, field := range fields {
		raw, ok := params[field.parameter]
		if !ok {
			continue
		}
		changed = true
		value := fmt.Sprint(raw)
		if flag, ok := raw.(bool); ok {
			if flag {
				value = "on"
			} else {
				value = "off"
			}
		}
		if field.parameter == "device" {
			device, err := resolveAudioDevice(ctx, value, "")
			if err != nil {
				return nil, true, err
			}
			value = device
		}
		mutations = append(mutations, audioMutation{command: field.command, value: value})
	}
	if !changed && len(params) > 0 {
		return nil, true, errors.New("no recognized audio settings")
	}
	if changed {
		d.mu.Lock()
		defer d.mu.Unlock()
		previous := d.audio
		next := d.audio
		for _, mutation := range mutations {
			var err error
			next, err = updateAudioSettings(next, mutation.command+" "+mutation.value)
			if err != nil {
				return nil, true, err
			}
		}
		response := d.applyAudioSettings(next)
		if !commandSucceeded(response) {
			var result reply
			_ = json.Unmarshal([]byte(response), &result)
			return nil, true, errors.New(result.Msg)
		}
		if d.audio != previous {
			d.runtimeRevision++
		}
		d.updateMedia()
		return d.audio.status(d.activeAudioDevice), true, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.audio.status(d.activeAudioDevice), true, nil
}

func (d *Daemon) remoteLegacyResult(action, arg string, expected *uint64) (any, bool, error) {
	response := d.executeWithRevision(action, arg, expected)
	var result reply
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, true, fmt.Errorf("invalid operation response: %w", err)
	}
	if !result.OK {
		return nil, true, errors.New(result.Msg)
	}
	return operationResult{Message: result.Msg}, true, nil
}

func legacyRemoteOperation(operation string, params map[string]any) (string, string, *uint64, error) {
	value := func(names ...string) string {
		for _, name := range names {
			if raw, ok := params[name]; ok {
				return strings.TrimSpace(fmt.Sprint(raw))
			}
		}
		return ""
	}
	var expected *uint64
	if raw, ok := params["if_revision"]; ok {
		revision, ok := numberToUint64(raw)
		if !ok {
			return "", "", nil, errors.New("if_revision must be a non-negative integer")
		}
		expected = new(revision)
	}
	switch operation {
	case "playback.play":
		return "play", value("name"), nil, nil
	case "playback.pause":
		return "pause", "", nil, nil
	case "playback.resume":
		return "resume", "", nil, nil
	case "playback.toggle":
		return "toggle", "", nil, nil
	case "playback.stop":
		return "halt", "", nil, nil
	case "playback.next":
		return "next", "", expected, nil
	case "playback.previous":
		return "prev", "", expected, nil
	case "playback.seek":
		return requiredValue("seek", value("value"))
	case "playback.speed":
		return requiredValue("speed", value("value"))
	case "playback.volume":
		return requiredValue("vol", value("value"))
	case "playback.mute":
		return "mute", "", nil, nil
	case "playback.sleep":
		return requiredValue("sleep", value("value"))
	case "settings.shuffle":
		return requiredValue("shuffle", value("value"))
	case "settings.repeat":
		return requiredValue("repeat", value("value"))
	case "settings.equalizer":
		return "eq", value("value"), nil, nil
	case "settings.notifications":
		return "notifications", value("value"), nil, nil
	case "track.play":
		item, ok := params["item"]
		if !ok {
			return "", "", nil, errors.New("item is required")
		}
		request := map[string]any{"item": item}
		if restart, ok := params["restart"].(bool); ok {
			request["restart"] = restart
		}
		data, err := json.Marshal(request)
		return "media-play", string(data), nil, err
	case "queue.list":
		return "queue-list", "", nil, nil
	case "queue.append", "queue.next", "queue.replace":
		items, ok := params["items"]
		if !ok {
			return "", "", nil, errors.New("items are required")
		}
		data, err := json.Marshal(map[string]any{"items": items})
		action := map[string]string{"queue.append": "queue-append", "queue.next": "queue-next", "queue.replace": "queue-replace"}[operation]
		return action, string(data), expected, err
	case "queue.remove", "queue.play":
		index := value("index")
		if index == "" {
			return "", "", nil, errors.New("index is required")
		}
		action := map[string]string{"queue.remove": "queue-remove", "queue.play": "queue-play"}[operation]
		return action, index, expected, nil
	case "queue.move":
		index, indexOK := numberToInt(params["index"])
		to, toOK := numberToInt(params["to"])
		if !indexOK || !toOK {
			return "", "", nil, errors.New("index and to are required")
		}
		data, err := json.Marshal(map[string]any{"index": index, "to": to})
		return "queue-move", string(data), expected, err
	case "queue.clear":
		return "queue-clear", "", expected, nil
	case "queue.undo":
		return "queue-undo", "", expected, nil
	default:
		return "", "", nil, fmt.Errorf("operation %q is not available", operation)
	}
}

func requiredValue(action, value string) (string, string, *uint64, error) {
	if value == "" {
		return "", "", nil, errors.New("value is required")
	}
	return action, value, nil, nil
}

func numberToUint64(value any) (uint64, bool) {
	switch value := value.(type) {
	case float64:
		converted := uint64(value)
		return converted, value >= 0 && float64(converted) == value
	case int64:
		return uint64(value), value >= 0
	case uint64:
		return value, true
	default:
		return 0, false
	}
}

func numberToInt(value any) (int, bool) {
	switch value := value.(type) {
	case float64:
		converted := int(value)
		return converted, value >= 0 && float64(converted) == value
	case int:
		return value, value >= 0
	case int64:
		return int(value), value >= 0 && int64(int(value)) == value
	case uint64:
		return int(value), uint64(int(value)) == value
	case string:
		converted, err := strconv.Atoi(value)
		return converted, err == nil && converted >= 0
	default:
		return 0, false
	}
}

func numberToInt64(value any) (int64, bool) {
	switch value := value.(type) {
	case float64:
		converted := int64(value)
		return converted, value >= 0 && float64(converted) == value
	case int:
		return int64(value), value >= 0
	case int64:
		return value, value >= 0
	case uint64:
		return int64(value), value <= uint64(^uint64(0)>>1)
	case string:
		converted, err := strconv.ParseInt(value, 10, 64)
		return converted, err == nil && converted >= 0
	default:
		return 0, false
	}
}

func stringSliceParam(value any) ([]string, bool) {
	switch value := value.(type) {
	case []string:
		return slices.Clone(value), true
	case []any:
		result := make([]string, len(value))
		for index, raw := range value {
			text, ok := raw.(string)
			if !ok {
				return nil, false
			}
			result[index] = text
		}
		return result, true
	default:
		return nil, false
	}
}

func remoteErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "conflict"):
		return "conflict"
	case strings.Contains(message, "not found"):
		return "not_found"
	case strings.Contains(message, "unknown operation"), strings.Contains(message, "not available"):
		return "unknown_operation"
	case strings.Contains(message, "required"), strings.Contains(message, "must be"), strings.Contains(message, "invalid"):
		return "invalid_params"
	case strings.Contains(message, "too many"):
		return "unavailable"
	default:
		return "internal_error"
	}
}
