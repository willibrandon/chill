package streammeta

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestHTTPIdleReadTimeout checks finite, live and ranged responses stop stalled reads.
func TestHTTPIdleReadTimeout(t *testing.T) {
	for _, mode := range []string{"finite", "live", "range"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Accept-Ranges", "bytes")
				w.Header().Set("Content-Length", "100")
				if r.Header.Get("Range") != "" {
					w.Header().Set("Content-Length", "90")
					w.Header().Set("Content-Range", "bytes 10-99/100")
					w.WriteHeader(http.StatusPartialContent)
				}
				w.Write([]byte{1})
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			source, err := openWithIdleTimeout(t.Context(), server.URL, nil, nil, mode != "live", 40*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Body.Close()
			if mode == "range" {
				if _, err := source.Body.(io.Seeker).Seek(10, io.SeekStart); err != nil {
					t.Fatal(err)
				}
			}
			data, err := io.ReadAll(source.Body)
			if len(data) != 1 || !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("stalled read: %d bytes, %v", len(data), err)
			}
		})
	}
}

// TestHTTPIdleTimeoutIgnoresConsumerPause checks the deadline measures blocked reads only.
func TestHTTPIdleTimeoutIgnoresConsumerPause(t *testing.T) {
	proceed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte{1})
		w.(http.Flusher).Flush()
		select {
		case <-proceed:
			w.Write([]byte{2})
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	source, err := openWithIdleTimeout(t.Context(), server.URL, nil, nil, true, 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Body.Close()
	var data [1]byte
	if _, err := io.ReadFull(source.Body, data[:]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	close(proceed)
	if _, err := io.ReadFull(source.Body, data[:]); err != nil || data[0] != 2 {
		t.Fatalf("pause closed stream: %v, %v", data, err)
	}
}

// TestHTTPIdleTimeoutCancellation checks cancellation interrupts a read before its idle timeout.
func TestHTTPIdleTimeoutCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	source, err := openWithIdleTimeout(ctx, server.URL, nil, nil, true, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Body.Close()
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(source.Body); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt read")
	}
}

// TestHTTPIdleTimeoutResetsAfterProgress checks a long transfer can keep making progress.
func TestHTTPIdleTimeoutResetsAfterProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range 12 {
			w.Write([]byte{1})
			w.(http.Flusher).Flush()
			select {
			case <-time.After(10 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	}))
	defer server.Close()
	source, err := openWithIdleTimeout(t.Context(), server.URL, nil, nil, true, 80*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Body.Close()
	data, err := io.ReadAll(source.Body)
	if len(data) != 12 || err != nil {
		t.Fatalf("progress failed: %d bytes, %v", len(data), err)
	}
}
