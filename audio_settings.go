package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"github.com/willibrandon/chill/internal/playback"
	"slices"
	"strconv"
	"strings"
)

const (
	audioProfileAutomatic  = "Automatic"
	audioProfileLossless   = "Lossless"
	audioProfileLowLatency = "Low Latency"
	audioProfileStable     = "Stable Streaming"
	audioProfileCustom     = "Custom"
)

var audioProfiles = []string{audioProfileAutomatic, audioProfileLossless, audioProfileLowLatency, audioProfileStable, audioProfileCustom}

const audioCommandHelp = `Usage: chill audio [setting [value]]

Settings:
  profile <name>          Automatic, Lossless, Low Latency, Stable Streaming, or Custom
  device <id|name|auto>   switch output without losing queue position
  sample-rate <hz>        44100, 48000, 88200, 96000, 176400, or 192000
  buffer <milliseconds>   output buffer from 50 through 5000
  resample-quality <1-4>  choose resampling quality
  mono <on|off|toggle>    control mono downmix
  channels <mono|stereo>  select the output layout
  exclusive <on|off>      request exclusive mode where supported
  list                    list output devices`

const deviceCommandHelp = `Usage: chill device [list|set <id|name>|default]

With no command, lists every available output device.`

// AudioSettings contains durable output routing and quality preferences.
type AudioSettings struct {
	Profile         string `json:"profile"`          // Profile selects a named quality configuration.
	Device          string `json:"device,omitempty"` // Device is a native output identifier, or auto.
	SampleRate      int    `json:"sample_rate"`      // SampleRate is decoded PCM Hz.
	BufferMS        int    `json:"buffer_ms"`        // BufferMS is the output buffer in milliseconds.
	ResampleQuality int    `json:"resample_quality"` // ResampleQuality ranges from one through four.
	Mono            bool   `json:"mono"`             // Mono downmixes both channels before effects.
	Channels        string `json:"channels"`         // Channels is stereo or mono.
	Exclusive       bool   `json:"exclusive"`        // Exclusive requests direct device ownership when supported.
}

// AudioStatus describes desired and active audio output state.
type AudioStatus struct {
	// Backend identifies the active native audio backend.
	Backend string `json:"backend,omitempty"`
	// ActiveExclusive reports successful exclusive device access.
	ActiveExclusive bool `json:"active_exclusive"`
	// OutputWarning explains a fallback or output limitation.
	OutputWarning string `json:"output_warning,omitempty"`
	// DeviceSampleRate reports the rate negotiated with the native backend.
	DeviceSampleRate int `json:"device_sample_rate,omitzero"`
	AudioSettings
	ActiveDevice     string `json:"active_device"`      // ActiveDevice is the currently requested output device.
	ActiveSampleRate int    `json:"active_sample_rate"` // ActiveSampleRate is the PCM pipeline rate.
	Format           string `json:"format"`             // Format describes PCM precision and layout.
}

// AudioDevice describes one native output destination.
type AudioDevice struct {
	ID      string `json:"id"`      // ID is the stable native device identifier.
	Name    string `json:"name"`    // Name is the human-readable device label.
	Default bool   `json:"default"` // Default identifies automatic system routing.
	Active  bool   `json:"active"`  // Active identifies the selected preference.
}

func defaultAudioSettings() AudioSettings {
	return AudioSettings{Profile: audioProfileAutomatic, Device: "auto", SampleRate: 48000, BufferMS: 100, ResampleQuality: 3, Channels: "stereo"}
}

func normalizeAudioSettings(settings AudioSettings) AudioSettings {
	settings.Profile = canonicalAudioProfile(settings.Profile)
	if settings.Profile == "" {
		settings.Profile = audioProfileAutomatic
	}
	if strings.TrimSpace(settings.Device) == "" {
		settings.Device = "auto"
	}
	switch settings.Profile {
	case audioProfileAutomatic:
		settings.SampleRate, settings.BufferMS, settings.ResampleQuality = 48000, 100, 3
	case audioProfileLossless:
		settings.SampleRate, settings.BufferMS, settings.ResampleQuality = 96000, 250, 4
	case audioProfileLowLatency:
		settings.SampleRate, settings.BufferMS, settings.ResampleQuality = 48000, 50, 2
	case audioProfileStable:
		settings.SampleRate, settings.BufferMS, settings.ResampleQuality = 48000, 2000, 3
	case audioProfileCustom:
		if !slices.Contains([]int{44100, 48000, 88200, 96000, 176400, 192000}, settings.SampleRate) {
			settings.SampleRate = 48000
		}
		settings.BufferMS = min(5000, max(50, settings.BufferMS))
		settings.ResampleQuality = min(4, max(1, settings.ResampleQuality))
	}
	settings.Channels = strings.ToLower(strings.TrimSpace(settings.Channels))
	if settings.Channels != "mono" && settings.Channels != "stereo" {
		settings.Channels = "stereo"
	}
	settings.Mono = settings.Mono || settings.Channels == "mono"
	if settings.Mono {
		settings.Channels = "mono"
	}
	return settings
}

