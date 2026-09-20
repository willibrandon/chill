package main

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/willibrandon/chill/internal/audio"
)

type audioTestPlayer struct{ fakePlayer }

func (*audioTestPlayer) audioFrame() audio.Frame {
	return audio.Frame{At: time.Now(), Sequence: 1, Peak: [2]float64{0.8, 0.2}}
}

func TestVisualizerSubscriptionPauseAndSlowReader(t *testing.T) {
	d := &Daemon{state: "playing", player: &audioTestPlayer{}, generation: 7}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan struct{})
	go func() { d.serveVisualizer(server); close(done) }()
	decoder := json.NewDecoder(client)
	read := func() visualizerPacket {
		t.Helper()
		client.SetReadDeadline(time.Now().Add(time.Second))
		var packet visualizerPacket
		if err := decoder.Decode(&packet); err != nil {
			t.Fatal(err)
		}
		return packet
	}
	p := read()
	if p.Version != 1 || p.Generation != 7 || p.Frame.Peak != [2]float64{0.8, 0.2} {
		t.Fatalf("bad packet: %+v", p)
	}
	d.mu.Lock()
	d.state = "paused"
	d.mu.Unlock()
	if p := read(); p.State != "paused" || p.Frame != (audio.Frame{}) {
		t.Fatal("paused subscription retained live audio")
	}
	// A blocked stream write must not block commands, and must time out.
	statusDone := make(chan struct{})
	go func() { d.execute("status", ""); close(statusDone) }()
	select {
	case <-statusDone:
	case <-time.After(time.Second):
		t.Fatal("slow reader blocked daemon commands")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow subscriber leaked")
	}
}

func TestVisualizerClientCloseEndsSubscription(t *testing.T) {
	d := &Daemon{state: "idle"}
	client, server := net.Pipe()
	defer server.Close()
	done := make(chan struct{})
	go func() { d.serveVisualizer(server); close(done) }()
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnected subscriber leaked")
	}
}
