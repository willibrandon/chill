package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/audio"
	"github.com/willibrandon/chill/internal/lyrics"
	"github.com/willibrandon/chill/internal/media"
	"github.com/willibrandon/chill/internal/notify"
	"github.com/willibrandon/chill/internal/streammeta"
	"github.com/willibrandon/chill/internal/tracklog"
)

type foregroundPlayerMsg struct {
	generation uint64
	event      playerEvent
	open       bool
}

type foregroundHistoryMsg struct{ err error }

type foregroundMediaTickMsg struct{}

type foregroundLyricsMsg struct {
	raw    string
	result lyrics.Result
	err    error
}

type foregroundModel struct {
	width, height  int
	station        *Station
	player         *pcmPlayer
	settings       playbackSettings
	eq             equalizerConfig
	eqCursor       int
	generation     uint64
	state          string
	err            string
	muted          bool
	paused         bool
	vibe           string
	nowPlaying     string
	now            *streammeta.NowPlaying
	lyricsOpen     bool
	lyricsLoading  bool
	lyricsLines    []string
	lyricsOffset   int
	lyricsHeading  string
	media          *media.Service
	library        *libraryState
	stationHistory []Station
	stationForward []Station
	presentation   interfaceSettings
}

func startForegroundPCM(station *Station, settings playbackSettings, muted, paused bool, offset time.Duration) (*pcmPlayer, error) {
	raw, err := newPCMPlayer(settings.Volume, muted, paused, offset, false)
	if err != nil {
		return nil, err
	}
	p := raw.(*pcmPlayer)
	p.setEqualizer(settings.equalizer().activeBands())
	if err := p.load(station.URL); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

func waitForegroundPlayer(p *pcmPlayer, generation uint64) tea.Cmd {
	return func() tea.Msg {
		event, open := <-p.events()
		return foregroundPlayerMsg{generation: generation, event: event, open: open}
	}
}

// Init waits for the foreground PCM pipeline to become audible.
func (m *foregroundModel) Init() tea.Cmd {
	return tea.Batch(waitForegroundPlayer(m.player, m.generation), foregroundMediaTick(effectiveInterfaceSettings(m.presentation).LowPower))
}

func foregroundMediaTick(lowPower ...bool) tea.Cmd {
	interval := time.Second
	if len(lowPower) > 0 && lowPower[0] {
		interval = 3 * time.Second
	}
	return tea.Tick(interval, func(time.Time) tea.Msg { return foregroundMediaTickMsg{} })
}

func (m *foregroundModel) mediaState() media.State {
	status := media.StatusStopped
	if m.state == "playing" {
		status = media.StatusPlaying
	} else if m.paused || m.state == "paused" {
		status = media.StatusPaused
	}
	title, artist := m.station.Desc, ""
	if m.now != nil {
		title, artist = m.now.Title, m.now.Artist
		if title == "" {
			title = m.now.Raw
		}
	}
	volume := float64(m.settings.Volume) / 100
	if m.muted {
		volume = 0
	}
	audio := activeAudioStatus(m.settings.Audio, m.settings.Audio.Device, m.player)
	return media.State{
		Status: status, Volume: volume, Position: m.player.position(),
		AudioDevice: audio.ActiveDevice, AudioFormat: audio.Format,
		Track:     media.Track{Title: title, Artist: artist, Album: m.station.Name, Genre: m.station.Tags, URL: m.station.URL, ArtURL: m.station.Artwork},
		CanGoNext: len(stationSnapshot()) > 1 || len(m.stationForward) > 0, CanGoPrevious: len(m.stationHistory) > 0,
	}
}

func (m *foregroundModel) updateMedia() {
	if m.media != nil {
		m.media.Update(m.mediaState())
	}
}

func (m *foregroundModel) switchStation(station Station, remember bool) tea.Cmd {
	if remember && m.station != nil {
		m.stationHistory = append(m.stationHistory, *m.station)
		if len(m.stationHistory) > 100 {
			m.stationHistory = m.stationHistory[len(m.stationHistory)-100:]
		}
		m.stationForward = nil
	}
	m.station = &station
	m.now, m.nowPlaying = nil, ""
	return m.restartAt(0)
}

func (m *foregroundModel) nextStation() tea.Cmd {
	if len(m.stationForward) > 0 {
		next := m.stationForward[len(m.stationForward)-1]
		m.stationForward = m.stationForward[:len(m.stationForward)-1]
		if m.station != nil {
			m.stationHistory = append(m.stationHistory, *m.station)
		}
		return m.switchStation(next, false)
	}
	stations := stationSnapshot()
	if len(stations) == 0 {
		m.err = "no stations"
		return nil
	}
	for attempts := 0; attempts < len(stations)*2; attempts++ {
		next := stations[randInt(len(stations))]
		if m.station == nil || next.URL != m.station.URL || len(stations) == 1 {
			return m.switchStation(next, true)
		}
	}
	return nil
}

func (m *foregroundModel) previousStation() tea.Cmd {
	if len(m.stationHistory) == 0 {
		m.err = "no previous station"
		return nil
	}
	previous := m.stationHistory[len(m.stationHistory)-1]
	m.stationHistory = m.stationHistory[:len(m.stationHistory)-1]
	if m.station != nil {
		m.stationForward = append(m.stationForward, *m.station)
	}
	return m.switchStation(previous, false)
}

func (m *foregroundModel) saveSettings() error {
	m.settings.setEqualizer(m.eq)
	return savePlaybackSettings(m.settings)
}

func (m *foregroundModel) setEQ(next equalizerConfig) {
	next = normalizeEqualizerConfig(next)
	if next == m.eq {
		return
	}
	previous, previousSettings := m.eq, m.settings
	m.eq = next
	if err := m.saveSettings(); err != nil {
		m.eq, m.settings = previous, previousSettings
		m.err = "saving EQ: " + err.Error()
		return
	}
	m.player.setEqualizer(next.activeBands())
	m.err = ""
}

func (m *foregroundModel) setVolume(volume int) {
	volume = max(0, min(100, volume))
	if err := m.player.setVolume(volume); err != nil {
		m.err = err.Error()
		return
	}
	previous := m.settings.Volume
	m.settings.Volume = volume
	if err := m.saveSettings(); err != nil {
		m.settings.Volume = previous
		_ = m.player.setVolume(previous)
		m.err = "saving volume: " + err.Error()
		return
	}
	m.err = ""
}

func (m *foregroundModel) restartAt(offset time.Duration) tea.Cmd {
	offset = max(time.Duration(0), offset)
	m.player.close()
	m.generation++
	p, err := startForegroundPCM(m.station, m.settings, m.muted, m.paused, offset)
	if err != nil {
		m.state, m.err = "failed", err.Error()
		return nil
	}
	m.player, m.state, m.err = p, "loading", ""
	return waitForegroundPlayer(p, m.generation)
}

func (m *foregroundModel) loadLyrics() tea.Cmd {
	if m.now == nil {
		m.err = "no recognized live track"
		return nil
	}
	now := *m.now
	m.lyricsLoading, m.lyricsLines, m.lyricsOffset = true, nil, 0
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		result, err := lyrics.NewClient().Get(ctx, now.Artist, now.Title)
		return foregroundLyricsMsg{raw: now.Raw, result: result, err: err}
	}
}

