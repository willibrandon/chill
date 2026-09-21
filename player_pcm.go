package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/willibrandon/chill/internal/audio"
	"github.com/willibrandon/chill/internal/streammeta"
)

// pcmPlayer owns one resolver, one decoder and one audio output. Only the
// decoder opens the resolved media stream. The bounded tap sees exactly the
// samples delivered to mpv, and never performs analysis on the playback path.
type pcmPlayer struct {
	output     *mpvPlayer
	pipe       *os.File
	buffer     audio.Buffer
	equalizer  *audio.Equalizer
	ctx        context.Context
	cancel     context.CancelFunc
	event      chan playerEvent
	watchDone  chan struct{}
	once       sync.Once
	loaded     bool // commands are serialized by the daemon
	decoderMu  sync.Mutex
	decoders   sync.WaitGroup
	prepared   *preparedDecoder
	active     *preparedDecoder
	offset     time.Duration
	base       time.Duration
	finite     bool
	sampleRate int
	audio      AudioSettings
}

type preparedDecoder struct {
	source   string
	offset   time.Duration
	finite   bool
	ctx      context.Context
	cancel   context.CancelFunc
	activate chan struct{}
	ready    chan struct{}
	once     sync.Once
	mu       sync.Mutex
	started  bool
	complete bool
	err      error
	duration time.Duration
	artwork  string
}

func (decoder *preparedDecoder) start() {
	decoder.once.Do(func() {
		decoder.mu.Lock()
		decoder.started = true
		decoder.mu.Unlock()
		close(decoder.activate)
	})
}

func (decoder *preparedDecoder) finish(err error) (bool, error) {
	decoder.mu.Lock()
	defer decoder.mu.Unlock()
	decoder.complete, decoder.err = true, err
	return decoder.started, err
}

func (decoder *preparedDecoder) failed() bool {
	decoder.mu.Lock()
	defer decoder.mu.Unlock()
	return decoder.complete
}

func startPCMPlayer(volume int, muted, paused bool) (player, error) {
	return newPCMPlayer(volume, muted, paused, 0, false)
}

func newPCMPlayer(volume int, muted, paused bool, offset time.Duration, finite bool) (player, error) {
	settings, err := loadPlaybackSettings()
	if err != nil {
		settings = defaultPlaybackSettings()
	}
	return startPCMPlayerWithAudio(volume, muted, paused, offset, finite, settings.Audio)
}

