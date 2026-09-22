package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/willibrandon/chill/internal/playback"
	"github.com/willibrandon/chill/internal/streammeta"
)

type pcmReadCloser struct {
	io.Reader
	close func() error
}

// Close releases resources and waits for owned workers to stop.
func (r *pcmReadCloser) Close() error { return r.close() }

// replaySource protects a live response while a native decoder initializes.
// Failed initialization can hand the exact bytes to FFmpeg without opening a
// second live connection. The initialization prefix is strictly bounded.
type replaySource struct {
	source    io.Reader
	prefix    bytes.Buffer
	committed bool
}

// Read fills the caller's buffer and reports source errors.
func (r *replaySource) Read(p []byte) (int, error) {
	if !r.committed {
		remaining := (1 << 20) - r.prefix.Len()
		if remaining == 0 {
			return 0, fmt.Errorf("native decoder exceeded initialization buffer")
		}
		p = p[:min(len(p), remaining)]
	}
	n, err := r.source.Read(p)
	if !r.committed {
		r.prefix.Write(p[:n])
	}
	return n, err
}

// Close releases resources and waits for owned workers to stop.
func (r *replaySource) Close() error { return nil }

func openPCM(ctx context.Context, source string, offset time.Duration, finite bool, settings AudioSettings, onTitle func(string)) (io.ReadCloser, string, error) {
	return openPCMSource(ctx, source, offset, finite, settings, onTitle, true)
}

func openPCMSource(ctx context.Context, source string, offset time.Duration, finite bool, settings AudioSettings, onTitle func(string), allowRange bool) (io.ReadCloser, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if sourceNeedsYtdl(source) && offset == 0 {
		return openWebsitePCM(ctx, source, settings, onTitle)
	}
	resolved, err := resolveAudio(ctx, source)
	if err != nil {
		return nil, "", err
	}
	remote := strings.HasPrefix(resolved.URL, "http://") || strings.HasPrefix(resolved.URL, "https://")
	var body io.ReadCloser
	if remote {
		open := streammeta.Open
		if finite && allowRange {
			open = streammeta.OpenFinite
		}
		opened, err := open(ctx, resolved.URL, resolved.Headers, onTitle)
		if err != nil {
			return nil, "", err
		}
		if opened.Playlist {
			opened.Body.Close()
			r, err := openFFmpegPCM(ctx, resolved, nil, offset, settings, onTitle)
			return r, resolved.Artwork, err
		}
		body = opened.Body
	} else {
		body, err = os.Open(resolved.URL)
		if err != nil {
			return nil, "", err
		}
	}
	stop := context.AfterFunc(ctx, func() { body.Close() })
	cleanup := func() error { stop(); return body.Close() }
	buffered := bufio.NewReaderSize(body, 4096)
	prefix, _ := buffered.Peek(512)
	kind := playback.Format(prefix)
	if i := bytes.Index(prefix, []byte("\x01vorbis")); kind == "vorbis" && i >= 0 && i+11 < len(prefix) && prefix[i+11] > 2 {
		kind = "" // Multichannel sources need FFmpeg's layout-aware downmix.
	}
	var input io.ReadCloser
	var replay *replaySource
	seekable := !remote
	if file, ok := body.(*os.File); ok {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			cleanup()
			return nil, "", err
		}
		input = file
	} else if source, ok := body.(io.ReadSeekCloser); ok && kind != "mp3" && kind != "vorbis" && kind != "" {
		// MP3 and chained Vorbis indexes scan the encoded file when given a seeker.
		// Keep their network paths incremental; WAV and FLAC can seek lazily.
		input = &bufferedSeekSource{reader: buffered, source: source}
		seekable = true
	} else {
		replay = &replaySource{source: buffered}
		input = replay
	}
	if kind != "" {
		decoder, format, decodeErr := playback.DecodeWithMetadata(kind, input, onTitle)
		if decodeErr == nil && format.NumChannels > 2 {
			decoder.Close()
			decodeErr = fmt.Errorf("multichannel audio requires FFmpeg")
		}
		if decodeErr == nil {
			if onTitle != nil && kind != "vorbis" {
				metadata := prefix
				if replay != nil {
					metadata = replay.prefix.Bytes()
				}
				if title := nativeStreamTitle(metadata); title != "" {
					onTitle(title)
				}
			}
			if replay != nil {
				replay.committed = true
				replay.prefix.Reset()
			}
			reader, err := playback.PCM(ctx, decoder, format, settings.SampleRate, settings.ResampleQuality, offset, seekable)
			if err != nil {
				decoder.Close()
				cleanup()
				if remote && seekable && ctx.Err() == nil {
					return openPCMSource(ctx, source, offset, finite, settings, onTitle, false)
				}
				return nil, "", err
			}
			return &pcmReadCloser{Reader: reader, close: func() error { decoder.Close(); return cleanup() }}, resolved.Artwork, nil
		}
		if ctx.Err() != nil {
			cleanup()
			return nil, "", ctx.Err()
		}
		if remote && seekable {
			cleanup()
			return openPCMSource(ctx, source, offset, finite, settings, onTitle, false)
		}
	}
	if !remote || finite {
		cleanup()
		r, err := openFFmpegPCM(ctx, resolved, nil, offset, settings, onTitle)
		return r, resolved.Artwork, err
	}
	input = &pcmReadCloser{Reader: io.MultiReader(bytes.NewReader(replay.prefix.Bytes()), buffered), close: cleanup}
	r, err := openFFmpegPCM(ctx, resolved, input, offset, settings, onTitle)
	if err != nil {
		input.Close()
	}
	return r, resolved.Artwork, err
}

