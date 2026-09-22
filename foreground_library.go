package main

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/willibrandon/chill/internal/audio"
	"github.com/willibrandon/chill/internal/lyrics"
	"github.com/willibrandon/chill/internal/media"
	"github.com/willibrandon/chill/internal/notify"
)

type foregroundMediaModel struct {
	width, height     int
	items             []MediaItem
	index             int
	player            *pcmPlayer
	settings          playbackSettings
	eq                equalizerConfig
	eqCursor          int
	state, err        string
	paused, muted     bool
	generation        uint64
	shuffle           bool
	repeat            string
	media             *media.Service
	library           *libraryState
	podcasts          *podcastLibrary
	rate              float64
	lyricsOpen        bool
	lyricsLoading     bool
	lyricsLines       []string
	lyricsHeading     string
	lyricsOffset      int
	lastProgress      time.Time
	notifiedID        string
	played            map[int]bool
	providerSync      providerProgressSequencer
	providerDone      bool
	providerScrobbled bool
	presentation      interfaceSettings
}

type foregroundProviderSyncMsg struct{ err error }

func foregroundPlaybackState(paused bool) string {
	if paused {
		return "paused"
	}
	return "playing"
}

func (m *foregroundMediaModel) current() MediaItem { return m.items[m.index] }

func (m *foregroundMediaModel) start(offset time.Duration) tea.Cmd {
	if m.player != nil {
		m.player.close()
		m.player = nil
	}
	m.generation++
	m.providerDone = false
	m.providerScrobbled = false
	item := m.current()
	rememberForegroundProviderItem(item)
	raw, err := newPCMPlayer(m.settings.Volume, m.muted, m.paused, max(time.Duration(0), offset), item.finite())
	if err != nil {
		m.state, m.err = "failed", err.Error()
		return nil
	}
	p := raw.(*pcmPlayer)
	p.setEqualizer(m.eq.activeBands())
	if m.rate != 0 && m.rate != 1 {
		if err := p.command("set_property", "speed", m.rate); err != nil {
			p.close()
			m.state, m.err = "failed", err.Error()
			return nil
		}
	}
	if err := p.command("loadfile", item.Source, "replace"); err != nil {
		p.close()
		m.state, m.err = "failed", err.Error()
		return nil
	}
	m.player, m.state, m.err = p, "loading", ""
	m.preloadNextLocal()
	return waitForegroundPlayer(p, m.generation)
}

func rememberForegroundProviderItem(item MediaItem) {
	if item.Kind != MediaProvider {
		return
	}
	if registry, err := providers(); err == nil {
		registry.remember([]MediaItem{item})
	}
}

func (m *foregroundMediaModel) preloadNextLocal() {
	if m.player == nil || m.current().Kind != MediaTrack || m.shuffle {
		return
	}
	next := m.index + 1
	if m.repeat == "one" {
		next = m.index
	} else if next >= len(m.items) && m.repeat == "all" {
		next = 0
	}
	if next < len(m.items) && m.items[next].Kind == MediaTrack {
		item := m.items[next]
		offset := time.Duration(0)
		if m.library != nil && m.repeat != "one" {
			point := m.library.Resume[item.ID]
			if !point.Played && point.Position >= 15 {
				offset = time.Duration(max(0, point.Position-5) * float64(time.Second))
			}
		}
		m.player.preload(item.Source, offset, true)
	}
}

func (m *foregroundMediaModel) continueOrStart(previous MediaItem, offset time.Duration) tea.Cmd {
	if m.player != nil && previous.Kind == MediaTrack && m.current().Kind == MediaTrack && m.player.transition(m.current().Source, offset, true) {
		m.state, m.err = "loading", ""
		m.preloadNextLocal()
		return waitForegroundPlayer(m.player, m.generation)
	}
	return m.start(offset)
}

