package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/willibrandon/chill/internal/audio"
	"github.com/willibrandon/chill/internal/playback"
	"github.com/willibrandon/chill/internal/streammeta"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

var openAudioOutput = func(settings playback.Settings, volume int, muted, paused bool) (*playback.Output, error) {
	return playback.NewOutput(settings, playback.OpenDevice, volume, muted, paused)
}

type pcmPlayer struct {
	output           *playback.Output
	buffer           audio.Buffer
	equalizer        *audio.Equalizer
	ctx              context.Context
	cancel           context.CancelFunc
	event            chan playerEvent
	watchDone        chan struct{}
	once             sync.Once
	loaded           bool
	decoderMu        sync.Mutex
	renderMu         sync.Mutex
	decoders         sync.WaitGroup
	prepared, active *preparedDecoder
	offset           time.Duration
	finite           bool
	sampleRate       int
	audio            AudioSettings
	speed            atomic.Uint64
	nextID           atomic.Uint64
}
type preparedDecoder struct {
	source                    string
	offset                    time.Duration
	finite                    bool
	id                        uint64
	ctx                       context.Context
	cancel                    context.CancelFunc
	activate, ready, selected chan struct{}
	announced                 sync.Once
	once                      sync.Once
	mu                        sync.Mutex
	started, complete         bool
	err                       error
	duration                  time.Duration
	artwork                   string
	title                     string
}

func (d *preparedDecoder) start() {
	d.once.Do(func() { d.mu.Lock(); d.started = true; d.mu.Unlock(); close(d.activate) })
}
func (d *preparedDecoder) failed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.complete && d.err != nil
}
func startPCMPlayer(volume int, muted, paused bool) (player, error) {
	return newPCMPlayer(volume, muted, paused, 0, false)
}
func newPCMPlayer(volume int, muted, paused bool, offset time.Duration, finite bool) (player, error) {
	settings, err := loadPlaybackSettings()
	if err != nil {
		return nil, err
	}
	return startPCMPlayerWithAudio(volume, muted, paused, offset, finite, settings.Audio)
}
func outputSettings(settings AudioSettings) playback.Settings {
	settings = normalizeAudioSettings(settings)
	return playback.Settings{Device: settings.Device, SampleRate: settings.SampleRate, BufferMS: settings.BufferMS, Exclusive: settings.Exclusive}
}
func startPCMPlayerWithAudio(volume int, muted, paused bool, offset time.Duration, finite bool, settings AudioSettings) (player, error) {
	settings = normalizeAudioSettings(settings)
	output, err := openAudioOutput(outputSettings(settings), volume, muted, paused)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &pcmPlayer{output: output, ctx: ctx, cancel: cancel, offset: offset, finite: finite, sampleRate: settings.SampleRate, audio: settings, buffer: audio.NewBuffer(settings.SampleRate), equalizer: audio.NewEqualizer(settings.SampleRate), event: make(chan playerEvent, 16), watchDone: make(chan struct{})}
	p.speed.Store(math.Float64bits(1))
	go p.watch()
	return p, nil
}
func (p *pcmPlayer) events() <-chan playerEvent          { return p.event }
func (p *pcmPlayer) audioFrame() audio.Frame             { return p.buffer.Snapshot() }
func (p *pcmPlayer) setEqualizer(b audio.EqualizerBands) { p.equalizer.SetBands(b) }
func (p *pcmPlayer) setPaused(v bool) error              { p.output.SetPaused(v); return nil }
func (p *pcmPlayer) setMuted(v bool) error               { p.output.SetMuted(v); return nil }
func (p *pcmPlayer) setVolume(v int) error               { p.output.SetVolume(v); return nil }
func (p *pcmPlayer) setSpeed(v float64) error {
	if math.IsNaN(v) || v < 0.5 || v > 3 {
		return fmt.Errorf("speed must be between 0.5 and 3")
	}
	p.speed.Store(math.Float64bits(v))
	return nil
}
func (p *pcmPlayer) setDevice(v string) error { return p.output.SetDevice(v) }
func (p *pcmPlayer) position() time.Duration {
	p.decoderMu.Lock()
	d := p.active
	offset := p.offset
	p.decoderMu.Unlock()
	if p.output == nil {
		return offset
	}
	id, pos := p.output.Position()
	if d != nil && id == d.id {
		return pos
	}
	return offset
}
func (p *pcmPlayer) emit(e playerEvent) {
	select {
	case p.event <- e:
	case <-p.ctx.Done():
	}
}
func (p *pcmPlayer) currentEvent(e playerEvent) bool {
	if e.decoder == 0 {
		return true // Output failures apply to every decoder on this device.
	}
	p.decoderMu.Lock()
	defer p.decoderMu.Unlock()
	return p.active != nil && p.active.id == e.decoder
}