func startPCMPlayerWithAudio(volume int, muted, paused bool, offset time.Duration, finite bool, settings AudioSettings) (player, error) {
	read, write, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	sampleRate, mpvOptions, _ := audioSettingsArgs(settings)
	output, err := startMPV(volume, muted, paused, read, mpvOptions)
	read.Close()
	if err != nil {
		write.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &pcmPlayer{output: output, pipe: write, ctx: ctx, cancel: cancel,
		offset: offset, finite: finite, sampleRate: sampleRate, audio: normalizeAudioSettings(settings),
		buffer: audio.NewBuffer(sampleRate), equalizer: audio.NewEqualizer(sampleRate),
		event: make(chan playerEvent, 8), watchDone: make(chan struct{})}
	go p.watch()
	return p, nil
}

func (p *pcmPlayer) events() <-chan playerEvent { return p.event }
func (p *pcmPlayer) audioFrame() audio.Frame    { return p.buffer.Snapshot() }
func (p *pcmPlayer) setEqualizer(bands audio.EqualizerBands) {
	p.equalizer.SetBands(bands)
}
func (p *pcmPlayer) position() time.Duration {
	p.decoderMu.Lock()
	offset, base := p.offset, p.base
	p.decoderMu.Unlock()
	return offset + max(time.Duration(0), time.Duration(p.output.positionNS.Load())-base)
}

func (p *pcmPlayer) emitNowPlaying(raw string) {
	now := streammeta.Parse(raw)
	select {
	case p.event <- playerEvent{nowPlaying: &now}:
	case <-p.ctx.Done():
	default:
	}
}

func (p *pcmPlayer) emit(event playerEvent) {
	select {
	case p.event <- event:
	case <-p.ctx.Done():
	}
}

// Raw-input mpv can announce file-loaded before the extractor or decoder has
// produced audio. Playing is only true once both output and PCM are ready.
func (p *pcmPlayer) watch() {
	defer close(p.watchDone)
	emit := func(e playerEvent) {
		select {
		case p.event <- e:
		case <-p.ctx.Done():
		}
	}
	for {
		select {
		case e := <-p.output.events():
			if e.err != "" {
				e.output = true
				emit(e)
				return
			}
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *pcmPlayer) command(args ...any) error {
	if len(args) > 0 && args[0] == "loadfile" {
		if p.loaded || len(args) < 2 {
			return fmt.Errorf("PCM player requires a new instance for each stream")
		}
		source, ok := args[1].(string)
		if !ok {
			return fmt.Errorf("invalid stream URL")
		}
		p.loaded = true
		decoder := p.prepare(source, p.offset, p.finite)
		if err := p.output.command("loadfile", "fd://0", "replace"); err != nil {
			decoder.cancel()
			return err
		}
		p.decoderMu.Lock()
		if p.prepared == decoder {
			p.prepared = nil
		}
		p.active = decoder
		p.decoderMu.Unlock()
		decoder.start()
		return nil
	}
	return p.output.command(args...)
}

func (p *pcmPlayer) close() {
	p.once.Do(func() {
		p.cancel()
		p.pipe.Close() // unblocks a write even when output is paused
		p.output.close()
		<-p.watchDone
		p.decoders.Wait()
		close(p.event)
	})
}

func (p *pcmPlayer) prepare(source string, offset time.Duration, finite bool) *preparedDecoder {
	ctx, cancel := context.WithCancel(p.ctx)
	decoder := &preparedDecoder{source: source, offset: offset, finite: finite, ctx: ctx, cancel: cancel, activate: make(chan struct{}), ready: make(chan struct{})}
	p.decoderMu.Lock()
	if p.prepared != nil {
		p.prepared.cancel()
	}
	p.prepared = decoder
	p.decoderMu.Unlock()
	p.decoders.Add(1)
	go func() {
		defer p.decoders.Done()
		err := p.decode(decoder)
		started, err := decoder.finish(err)
		if decoder.ctx.Err() != nil || p.ctx.Err() != nil {
			return
		}
		if !started {
			return
		} else if err != nil {
			p.emit(playerEvent{err: err.Error()})
		} else {
			p.emit(playerEvent{ended: true})
		}
	}()
	return decoder
}

func (p *pcmPlayer) preload(source string, offset time.Duration, finite bool) {
	p.decoderMu.Lock()
	if p.prepared != nil && p.prepared.source == source && p.prepared.offset == offset && p.prepared.finite == finite {
		p.decoderMu.Unlock()
		return
	}
	p.decoderMu.Unlock()
	p.prepare(source, offset, finite)
}

func (p *pcmPlayer) transition(source string, offset time.Duration, finite bool) bool {
	p.decoderMu.Lock()
	decoder := p.prepared
	if decoder == nil || decoder.source != source || decoder.offset != offset || decoder.finite != finite || decoder.failed() {
		p.decoderMu.Unlock()
		return false
	}
	p.prepared = nil
	if p.active != nil {
		p.base += p.active.duration
	}
	p.active = decoder
	p.offset = offset
	p.decoderMu.Unlock()
	decoder.start()
	return true
}

type resolvedAudio struct {
	// URL is the resolved media address consumed by FFmpeg.
	URL string `json:"url"`
	// Headers contains the extractor's required request headers.
	Headers map[string]string `json:"http_headers"`
	// Artwork is a safely cached provider image for the active item.
	Artwork string `json:"artwork,omitempty"`
}

var (
	findExtractor        = findYtdl
	runDiagnosticCommand = diagnosticCommandContext
)

func resolveAudio(ctx context.Context, source string) (resolvedAudio, error) {
	return resolveAudioWithCookies(ctx, source, "")
}

func resolveAudioWithCookies(ctx context.Context, source, cookiesFrom string) (resolvedAudio, error) {
	if err := ctx.Err(); err != nil {
		return resolvedAudio{}, err
	}
	u, err := url.Parse(source)
	if err != nil {
		// Local filenames can contain percent signs that are not URL escapes.
		if !strings.Contains(source, "://") {
			return resolvedAudio{URL: source}, nil
		}
		return resolvedAudio{}, err
	}
	if u.Scheme == "chill-provider" {
		return resolveProviderAudio(ctx, source)
	}
	// Local media and direct radio URLs do not need extraction. YouTube page
	// URLs do; yt-dlp supplies the signed URL and required HTTP headers together.
	if !sourceNeedsYtdl(source) {
		return resolvedAudio{URL: source}, nil
	}
	mpv, _ := exec.LookPath("mpv")
	extractor := findExtractor(mpv)
	if extractor == "" {
		return resolvedAudio{}, fmt.Errorf("yt-dlp not found")
	}
	args := []string{
		"--ignore-config", "--no-playlist", "--no-progress", "--socket-timeout", "10",
		"--retries", "0", "--format", extractorAudioFormat(source), "--print", `{"url":%(url)j,"http_headers":%(http_headers)j}`,
	}
	if cookiesFrom != "" {
		args = append(args, "--cookies-from-browser", cookiesFrom)
	}
	args = append(args, "--", source)
	stdout, stderr, err := runDiagnosticCommand(ctx, extractor, 35*time.Second, args...)
	if err != nil {
		return resolvedAudio{}, fmt.Errorf("stream resolution: %w; %s", err, stderr)
	}
	var resolved resolvedAudio
	if err := json.Unmarshal([]byte(stdout), &resolved); err != nil {
		return resolvedAudio{}, fmt.Errorf("stream resolution: %w", err)
	}
	if resolved.URL == "" {
		return resolvedAudio{}, fmt.Errorf("extractor returned no audio URL")
	}
	return resolved, nil
}

func extractorAudioFormat(source string) string {
	u, err := url.Parse(source)
	if err == nil {
		host := strings.ToLower(u.Hostname())
		if host == "mixcloud.com" || strings.HasSuffix(host, ".mixcloud.com") {
			return "bestaudio[protocol=https]/bestaudio[protocol=http]/bestaudio[protocol=m3u8_native]/bestaudio[protocol=m3u8]/bestaudio/best"
		}
	}
	return "bestaudio/best"
}

func (p *pcmPlayer) decode(decoder *preparedDecoder) error {
	resolved, err := resolveAudio(decoder.ctx, decoder.source)
	if err != nil {
		return err
	}
	if err := decoder.ctx.Err(); err != nil {
		return err
	}
	decoder.mu.Lock()
	decoder.artwork = resolved.Artwork
	decoder.mu.Unlock()
	logLevel := "error"
	if !decoder.finite {
		logLevel = "info"
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", logLevel}
	remote := strings.HasPrefix(resolved.URL, "https://") || strings.HasPrefix(resolved.URL, "http://")
	input := resolved.URL
	var liveBody io.ReadCloser
	if remote && !p.finite {
		live, openErr := streammeta.Open(decoder.ctx, resolved.URL, resolved.Headers, p.emitNowPlaying)
		if openErr == nil {
			if live.Playlist {
				live.Body.Close()
			} else {
				liveBody, input = live.Body, "pipe:0"
				defer liveBody.Close()
			}
		}
	}
	if remote && liveBody == nil {
		args = append(args, "-rw_timeout", "15000000")
		var headers strings.Builder
		for key, value := range resolved.Headers {
			if !strings.ContainsAny(key+value, "\r\n") {
				fmt.Fprintf(&headers, "%s: %s\r\n", key, value)
			}
		}
		if headers.Len() > 0 {
			args = append(args, "-headers", headers.String())
		}
	}
	// Input pacing and small output buffers keep analysis close to audible
	// playback even for local files, which otherwise decode as fast as possible.
	if !decoder.finite {
		args = append(args, "-re")
	}
	if decoder.offset > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.6f", decoder.offset.Seconds()))
	}
	_, _, filterArgs := audioSettingsArgs(p.audio)
	args = append(args, "-i", input, "-map", "0:a:0", "-vn", "-sn", "-dn")
	args = append(args, filterArgs...)
	args = append(args, "-ac", "2", "-ar", strconv.Itoa(p.sampleRate), "-c:a", "pcm_f32le", "-f", "f32le", "pipe:1")
	cmd := exec.Command("ffmpeg", args...)
	var diagnostics tailBuffer
	cmd.Stderr = newMetadataDiagnostics(&diagnostics, p.emitNowPlaying)
	if liveBody != nil {
		cmd.Stdin = liveBody
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	tree, err := startInTree(cmd)
	if err != nil {
		stdout.Close()
		return fmt.Errorf("starting decoder: %w", err)
	}
	var killed sync.Once
	kill := func() { killed.Do(func() { tree.kill() }) }
	stop := context.AfterFunc(decoder.ctx, kill)
	defer stop()
	defer kill()
	var block [512 * 8]byte // 10.7 ms, always whole stereo sample frames
	var copyErr error
	ready, announced := false, false
	written := int64(0)
	for {
		n, readErr := io.ReadFull(stdout, block[:])
		if n > 0 {
			n -= n % 8
			if !ready && n > 0 {
				close(decoder.ready)
				select {
				case <-decoder.activate:
				case <-decoder.ctx.Done():
					return decoder.ctx.Err()
				}
				ready = true
			}
			p.equalizer.Process(block[:n])
			if _, copyErr = p.pipe.Write(block[:n]); copyErr != nil {
				break
			}
			written += int64(n)
			p.buffer.Push(block[:n])
			if ready && !announced {
				p.emit(playerEvent{loaded: true})
				announced = true
			}
		}
		if readErr != nil {
			if readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
				copyErr = readErr
			}
			break
		}
	}
	if copyErr != nil {
		kill()
	}
	err = cmd.Wait()
	decoder.mu.Lock()
	decoder.duration = time.Duration(float64(written) / (float64(p.sampleRate) * 2 * 4) * float64(time.Second))
	decoder.mu.Unlock()
	if decoder.ctx.Err() != nil {
		return decoder.ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("audio decoder: %w; %s", err, diagnostics.String())
	}
	return copyErr
}

func (p *pcmPlayer) artwork() string {
	p.decoderMu.Lock()
	decoder := p.active
	p.decoderMu.Unlock()
	if decoder == nil {
		return ""
	}
	decoder.mu.Lock()
	defer decoder.mu.Unlock()
	return decoder.artwork
}
