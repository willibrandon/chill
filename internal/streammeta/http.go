package streammeta

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Source is an open live response that can be passed directly to a decoder.
type Source struct {
	// Body contains audio with any interleaved ICY blocks removed.
	Body io.ReadCloser
	// ContentType is the normalized response media type.
	ContentType string
	// StationName is the server's advertised station name.
	StationName string
	// Playlist reports content that the decoder must open by URL to retain a base path.
	Playlist bool
}

// Open requests one live stream with ICY metadata enabled.
func Open(ctx context.Context, rawURL string, headers map[string]string, onTitle func(string)) (Source, error) {
	return open(ctx, rawURL, headers, onTitle, false)
}

// OpenFinite allows validated byte-range seeking for finite, non-ICY responses.
func OpenFinite(ctx context.Context, rawURL string, headers map[string]string, onTitle func(string)) (Source, error) {
	return open(ctx, rawURL, headers, onTitle, true)
}

func open(ctx context.Context, rawURL string, headers map[string]string, onTitle func(string), finite bool) (Source, error) {
	return openWithIdleTimeout(ctx, rawURL, headers, onTitle, finite, 15*time.Second)
}

func openWithIdleTimeout(ctx context.Context, rawURL string, headers map[string]string, onTitle func(string), finite bool, idle time.Duration) (Source, error) {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	client := &http.Client{Transport: idleTransport{transport, idle}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("stream redirected to an unsupported URL")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Source{}, err
	}
	req.Header.Set("User-Agent", "chill/1.0")
	req.Header.Set("Icy-MetaData", "1")
	req.Header.Set("Accept-Encoding", "identity")
	for key, value := range headers {
		if !strings.ContainsAny(key+value, "\r\n") {
			req.Header.Set(key, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Source{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return Source{}, fmt.Errorf("stream returned %s", resp.Status)
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	playlist := strings.Contains(contentType, "mpegurl") || strings.Contains(contentType, "x-scpls") ||
		strings.Contains(contentType, "application/vnd.apple.mpegurl") || strings.HasSuffix(strings.ToLower(resp.Request.URL.Path), ".m3u8") ||
		strings.HasSuffix(strings.ToLower(resp.Request.URL.Path), ".m3u") || strings.HasSuffix(strings.ToLower(resp.Request.URL.Path), ".pls")
	body := io.ReadCloser(resp.Body)
	if value := strings.TrimSpace(resp.Header.Get("icy-metaint")); value != "" {
		interval, parseErr := strconv.Atoi(value)
		if parseErr != nil || interval < 1 || interval > 64<<20 {
			resp.Body.Close()
			return Source{}, fmt.Errorf("invalid ICY metadata interval")
		}
		body = NewReader(resp.Body, interval, onTitle)
	} else if finite && !playlist && resp.ContentLength > 0 && resp.Header.Get("Accept-Ranges") == "bytes" && resp.Header.Get("Content-Encoding") == "" {
		body = newRangeReader(ctx, client, resp)
	}
	return Source{Body: body, ContentType: contentType, StationName: clean(resp.Header.Get("icy-name")), Playlist: playlist}, nil
}
