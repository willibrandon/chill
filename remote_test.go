package main

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// TestRemoteCapabilitiesEnvelope checks versioning, discovery, and exactly-once responses.
func TestRemoteCapabilitiesEnvelope(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	service := newRemoteService(&Daemon{})
	done := make(chan bool, 1)
	go func() {
		done <- service.handle(server, []byte(`{"version":2,"id":"test","method":"capabilities"}`))
		server.Close()
	}()
	reader := bufio.NewReader(client)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response remoteResponse
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.ID != "test" || response.Version != remoteVersion || len(response.Capabilities) == 0 || len(response.Topics) == 0 {
		t.Fatalf("capabilities response = %+v", response)
	}
	if streamed := <-done; streamed {
		t.Fatal("capabilities request unexpectedly became a stream")
	}
	_ = client.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := reader.ReadBytes('\n'); err == nil {
		t.Fatal("server wrote a duplicate response")
	}
}

// TestRemoteResponseDoesNotWaitForEOF guards persistent daemon connections.
func TestRemoteResponseDoesNotWaitForEOF(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	release := make(chan struct{})
	go func() {
		defer server.Close()
		_, _ = server.Write([]byte(`{"version":2,"id":"live","ok":true}` + "\n"))
		<-release
	}()
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	response, err := readRemoteResponse(bufio.NewReader(client))
	close(release)
	if err != nil || !response.OK || response.ID != "live" {
		t.Fatalf("response = %+v, err = %v", response, err)
	}
}

// TestRemoteQueueRevisionConflict checks optimistic concurrency across remote clients.
func TestRemoteQueueRevisionConflict(t *testing.T) {
	withConfigDir(t)
	library := emptyLibrary()
	library.QueueRevision = 17
	station := itemFromStation(Station{Name: "live", Desc: "Live", URL: "https://example.com/live"})
	d := &Daemon{library: library, queueRevision: library.QueueRevision, current: &station, station: station.Station}
	item := MediaItem{Kind: MediaTrack, Source: filepath.Join(t.TempDir(), "one.flac"), Title: "One"}
	result, err := d.performRemoteOperation(context.Background(), "queue.append", map[string]any{"items": []MediaItem{item}, "if_revision": uint64(17)}, nil)
	if err != nil || result == nil || d.queueRevision != 18 {
		t.Fatalf("append result = %#v, revision = %d, err = %v", result, d.queueRevision, err)
	}
	_, err = d.performRemoteOperation(context.Background(), "queue.clear", map[string]any{"if_revision": uint64(17)}, nil)
	if err == nil || remoteErrorCode(err) != "conflict" {
		t.Fatalf("stale mutation error = %v", err)
	}
}

// TestAutomaticQueueConsumptionAdvancesRevision covers transitions outside explicit commands.
func TestAutomaticQueueConsumptionAdvancesRevision(t *testing.T) {
	withConfigDir(t)
	library := emptyLibrary()
	library.QueueRevision = 8
	library.Queue = []MediaItem{MediaItem{Kind: MediaTrack, Source: filepath.Join(t.TempDir(), "one.flac"), Title: "One"}}
	d := &Daemon{library: library, queueRevision: library.QueueRevision}
	d.ensureQueueFingerprint()
	d.mu.Lock()
	item, ok := d.nextQueued()
	d.updateMedia()
	d.mu.Unlock()
	if !ok || item.Title != "One" || d.queueRevision != 9 || d.library.QueueRevision != 9 {
		t.Fatalf("item = %+v, ok = %t, revisions = %d/%d", item, ok, d.queueRevision, d.library.QueueRevision)
	}
	reloaded, err := loadLibrary()
	if err != nil || reloaded.QueueRevision != 9 {
		t.Fatalf("persisted revision = %v, err = %v", reloaded.QueueRevision, err)
	}
}

// TestRemoteRejectsInvalidEnvelope checks stable machine-readable error codes.
func TestRemoteRejectsInvalidEnvelope(t *testing.T) {
	response := remoteFailure("bad", "invalid_request", "bad request", "")
	if response.OK || response.Error == nil || response.Error.Code != "invalid_request" {
		t.Fatalf("failure response = %+v", response)
	}
	if code := remoteErrorCode(context.Canceled); code != "canceled" {
		t.Fatalf("canceled code = %q", code)
	}
	if code := remoteErrorCode(errors.New("queue revision conflict")); code != "conflict" {
		t.Fatalf("conflict code = %q", code)
	}
}

// TestRemoteInterfaceSettings checks discovery and validated durable mutation.
func TestRemoteInterfaceSettings(t *testing.T) {
	withConfigDir(t)
	found := false
	for _, capability := range remoteCapabilities {
		found = found || capability.Name == "settings.interface"
	}
	if !found {
		t.Fatal("settings.interface is missing from capability discovery")
	}
	d := &Daemon{}
	result, handled, err := d.performStructuredRemoteOperation(context.Background(), "settings.interface", map[string]any{
		"theme": "Paper", "seek_step": 12, "simplified": true, "panels": map[string]bool{"metadata": true},
	}, nil)
	if err != nil || !handled {
		t.Fatalf("remote interface update: handled=%t result=%#v err=%v", handled, result, err)
	}
	settings, err := loadInterfaceSettings()
	if err != nil || settings.Theme != "Paper" || settings.SeekStep != 12 || !settings.Simplified || !settings.Panels["metadata"] {
		t.Fatalf("persisted interface settings = %+v, err = %v", settings, err)
	}
}
