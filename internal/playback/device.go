package playback

// #include <stdlib.h>
import "C"

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/gen2brain/malgo"
)

type nativeDevice struct {
	context *malgo.AllocatedContext
	device  *malgo.Device
	info    DeviceInfo
	closing atomic.Bool
	stopped atomic.Bool
}

// Start begins consuming PCM samples.
func (d *nativeDevice) Start() error { return d.device.Start() }

// Info returns the effective output configuration.
func (d *nativeDevice) Info() DeviceInfo { return d.info }

// Err reports unexpected output loss so the owner can recover playback.
func (d *nativeDevice) Err() error {
	if d.stopped.Load() {
		return fmt.Errorf("%s audio output stopped; check the selected device", d.info.Backend)
	}
	return nil
}

// Close releases resources and waits for owned workers to stop.
func (d *nativeDevice) Close() {
	d.closing.Store(true)
	d.device.Uninit()
	d.context.Uninit()
	d.context.Free()
}

type backend struct {
	name string
	kind malgo.Backend
}

func backends() []backend {
	switch runtime.GOOS {
	case "darwin":
		return []backend{{"coreaudio", malgo.BackendCoreaudio}}
	case "windows":
		return []backend{{"wasapi", malgo.BackendWasapi}}
	default:
		return []backend{{"pulse", malgo.BackendPulseaudio}, {"alsa", malgo.BackendAlsa}}
	}
}

// Native IDs retain the OS identity used by existing saved preferences.
func deviceID(b backend, id malgo.DeviceID) string {
	var value string
	if b.name == "wasapi" {
		units := make([]uint16, 0, len(id)/2)
		for i := 0; i+1 < len(id); i += 2 {
			v := binary.LittleEndian.Uint16(id[i:])
			if v == 0 {
				break
			}
			units = append(units, v)
		}
		value = string(utf16.Decode(units))
	} else {
		value = strings.TrimRight(string(id[:]), "\x00")
	}
	return b.name + "/" + value
}

// Devices enumerates native outputs without starting playback.
func Devices(ctx context.Context) ([]DeviceInfo, error) {
	devices := []DeviceInfo{{ID: "auto", Name: "System default", Default: true}}
	var last error
	for _, b := range backends() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, err := malgo.InitContext([]malgo.Backend{b.kind}, malgo.ContextConfig{}, nil)
		if err != nil {
			last = err
			continue
		}
		infos, err := c.Devices(malgo.Playback)
		if err == nil {
			for _, info := range infos {
				devices = append(devices, DeviceInfo{ID: deviceID(b, info.ID), Name: info.Name(), Backend: b.name})
			}
		} else {
			last = err
		}
		c.Uninit()
		c.Free()
	}
	if len(devices) == 1 {
		return nil, fmt.Errorf("no audio output devices available: %v", last)
	}
	return devices, nil
}

// OpenDevice prepares a native device without changing system-wide routing.
func OpenDevice(settings Settings, render func([]byte)) (Device, error) {
	var last error
	for _, b := range backends() {
		if settings.Device != "" && settings.Device != "auto" && !strings.HasPrefix(settings.Device, b.name+"/") {
			continue
		}
		if settings.Exclusive && b.name == "pulse" {
			last = fmt.Errorf("exclusive output is not supported by PulseAudio; select an ALSA device")
			continue
		}
		c, err := malgo.InitContext([]malgo.Backend{b.kind}, malgo.ContextConfig{}, nil)
		if err != nil {
			last = err
			continue
		}
		cfg := malgo.DefaultDeviceConfig(malgo.Playback)
		cfg.Playback.Format = malgo.FormatF32
		cfg.Playback.Channels = 2
		cfg.SampleRate = uint32(settings.SampleRate)
		cfg.PeriodSizeInMilliseconds = uint32(min(50, max(5, settings.BufferMS/2)))
		cfg.Periods = 2
		if settings.Exclusive {
			cfg.Playback.ShareMode = malgo.Exclusive
		}
		info := DeviceInfo{ID: "auto", Backend: b.name, SampleRate: settings.SampleRate, Exclusive: settings.Exclusive}
		var id unsafe.Pointer
		if settings.Device != "" && settings.Device != "auto" {
			infos, listErr := c.Devices(malgo.Playback)
			if listErr == nil {
				for _, candidate := range infos {
					if deviceID(b, candidate.ID) == settings.Device {
						id = candidate.ID.Pointer()
						cfg.Playback.DeviceID = id
						info.ID = settings.Device
						info.Name = candidate.Name()
						break
					}
				}
			}
			if id == nil {
				c.Uninit()
				c.Free()
				last = fmt.Errorf("audio device %q is unavailable", settings.Device)
				continue
			}
		}
		d := &nativeDevice{context: c}
		dev, err := malgo.InitDevice(c.Context, cfg, malgo.DeviceCallbacks{
			Data: func(out, in []byte, frames uint32) { render(out) },
			Stop: func() {
				if !d.closing.Load() {
					d.stopped.Store(true)
				}
			},
		})
		if id != nil {
			C.free(id)
		}
		if err != nil {
			c.Uninit()
			c.Free()
			last = err
			continue
		}
		info.SampleRate = int(dev.PlaybackInternalSampleRate())
		info.Latency = time.Duration(cfg.PeriodSizeInMilliseconds*cfg.Periods) * time.Millisecond
		d.device, d.info = dev, info
		return d, nil
	}
	return nil, fmt.Errorf("open audio output %q: %v", settings.Device, last)
}
