// Package playback owns native audio output and bounded sample transport.
package playback

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Settings describes the PCM format and requested output routing.
type Settings struct {
	// Device selects a native output identity or auto.
	Device string
	// SampleRate and BufferMS configure PCM rate and bounded buffering.
	SampleRate, BufferMS int
	// Exclusive requests direct device ownership.
	Exclusive bool
}

// DeviceInfo describes an output and its effective configuration.
type DeviceInfo struct {
	// ID, Name, and Backend describe the native output identity.
	ID, Name, Backend string
	// Default and Exclusive describe output routing and access.
	Default, Exclusive bool
	// SampleRate reports the rate negotiated with the device.
	SampleRate int
	// Latency is the device buffering allowed to finish after the final callback.
	Latency time.Duration
}

// Device is owned by one Output. Close waits for outstanding callbacks.
type Device interface {
	// Start begins device callbacks.
	Start() error
	// Close stops callbacks before releasing the device.
	Close()
	// Info reports the effective output configuration.
	Info() DeviceInfo
}

type streamToken struct {
	id   uint64
	done <-chan struct{}
	end  atomic.Uint64
}

type sample struct {
	token       *streamToken
	left, right float32
	position    int64
	track       uint64
}

// Output has one producer and one audio callback consumer. Device changes are
// serialized, and stop the old callback before starting the replacement.
type Output struct {
	createdAt                   time.Time
	settings                    Settings
	device                      Device
	mu                          sync.Mutex
	closed                      bool
	frames                      []sample
	writeToken                  *streamToken
	read, written               atomic.Uint64
	track                       atomic.Uint64
	position                    atomic.Int64
	clockVersion                atomic.Uint64
	paused, muted               atomic.Bool
	volume                      atomic.Uint64
	underruns                   atomic.Uint64
	submitted, nonzero, missing atomic.Uint64
	lastPull                    atomic.Int64
	peak                        atomic.Uint64
	wake                        chan struct{}
	done                        chan struct{}
	// Analysis is a separate SPSC ring. A slow reader drops new analysis
	// samples, never delaying the device or overwriting an unread slot.
	analysis                      []sample
	analysisRead, analysisWritten atomic.Uint64
}

// NewOutput opens a device and creates bounded sample and analysis queues.
func NewOutput(settings Settings, volume int, muted, paused bool) (*Output, error) {
	if settings.SampleRate <= 0 || settings.BufferMS < 1 {
		return nil, errors.New("invalid audio output settings")
	}
	n := max(2048, settings.SampleRate*settings.BufferMS/1000)
	o := &Output{createdAt: time.Now(), settings: settings, frames: make([]sample, n), analysis: make([]sample, 8192), wake: make(chan struct{}, 1), done: make(chan struct{})}
	o.SetVolume(volume)
	o.SetMuted(muted)
	o.SetPaused(paused)
	dev, err := OpenDevice(settings, o.render)
	if err != nil {
		return nil, err
	}
	o.device = dev
	if err := dev.Start(); err != nil {
		dev.Close()
		return nil, err
	}
	return o, nil
}

// SetVolume changes the output gain without changing analysis samples.
func (o *Output) SetVolume(volume int) {
	level := float64(min(100, max(0, volume))) / 100
	o.volume.Store(math.Float64bits(level * level * level))
}

// SetMuted silences output while retaining source progress.
func (o *Output) SetMuted(muted bool) { o.muted.Store(muted) }

// SetPaused controls consumption without discarding queued samples.
func (o *Output) SetPaused(paused bool) { o.paused.Store(paused) }

// Paused reports whether queued samples are being retained.
func (o *Output) Paused() bool { return o.paused.Load() }

// Position returns the consumed track and its source-time cursor.
func (o *Output) Position() (uint64, time.Duration) {
	for {
		version := o.clockVersion.Load()
		if version%2 != 0 {
			continue
		}
		track, position := o.track.Load(), o.position.Load()
		if o.clockVersion.Load() == version {
			return track, time.Duration(position)
		}
	}
}