// Update handles foreground playback and equalizer controls.
func (m *foregroundModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case foregroundPlayerMsg:
		if msg.generation != m.generation || !msg.open {
			return m, nil
		}
		switch {
		case msg.event.err != "":
			m.state, m.err = "failed", msg.event.err
			m.updateMedia()
			return m, nil
		case msg.event.ended:
			m.state = "ended"
			m.updateMedia()
			return m, nil
		case msg.event.loaded:
			m.state = "playing"
			if m.paused {
				m.state = "paused"
			}
		case msg.event.nowPlaying != nil:
			if msg.event.nowPlaying.Raw == "" {
				m.now, m.nowPlaying = nil, ""
				m.updateMedia()
				return m, waitForegroundPlayer(m.player, m.generation)
			}
			m.nowPlaying = msg.event.nowPlaying.Raw
			now := *msg.event.nowPlaying
			m.now = &now
			station := *m.station
			if m.settings.Notifications {
				title, body := now.Title, now.Artist
				if title == "" {
					title = now.Raw
				}
				if body == "" {
					body = station.Name
				} else {
					body += " · " + station.Name
				}
				go func() { _ = notify.Show(title, body, station.Artwork) }()
			}
			commands := []tea.Cmd{func() tea.Msg {
				return foregroundHistoryMsg{tracklog.DefaultStore().Record(tracklog.Entry{
					PlayedAt: time.Now().UTC(), Station: station.Name, StationURL: station.URL,
					Artist: now.Artist, Title: now.Title, Raw: now.Raw, Artwork: station.Artwork,
				})}
			}, waitForegroundPlayer(m.player, m.generation)}
			if m.lyricsOpen {
				commands = append(commands, m.loadLyrics())
			}
			m.updateMedia()
			return m, tea.Batch(commands...)
		}
		m.updateMedia()
		return m, waitForegroundPlayer(m.player, m.generation)
	case foregroundMediaTickMsg:
		m.updateMedia()
		return m, foregroundMediaTick(effectiveInterfaceSettings(m.presentation).LowPower)
	case media.Command:
		switch msg.Kind {
		case media.Toggle:
			if m.paused {
				msg.Kind = media.Play
			} else {
				msg.Kind = media.Pause
			}
			fallthrough
		case media.Play, media.Pause:
			paused := msg.Kind == media.Pause
			if paused != m.paused {
				if err := m.player.setPaused(paused); err != nil {
					m.err = err.Error()
				} else {
					m.paused = paused
					if paused {
						m.state = "paused"
					} else {
						m.state = "playing"
					}
				}
			}
		case media.Stop:
			return m, tea.Quit
		case media.Next:
			return m, m.nextStation()
		case media.Previous:
			return m, m.previousStation()
		case media.SetVolume:
			m.setVolume(int(msg.Volume*100 + 0.5))
		}
		m.updateMedia()
		return m, nil
	case foregroundHistoryMsg:
		if msg.err != nil {
			m.err = "saving track history: " + msg.err.Error()
		}
	case foregroundLyricsMsg:
		if m.now == nil || msg.raw != m.now.Raw {
			return m, nil
		}
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
	case tea.KeyPressMsg:
		presentation := effectiveInterfaceSettings(m.presentation)
		if m.lyricsOpen {
			room := max(1, m.height-6)
			switch presentation.mapKey("foreground-lyrics", msg.String()) {
			case "q", "ctrl+c":
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
		keyName := presentation.mapKey("playback", msg.String())
		switch keyName {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "space":
			m.paused = !m.paused
			if err := m.player.setPaused(m.paused); err != nil {
				m.paused = !m.paused
				m.err = err.Error()
			} else if m.paused {
				m.state = "paused"
			} else {
				m.state = "playing"
			}
		case "m":
			if err := m.player.setMuted(!m.muted); err != nil {
				m.err = err.Error()
			} else {
				m.muted = !m.muted
			}
		case "y":
			m.lyricsOpen = true
			return m, m.loadLyrics()
		case "f", "B":
			if m.library != nil {
				bookmark, label := keyName == "B", "favorite"
				if bookmark {
					label = "bookmark"
				}
				marked, err := m.library.setMarked(itemFromStation(*m.station), bookmark, nil)
				if err != nil {
					m.err = err.Error()
				} else if err := m.library.commit(); err != nil {
					m.err = err.Error()
				} else {
					m.err = fmt.Sprintf("%s: %t", label, marked)
				}
			}
		case "9":
			m.setVolume(m.settings.Volume - 5)
		case "0":
			m.setVolume(m.settings.Volume + 5)
		case "left":
			return m, m.restartAt(m.player.position() - time.Duration(presentation.SeekStep)*time.Second)
		case "right":
			return m, m.restartAt(m.player.position() + time.Duration(presentation.SeekStep)*time.Second)
		case "shift+left":
			return m, m.restartAt(m.player.position() - time.Duration(presentation.SeekLargeStep)*time.Second)
		case "shift+right":
			return m, m.restartAt(m.player.position() + time.Duration(presentation.SeekLargeStep)*time.Second)
		case ">", ".":
			return m, m.nextStation()
		case "<", ",":
			return m, m.previousStation()
		case "h":
			m.eqCursor = max(0, m.eqCursor-1)
		case "l":
			m.eqCursor = min(audio.EqualizerBandCount-1, m.eqCursor+1)
		case "k", "j", "x":
			bands := m.eq.activeBands()
			switch keyName {
			case "k":
				bands[m.eqCursor]++
			case "j":
				bands[m.eqCursor]--
			case "x":
				bands[m.eqCursor] = 0
			}
			bands[m.eqCursor] = audio.ClampEqualizerGain(bands[m.eqCursor])
			m.setEQ(equalizerConfig{Preset: customEqualizerPreset, Custom: bands})
		case "e":
			m.setEQ(equalizerCycle(m.eq, 1))
		case "E", "shift+e":
			m.setEQ(equalizerCycle(m.eq, -1))
		case "r":
			next := m.eq
			next.Preset = equalizerPresets[0].Name
			m.setEQ(next)
		case "c":
			next := m.eq
			next.Preset = customEqualizerPreset
			m.setEQ(next)
		}
	}
	return m, nil
}

func foregroundLine(text string, width int, style lipgloss.Style) string {
	text = ansi.Truncate(text, max(1, width), "…")
	return style.Render(text)
}

// View renders foreground playback and the active equalizer curve.
func (m *foregroundModel) View() tea.View {
	var view tea.View
	view.AltScreen = true
	presentation := effectiveInterfaceSettings(m.presentation)
	tier := interfaceLayoutTier(m.width, m.height, false, presentation.Simplified)
	minimal := tier == "minimal" || tier == "too-small"
	if m.width == 0 {
		return presentation.decorateView(view, m.width)
	}
	if m.lyricsOpen {
		lines := []string{
			foregroundLine("chill · foreground lyrics", m.width, styleHeading),
			foregroundLine(m.lyricsHeading, m.width, styleSelected),
			"",
		}
		room := max(0, m.height-6)
		for i := 0; i < room && m.lyricsOffset+i < len(m.lyricsLines); i++ {
			lines = append(lines, foregroundLine("  "+m.lyricsLines[m.lyricsOffset+i], m.width, styleInput))
		}
		if !presentation.Simplified {
			for len(lines) < max(3, m.height-3) {
				lines = append(lines, "")
			}
		}
		note := "live streams use manual scrolling"
		if m.lyricsLoading {
			note = "loading lyrics…"
		}
		if m.err != "" {
			note = "error: " + m.err
		}
		lines = append(lines, foregroundLine(note, m.width, styleDim))
		if presentation.ShowHelp {
			hints := []string{presentation.bindingHint("foreground-lyrics.down", "scroll"), presentation.bindingHint("foreground-lyrics.page-down", "page"), presentation.bindingHint("foreground-lyrics.refresh", "refresh"), presentation.bindingHint("foreground-lyrics.close", "back"), presentation.bindingHint("foreground-lyrics.quit", "quit")}
			lines = append(lines, foregroundLine(strings.Join(hints, " · "), m.width, styleDim))
		}
		if m.height > 0 {
			lines = lines[:min(len(lines), m.height)]
		}
		view.SetContent(strings.Join(lines, "\n"))
		return presentation.decorateView(view, m.width)
	}
	bands := m.eq.activeBands()
	curve := make([]string, audio.EqualizerBandCount)
	for i, label := range equalizerBandLabels {
		value := fmt.Sprintf("%s:%s", label, formatEqualizerGain(bands[i]))
		if i == m.eqCursor {
			value = "[" + value + "]"
		}
		curve[i] = value
	}
	lines := []string{
		foregroundLine("chill · foreground", m.width, styleHeading),
		foregroundLine("♪ "+m.station.Desc, m.width, styleSelected),
	}
	if !minimal {
		lines = append(lines, foregroundLine("~ "+m.vibe+" ~", m.width, styleDim))
	}
	if presentation.ShowStatus {
		mutedStatus := ""
		if m.muted {
			mutedStatus = "muted"
		}
		title := m.station.Desc
		if m.nowPlaying != "" {
			title = m.nowPlaying
		}
		status := orderedInterfaceStatus(presentation, map[string]string{
			"state": m.state, "position": clock(m.player.position().Seconds()), "title": title,
			"volume": fmt.Sprintf("vol %d", m.settings.Volume), "muted": mutedStatus, "equalizer": "eq " + m.eq.Preset,
			"network": m.state, "audio": activeAudioStatus(m.settings.Audio, m.settings.Audio.Device, m.player).Format,
		})
		if status != "" {
			lines = append(lines, "", foregroundLine(status, m.width, styleInput))
		}
	}
	if !minimal {
		lines = append(lines, foregroundLine("EQ ["+m.eq.Preset+"]  "+strings.Join(curve, "  ")+" dB", m.width, styleCommand))
	}
	if m.nowPlaying != "" {
		lines = append(lines[:2], append([]string{foregroundLine("♫ "+m.nowPlaying, m.width, styleCommand)}, lines[2:]...)...)
	}
	if m.err != "" {
		lines = append(lines, foregroundLine("error: "+m.err, m.width, styleError))
	}
	metadata := m.station.Desc
	if m.nowPlaying != "" {
		metadata = m.nowPlaying
	}
	lines = append(lines, foregroundPanelLines(presentation, m.width, m.height, map[string]string{
		"source": "radio · " + m.station.Name, "queue": "live stream", "equalizer": m.eq.Preset,
		"audio": activeAudioStatus(m.settings.Audio, m.settings.Audio.Device, m.player).Format, "network": m.state, "metadata": metadata,
	})...)
	if presentation.ShowHelp {
		hints := []string{presentation.bindingHint("playback.quit", "quit"), presentation.bindingHint("playback.pause", "pause"), presentation.bindingHint("playback.mute", "mute"), presentation.bindingHint("playback.lyrics", "lyrics"), presentation.bindingHint("playback.favorite", "favorite"), presentation.bindingHint("playback.bookmark", "bookmark"), presentation.bindingPairHint("playback.volume-down", "playback.volume-up", "volume"), presentation.bindingPairHint("playback.seek-back", "playback.seek-forward", "seek"), presentation.bindingPairHint("playback.previous", "playback.next", "previous/next")}
		lines = append(lines, "", foregroundLine(strings.Join(hints, " · "), m.width, styleDim))
		if !minimal {
			hints = []string{presentation.bindingPairHint("playback.eq-left", "playback.eq-right", "band"), presentation.bindingPairHint("playback.eq-lower", "playback.eq-raise", "gain"), presentation.bindingHint("playback.eq-zero", "zero"), presentation.bindingPairHint("playback.eq-next", "playback.eq-previous", "preset"), presentation.bindingHint("playback.eq-flat", "flat"), presentation.bindingHint("playback.eq-custom", "custom")}
			lines = append(lines, foregroundLine(strings.Join(hints, " · "), m.width, styleDim))
		}
	}
	if m.height > 0 {
		lines = lines[:min(len(lines), m.height)]
		if !presentation.Simplified {
			for len(lines) < m.height {
				lines = append(lines, "")
			}
		}
	}
	view.SetContent(strings.Join(lines, "\n"))
	return presentation.decorateView(view, m.width)
}

func (m *foregroundModel) close() {
	if m.player != nil {
		m.player.close()
	}
}

func runForeground(station *Station) error {
	if err := checkMediaRequirements([]MediaItem{itemFromStation(*station)}); err != nil {
		return err
	}
	settings, err := loadPlaybackSettings()
	if err != nil {
		return fmt.Errorf("reading playback settings: %w", err)
	}
	library, err := loadLibrary()
	if err != nil {
		return err
	}
	if err := migrateRadioFavorites(library); err != nil {
		return err
	}
	p, err := startForegroundPCM(station, settings, false, false, 0)
	if err != nil {
		return err
	}
	model := &foregroundModel{
		station: station, player: p, settings: settings, eq: settings.equalizer(),
		state: "loading", vibe: vibes[randInt(len(vibes))], library: library, presentation: currentInterfaceSettings(),
	}
	program := tea.NewProgram(model, interfaceProgramOptions(model.presentation)...)
	mediaService, mediaErr := media.New(func(command media.Command) { program.Send(command) })
	if mediaErr != nil {
		model.err = "media controls: " + mediaErr.Error()
	} else {
		model.media = mediaService
	}
	runErr := media.Run(mediaService, func() error {
		_, err := program.Run()
		return err
	})
	if mediaService != nil {
		mediaService.Close()
	}
	model.close()
	return runErr
}