func (m *foregroundMediaModel) next() tea.Cmd {
	previous := m.current()
	m.saveProgress(true)
	if previous.Kind == MediaProvider {
		m.providerDone = true
	}
	providerSync := m.syncProviderProgress("finished")
	if len(m.items) == 1 && m.repeat == "off" {
		m.state = "ended"
		m.persistQueue()
		return providerSync
	}
	if m.repeat == "one" {
		return tea.Batch(providerSync, m.continueOrStart(previous, 0))
	}
	if m.shuffle && len(m.items) > 1 {
		var candidates []int
		for index := range m.items {
			if index != m.index && !m.played[index] {
				candidates = append(candidates, index)
			}
		}
		if len(candidates) == 0 && m.repeat == "all" {
			m.played = map[int]bool{m.index: true}
			for index := range m.items {
				if index != m.index {
					candidates = append(candidates, index)
				}
			}
		}
		if len(candidates) == 0 {
			m.state = "ended"
			m.persistQueue()
			return providerSync
		}
		m.index = candidates[rand.Intn(len(candidates))]
	} else if m.index+1 < len(m.items) {
		m.index++
	} else if m.repeat == "all" {
		m.index = 0
		m.played = map[int]bool{}
	} else {
		m.state = "ended"
		m.persistQueue()
		return providerSync
	}
	m.played[m.index] = true
	m.persistQueue()
	return tea.Batch(providerSync, m.continueOrStart(previous, m.resumePosition()))
}

func (m *foregroundMediaModel) previous() tea.Cmd {
	m.saveProgress(false)
	providerSync := m.syncProviderProgress(foregroundPlaybackState(m.paused))
	if m.player != nil && m.player.position() > 3*time.Second {
		return tea.Batch(providerSync, m.start(0))
	}
	if m.index > 0 {
		m.index--
	} else if m.repeat == "all" {
		m.index = len(m.items) - 1
	} else {
		m.err = "no previous item"
		return providerSync
	}
	m.persistQueue()
	return tea.Batch(providerSync, m.start(m.resumePosition()))
}

func (m *foregroundMediaModel) persistQueue() {
	if m.library == nil {
		return
	}
	previous := m.library.queueFingerprint()
	var pending []MediaItem
	if m.shuffle {
		for index, item := range m.items {
			if index != m.index && !m.played[index] {
				pending = append(pending, item)
			}
		}
	} else if m.index+1 < len(m.items) {
		pending = cloneItems(m.items[m.index+1:])
	}
	m.library.Queue, m.library.PlayNext = pending, nil
	m.library.Cycle = cloneItems(m.items)
	m.library.Shuffle, m.library.Repeat = m.shuffle, m.repeat
	_ = m.library.commitQueue(previous)
}

func (m *foregroundMediaModel) resumePosition() time.Duration {
	item := m.current()
	if item.Kind == MediaPodcast && m.podcasts != nil {
		return m.podcasts.resume(*item.Episode)
	}
	if m.library != nil {
		point := m.library.Resume[item.ID]
		if !point.Played && point.Position >= 15 {
			return time.Duration(max(0, point.Position-5) * float64(time.Second))
		}
	}
	return 0
}

func (m *foregroundMediaModel) saveProgress(ended bool) {
	if m.player == nil || !m.current().finite() {
		return
	}
	m.saveProgressAt(ended, m.player.position())
}

func (m *foregroundMediaModel) saveProgressAt(ended bool, playhead time.Duration) {
	if !m.current().finite() {
		return
	}
	item, position := m.current(), playhead.Seconds()
	if item.Kind == MediaPodcast && m.podcasts != nil {
		_ = m.podcasts.record(*item.Episode, position, item.Duration, ended)
		return
	}
	if m.library == nil {
		return
	}
	duration := item.Duration
	threshold := duration - 60
	if duration < 120 {
		threshold = duration / 2
	}
	m.library.Resume[item.ID] = resumePoint{Position: max(0, position), Duration: duration, Played: ended || duration > 0 && position >= threshold, Updated: time.Now().UTC()}
	m.library.recordRecent(item)
	_ = m.library.commit()
}

func (m *foregroundMediaModel) syncProviderProgress(state string) tea.Cmd {
	item := m.current()
	if item.Kind != MediaProvider {
		return nil
	}
	position := time.Duration(0)
	if m.player != nil {
		position = m.player.position()
	}
	duration := time.Duration(item.Duration * float64(time.Second))
	scrobble := state == "finished" && !m.providerScrobbled
	if scrobble {
		m.providerScrobbled = true
	}
	result := m.providerSync.submit(providerProgressUpdate{
		item: item, position: position, duration: duration, state: state, completed: m.providerDone || state == "finished", scrobble: scrobble,
	}, nil)
	return func() tea.Msg {
		return foregroundProviderSyncMsg{err: <-result}
	}
}

