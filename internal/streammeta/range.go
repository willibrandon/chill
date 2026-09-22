package streammeta

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// rangeReader owns one finite HTTP representation. A seek commits only after
// the server proves it returned the requested range of the same representation.
type rangeReader struct {
	ctx                        context.Context
	client                     *http.Client
	request                    *http.Request
	size, position             int64
	validator, validatorHeader string
	mu                         sync.Mutex
	body                       io.ReadCloser
	closed                     bool
}

func newRangeReader(ctx context.Context, client *http.Client, response *http.Response) *rangeReader {
	r := &rangeReader{ctx: ctx, client: client, request: response.Request, size: response.ContentLength, body: response.Body}
	if etag := response.Header.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
		r.validator, r.validatorHeader = etag, "ETag"
	} else if modified := response.Header.Get("Last-Modified"); modified != "" {
		r.validator, r.validatorHeader = modified, "Last-Modified"
	}
	return r
}

// Read consumes the current response without buffering the complete source.
func (r *rangeReader) Read(dst []byte) (int, error) {
	r.mu.Lock()
	body, closed := r.body, r.closed
	r.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}
	n, err := body.Read(dst)
	r.position += int64(n)
	return n, err
}

// Seek validates Content-Range, length, and validators before replacing input.
func (r *rangeReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += r.position
	case io.SeekEnd:
		offset += r.size
	default:
		return r.position, fmt.Errorf("invalid HTTP seek origin")
	}
	if offset < 0 || offset > r.size {
		return r.position, fmt.Errorf("HTTP seek outside source")
	}
	if offset == r.position {
		return offset, nil
	}
	var body io.ReadCloser
	if offset == r.size {
		body = io.NopCloser(strings.NewReader(""))
	} else {
		req := r.request.Clone(r.ctx)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		if r.validator != "" {
			req.Header.Set("If-Range", r.validator)
		}
		resp, err := r.client.Do(req)
		if err != nil {
			return r.position, err
		}
		want := fmt.Sprintf("bytes %d-%d/%d", offset, r.size-1, r.size)
		if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Range") != want ||
			(resp.ContentLength >= 0 && resp.ContentLength != r.size-offset) ||
			(resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") ||
			(r.validator != "" && resp.Header.Get(r.validatorHeader) != r.validator) {
			resp.Body.Close()
			return r.position, fmt.Errorf("server did not honor byte range %s (status %s)", strconv.FormatInt(offset, 10), resp.Status)
		}
		body = resp.Body
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		body.Close()
		return r.position, io.ErrClosedPipe
	}
	r.body.Close()
	r.body, r.position = body, offset
	return offset, nil
}

// Close interrupts the current read and releases idle network connections.
func (r *rangeReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.body.Close()
	r.client.CloseIdleConnections()
	return err
}
