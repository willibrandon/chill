package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type audioBrowser struct {
	open, loading bool
	selected      int
	settings      AudioSettings
	devices       []AudioDevice
	note          string
	id            uint64
	cancel        context.CancelFunc
}

type audioBrowserRow struct {
	kind, value, label string
	active             bool
}

type audioResultMsg struct {
	id       uint64
	settings AudioSettings
	devices  []AudioDevice
	note     string
	err      error
}

func (t *tui) openAudio() tea.Cmd {
	browser := &t.audioUI
	browser.open, browser.selected, browser.note = true, 0, ""
	t.closeProviders()
	t.closeLyrics()
	t.closeRadio()
	t.closePodcasts()
	t.closeEqualizer()
	t.closeLibrary()
	t.help = false
	t.viz.focused = false
	return t.loadAudio()
}

func (t *tui) closeAudio() {
	browser := &t.audioUI
	if browser.cancel != nil {
		browser.cancel()
	}
	browser.id++
	browser.open, browser.loading = false, false
}

func (t *tui) loadAudio() tea.Cmd {
	browser := &t.audioUI
	if browser.cancel != nil {
		browser.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	browser.cancel = cancel
	browser.id++
	browser.loading = true
	id := browser.id
	return func() tea.Msg {
		defer cancel()
		settings, err := currentAudioSettings()
		if err != nil {
			return audioResultMsg{id: id, err: err}
		}
		devices, deviceErr := listAudioDevices(ctx, settings.Device)
		if deviceErr != nil {
			devices = []AudioDevice{{ID: "auto", Name: "System default", Default: true, Active: settings.Device == "auto"}}
		}
		return audioResultMsg{id: id, settings: settings, devices: devices, note: errorText(deviceErr)}
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (t *tui) audioResult(message audioResultMsg) tea.Cmd {
	browser := &t.audioUI
	if message.id != browser.id || !browser.open {
		return nil
	}
	browser.loading = false
	if message.err != nil {
		browser.note = message.err.Error()
		return nil
	}
	browser.settings, browser.devices = message.settings, message.devices
	if message.note != "" {
		browser.note = message.note
	}
	browser.selected = min(browser.selected, max(0, len(browser.rows())-1))
	return refreshStatus
}

func (browser *audioBrowser) rows() []audioBrowserRow {
	settings := normalizeAudioSettings(browser.settings)
	rows := make([]audioBrowserRow, 0, len(audioProfiles)+len(browser.devices)+5)
	for _, profile := range audioProfiles[:len(audioProfiles)-1] {
		rows = append(rows, audioBrowserRow{kind: "profile", value: profile, label: "Profile  " + profile, active: settings.Profile == profile})
	}
	for _, device := range browser.devices {
		rows = append(rows, audioBrowserRow{kind: "device", value: device.ID, label: "Device   " + device.Name, active: settings.Device == device.ID})
	}
	rows = append(rows,
		audioBrowserRow{kind: "sample-rate", value: fmt.Sprint(settings.SampleRate), label: fmt.Sprintf("Sample rate       %d Hz", settings.SampleRate)},
		audioBrowserRow{kind: "buffer", value: fmt.Sprint(settings.BufferMS), label: fmt.Sprintf("Buffer            %d ms", settings.BufferMS)},
		audioBrowserRow{kind: "resample-quality", value: fmt.Sprint(settings.ResampleQuality), label: fmt.Sprintf("Resample quality  %d", settings.ResampleQuality)},
		audioBrowserRow{kind: "channels", value: settings.Channels, label: "Channels          " + settings.Channels},
		audioBrowserRow{kind: "exclusive", value: fmt.Sprint(settings.Exclusive), label: fmt.Sprintf("Exclusive mode    %t", settings.Exclusive)},
	)
	return rows
}

func (t *tui) audioKey(message tea.KeyPressMsg) tea.Cmd {
	browser := &t.audioUI
	key := t.presentation.mapKey("audio", message.String())
	if key == "ctrl+q" {
		return tea.Quit
	}
	switch key {
	case "f9", "esc":
		t.closeAudio()
	case "f8":
		t.closeAudio()
		return t.openProviders()
	case "f7":
		t.closeAudio()
		return t.openLibrary()
	case "f6":
		t.closeAudio()
		return t.openLyrics()
	case "f5":
		t.closeAudio()
		return t.openRadio()
	case "f4":
		t.closeAudio()
		t.openEqualizer()
	case "f3":
		t.closeAudio()
		return t.openPodcasts("")
	case "ctrl+c":
		if browser.cancel != nil {
			browser.cancel()
		}
		browser.id++
		browser.loading, browser.note = false, "Cancelled"
	case "up", "k":
		browser.selected = max(0, browser.selected-1)
	case "down", "j":
		browser.selected = min(max(0, len(browser.rows())-1), browser.selected+1)
	case "pgup":
		browser.selected = max(0, browser.selected-max(1, t.height-8))
	case "pgdown":
		browser.selected = min(max(0, len(browser.rows())-1), browser.selected+max(1, t.height-8))
	case "home":
		browser.selected = 0
	case "end":
		browser.selected = max(0, len(browser.rows())-1)
	case "ctrl+r":
		return t.loadAudio()
	case "enter", "left", "right", "space":
		if browser.loading || len(browser.rows()) == 0 {
			return nil
		}
		row := browser.rows()[browser.selected]
		command, value := row.kind, row.value
		direction := 1
		if key == "left" {
			direction = -1
		}
		switch row.kind {
		case "sample-rate":
			value = cycleAudioValue([]int{44100, 48000, 88200, 96000, 176400, 192000}, browser.settings.SampleRate, direction)
		case "buffer":
			value = cycleAudioValue([]int{50, 100, 250, 500, 1000, 2000, 5000}, browser.settings.BufferMS, direction)
		case "resample-quality":
			value = cycleAudioValue([]int{1, 2, 3, 4}, browser.settings.ResampleQuality, direction)
		case "channels":
			if browser.settings.Channels == "mono" {
				value = "stereo"
			} else {
				value = "mono"
			}
		case "exclusive":
			if browser.settings.Exclusive {
				value = "off"
			} else {
				value = "on"
			}
		}
		return t.changeAudio(command, value)
	}
	return nil
}

func cycleAudioValue(values []int, current, direction int) string {
	index := slices.Index(values, current)
	if index < 0 {
		index = 0
	}
	index = (index + direction + len(values)) % len(values)
	return fmt.Sprint(values[index])
}

func (t *tui) changeAudio(command, value string) tea.Cmd {
	browser := &t.audioUI
	browser.id++
	id := browser.id
	browser.loading = true
	return func() tea.Msg {
		out, err := runAudioCommand(context.Background(), []string{command, value}, false)
		if err != nil {
			return audioResultMsg{id: id, err: err}
		}
		settings, err := currentAudioSettings()
		if err != nil {
			return audioResultMsg{id: id, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		devices, _ := listAudioDevices(ctx, settings.Device)
		return audioResultMsg{id: id, settings: settings, devices: devices, note: out}
	}
}

func (t *tui) audioView() tea.View {
	browser := &t.audioUI
	height, width := max(1, t.height), max(1, t.width)
	lines := make([]string, height)
	fit := func(value string) string { return ansi.Truncate(value, width, "") }
	lines[0] = fit(styleHeading.Render("Audio Output"))
	layout := t.contentLayout(height, false, 2)
	rows, room := browser.rows(), layout.room
	first := max(0, min(browser.selected-room/2, len(rows)-room))
	for row := 0; row < room && first+row < len(rows); row++ {
		index := first + row
		entry := rows[index]
		mark := "  "
		if entry.active {
			mark = "● "
		}
		lines[row+2] = renderBrowserRow(mark+entry.label, width, index == browser.selected)
	}
	if layout.note >= 0 {
		note := browser.note
		if browser.loading {
			note = "Applying audio settings…"
		} else if note == "" {
			note = formatAudioSettings(browser.settings)
		}
		lines[layout.note] = fit(styleDim.Render(note))
		if layout.showHelp {
			lines[layout.firstHint] = fit(styleDim.Render(t.presentation.bindingHint("audio.adjust-right", "choose/adjust") + " · " + t.presentation.bindingHint("audio.adjust-left", "previous") + " · " + t.presentation.bindingHint("browser.refresh", "refresh devices")))
			lines[layout.secondHint] = fit(styleDim.Render("Changes preserve the queue and playhead · " + t.presentation.bindingHint("browser.back", "back") + " · " + t.presentation.bindingHint("global.audio", "prompt")))
		}
		if layout.status >= 0 {
			lines[layout.status] = t.statusBar()
		}
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}