func foregroundProviderFavorite(item MediaItem, favorite bool) tea.Cmd {
	if item.Kind != MediaProvider {
		return nil
	}
	return func() tea.Msg { return foregroundProviderSyncMsg{err: setProviderFavorite(item, favorite)} }
}

func (m *foregroundMediaModel) loadLyrics() tea.Cmd {
	item := m.current()
	m.lyricsLoading, m.lyricsLines, m.lyricsOffset = true, nil, 0
	if strings.TrimSpace(item.EmbeddedLyrics) != "" {
		return func() tea.Msg {
			return foregroundLyricsMsg{raw: item.display(), result: lyrics.Result{Track: item.Title, Artist: item.Artist, Album: item.Album, Plain: item.EmbeddedLyrics}}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		result, err := lyrics.NewClient().Get(ctx, item.Artist, item.Title)
		return foregroundLyricsMsg{raw: item.display(), result: result, err: err}
	}
}

func (m *foregroundMediaModel) mediaState() media.State {
	status := media.StatusStopped
	if m.state == "playing" {
		status = media.StatusPlaying
	} else if m.paused {
		status = media.StatusPaused
	}
	item := m.current()
	audio := m.settings.Audio.status(m.settings.Audio.Device)
	state := media.State{Status: status, Volume: float64(m.settings.Volume) / 100,
		AudioDevice: audio.ActiveDevice, AudioFormat: audio.Format,
		Track: media.Track{Title: item.Title, Artist: item.Artist, Album: item.Album, Genre: item.Genre, URL: item.Source, ArtURL: item.Artwork,
			Duration: time.Duration(item.Duration * float64(time.Second))},
		CanGoNext: len(m.items) > 1 || m.repeat != "off", CanGoPrevious: m.index > 0 || m.repeat == "all", Seekable: item.finite()}
	if m.muted {
		state.Volume = 0
	}
	if m.player != nil {
		state.Position = m.player.position()
	}
	return state
}

func (m *foregroundMediaModel) updateMedia() {
	if m.media != nil {
		m.media.Update(m.mediaState())
	}
}

// Init waits for the first foreground decoder event and starts media updates.
func (m *foregroundMediaModel) Init() tea.Cmd {
	return tea.Batch(waitForegroundPlayer(m.player, m.generation), foregroundMediaTick(effectiveInterfaceSettings(m.presentation).LowPower))
}

// Update applies terminal and native media controls to the foreground queue.
func (m *foregroundMediaModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case foregroundPlayerMsg:
		if msg.generation != m.generation || !msg.open {
			return m, nil
		}
		if msg.event.err != "" {
			m.state, m.err = "failed", msg.event.err
			return m, nil
		}
		if msg.event.ended {
			return m, m.next()
		}
		if msg.event.loaded {
			m.state = "playing"
			if m.paused {
				m.state = "paused"
			}
			if item := m.current(); item.Kind == MediaProvider && item.Artwork == "" {
				if artwork := m.player.artwork(); artwork != "" {
					m.items[m.index].Artwork = artwork
				}
			}
			if m.settings.Notifications && m.notifiedID != m.current().ID {
				item := m.current()
				m.notifiedID = item.ID
				body := item.Artist
				if item.Album != "" {
					if body != "" {
						body += " · "
					}
					body += item.Album
				}
				go func() { _ = notify.Show(item.Title, body, item.Artwork) }()
			}
		}
		m.updateMedia()
		return m, tea.Batch(waitForegroundPlayer(m.player, m.generation), m.syncProviderProgress("started"))
	case foregroundMediaTickMsg:
		var providerSync tea.Cmd
		if m.state == "playing" && time.Since(m.lastProgress) >= 15*time.Second {
			m.saveProgress(false)
			providerSync = m.syncProviderProgress("playing")
			m.lastProgress = time.Now()
		}
		m.updateMedia()
		return m, tea.Batch(foregroundMediaTick(effectiveInterfaceSettings(m.presentation).LowPower), providerSync)
	case foregroundProviderSyncMsg:
		if msg.err != nil {
			m.err = "provider sync: " + msg.err.Error()
		} else if strings.HasPrefix(m.err, "provider sync:") {
			m.err = ""
		}
	case foregroundLyricsMsg:
		m.lyricsLoading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err, m.lyricsLines = "", msg.result.Lines()
		m.lyricsHeading = msg.result.Track
		if msg.result.Artist != "" {
			m.lyricsHeading = msg.result.Artist + " — " + msg.result.Track
		}
		if msg.result.Instrumental {
			m.lyricsLines = []string{"Instrumental"}
		}
	case media.Command:
		if m.player == nil {
			if msg.Kind == media.Stop {
				return m, tea.Quit
			}
			m.err = "playback is unavailable"
			return m, nil
		}
		var providerSync tea.Cmd
		switch msg.Kind {
		case media.Toggle:
			m.paused = !m.paused
			_ = m.player.command("set_property", "pause", m.paused)
			providerSync = m.syncProviderProgress(foregroundPlaybackState(m.paused))
		case media.Play:
			m.paused = false
			_ = m.player.command("set_property", "pause", false)
			providerSync = m.syncProviderProgress("playing")
		case media.Pause:
			m.paused = true
			_ = m.player.command("set_property", "pause", true)
			providerSync = m.syncProviderProgress("paused")
		case media.Stop:
			return m, tea.Quit
		case media.Next:
			return m, m.next()
		case media.Previous:
			return m, m.previous()
		case media.Seek:
			return m, m.start(m.player.position() + msg.Position)
		case media.SetPosition:
			return m, m.start(msg.Position)
		case media.SetVolume:
			m.setVolume(int(msg.Volume*100 + 0.5))
		}
		m.updateMedia()
		return m, providerSync
	case tea.KeyPressMsg:
		presentation := effectiveInterfaceSettings(m.presentation)
		if m.lyricsOpen {
			room := max(1, m.height-6)
			switch presentation.mapKey("foreground-lyrics", msg.String()) {
			case "q", "ctrl+c":
				m.saveProgress(false)
				return m, tea.Quit
			case "y", "esc":
				m.lyricsOpen = false
			case "up", "k":
				m.lyricsOffset = max(0, m.lyricsOffset-1)
			case "down", "j":
				m.lyricsOffset = min(max(0, len(m.lyricsLines)-room), m.lyricsOffset+1)
			case "pgup":
				m.lyricsOffset = max(0, m.lyricsOffset-room)
			case "pgdown", "space":
				m.lyricsOffset = min(max(0, len(m.lyricsLines)-room), m.lyricsOffset+room)
			case "r":
				return m, m.loadLyrics()
			}
			return m, nil
		}
		key := presentation.mapKey("playback", msg.String())
		if m.player == nil && key != "q" && key != "ctrl+c" {
			m.err = "playback is unavailable"
			return m, nil
		}
		switch key {
		case "q", "ctrl+c":
			m.saveProgress(false)
			return m, tea.Quit
		case "space":
			m.paused = !m.paused
			if err := m.player.command("set_property", "pause", m.paused); err != nil {
				m.err = err.Error()
			}
			return m, m.syncProviderProgress(foregroundPlaybackState(m.paused))
		case "m":
			m.muted = !m.muted
			if err := m.player.command("set_property", "mute", m.muted); err != nil {
				m.err = err.Error()
			}
		case "9":
			m.setVolume(m.settings.Volume - 5)
		case "0":
			m.setVolume(m.settings.Volume + 5)
		case "left":
			return m, m.start(m.player.position() - time.Duration(presentation.SeekStep)*time.Second)
		case "right":
			return m, m.start(m.player.position() + time.Duration(presentation.SeekStep)*time.Second)
		case "shift+left":
			return m, m.start(m.player.position() - time.Duration(presentation.SeekLargeStep)*time.Second)
		case "shift+right":
			return m, m.start(m.player.position() + time.Duration(presentation.SeekLargeStep)*time.Second)
		case ">", ".":
			return m, m.next()
		case "<", ",":
			return m, m.previous()
		case "z":
			m.shuffle = !m.shuffle
			m.persistQueue()
			m.preloadNextLocal()
		case "R":
			m.repeat = map[string]string{"off": "all", "all": "one", "one": "off"}[m.repeat]
			m.persistQueue()
			m.preloadNextLocal()
		case "[", "]":
			delta := -0.25
			if key == "]" {
				delta = 0.25
			}
			m.rate = min(3, max(0.5, m.rate+delta))
			if err := m.player.command("set_property", "speed", m.rate); err != nil {
				m.err = err.Error()
			}
		case "y":
			m.lyricsOpen = true
			return m, m.loadLyrics()
		case "f", "B":
			if m.library != nil {
				bookmark, label := key == "B", "favorite"
				if bookmark {
					label = "bookmark"
				}
				item := m.current()
				marked, err := m.library.setMarked(item, bookmark, nil)
				if err != nil {
					m.err = err.Error()
					break
				}
				if err := m.library.commit(); err != nil {
					m.err = err.Error()
				} else {
					m.err = fmt.Sprintf("%s: %t", label, marked)
					if !bookmark {
						return m, foregroundProviderFavorite(item, marked)
					}
				}
			}
		case "h":
			m.eqCursor = max(0, m.eqCursor-1)
		case "l":
			m.eqCursor = min(audio.EqualizerBandCount-1, m.eqCursor+1)
		case "j", "k", "x":
			bands := m.eq.activeBands()
			if key == "j" {
				bands[m.eqCursor]--
			} else if key == "k" {
				bands[m.eqCursor]++
			} else {
				bands[m.eqCursor] = 0
			}
			bands[m.eqCursor] = audio.ClampEqualizerGain(bands[m.eqCursor])
			m.setEQ(equalizerConfig{Preset: customEqualizerPreset, Custom: bands})
		case "e":
			m.setEQ(equalizerCycle(m.eq, 1))
		case "E", "shift+e":
			m.setEQ(equalizerCycle(m.eq, -1))
		}
	}
	return m, nil
}

