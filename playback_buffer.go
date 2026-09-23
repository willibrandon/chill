package main

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"
)

type pcmTitle struct {
	offset uint64
	value  string
}

// bufferedPCM reads network audio independently of the device's small output
// queue. Effects, speed, gain, and the playback clock remain on the consumer.
type bufferedPCM struct {
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	onTitle    func(string)
	mu         sync.Mutex
	changed    chan struct{}
	data       []byte
	read       uint64
	written    uint64
	err        error
	titles     []pcmTitle
	minimum    int
	maxMinimum int
	playing    bool
}

func openPlaybackPCM(ctx context.Context, source string, offset time.Duration, finite bool, settings AudioSettings, onTitle func(string)) (io.ReadCloser, string, error) {
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") && !strings.HasPrefix(source, "chill-provider:") {
		return openPCM(ctx, source, offset, finite, settings, onTitle)
	}
	// Network read-ahead and device latency serve different purposes. Retain a
	// bounded reserve without delaying output controls or changing saved settings.
	capacity, minimum := min(32<<20, settings.SampleRate*8*30), settings.SampleRate*8
	if settings.Profile == audioProfileLowLatency {
		capacity, minimum = settings.SampleRate*8, settings.SampleRate*8/20
	} else if settings.Profile == audioProfileStable {
		minimum *= 2
	}
	return newBufferedPCM(ctx, capacity, minimum, onTitle, func(ctx context.Context, title func(string)) (io.ReadCloser, string, error) {
		return openPCM(ctx, source, offset, finite, settings, title)
	})
}

func newBufferedPCM(ctx context.Context, capacity, minimum int, onTitle func(string), open func(context.Context, func(string)) (io.ReadCloser, string, error)) (*bufferedPCM, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	r := &bufferedPCM{ctx: ctx, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{}), data: make([]byte, capacity), minimum: min(capacity, minimum), maxMinimum: min(capacity, minimum*5), onTitle: onTitle}
	source, artwork, err := open(ctx, r.recordTitle)
	if err != nil {
		cancel()
		return nil, "", err
	}
	go r.fill(source)
	return r, artwork, nil
}

func (r *bufferedPCM) recordTitle(title string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.titles); n > 0 && r.titles[n-1].offset == r.written {
		r.titles[n-1].value = title
	} else {
		r.titles = append(r.titles, pcmTitle{r.written, title})
	}
}

func (r *bufferedPCM) notify() {
	close(r.changed)
	r.changed = make(chan struct{})
}

func (r *bufferedPCM) fill(source io.ReadCloser) {
	defer close(r.done)
	defer source.Close()
	var block [16 * 1024]byte
	for {
		r.mu.Lock()
		space := len(r.data) - int(r.written-r.read)
		changed := r.changed
		r.mu.Unlock()
		if r.ctx.Err() != nil {
			return
		}
		if space == 0 {
			select {
			case <-changed:
			case <-r.ctx.Done():
				return
			}
			continue
		}
		n, err := source.Read(block[:min(space, len(block))])
		r.mu.Lock()
		at := int(r.written % uint64(len(r.data)))
		first := copy(r.data[at:], block[:n])
		copy(r.data, block[first:n])
		r.written += uint64(n)
		r.err = err
		r.notify()
		r.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// Read delivers prefetched PCM and titles at their consumption boundary. A
// depleted stream refills before resuming, instead of playing each tiny arrival.
func (r *bufferedPCM) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		r.mu.Lock()
		available := int(r.written - r.read)
		if !r.playing && (available >= r.minimum || r.err != nil) {
			r.playing = true
		}
		if r.playing && available > 0 {
			var title string
			hasTitle := len(r.titles) > 0 && r.titles[0].offset <= r.read
			if hasTitle {
				title = r.titles[0].value
				r.titles = r.titles[1:]
			}
			n := min(len(dst), available)
			if len(r.titles) > 0 {
				n = min(n, int(r.titles[0].offset-r.read))
			}
			at := int(r.read % uint64(len(r.data)))
			first := copy(dst[:n], r.data[at:min(len(r.data), at+n)])
			copy(dst[first:n], r.data[:n-first])
			r.read += uint64(n)
			r.notify()
			r.mu.Unlock()
			if hasTitle && r.onTitle != nil {
				r.onTitle(title)
			}
			return n, nil
		}
		if available == 0 && r.err != nil {
			err := r.err
			r.mu.Unlock()
			return 0, err
		}
		if r.playing && available == 0 {
			r.playing = false
			r.minimum = min(r.maxMinimum, r.minimum*2)
		}
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	}
}

// Close cancels network reads and waits before releasing decoder state.
func (r *bufferedPCM) Close() error {
	r.cancel()
	<-r.done
	return nil
}
