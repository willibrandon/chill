//go:build darwin && cgo

package playback

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
	"github.com/gopxl/beep/v2"
)

// CLIAMP uses Beep streams feeding Oto's AudioQueue backend. Keep Oto's
// process-wide context alive across station changes and check its asynchronous
// errors; opening a context successfully does not establish working output.
var systemSpeaker struct {
	sync.Mutex
	initialized bool
	context     *oto.Context
	rate        int
	latency     time.Duration
	users       int
	err         error
}

type speakerDevice struct {
	player          *oto.Player
	reader          *streamReader
	info            DeviceInfo
	started, closed bool // guarded by systemSpeaker
}

// OpenDevice prepares macOS output without starting sample consumption.
func OpenDevice(settings Settings, render func([]byte)) (Device, error) {
	if settings.Exclusive || settings.Device != "" && settings.Device != "auto" {
		return openNativeDevice(settings, render)
	}
	systemSpeaker.Lock()
	defer systemSpeaker.Unlock()
	if !systemSpeaker.initialized {
		systemSpeaker.initialized = true
		systemSpeaker.rate = DeviceSampleRate()
		if systemSpeaker.rate == 0 {
			systemSpeaker.rate = settings.SampleRate
		}
		systemSpeaker.latency = time.Duration(settings.BufferMS) * time.Millisecond / 2
		var ready chan struct{}
		systemSpeaker.context, ready, systemSpeaker.err = oto.NewContext(&oto.NewContextOptions{
			SampleRate: systemSpeaker.rate, ChannelCount: 2, Format: oto.FormatFloat32LE, BufferSize: systemSpeaker.latency,
		})
		if systemSpeaker.err == nil {
			select {
			case <-ready:
				systemSpeaker.err = systemSpeaker.context.Suspend()
			case <-time.After(10 * time.Second):
				systemSpeaker.err = errors.New("timed out opening macOS audio output")
			}
		}
	}
	if systemSpeaker.err != nil {
		return nil, fmt.Errorf("audio output unavailable: %w", systemSpeaker.err)
	}
	if err := systemSpeaker.context.Err(); err != nil {
		return nil, fmt.Errorf("audio output unavailable: %w", err)
	}
	var source beep.Streamer = &outputStream{render: render}
	if settings.SampleRate != systemSpeaker.rate {
		source = beep.Resample(4, beep.SampleRate(settings.SampleRate), beep.SampleRate(systemSpeaker.rate), source)
	}
	reader := &streamReader{source: source}
	p := systemSpeaker.context.NewPlayer(reader)
	p.SetBufferSize(systemSpeaker.rate * settings.BufferMS / 2000 * 8)
	return &speakerDevice{player: p, reader: reader, info: DeviceInfo{ID: "auto", Name: "System default", Backend: "coreaudio/oto", Default: true, SampleRate: systemSpeaker.rate, Latency: systemSpeaker.latency + time.Duration(settings.BufferMS)*time.Millisecond/2}}, nil
}

// Start begins playback and checks asynchronous driver errors.
func (d *speakerDevice) Start() error {
	systemSpeaker.Lock()
	defer systemSpeaker.Unlock()
	if d.closed {
		return errors.New("audio output is closed")
	}
	if d.started {
		return nil
	}
	if err := systemSpeaker.context.Resume(); err != nil {
		return fmt.Errorf("start audio output: %w", err)
	}
	d.player.Play()
	if err := d.Err(); err != nil {
		d.reader.close()
		d.player.Close()
		return err
	}
	d.started = true
	systemSpeaker.users++
	return nil
}

// Err reports driver initialization and streaming failures.
func (d *speakerDevice) Err() error { return errors.Join(systemSpeaker.context.Err(), d.player.Err()) }

// Info reports the effective device format and buffering.
func (d *speakerDevice) Info() DeviceInfo { return d.info }

// Close stops source reads before releasing the device to its replacement.
func (d *speakerDevice) Close() {
	systemSpeaker.Lock()
	defer systemSpeaker.Unlock()
	if d.closed {
		return
	}
	d.closed = true
	// Oto drops its player lock while calling Reader.Read. Player.Close alone
	// does not wait for that read. Retire our reader first so a replacement
	// device cannot consume the same output queue concurrently.
	d.reader.close()
	d.player.Close()
	if d.started {
		systemSpeaker.users--
		if systemSpeaker.users == 0 {
			systemSpeaker.err = systemSpeaker.context.Suspend()
		}
	}
}

// These adapters keep one frame representation at each boundary: interleaved
// float32 for Chill/Oto and stereo float64 pairs for Beep's resampler.
type outputStream struct {
	render func([]byte)
	data   [4096 * 8]byte
}

// Stream converts queued stereo float32 frames into Beep samples.
func (s *outputStream) Stream(samples [][2]float64) (int, bool) {
	for at := 0; at < len(samples); {
		n := min(len(samples)-at, len(s.data)/8)
		s.render(s.data[:n*8])
		for i := range n {
			samples[at+i][0] = float64(math.Float32frombits(binary.LittleEndian.Uint32(s.data[i*8:])))
			samples[at+i][1] = float64(math.Float32frombits(binary.LittleEndian.Uint32(s.data[i*8+4:])))
		}
		at += n
	}
	return len(samples), true
}

// Err is nil because the output queue reports device failures separately.
func (*outputStream) Err() error { return nil }

type streamReader struct {
	mu      sync.Mutex
	closed  bool
	source  beep.Streamer
	samples [4096][2]float64
}

// Read serializes stream access and rejects all reads after retirement.
func (r *streamReader) Read(dst []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.EOF
	}
	n := min(len(dst)/8, len(r.samples))
	if n == 0 {
		return 0, errors.New("unaligned audio output read")
	}
	n, ok := r.source.Stream(r.samples[:n])
	for i := range n {
		binary.LittleEndian.PutUint32(dst[i*8:], math.Float32bits(float32(r.samples[i][0])))
		binary.LittleEndian.PutUint32(dst[i*8+4:], math.Float32bits(float32(r.samples[i][1])))
	}
	if err := r.source.Err(); err != nil {
		return n * 8, err
	}
	if !ok {
		return n * 8, io.EOF
	}
	return n * 8, nil
}

func (r *streamReader) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
}