func (m *foregroundMediaModel) setVolume(volume int) {
	volume = max(0, min(100, volume))
	if err := m.player.command("set_property", "volume", volume); err != nil {
		m.err = err.Error()
		return
	}
	m.settings.Volume = volume
	if err := savePlaybackSettings(m.settings); err != nil {
		m.err = err.Error()
	}
}

func (m *foregroundMediaModel) setEQ(eq equalizerConfig) {
	m.eq = normalizeEqualizerConfig(eq)
	m.settings.setEqualizer(m.eq)
	m.player.setEqualizer(m.eq.activeBands())
	if err := savePlaybackSettings(m.settings); err != nil {
		m.err = err.Error()
	}
}

// View renders the current item, queue position, modes, and controls.
func (m *foregroundMediaModel) View() tea.View {
	var view tea.View
	view.AltScreen = true
	presentation := effectiveInterfaceSettings(m.presentation)
	tier := interfaceLayoutTier(m.width, m.height, false, presentation.Simplified)
	minimal := tier == "minimal" || tier == "too-small"
	if m.width == 0 {
		return presentation.decorateView(view, m.width)
	}
	if m.lyricsOpen {
		lines := []string{foregroundLine("chill · foreground lyrics", m.width, styleHeading), foregroundLine(m.lyricsHeading, m.width, styleSelected), ""}
		room := max(0, m.height-6)
		for i := 0; i < room && m.lyricsOffset+i < len(m.lyricsLines); i++ {
			lines = append(lines, foregroundLine("  "+m.lyricsLines[m.lyricsOffset+i], m.width, styleInput))
		}
		note := ""
		if m.lyricsLoading {
			note = "loading lyrics…"
		} else if m.err != "" {
			note = "error: " + m.err
		}
		lines = append(lines, foregroundLine(note, m.width, styleDim))
		if presentation.ShowHelp {
			hints := []string{presentation.bindingHint("foreground-lyrics.down", "scroll"), presentation.bindingHint("foreground-lyrics.page-down", "page"), presentation.bindingHint("foreground-lyrics.refresh", "refresh"), presentation.bindingHint("foreground-lyrics.close", "back"), presentation.bindingHint("foreground-lyrics.quit", "quit")}
			lines = append(lines, foregroundLine(strings.Join(hints, " · "), m.width, styleDim))
		}
		if !presentation.Simplified {
			for len(lines) < m.height {
				lines = append(lines, "")
			}
		}
		view.SetContent(strings.Join(lines[:min(len(lines), m.height)], "\n"))
		return presentation.decorateView(view, m.width)
	}
	item := m.current()
	position := time.Duration(0)
	if m.player != nil {
		position = m.player.position()
	}
	duration := "?"
	if item.Duration > 0 {
		duration = clock(item.Duration)
	}
	mode := fmt.Sprintf("shuffle %t · repeat %s · %.2fx", m.shuffle, m.repeat, m.rate)
	lines := []string{foregroundLine("chill · foreground queue", m.width, styleHeading), foregroundLine("♪ "+item.display(), m.width, styleSelected)}
	if item.Album != "" {
		lines = append(lines, foregroundLine(item.Album, m.width, styleDim))
	}
	if presentation.ShowStatus {
		shuffleStatus, repeatStatus, mutedStatus := "", "", ""
		if m.shuffle {
			shuffleStatus = "shuffle"
		}
		if m.repeat != "" && m.repeat != "off" {
			repeatStatus = "repeat " + m.repeat
		}
		if m.muted {
			mutedStatus = "muted"
		}
		status := orderedInterfaceStatus(presentation, map[string]string{
			"state": m.state, "position": clock(position.Seconds()) + " / " + duration, "title": item.display(),
			"queue": fmt.Sprintf("%d of %d", m.index+1, len(m.items)), "volume": fmt.Sprintf("vol %d", m.settings.Volume),
			"equalizer": "eq " + m.eq.Preset, "network": m.state, "audio": m.settings.Audio.status(m.settings.Audio.Device).Format,
			"speed": fmt.Sprintf("%.2fx", m.rate), "shuffle": shuffleStatus, "repeat": repeatStatus, "muted": mutedStatus,
		})
		if status != "" {
			lines = append(lines, "", foregroundLine(status, m.width, styleInput))
		}
	}
	if !minimal {
		lines = append(lines, foregroundLine(mode, m.width, styleCommand))
	}
	if m.err != "" {
		lines = append(lines, foregroundLine("error: "+m.err, m.width, styleError))
	}
	readyDownloads := ""
	if m.podcasts != nil {
		ready := 0
		for _, download := range m.podcasts.Downloads {
			if download.State == "ready" {
				ready++
			}
		}
		readyDownloads = fmt.Sprintf("%d ready", ready)
	}
	lines = append(lines, foregroundPanelLines(presentation, m.width, m.height, map[string]string{
		"source": string(item.Kind), "queue": fmt.Sprintf("%d of %d", m.index+1, len(m.items)), "equalizer": m.eq.Preset,
		"audio": m.settings.Audio.status(m.settings.Audio.Device).Format, "downloads": readyDownloads, "network": m.state, "metadata": item.display(),
	})...)
	if presentation.ShowHelp {
		hints := []string{presentation.bindingHint("playback.quit", "quit"), presentation.bindingHint("playback.pause", "pause"), presentation.bindingHint("playback.mute", "mute"), presentation.bindingHint("playback.lyrics", "lyrics"), presentation.bindingHint("playback.favorite", "favorite"), presentation.bindingHint("playback.bookmark", "bookmark"), presentation.bindingPairHint("playback.seek-back", "playback.seek-forward", "seek"), presentation.bindingPairHint("playback.previous", "playback.next", "previous/next")}
		lines = append(lines, "", foregroundLine(strings.Join(hints, " · "), m.width, styleDim))
		if !minimal {
			hints = []string{presentation.bindingPairHint("playback.volume-down", "playback.volume-up", "volume"), presentation.bindingPairHint("playback.speed-down", "playback.speed-up", "speed"), presentation.bindingHint("playback.shuffle", "shuffle"), presentation.bindingHint("playback.repeat", "repeat"), presentation.bindingPairHint("playback.eq-left", "playback.eq-right", "band"), presentation.bindingPairHint("playback.eq-lower", "playback.eq-raise", "gain"), presentation.bindingHint("playback.eq-zero", "zero"), presentation.bindingPairHint("playback.eq-next", "playback.eq-previous", "preset")}
			lines = append(lines, foregroundLine(strings.Join(hints, " · "), m.width, styleDim))
		}
	}
	if !presentation.Simplified {
		for len(lines) < m.height {
			lines = append(lines, "")
		}
	}
	if m.height > 0 {
		lines = lines[:min(len(lines), m.height)]
	}
	view.SetContent(strings.Join(lines, "\n"))
	return presentation.decorateView(view, m.width)
}