func canonicalAudioProfile(value string) string {
	normalized := strings.NewReplacer("-", " ", "_", " ").Replace(strings.ToLower(strings.TrimSpace(value)))
	for _, profile := range audioProfiles {
		if strings.ToLower(profile) == normalized {
			return profile
		}
	}
	return ""
}

func (settings AudioSettings) status(activeDevice string) AudioStatus {
	settings = normalizeAudioSettings(settings)
	if activeDevice == "" {
		activeDevice = settings.Device
	}
	format := fmt.Sprintf("f32le/stereo/%dHz", settings.SampleRate)
	if settings.Mono {
		format += "/mono-downmix"
	}
	return AudioStatus{AudioSettings: settings, ActiveDevice: activeDevice, ActiveSampleRate: settings.SampleRate, Format: format}
}

var enumerateAudioDevices = listAudioDevices
var nativeAudioDevices = playback.Devices

func listAudioDevices(ctx context.Context, selected string) ([]AudioDevice, error) {
	infos, err := nativeAudioDevices(ctx)
	if err != nil {
		return nil, err
	}
	devices := make([]AudioDevice, 0, len(infos))
	for _, info := range infos {
		devices = append(devices, AudioDevice{ID: info.ID, Name: info.Name, Default: info.Default, Active: info.ID == selected})
	}
	return devices, nil
}
func activeAudioStatus(settings AudioSettings, selected string, p player) AudioStatus {
	status := settings.status(selected)
	if native, ok := p.(*pcmPlayer); ok && native.output != nil {
		info := native.output.Info()
		status.Backend = info.Backend
		status.ActiveExclusive = info.Exclusive
		status.DeviceSampleRate = info.SampleRate
	}
	if status.ActiveDevice != status.Device {
		status.OutputWarning = fmt.Sprintf("audio device %q unavailable; using %s", status.Device, status.ActiveDevice)
	}
	return status
}

func resolveAudioDevice(ctx context.Context, value, selected string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "default") || strings.EqualFold(value, "auto") {
		return "auto", nil
	}
	devices, err := enumerateAudioDevices(ctx, selected)
	if err != nil {
		return "", err
	}
	var partial []AudioDevice
	for _, device := range devices {
		if strings.EqualFold(device.ID, value) || strings.EqualFold(device.Name, value) {
			return device.ID, nil
		}
		if strings.Contains(strings.ToLower(device.ID), strings.ToLower(value)) || strings.Contains(strings.ToLower(device.Name), strings.ToLower(value)) {
			partial = append(partial, device)
		}
	}
	if len(partial) == 1 {
		return partial[0].ID, nil
	}
	if len(partial) > 1 {
		return "", fmt.Errorf("audio device %q is ambiguous", value)
	}
	return "", fmt.Errorf("audio device %q was not found", value)
}

func formatAudioSettings(settings AudioSettings) string {
	settings = normalizeAudioSettings(settings)
	exclusive := "off"
	if settings.Exclusive {
		exclusive = "on"
	}
	return fmt.Sprintf("audio: %s │ %d Hz │ %d ms │ resample %d │ %s │ device %s │ exclusive %s",
		settings.Profile, settings.SampleRate, settings.BufferMS, settings.ResampleQuality, settings.Channels, settings.Device, exclusive)
}

