package main

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/willibrandon/chill/internal/audio"
)

type foregroundPlayerMsg struct {
	generation uint64
	event      playerEvent
	open       bool
}

type foregroundModel struct {
	width, height int
	station       *Station
	player        *pcmPlayer
	settings      playbackSettings
	eq            equalizerConfig
	eqCursor      int
	generation    uint64
	state         string
	err           string
	muted         bool
	paused        bool
	vibe          string
}

func startForegroundPCM(station *Station, settings playbackSettings, muted, paused bool, offset time.Duration) (*pcmPlayer, error) {
	raw, err := newPCMPlayer(settings.Volume, muted, paused, offset, false)
	if err != nil {
		return nil, err
	}
	p := raw.(*pcmPlayer)
	p.setEqualizer(settings.equalizer().activeBands())
	if err := p.command("loadfile", station.URL, "replace"); err != nil {
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
	return waitForegroundPlayer(m.player, m.generation)
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
	if err := m.player.command("set_property", "volume", volume); err != nil {
		m.err = err.Error()
		return
	}
	previous := m.settings.Volume
	m.settings.Volume = volume
	if err := m.saveSettings(); err != nil {
		m.settings.Volume = previous
		_ = m.player.command("set_property", "volume", previous)
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
			return m, nil
		case msg.event.ended:
			m.state = "ended"
			return m, nil
		case msg.event.loaded:
			m.state = "playing"
			if m.paused {
				m.state = "paused"
			}
		}
		return m, waitForegroundPlayer(m.player, m.generation)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "space":
			m.paused = !m.paused
			if err := m.player.command("set_property", "pause", m.paused); err != nil {
				m.paused = !m.paused
				m.err = err.Error()
			} else if m.paused {
				m.state = "paused"
			} else {
				m.state = "playing"
			}
		case "m":
			if err := m.player.command("set_property", "mute", !m.muted); err != nil {
				m.err = err.Error()
			} else {
				m.muted = !m.muted
			}
		case "9":
			m.setVolume(m.settings.Volume - 5)
		case "0":
			m.setVolume(m.settings.Volume + 5)
		case "left":
			return m, m.restartAt(m.player.position() - 5*time.Second)
		case "right":
			return m, m.restartAt(m.player.position() + 5*time.Second)
		case "h":
			m.eqCursor = max(0, m.eqCursor-1)
		case "l":
			m.eqCursor = min(audio.EqualizerBandCount-1, m.eqCursor+1)
		case "k", "j", "x":
			bands := m.eq.activeBands()
			switch msg.String() {
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
	if m.width == 0 {
		return view
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
	muted := ""
	if m.muted {
		muted = " · muted"
	}
	lines := []string{
		foregroundLine("chill · foreground", m.width, styleHeading),
		foregroundLine("♪ "+m.station.Desc, m.width, styleSelected),
		foregroundLine("~ "+m.vibe+" ~", m.width, styleDim),
		"",
		foregroundLine(fmt.Sprintf("%s · %s · vol %d%s", m.state, clock(m.player.position().Seconds()), m.settings.Volume, muted), m.width, styleInput),
		foregroundLine("EQ ["+m.eq.Preset+"]  "+strings.Join(curve, "  ")+" dB", m.width, styleCommand),
	}
	if m.err != "" {
		lines = append(lines, foregroundLine("error: "+m.err, m.width, styleError))
	}
	lines = append(lines, "", foregroundLine("q quit · Space pause · m mute · 9/0 volume · ←/→ seek", m.width, styleDim))
	lines = append(lines, foregroundLine("h/l band · j/k gain · x zero · e/E preset · r flat · c custom", m.width, styleDim))
	if m.height > 0 {
		lines = lines[:min(len(lines), m.height)]
		for len(lines) < m.height {
			lines = append(lines, "")
		}
	}
	view.SetContent(strings.Join(lines, "\n"))
	return view
}

func (m *foregroundModel) close() {
	if m.player != nil {
		m.player.close()
	}
}

func runForeground(station *Station) error {
	if err := checkRequirements(); err != nil {
		return err
	}
	settings, err := loadPlaybackSettings()
	if err != nil {
		return fmt.Errorf("reading playback settings: %w", err)
	}
	p, err := startForegroundPCM(station, settings, false, false, 0)
	if err != nil {
		return err
	}
	model := &foregroundModel{
		station: station, player: p, settings: settings, eq: settings.equalizer(),
		state: "loading", vibe: vibes[randInt(len(vibes))],
	}
	_, runErr := tea.NewProgram(model).Run()
	model.close()
	return runErr
}