func (p *pcmPlayer) emitDecoder(d *preparedDecoder, e playerEvent) {
	e.decoder = d.id
	select {
	case p.event <- e:
	case <-d.ctx.Done():
	}
}

func (p *pcmPlayer) emitNowPlaying(d *preparedDecoder, raw string) {
	now := streammeta.Parse(raw)
	select {
	case p.event <- playerEvent{decoder: d.id, nowPlaying: &now}:
	case <-d.ctx.Done():
	default:
	}
}
func (p *pcmPlayer) watch() {
	defer close(p.watchDone)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	var block [8192 * 8]byte
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-tick.C:
			if err := p.output.Err(); err != nil {
				p.emit(playerEvent{err: err.Error(), output: true})
				return
			}
			if n := p.output.ReadAnalysis(block[:]); n > 0 {
				p.buffer.Push(block[:n])
			}
		}
	}
}
func (p *pcmPlayer) load(source string) error {
	if p.loaded {
		return fmt.Errorf("player already has a source")
	}
	p.loaded = true
	d := p.prepare(source, p.offset, p.finite)
	p.decoderMu.Lock()
	p.prepared = nil
	p.active = d
	close(d.selected)
	p.decoderMu.Unlock()
	d.start()
	return nil
}
func (p *pcmPlayer) close() {
	p.once.Do(func() { p.cancel(); p.output.Close(); <-p.watchDone; p.decoders.Wait(); close(p.event) })
}
func (p *pcmPlayer) prepare(source string, offset time.Duration, finite bool) *preparedDecoder {
	ctx, cancel := context.WithCancel(p.ctx)
	d := &preparedDecoder{source: source, offset: offset, finite: finite, id: p.nextID.Add(1), ctx: ctx, cancel: cancel, activate: make(chan struct{}), ready: make(chan struct{}), selected: make(chan struct{})}
	p.decoderMu.Lock()
	if p.prepared != nil {
		p.prepared.cancel()
	}
	p.prepared = d
	p.decoderMu.Unlock()
	p.decoders.Go(func() {
		defer cancel()
		err := p.decode(d)
		d.mu.Lock()
		d.complete = true
		d.err = err
		started := d.started
		d.mu.Unlock()
		if ctx.Err() != nil || !started {
			return
		}
		select {
		case <-d.selected:
		case <-ctx.Done():
			return
		}
		if err != nil {
			_, missing := errors.AsType[*requirementsError](err)
			_, invalidConfig := errors.AsType[*toolConfigError](err)
			p.emitDecoder(d, playerEvent{err: err.Error(), permanent: missing || invalidConfig})
		} else {
			p.emitDecoder(d, playerEvent{ended: true})
		}
	})
	return d
}
func (p *pcmPlayer) preload(source string, offset time.Duration, finite bool) {
	p.decoderMu.Lock()
	d := p.prepared
	match := d != nil && d.source == source && d.offset == offset && d.finite == finite
	p.decoderMu.Unlock()
	if !match {
		p.prepare(source, offset, finite)
	}
}

func (p *pcmPlayer) cancelPreload() {
	p.decoderMu.Lock()
	defer p.decoderMu.Unlock()
	if p.prepared != nil {
		p.prepared.cancel()
		p.prepared = nil
	}
}