func runAudioCommand(ctx context.Context, args []string, jsonOutput bool) (string, error) {
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--json" || arg == "-json" {
			jsonOutput = true
			continue
		}
		filtered = append(filtered, arg)
	}
	args = filtered
	if helpRequested(args) {
		return audioCommandHelp, nil
	}
	if len(args) > 0 && args[0] == "list" {
		if len(args) != 1 {
			return "", errors.New("usage: chill audio list [--json]")
		}
		settings, err := currentAudioSettings()
		if err != nil {
			return "", err
		}
		devices, err := listAudioDevices(ctx, settings.Device)
		if err != nil {
			return "", err
		}
		if jsonOutput {
			data, err := json.Marshal(devices)
			return string(data), err
		}
		var lines []string
		for _, device := range devices {
			marker := "  "
			if device.Active {
				marker = "* "
			}
			lines = append(lines, fmt.Sprintf("%s%s  %s", marker, device.ID, device.Name))
		}
		return strings.Join(lines, "\n"), nil
	}
	if len(args) == 0 {
		if jsonOutput {
			status, err := currentAudioStatus()
			if err != nil {
				return "", err
			}
			data, err := json.Marshal(status)
			return string(data), err
		}
		settings, err := currentAudioSettings()
		if err != nil {
			return "", err
		}
		return formatAudioSettings(settings), nil
	}
	command := strings.ToLower(args[0])
	value := strings.Join(args[1:], " ")
	if command == "device" {
		settings, err := currentAudioSettings()
		if err != nil {
			return "", err
		}
		value, err = resolveAudioDevice(ctx, value, settings.Device)
		if err != nil {
			return "", err
		}
	}
	if len(args) < 2 && command != "mono" {
		return "", fmt.Errorf("audio %s needs a value", command)
	}
	if command == "mono" && value == "" {
		value = "toggle"
	}
	argument := strings.TrimSpace(command + " " + value)
	if isDaemonRunning() {
		if err := ensureDaemon(); err != nil {
			return "", err
		}
		output, err := ask("audio " + argument)
		if err != nil || !jsonOutput {
			return output, err
		}
		status, err := fetchStatus()
		if err != nil {
			return "", err
		}
		if status == nil {
			return "", errors.New("daemon stopped before audio settings could be read")
		}
		data, err := json.Marshal(status.Audio)
		return string(data), err
	}
	settings, err := loadPlaybackSettings()
	if err != nil {
		return "", err
	}
	next, err := updateAudioSettings(settings.Audio, argument)
	if err != nil {
		return "", err
	}
	settings.Audio = next
	if err := savePlaybackSettings(settings); err != nil {
		return "", err
	}
	if jsonOutput {
		data, err := json.Marshal(next.status(next.Device))
		return string(data), err
	}
	return formatAudioSettings(next), nil
}

func currentAudioSettings() (AudioSettings, error) {
	if isDaemonRunning() {
		status, err := fetchStatus()
		if err != nil {
			return AudioSettings{}, err
		}
		if status != nil {
			return status.Audio.AudioSettings, nil
		}
	}
	settings, err := loadPlaybackSettings()
	return settings.Audio, err
}

func currentAudioStatus() (AudioStatus, error) {
	if isDaemonRunning() {
		status, err := fetchStatus()
		if err != nil {
			return AudioStatus{}, err
		}
		if status != nil {
			return status.Audio, nil
		}
	}
	settings, err := loadPlaybackSettings()
	if err != nil {
		return AudioStatus{}, err
	}
	return settings.Audio.status(settings.Audio.Device), nil
}

func (d *Daemon) audioCmd(arg string) string {
	parts := strings.Fields(strings.TrimSpace(arg))
	if len(parts) == 0 {
		return ok(formatAudioSettings(d.audio))
	}
	next, err := updateAudioSettings(d.audio, arg)
	if err != nil {
		return fail(err.Error())
	}
	return d.applyAudioSettings(next)
}

func updateAudioSettings(current AudioSettings, arg string) (AudioSettings, error) {
	parts := strings.Fields(strings.TrimSpace(arg))
	if len(parts) == 0 {
		return normalizeAudioSettings(current), nil
	}
	field, value := strings.ToLower(parts[0]), strings.TrimSpace(strings.TrimPrefix(arg, parts[0]))
	next := normalizeAudioSettings(current)
	switch field {
	case "profile":
		next.Profile = canonicalAudioProfile(value)
		if next.Profile == "" {
			return current, errors.New("unknown audio profile; use Automatic, Lossless, Low Latency, Stable Streaming, or Custom")
		}
	case "device":
		if value == "" {
			return current, errors.New("audio device needs an id")
		}
		next.Device = value
	case "sample-rate":
		n, err := strconv.Atoi(value)
		if err != nil || !slices.Contains([]int{44100, 48000, 88200, 96000, 176400, 192000}, n) {
			return current, errors.New("sample rate must be 44100, 48000, 88200, 96000, 176400, or 192000")
		}
		next.Profile, next.SampleRate = audioProfileCustom, n
	case "buffer":
		n, err := strconv.Atoi(value)
		if err != nil || n < 50 || n > 5000 {
			return current, errors.New("audio buffer must be between 50 and 5000 milliseconds")
		}
		next.Profile, next.BufferMS = audioProfileCustom, n
	case "resample-quality":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 4 {
			return current, errors.New("resample quality must be between 1 and 4")
		}
		next.Profile, next.ResampleQuality = audioProfileCustom, n
	case "mono":
		switch strings.ToLower(value) {
		case "on", "true", "1":
			next.Mono, next.Channels = true, "mono"
		case "off", "false", "0":
			next.Mono, next.Channels = false, "stereo"
		case "toggle":
			next.Mono = !next.Mono
			if next.Mono {
				next.Channels = "mono"
			} else {
				next.Channels = "stereo"
			}
		default:
			return current, errors.New("mono must be on, off, or toggle")
		}
	case "channels":
		if value != "stereo" && value != "mono" {
			return current, errors.New("channels must be stereo or mono")
		}
		next.Channels, next.Mono = value, value == "mono"
	case "exclusive":
		switch strings.ToLower(value) {
		case "on", "true", "1":
			next.Exclusive = true
		case "off", "false", "0":
			next.Exclusive = false
		default:
			return current, errors.New("exclusive mode must be on or off")
		}
	default:
		return current, errors.New("unknown audio setting: " + field)
	}
	return normalizeAudioSettings(next), nil
}