// Err reports a stopped or unavailable device without running on its callback.
func (o *Output) Err() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	if o.device == nil {
		return errors.New("audio output is unavailable")
	}
	if device, ok := o.device.(interface{ Err() error }); ok {
		if err := device.Err(); err != nil {
			return err
		}
	}
	if !o.paused.Load() && o.written.Load() > o.read.Load() {
		last := o.createdAt
		if at := o.lastPull.Load(); at > 0 {
			last = time.Unix(0, at)
		}
		if time.Since(last) > 5*time.Second {
			return errors.New("audio output stopped consuming samples")
		}
	}
	return nil
}

// Underruns reports callback buffer shortages.
func (o *Output) Underruns() uint64 { return o.underruns.Load() }

// Info returns the effective output configuration.
func (o *Output) Info() DeviceInfo {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.device == nil {
		return DeviceInfo{}
	}
	return o.device.Info()
}

// Write queues interleaved stereo floats, tagged with their source position.
// The caller serializes producers, including preloaded track handoff.
func (o *Output) Write(ctx context.Context, pcm []byte, track uint64, start time.Duration, step float64) error {
	if o.writeToken == nil || o.writeToken.id != track {
		o.writeToken = &streamToken{id: track, done: ctx.Done()}
	}
	token := o.writeToken
	for len(pcm) >= 8 {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-o.done:
			return errors.New("audio output closed")
		default:
		}
		w, r := o.written.Load(), o.read.Load()
		n := min(len(pcm)/8, len(o.frames)-int(w-r))
		if n == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-o.done:
				return errors.New("audio output closed")
			case <-o.wake:
			}
			continue
		}
		for i := 0; i < n; i++ {
			o.frames[(w+uint64(i))%uint64(len(o.frames))] = sample{token: token, left: math.Float32frombits(binary.LittleEndian.Uint32(pcm[i*8:])), right: math.Float32frombits(binary.LittleEndian.Uint32(pcm[i*8+4:])), track: track, position: int64(start) + int64(float64(i+1)*step)}
		}
		token.end.Store(w + uint64(n))
		o.written.Store(w + uint64(n))
		pcm = pcm[n*8:]
		start += time.Duration(float64(n) * step)
	}
	return nil
}

// Written returns the sample boundary used to wait for a track to drain.
func (o *Output) Written() uint64 { return o.written.Load() }

// Drain waits for consumption through a previously captured sample boundary.
func (o *Output) Drain(ctx context.Context, through uint64) error {
	for o.read.Load() < through {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-o.done:
			return errors.New("audio output closed")
		case <-o.wake:
		}
	}
	if latency := o.Info().Latency; latency > 0 {
		timer := time.NewTimer(latency)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		case <-o.done:
			return errors.New("audio output closed")
		}
	}
	return nil
}

