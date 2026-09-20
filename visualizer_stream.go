package main

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/willibrandon/chill/internal/audio"
)

const visualizerInterval = time.Second / 30

// Audio uses a separate, read-only subscription, never the command connection.
// Each packet is a current snapshot; a slow consumer is disconnected rather
// than retaining frames or holding up playback/control commands.
type visualizerPacket struct {
	Version    int         `json:"version"`
	Generation uint64      `json:"generation"`
	State      string      `json:"state"`
	Frame      audio.Frame `json:"frame"`
}

func (d *Daemon) serveVisualizer(conn net.Conn) {
	ticker := time.NewTicker(visualizerInterval)
	defer ticker.Stop()
	encoder := json.NewEncoder(conn)
	for {
		d.mu.Lock()
		p, state, generation := d.player, d.state, d.generation
		d.mu.Unlock()
		packet := visualizerPacket{Version: 1, State: state, Generation: generation}
		if state == "playing" {
			if source, ok := p.(interface{ audioFrame() audio.Frame }); ok {
				packet.Frame = source.audioFrame()
			} else {
				packet.State = "unavailable"
			}
		}
		conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
		if err := encoder.Encode(packet); err != nil {
			return
		}
		<-ticker.C
	}
}

type visualizerStream struct {
	conn    net.Conn
	decoder *json.Decoder
	id      uint64
}

type visualizerConnectedMsg struct {
	id     uint64
	stream *visualizerStream
	err    error
}
type visualizerFrameMsg struct {
	id     uint64
	packet visualizerPacket
	err    error
}
type visualizerRetryMsg uint64

func connectVisualizer(id uint64) tea.Cmd {
	return func() tea.Msg {
		conn, err := dialSocket()
		if err != nil {
			return visualizerConnectedMsg{id: id, err: err}
		}
		conn.SetWriteDeadline(time.Now().Add(time.Second))
		if _, err := fmt.Fprintln(conn, "visualize"); err != nil {
			conn.Close()
			return visualizerConnectedMsg{id: id, err: err}
		}
		return visualizerConnectedMsg{id: id, stream: &visualizerStream{conn: conn, decoder: json.NewDecoder(conn), id: id}}
	}
}

func (s *visualizerStream) next() tea.Msg {
	s.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var packet visualizerPacket
	err := s.decoder.Decode(&packet)
	if err == nil && packet.Version != 1 {
		err = fmt.Errorf("daemon does not support visualizers")
	}
	return visualizerFrameMsg{id: s.id, packet: packet, err: err}
}
