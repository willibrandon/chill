package main

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMPVProtocolInterleavedEventsAndReplies(t *testing.T) {
	client, server := net.Pipe()
	p := &mpvPlayer{conn: client, done: make(chan struct{}), reply: make(chan mpvMessage, 8), event: make(chan playerEvent, 8)}
	defer client.Close()
	defer server.Close()
	defer close(p.done)
	go p.read()
	go func() {
		decoder := json.NewDecoder(server)
		encoder := json.NewEncoder(server)
		for i := 0; i < 2; i++ {
			var request struct {
				ID int `json:"request_id"`
			}
			if decoder.Decode(&request) != nil {
				return
			}
			encoder.Encode(map[string]any{"event": "end-file", "reason": "redirect"})
			encoder.Encode(map[string]any{"event": "file-loaded"})
			// Stale replies must not acknowledge the current command.
			encoder.Encode(map[string]any{"request_id": request.ID + 100, "error": "success"})
			result := "success"
			if i == 1 {
				result = "property unavailable"
			}
			encoder.Encode(map[string]any{"request_id": request.ID, "error": result})
		}
		encoder.Encode(map[string]any{"event": "log-message", "text": "YouTube stream is offline"})
		encoder.Encode(map[string]any{"event": "end-file", "reason": "error", "file_error": "loading failed"})
	}()
	if err := p.command("set_property", "volume", 30); err != nil {
		t.Fatal(err)
	}
	if err := p.command("set_property", "pause", true); err == nil || !strings.Contains(err.Error(), "property unavailable") {
		t.Fatalf("command error lost: %v", err)
	}
	for i := 0; i < 2; i++ {
		select {
		case event := <-p.events():
			if !event.loaded {
				t.Fatalf("loaded event lost: %+v", event)
			}
		case <-time.After(time.Second):
			t.Fatal("missing loaded event")
		}
	}
	select {
	case event := <-p.events():
		if !strings.Contains(event.err, "YouTube stream is offline") {
			t.Fatalf("stream diagnostics lost: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("missing error event")
	}
}

// This uses a generated local WAV and mpv's null audio output. Opt in so the
// ordinary unit suite also runs on machines without mpv or an audio device.
func TestMPVIntegration(t *testing.T) {
	if os.Getenv("CHILL_TEST_MPV") != "1" {
		t.Skip("set CHILL_TEST_MPV=1 to exercise the installed mpv")
	}
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("MPV_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "mpv.conf"), []byte("ao=null\nloop-file=inf\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// One second of silent, mono, 8 kHz, 16-bit PCM.
	wav := make([]byte, 44+16000)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 8000)
	binary.LittleEndian.PutUint32(wav[28:], 16000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 16000)
	path := filepath.Join(dir, "silence.wav")
	if err := os.WriteFile(path, wav, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := startPlayer(55, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if err := p.command("loadfile", path, "replace"); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-p.events():
		if !e.loaded {
			t.Fatalf("local file failed: %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mpv did not load the local file")
	}
	for _, cmd := range [][]any{{"set_property", "pause", true}, {"set_property", "volume", 32}, {"set_property", "mute", true}} {
		if err := p.command(cmd...); err != nil {
			t.Fatal(err)
		}
	}
	// An independent IPC connection verifies actual mpv state, rather than
	// trusting the acknowledgements or the daemon's own cached fields.
	endpoint := playerEndpoint(p.(*mpvPlayer).dir)
	conn, err := dialPlayer(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	decoder := json.NewDecoder(conn)
	property := func(name string) any {
		t.Helper()
		conn.SetDeadline(time.Now().Add(time.Second))
		if err := json.NewEncoder(conn).Encode(map[string]any{"command": []any{"get_property", name}, "request_id": 1}); err != nil {
			t.Fatal(err)
		}
		for {
			var response struct {
				ID    int    `json:"request_id"`
				Data  any    `json:"data"`
				Error string `json:"error"`
			}
			if err := decoder.Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.ID != 1 {
				continue
			}
			if response.Error != "success" {
				t.Fatalf("get %s: %s", name, response.Error)
			}
			return response.Data
		}
	}
	for key, want := range map[string]any{"volume": float64(32), "pause": true, "mute": true, "path": path} {
		if got := property(key); got != want {
			t.Errorf("mpv %s = %v, want %v", key, got, want)
		}
	}
	if err := p.command("set_property", "mute", false); err != nil {
		t.Fatal(err)
	}
	if got := property("volume"); got != float64(32) {
		t.Fatalf("unmute changed volume to %v", got)
	}
	// A failed stream emits a useful error even though mpv stays alive in idle mode.
	if err := p.command("loadfile", filepath.Join(dir, "missing.wav"), "replace"); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-p.events():
		if e.err == "" {
			t.Fatalf("missing file did not fail: %+v", e)
		}
		t.Logf("mpv error surfaced: %s", e.err)
	case <-time.After(5 * time.Second):
		t.Fatal("mpv did not report the missing file")
	}
	p.close()
	select {
	case <-p.(*mpvPlayer).exited:
	default:
		t.Fatal("mpv still running after close")
	}
}
