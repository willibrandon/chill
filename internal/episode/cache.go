// Package episode buffers a finite media download on disk for seekable playback.
package episode

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxBytes = 1 << 30 // encoded media, never decoded PCM

// Cache exposes a private loopback URL with byte ranges. FFmpeg can request
// container headers or seek without reopening the publisher's URL. Readers
// wait for the background download and never observe a temporary file EOF.
type Cache struct {
	mu              sync.Mutex
	file            *os.File
	ctx             context.Context
	cancel          context.CancelFunc
	changed         chan struct{}
	done            chan struct{}
	server          *http.Server
	url             string
	written, size   int64
	ready, finished bool
	err             error
	once            sync.Once
}

// Open begins a progressive media download. The caller must close the cache.
func Open(raw string) (*Cache, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("episode source must be an HTTP(S) URL")
	}
	file, err := os.CreateTemp("", "chill-episode-*")
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Cache{file: file, ctx: ctx, cancel: cancel, changed: make(chan struct{}), done: make(chan struct{}), size: -1}
	path := "/" + rand.Text()
	c.url = "http://" + ln.Addr().String() + path
	c.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		c.serve(w, r)
	})}
	go c.server.Serve(ln)
	go c.download(raw)
	return c, nil
}

// URL returns the private loopback address used by the decoder.
func (c *Cache) URL() string { return c.url }

// Context is cancelled when the cache closes or its download stalls.
func (c *Cache) Context() context.Context { return c.ctx }

// Done closes when the publisher download finishes or fails.
func (c *Cache) Done() <-chan struct{} { return c.done }

// CompletedPath returns the downloaded file only after successful completion.
func (c *Cache) CompletedPath() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return "", c.err
	}
	if !c.finished {
		return "", fmt.Errorf("episode is still downloading")
	}
	return c.file.Name(), nil
}

func (c *Cache) notify() { close(c.changed); c.changed = make(chan struct{}) }

func (c *Cache) download(raw string) {
	defer close(c.done)
	err := c.fetch(raw)
	c.mu.Lock()
	c.err, c.finished = err, true
	if err == nil {
		c.size = c.written
	}
	c.notify()
	c.mu.Unlock()
}

func (c *Cache) fetch(raw string) error {
	req, err := http.NewRequestWithContext(c.ctx, "GET", raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "chill/1.0")
	req.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 15 * time.Second}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.Scheme != "http" && req.URL.Scheme != "https" || req.URL.User != nil {
			return fmt.Errorf("invalid episode redirect")
		}
		return nil
	}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("episode server returned %s", resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("episode exceeds the 1 GiB download limit")
	}
	c.mu.Lock()
	c.size, c.ready = resp.ContentLength, true
	c.notify()
	c.mu.Unlock()
	buffer := make([]byte, 32<<10)
	for {
		timer := time.AfterFunc(30*time.Second, c.cancel)
		n, readErr := resp.Body.Read(buffer)
		timer.Stop()
		if n > 0 {
			if c.written+int64(n) > maxBytes {
				return fmt.Errorf("episode exceeds the 1 GiB download limit")
			}
			if _, err := c.file.WriteAt(buffer[:n], c.written); err != nil {
				return err
			}
			c.mu.Lock()
			c.written += int64(n)
			c.notify()
			c.mu.Unlock()
		}
		if readErr == io.EOF {
			if resp.ContentLength >= 0 && c.written != resp.ContentLength {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (c *Cache) wait(ctx context.Context, complete bool) (int64, error) {
	for {
		c.mu.Lock()
		ready, finished, size, err, changed := c.ready, c.finished, c.size, c.err, c.changed
		c.mu.Unlock()
		if err != nil {
			return 0, err
		}
		if finished || ready && !complete {
			return size, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-c.ctx.Done():
			return 0, c.ctx.Err()
		}
	}
}

func (c *Cache) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	size, err := c.wait(r.Context(), false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	start, end := int64(0), size-1
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" && size < 0 {
		// Initial streaming requests may use bytes=0-. Later seeks wait until
		// a chunked download has a known size, rather than misreporting EOF.
		if rangeHeader == "bytes=0-" {
			rangeHeader = ""
		} else {
			size, err = c.wait(r.Context(), true)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			end = size - 1
		}
	}
	if rangeHeader != "" {
		first, last, ok := strings.Cut(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		start, err = strconv.ParseInt(first, 10, 64)
		if !strings.HasPrefix(rangeHeader, "bytes=") || !ok || err != nil || start < 0 || start >= size {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if last != "" {
			end, err = strconv.ParseInt(last, 10, 64)
			if err != nil || end < start {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			end = min(end, size-1)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.Header().Set("Accept-Ranges", "bytes")
	}
	if rangeHeader != "" {
		w.WriteHeader(http.StatusPartialContent)
	}
	if r.Method == "HEAD" {
		return
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	reader := &cacheReader{cache: c, ctx: r.Context(), pos: start}
	var source io.Reader = reader
	if size >= 0 {
		source = io.LimitReader(reader, end-start+1)
	}
	if _, err := io.Copy(w, source); err != nil {
		panic(http.ErrAbortHandler)
	}
}

type cacheReader struct {
	cache *Cache
	ctx   context.Context
	pos   int64
}

// Read waits for downloaded bytes instead of exposing a temporary EOF.
func (r *cacheReader) Read(p []byte) (int, error) {
	c := r.cache
	for {
		c.mu.Lock()
		available, finished, err, changed := c.written-r.pos, c.finished, c.err, c.changed
		c.mu.Unlock()
		if available > 0 {
			n, err := c.file.ReadAt(p[:min(int64(len(p)), available)], r.pos)
			r.pos += int64(n)
			return n, err
		}
		if err != nil {
			return 0, err
		}
		if finished {
			return 0, io.EOF
		}
		select {
		case <-changed:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		case <-c.ctx.Done():
			return 0, c.ctx.Err()
		}
	}
}

// Close cancels readers and the download, stops the server, and removes the file.
// Repeated calls are safe.
func (c *Cache) Close() {
	c.once.Do(func() { c.cancel(); c.server.Close(); <-c.done; c.file.Close(); os.Remove(c.file.Name()) })
}
