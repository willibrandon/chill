package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/willibrandon/chill/internal/audio"
)

// pcmPlayer owns one resolver, one decoder and one audio output. Only the
// decoder opens the resolved media stream. The bounded tap sees exactly the
// samples delivered to mpv, and never performs analysis on the playback path.
type pcmPlayer struct {
	output    *mpvPlayer
	pipe      *os.File
	buffer    audio.Buffer
	equalizer *audio.Equalizer
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	ready     chan struct{}
	event     chan playerEvent
	watchDone chan struct{}
	once      sync.Once
	loaded    bool // commands are serialized by the daemon
	offset    time.Duration
	finite    bool
}

func startPCMPlayer(volume int, muted, paused bool) (player, error) {
	return newPCMPlayer(volume, muted, paused, 0, false)
}

func startPCMPlayerAt(volume int, muted, paused bool, offset time.Duration) (player, error) {
	return newPCMPlayer(volume, muted, paused, offset, true)
}

func newPCMPlayer(volume int, muted, paused bool, offset time.Duration, finite bool) (player, error) {
	read, write, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	output, err := startMPV(volume, muted, paused, read, []string{
		"--demuxer=rawaudio", "--demuxer-rawaudio-format=floatle",
		"--demuxer-rawaudio-rate=48000", "--demuxer-rawaudio-channels=stereo",
		"--cache=no", "--demuxer-readahead-secs=0", "--demuxer-max-bytes=16384",
		"--audio-buffer=0.1", "--ytdl=no", "--loop-file=no",
	})
	read.Close()
	if err != nil {
		write.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &pcmPlayer{output: output, pipe: write, ctx: ctx, cancel: cancel,
		offset: offset, finite: finite,
		equalizer: audio.NewEqualizer(audio.SampleRate),
		done:      make(chan struct{}), ready: make(chan struct{}), event: make(chan playerEvent, 8), watchDone: make(chan struct{})}
	go p.watch()
	return p, nil
}

func (p *pcmPlayer) events() <-chan playerEvent { return p.event }
func (p *pcmPlayer) audioFrame() audio.Frame    { return p.buffer.Snapshot() }
func (p *pcmPlayer) setEqualizer(bands audio.EqualizerBands) {
	p.equalizer.SetBands(bands)
}
func (p *pcmPlayer) position() time.Duration {
	return p.offset + time.Duration(p.output.positionNS.Load())
}

// Raw-input mpv can announce file-loaded before the extractor or decoder has
// produced audio. Playing is only true once both output and PCM are ready.
func (p *pcmPlayer) watch() {
	defer close(p.watchDone)
	ready := p.ready
	loaded, samples, announced := false, false, false
	emit := func(e playerEvent) {
		select {
		case p.event <- e:
		case <-p.ctx.Done():
		}
	}
	for {
		select {
		case e := <-p.output.events():
			if e.err != "" || e.ended {
				emit(e)
				return
			}
			if e.loaded {
				loaded = true
			}
		case <-ready:
			samples, ready = true, nil
		case <-p.ctx.Done():
			return
		}
		if loaded && samples && !announced {
			announced = true
			emit(playerEvent{loaded: true})
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
		go func() {
			defer close(p.done)
			defer p.pipe.Close()
			if err := p.decode(source); err != nil && p.ctx.Err() == nil {
				p.output.emit(playerEvent{err: err.Error()})
			}
		}()
		return p.output.command("loadfile", "fd://0", "replace")
	}
	return p.output.command(args...)
}

func (p *pcmPlayer) close() {
	p.once.Do(func() {
		p.cancel()
		p.pipe.Close() // unblocks a write even when output is paused
		p.output.close()
		<-p.watchDone
		close(p.event)
		if p.loaded {
			<-p.done
		}
	})
}

type resolvedAudio struct {
	// URL is the resolved media address consumed by FFmpeg.
	URL string `json:"url"`
	// Headers contains the extractor's required request headers.
	Headers map[string]string `json:"http_headers"`
}

func resolveAudio(ctx context.Context, source string) (resolvedAudio, error) {
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
	// Local media and direct radio URLs do not need extraction. YouTube page
	// URLs do; yt-dlp supplies the signed URL and required HTTP headers together.
	host := strings.ToLower(u.Hostname())
	if host != "youtu.be" && host != "youtube.com" && !strings.HasSuffix(host, ".youtube.com") {
		return resolvedAudio{URL: source}, nil
	}
	mpv, _ := exec.LookPath("mpv")
	extractor := findYtdl(mpv)
	if extractor == "" {
		return resolvedAudio{}, fmt.Errorf("yt-dlp not found")
	}
	stdout, stderr, err := diagnosticCommandContext(ctx, extractor, 35*time.Second,
		"--ignore-config", "--no-playlist", "--no-progress", "--socket-timeout", "10",
		"--retries", "0", "--format", "bestaudio/best", "--print",
		`{"url":%(url)j,"http_headers":%(http_headers)j}`, "--", source)
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

func (p *pcmPlayer) decode(source string) error {
	resolved, err := resolveAudio(p.ctx, source)
	if err != nil {
		return err
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if strings.HasPrefix(resolved.URL, "https://") || strings.HasPrefix(resolved.URL, "http://") {
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
	if !p.finite {
		args = append(args, "-re")
	}
	if p.offset > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.6f", p.offset.Seconds()))
	}
	args = append(args, "-i", resolved.URL, "-map", "0:a:0", "-vn", "-sn", "-dn",
		"-ac", "2", "-ar", "48000", "-c:a", "pcm_f32le", "-f", "f32le", "pipe:1")
	cmd := exec.Command("ffmpeg", args...)
	var diagnostics tailBuffer
	cmd.Stderr = &diagnostics
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
	stop := context.AfterFunc(p.ctx, kill)
	defer stop()
	defer kill()
	var block [512 * 8]byte // 10.7 ms, always whole stereo sample frames
	var copyErr error
	ready := false
	for {
		n, readErr := io.ReadFull(stdout, block[:])
		if n > 0 {
			n -= n % 8
			p.equalizer.Process(block[:n])
			if _, copyErr = p.pipe.Write(block[:n]); copyErr != nil {
				break
			}
			p.buffer.Push(block[:n])
			if !ready && n > 0 {
				close(p.ready)
				ready = true
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
	if p.ctx.Err() != nil {
		return p.ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("audio decoder: %w; %s", err, diagnostics.String())
	}
	return copyErr
}