func (d *Daemon) applyAudioSettings(next AudioSettings) string {
	previous := d.audio
	previousActive := d.activeAudioDevice
	previousFallback := d.audioDeviceFallback
	if next == previous {
		if d.player == nil && d.audioDeviceFallback {
			d.activeAudioDevice = next.Device
			d.audioDeviceFallback = false
			if strings.HasPrefix(d.storageError, "audio device ") {
				d.storageError = ""
			}
		}
		return ok(formatAudioSettings(next))
	}
	deviceChanged := next.Device != previous.Device
	deviceOnly := deviceChanged
	compare := next
	compare.Device = previous.Device
	deviceOnly = deviceOnly && compare == previous
	if deviceOnly && d.player != nil {
		if err := d.player.setDevice(next.Device); err != nil {
			return fail("audio device unchanged: " + err.Error())
		}
		d.activeAudioDevice = next.Device
		d.audioDeviceFallback = false
		if strings.HasPrefix(d.storageError, "audio device ") {
			d.storageError = ""
		}
	}
	d.audio = next
	if deviceChanged && d.player == nil {
		d.activeAudioDevice = next.Device
		d.audioDeviceFallback = false
		if strings.HasPrefix(d.storageError, "audio device ") {
			d.storageError = ""
		}
	}
	if !deviceOnly && d.player != nil && d.current != nil {
		position := d.episodePosition()
		d.closePlayer()
		if deviceChanged {
			d.activeAudioDevice = next.Device
			d.audioDeviceFallback = false
		}
		if d.current.finite() {
			d.episodeOffset = position
		}
		if err := d.startPlayback(); err != nil {
			d.audio = previous
			d.activeAudioDevice = previousActive
			d.audioDeviceFallback = previousFallback
			_ = d.startPlayback()
			return fail("audio settings unchanged: " + err.Error())
		}
	}
	if err := savePlaybackSettings(d.playbackSettings()); err != nil {
		d.audio = previous
		d.activeAudioDevice = previousActive
		d.audioDeviceFallback = previousFallback
		var restoreErr error
		if d.player != nil {
			if deviceOnly {
				restoreErr = d.player.setDevice(previousActive)
			} else if d.current != nil {
				position := d.episodePosition()
				d.closePlayer()
				if d.current.finite() {
					d.episodeOffset = position
				}
				restoreErr = d.startPlayback()
			}
		}
		if restoreErr != nil {
			return fail("could not save audio settings or restore output: " + errors.Join(err, restoreErr).Error())
		}
		return fail("audio settings unchanged; could not save: " + err.Error())
	}
	return ok(formatAudioSettings(next))
}

func (d *Daemon) refreshAudioDevice(ctx context.Context) {
	d.mu.Lock()
	desired, active, fallback := d.audio.Device, d.activeAudioDevice, d.audioDeviceFallback
	d.mu.Unlock()
	if desired == "" || desired == "auto" {
		return
	}
	devices, err := listAudioDevices(ctx, desired)
	if err != nil {
		return
	}
	available := slices.ContainsFunc(devices, func(device AudioDevice) bool { return device.ID == desired })
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.audio.Device != desired || d.activeAudioDevice != active || d.audioDeviceFallback != fallback {
		return
	}
	if !available && active != "auto" {
		if d.player == nil || d.player.setDevice("auto") == nil {
			d.activeAudioDevice = "auto"
			d.audioDeviceFallback = true
			d.storageError = fmt.Sprintf("audio device %q disconnected; using the system default", desired)
		}
		return
	}
	if available && active == "auto" {
		if d.player == nil || d.player.setDevice(desired) == nil {
			d.activeAudioDevice = desired
			d.audioDeviceFallback = false
			if strings.HasPrefix(d.storageError, "audio device ") {
				d.storageError = ""
			}
		}
	}
}