// bufferedSeekSource preserves sniffed bytes and accounts for unread buffering.
// Decoder cleanup leaves the response owned by openPCM, including init failures.
type bufferedSeekSource struct {
	reader *bufio.Reader
	source io.ReadSeekCloser
}

// Read consumes buffered source bytes.
func (r *bufferedSeekSource) Read(dst []byte) (int, error) { return r.reader.Read(dst) }

// Seek resets lookahead only after a validated source seek succeeds.
func (r *bufferedSeekSource) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekCurrent && offset == 0 {
		position, err := r.source.Seek(0, io.SeekCurrent)
		return position - int64(r.reader.Buffered()), err
	}
	if whence == io.SeekCurrent {
		offset -= int64(r.reader.Buffered())
	}
	position, err := r.source.Seek(offset, whence)
	if err == nil {
		r.reader.Reset(r.source)
	}
	return position, err
}

// Close leaves source lifetime with the pipeline owner.
func (r *bufferedSeekSource) Close() error { return nil }

// processPCM reports decoder diagnostics after stdout ends and always reaps its
// process. Cancellation closes stdin as well as killing the complete tree.
type processPCM struct {
	reader io.ReadCloser
	eof    chan struct{}
	finish sync.Once
	done   chan error
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

// Read fills the caller's buffer and reports source errors.
func (p *processPCM) Read(dst []byte) (int, error) {
	n, err := p.reader.Read(dst)
	if err != nil {
		p.finish.Do(func() { close(p.eof) })
		p.once.Do(func() { p.err = <-p.done })
		if p.err != nil {
			return n, p.err
		}
	}
	return n, err
}

// Close releases resources and waits for owned workers to stop.
func (p *processPCM) Close() error {
	p.cancel()
	p.reader.Close()
	p.once.Do(func() { p.err = <-p.done })
	return nil
}

func startPCMProcess(ctx context.Context, cmd *exec.Cmd, input io.ReadCloser, diagnostics *tailBuffer) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	if input != nil {
		cmd.Stdin = input
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.WaitDelay = time.Second
	tree, err := startInTree(cmd)
	if err != nil {
		cancel()
		stdout.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() {
		if input != nil {
			input.Close()
		}
		tree.kill()
	})
	p := &processPCM{reader: stdout, eof: make(chan struct{}), done: make(chan error, 1), cancel: cancel}
	go func() {
		select {
		case <-p.eof:
		case <-ctx.Done():
		}
		err := cmd.Wait()
		stop()
		tree.kill()
		if input != nil {
			input.Close()
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if err != nil {
			err = fmt.Errorf("audio decoder: %w; %s", err, diagnostics.String())
		}
		p.done <- err
	}()
	return p, nil
}

func openFFmpegPCM(ctx context.Context, source resolvedAudio, input io.ReadCloser, offset time.Duration, settings AudioSettings, onTitle func(string)) (io.ReadCloser, error) {
	path, err := toolPath("ffmpeg")
	if err != nil {
		return nil, err
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "info"}
	remote := strings.HasPrefix(source.URL, "http://") || strings.HasPrefix(source.URL, "https://")
	if remote && input == nil {
		args = append(args, "-rw_timeout", "15000000")
		var h strings.Builder
		for k, v := range source.Headers {
			if !strings.ContainsAny(k+v, "\r\n") {
				fmt.Fprintf(&h, "%s: %s\r\n", k, v)
			}
		}
		if h.Len() > 0 {
			args = append(args, "-headers", h.String())
		}
	}
	url := source.URL
	if input != nil {
		url = "pipe:0"
	}
	if offset > 0 && input == nil {
		args = append(args, "-ss", fmt.Sprintf("%.6f", offset.Seconds()))
	}
	args = append(args, "-i", url)
	if offset > 0 && input != nil {
		args = append(args, "-ss", fmt.Sprintf("%.6f", offset.Seconds()))
	}
	args = append(args, "-map", "0:a:0", "-vn", "-sn", "-dn", "-af", fmt.Sprintf("aresample=%d:filter_size=%d", settings.SampleRate, []int{0, 16, 32, 64, 128}[settings.ResampleQuality]), "-ac", "2", "-ar", strconv.Itoa(settings.SampleRate), "-c:a", "pcm_f32le", "-f", "f32le", "pipe:1")
	cmd := exec.Command(path, args...)
	var diagnostics tailBuffer
	cmd.Stderr = newMetadataDiagnostics(&diagnostics, onTitle)
	return startPCMProcess(ctx, cmd, input, &diagnostics)
}

func openWebsitePCM(ctx context.Context, source string, settings AudioSettings, onTitle func(string)) (io.ReadCloser, string, error) {
	ffmpeg, err := toolPath("ffmpeg")
	if err != nil {
		return nil, "", err
	}
	path, err := toolPath("yt-dlp")
	if err != nil {
		return nil, "", err
	}
	args, err := extractorArgs()
	if err != nil {
		return nil, "", err
	}
	format := "bestaudio[protocol=https]/bestaudio[protocol=http]/bestaudio[protocol!=m3u8_native][protocol!=m3u8]/bestaudio/best"
	args = append(args, "--ffmpeg-location", ffmpeg, "--quiet", "--format", format, "--output", "-", "--", source)
	cmd := exec.Command(path, args...)
	var diagnostics tailBuffer
	cmd.Stderr = &diagnostics
	media, err := startPCMProcess(ctx, cmd, nil, &diagnostics)
	if err != nil {
		return nil, "", err
	}
	pcm, err := openFFmpegPCM(ctx, resolvedAudio{}, media, 0, settings, onTitle)
	if err != nil {
		media.Close()
		return nil, "", err
	}
	return &websitePCM{Reader: pcm, media: media}, "", nil
}

type websitePCM struct {
	io.Reader
	media io.ReadCloser
}

// Read fills the caller's buffer and reports source errors.
func (p *websitePCM) Read(dst []byte) (int, error) {
	n, err := p.Reader.Read(dst)
	if err != nil && !errors.Is(err, io.EOF) {
		if process, ok := p.media.(*processPCM); ok {
			process.Close()
			if process.err != nil && !errors.Is(process.err, context.Canceled) {
				return n, process.err
			}
		}
	}
	return n, err
}

// Close releases resources and waits for owned workers to stop.
func (p *websitePCM) Close() error { p.Reader.(io.Closer).Close(); return p.media.Close() }