func (o *Output) render(dst []byte) {
	clear(dst)
	o.lastPull.Store(time.Now().UnixNano())
	if o.paused.Load() {
		return
	}
	r, w := o.read.Load(), o.written.Load()
	n := 0
	gain := float32(math.Float64frombits(o.volume.Load()))
	if o.muted.Load() {
		gain = 0
	}
	aw, ar := o.analysisWritten.Load(), o.analysisRead.Load()
	var last sample
	var nonzero uint64
	var peak float64
	for r < w && n < len(dst)/8 {
		f := o.frames[r%uint64(len(o.frames))]
		select {
		case <-f.token.done:
			r = max(r+1, min(w, f.token.end.Load()))
			continue
		default:
		}
		left, right := f.left*gain, f.right*gain
		if math.IsNaN(float64(left)) || math.IsInf(float64(left), 0) {
			left = 0
		}
		if math.IsNaN(float64(right)) || math.IsInf(float64(right), 0) {
			right = 0
		}
		if left != 0 || right != 0 {
			nonzero++
		}
		peak = max(peak, math.Abs(float64(left)), math.Abs(float64(right)))
		if gain != 0 {
			binary.LittleEndian.PutUint32(dst[n*8:], math.Float32bits(min(1, max(-1, left))))
			binary.LittleEndian.PutUint32(dst[n*8+4:], math.Float32bits(min(1, max(-1, right))))
		}
		if aw-ar < uint64(len(o.analysis)) {
			o.analysis[aw%uint64(len(o.analysis))] = f
			aw++
		}
		last = f
		n++
		r++
	}
	if n > 0 {
		o.clockVersion.Add(1)
		o.position.Store(last.position)
		o.track.Store(last.track)
		o.clockVersion.Add(1)
	}
	o.submitted.Add(uint64(n))
	o.nonzero.Add(nonzero)
	o.peak.Store(math.Float64bits(peak))
	o.analysisWritten.Store(aw)
	o.read.Store(r)
	if n < len(dst)/8 {
		o.underruns.Add(1)
		o.missing.Add(uint64(len(dst)/8 - n))
	}
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// ReadAnalysis copies consumed samples without involving the device thread in
// spectrum calculation. Only one analysis reader may call this method.
func (o *Output) ReadAnalysis(dst []byte) int {
	r, w := o.analysisRead.Load(), o.analysisWritten.Load()
	n := min(len(dst)/8, int(w-r))
	for i := 0; i < n; i++ {
		f := o.analysis[(r+uint64(i))%uint64(len(o.analysis))]
		binary.LittleEndian.PutUint32(dst[i*8:], math.Float32bits(f.left))
		binary.LittleEndian.PutUint32(dst[i*8+4:], math.Float32bits(f.right))
	}
	o.analysisRead.Store(r + uint64(n))
	return n * 8
}

// SetDevice replaces the output while retaining the sample queue.
func (o *Output) SetDevice(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return errors.New("audio output closed")
	}
	settings := o.settings
	settings.Device = id
	// Prepare while the current device is still available. If exclusive mode
	// prevents this, retain the working output and return the error.
	next, err := OpenDevice(settings, o.render)
	if err != nil {
		return err
	}
	old := o.device
	if old != nil {
		old.Close()
	}
	o.device = nil
	if err = next.Start(); err != nil {
		next.Close()
		restored, restoreErr := OpenDevice(o.settings, o.render)
		if restoreErr == nil {
			restoreErr = restored.Start()
			if restoreErr == nil {
				o.device = restored
			} else {
				restored.Close()
			}
		}
		return errors.Join(err, restoreErr)
	}
	o.device = next
	o.settings = settings
	return nil
}

// Close releases resources and waits for owned workers to stop.
func (o *Output) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.closed = true
	close(o.done)
	if o.device != nil {
		o.device.Close()
		o.device = nil
	}
}

// Statistics describes samples supplied to the output driver. It does not claim
// that the speakers produced sound; the independent OS check measures that.
type Statistics struct {
	// SubmittedFrames counts source frames supplied to the driver.
	SubmittedFrames uint64 `json:"submitted_frames"`
	// NonzeroFrames counts submitted frames with a nonzero output sample.
	NonzeroFrames uint64 `json:"nonzero_frames"`
	// UnderrunFrames counts requested frames unavailable in the source queue.
	UnderrunFrames uint64 `json:"underrun_frames"`
	// QueuedFrames reports source frames waiting for the driver.
	QueuedFrames uint64 `json:"queued_frames"`
	// LastPullMS is the age of the last driver read, or -1 before the first read.
	LastPullMS int64 `json:"last_pull_ms"`
	// Peak is the greatest absolute sample value in the last submitted block.
	Peak float64 `json:"peak"`
}

// Statistics returns the current queue and driver delivery measurements.
func (o *Output) Statistics() Statistics {
	read, written := o.read.Load(), o.written.Load()
	age := int64(-1)
	if last := o.lastPull.Load(); last > 0 {
		age = max(0, time.Since(time.Unix(0, last)).Milliseconds())
	}
	return Statistics{SubmittedFrames: o.submitted.Load(), NonzeroFrames: o.nonzero.Load(), UnderrunFrames: o.missing.Load(), QueuedFrames: written - read, LastPullMS: age, Peak: math.Float64frombits(o.peak.Load())}
}
