package episode

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestProgressiveRangesReuseOneDownload checks seeking reuses the publisher download.
func TestProgressiveRangesReuseOneDownload(t *testing.T) {
	var requests atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Length", "10")
		fmt.Fprint(w, "abcde")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			fmt.Fprint(w, "fghij")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	c, err := Open(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	read := func(header string) string {
		t.Helper()
		req, _ := http.NewRequest("GET", c.URL(), nil)
		req.Header.Set("Range", header)
		resp, err := (&http.Client{Timeout: time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("range status: %s", resp.Status)
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read("bytes=1-3"); got != "bcd" {
		t.Fatal(got)
	}
	close(release)
	if got := read("bytes=7-"); got != "hij" {
		t.Fatal(got)
	}
	if got := read("bytes=0-2"); got != "abc" {
		t.Fatal(got)
	}
	if requests.Load() != 1 {
		t.Fatal("seek reconnected to publisher")
	}
	<-c.Done()
	path, err := c.CompletedPath()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("temporary download survived close")
	}
}

// TestChunkedDownloadAndCancellation checks unknown lengths and prompt cleanup.
func TestChunkedDownloadAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.(http.Flusher).Flush(); fmt.Fprint(w, "chunked audio") }))
	defer server.Close()
	c, err := Open(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	resp, err := http.Get(c.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || string(data) != "chunked audio" {
		t.Fatalf("chunked: %s %v", data, err)
	}
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer blocked.Close()
	b, err := Open(blocked.URL)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { b.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked")
	}
}
