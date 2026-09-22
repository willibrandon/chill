package streammeta

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

type idleTransport struct {
	base    *http.Transport
	timeout time.Duration
}

// RoundTrip applies the same read deadline to initial and range responses.
func (t idleTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err == nil {
		response.Body = &idleBody{ReadCloser: response.Body, timeout: t.timeout}
	}
	return response, err
}

// CloseIdleConnections releases idle sockets after finite playback.
func (t idleTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }

type idleBody struct {
	io.ReadCloser
	timeout time.Duration
}

// Read times out stalled network reads without counting pause/backpressure time.
func (r *idleBody) Read(dst []byte) (int, error) {
	var expired atomic.Bool
	finished := make(chan struct{})
	timer := time.AfterFunc(r.timeout, func() {
		expired.Store(true)
		r.ReadCloser.Close()
		close(finished)
	})
	n, err := r.ReadCloser.Read(dst)
	if !timer.Stop() {
		<-finished
	}
	if expired.Load() {
		return n, fmt.Errorf("audio source idle for %s: %w", r.timeout, os.ErrDeadlineExceeded)
	}
	return n, err
}