// The playback controller calls this only after checking its current policy.
func (p *pcmPlayer) startHandoff(active uint64) {
	p.decoderMu.Lock()
	defer p.decoderMu.Unlock()
	if p.active != nil && p.active.id == active && p.prepared != nil && !p.prepared.failed() {
		p.prepared.start()
	}
}

func (p *pcmPlayer) announce(d *preparedDecoder) {
	d.announced.Do(func() {
		p.emitDecoder(d, playerEvent{loaded: true})
		d.mu.Lock()
		title := d.title
		d.mu.Unlock()
		if title != "" {
			p.emitNowPlaying(d, title)
		}
	})
}
func (p *pcmPlayer) transition(source string, offset time.Duration, finite bool) bool {
	p.decoderMu.Lock()
	d := p.prepared
	if d == nil || d.source != source || d.offset != offset || d.finite != finite || d.failed() {
		p.decoderMu.Unlock()
		return false
	}
	// Manual skips may select the preload before the previous decoder reaches
	// EOF. Cancel its writes and queued PCM before activating the next source.
	if p.active != nil {
		p.active.cancel()
	}
	p.prepared = nil
	p.active = d
	p.offset = offset
	close(d.selected)
	p.decoderMu.Unlock()
	d.start()
	select {
	case <-d.ready:
		p.announce(d)
	default:
	}
	return true
}
func (p *pcmPlayer) artwork() string {
	p.decoderMu.Lock()
	d := p.active
	p.decoderMu.Unlock()
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.artwork
}
func (p *pcmPlayer) decode(d *preparedDecoder) error {
	reader, artwork, err := openPlaybackPCM(d.ctx, d.source, d.offset, d.finite, p.audio, func(title string) {
		d.mu.Lock()
		d.title = title
		d.mu.Unlock()
		p.decoderMu.Lock()
		active := p.active == d
		p.decoderMu.Unlock()
		if active && d.ctx.Err() == nil {
			select {
			case <-d.ready:
				p.emitNowPlaying(d, title)
			default:
			}
		}
	})
	if err != nil {
		return err
	}
	defer reader.Close()
	d.mu.Lock()
	d.artwork = artwork
	d.mu.Unlock()
	tempo := playback.NewTempo(reader, p.sampleRate, func() float64 {
		if !d.finite {
			return 1
		}
		return math.Float64frombits(p.speed.Load())
	})
	var block [512 * 8]byte
	ready := false
	position := d.offset
	for {
		n, advance, readErr := tempo.Read(block[:])
		if n > 0 {
			if !ready {
				close(d.ready)
				select {
				case <-d.activate:
				case <-d.ctx.Done():
					return d.ctx.Err()
				}
				ready = true
				select {
				case <-d.selected:
					p.announce(d)
				default:
				}
			}
			if p.audio.Mono {
				for i := 0; i+8 <= n; i += 8 {
					a := math.Float32frombits(binary.LittleEndian.Uint32(block[i:]))
					b := math.Float32frombits(binary.LittleEndian.Uint32(block[i+4:]))
					v := math.Float32bits((a + b) / 2)
					binary.LittleEndian.PutUint32(block[i:], v)
					binary.LittleEndian.PutUint32(block[i+4:], v)
				}
			}
			p.renderMu.Lock()
			p.equalizer.Process(block[:n])
			step := advance / float64(n/8) * float64(time.Second) / float64(p.sampleRate)
			err := p.output.Write(d.ctx, block[:n], d.id, position, step)
			p.renderMu.Unlock()
			if err != nil {
				return err
			}
			position += time.Duration(float64(n/8) * step)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return readErr
			}
			break
		}
	}
	if !ready {
		return fmt.Errorf("source produced no audio")
	}
	d.mu.Lock()
	d.duration = position - d.offset
	d.mu.Unlock()
	through := p.output.Written()
	// Ask the controller to revalidate the queue before a preload is audible.
	p.decoderMu.Lock()
	handoff := p.active == d && p.prepared != nil
	p.decoderMu.Unlock()
	if handoff {
		p.emitDecoder(d, playerEvent{handoff: d.id})
	}
	return p.output.Drain(d.ctx, through)
}