func runForegroundMediaItems(items []MediaItem) error {
	if len(items) == 0 {
		return fmt.Errorf("no playable media found")
	}
	if len(items) > 5000 {
		return fmt.Errorf("media session is limited to 5000 items")
	}
	if len(items) == 1 && items[0].Kind == MediaStation {
		return runForeground(items[0].Station)
	}
	if err := checkMediaRequirements(items); err != nil {
		return err
	}
	settings, err := loadPlaybackSettings()
	if err != nil {
		return err
	}
	library, err := loadLibrary()
	if err != nil {
		return err
	}
	if err := migrateRadioFavorites(library); err != nil {
		return err
	}
	podcasts, _ := loadPodcastLibrary()
	for i := range items {
		if items[i].Kind == MediaPodcast && podcasts != nil {
			if download, ok := podcasts.Downloads[items[i].ID]; ok && download.State == "ready" {
				if valid, validationErr := validEpisodeDownload(download); valid {
					items[i].Source = download.Path
				} else {
					reason := "download integrity check failed"
					if validationErr != nil {
						reason = validationErr.Error()
					}
					podcasts.Downloads[items[i].ID] = invalidEpisodeDownload(download, reason)
					_ = podcasts.commit(*podcasts)
				}
			}
		}
	}
	shuffle, repeat := false, "off"
	if library != nil {
		shuffle, repeat = library.Shuffle, library.Repeat
		library.rememberQueue()
	}
	rate := 1.0
	if items[0].Kind == MediaPodcast && podcasts != nil {
		rate = podcasts.Speed
	}
	model := &foregroundMediaModel{items: items, settings: settings, eq: settings.equalizer(), state: "loading", shuffle: shuffle, repeat: repeat, library: library, podcasts: podcasts, rate: rate, played: map[int]bool{0: true}, presentation: currentInterfaceSettings()}
	model.persistQueue()
	model.start(model.resumePosition())
	if model.player == nil {
		return fmt.Errorf("starting playback: %s", model.err)
	}
	program := tea.NewProgram(model, interfaceProgramOptions(model.presentation)...)
	service, mediaErr := media.New(func(command media.Command) { program.Send(command) })
	if mediaErr == nil {
		model.media = service
	} else {
		model.err = mediaErr.Error()
	}
	runErr := media.Run(service, func() error { _, err := program.Run(); return err })
	if service != nil {
		service.Close()
	}
	if model.player != nil {
		player := model.player
		finalizeForegroundPlayback(player, func(position time.Duration) {
			item := model.current()
			model.saveProgressAt(false, position)
			if item.Kind == MediaProvider {
				model.providerSync.submit(providerProgressUpdate{
					item: item, position: position, duration: time.Duration(item.Duration * float64(time.Second)), state: "stopped", completed: model.providerDone,
				}, nil)
			}
		})
		model.player = nil
	}
	model.providerSync.wait()
	if library != nil {
		model.persistQueue()
	}
	return runErr
}

type foregroundPlaybackCloser interface {
	position() time.Duration
	close()
}

func finalizeForegroundPlayback(player foregroundPlaybackCloser, afterClose func(time.Duration)) {
	position := player.position()
	player.close()
	afterClose(position)
}

func saveForegroundQueueModes(shuffle bool, repeat string) error {
	if !shuffle && repeat == "" {
		return nil
	}
	library, err := loadLibrary()
	if err != nil {
		return err
	}
	previous := library.queueFingerprint()
	if shuffle {
		library.Shuffle = true
	}
	if repeat != "" {
		repeat = strings.ToLower(strings.TrimSpace(repeat))
		if repeat != "off" && repeat != "all" && repeat != "one" {
			return fmt.Errorf("repeat must be off, all, or one")
		}
		library.Repeat = repeat
	}
	return library.commitQueue(previous)
}
