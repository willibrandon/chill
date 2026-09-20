package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type playerEvent struct {
	loaded bool
	err    string
}

// player keeps process management separate from daemon state, and lets tests
// exercise reconnects without a network stream or an audio device.
type player interface {
	command(...any) error
	events() <-chan playerEvent
	close()
}

type mpvMessage struct {
	RequestID int    `json:"request_id"`
	Error     string `json:"error"`
	Event     string `json:"event"`
	Reason    string `json:"reason"`
	FileError string `json:"file_error"`
	Text      string `json:"text"`
}

type mpvPlayer struct {
	conn   net.Conn
	tree   *processTree
	exited chan struct{}
	done   chan struct{}
	once   sync.Once
	dir    string
	mu     sync.Mutex // serializes commands and request IDs
	id     int
	reply  chan mpvMessage
	event  chan playerEvent
}

func startMPV(volume int, muted, paused bool, input io.Reader, options []string) (*mpvPlayer, error) {
	dir, err := os.MkdirTemp("", "chill-mpv-")
	if err != nil {
		return nil, err
	}
	endpoint := playerEndpoint(dir)
	cmd := exec.Command("mpv", "--no-video", "--really-quiet", "--idle=yes",
		"--input-terminal=no", "--input-ipc-server="+endpoint,
		fmt.Sprintf("--volume=%d", volume), fmt.Sprintf("--mute=%s", yesNo(muted)),
		fmt.Sprintf("--pause=%s", yesNo(paused)))
	cmd.Args = append(cmd.Args, options...)
	cmd.Stdin = input
	var diagnostics tailBuffer
	cmd.Stderr = &diagnostics
	tree, err := startInTree(cmd)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	p := &mpvPlayer{tree: tree, dir: dir, exited: make(chan struct{}),
		done: make(chan struct{}), reply: make(chan mpvMessage, 8), event: make(chan playerEvent, 8)}
	go func() {
		cmd.Wait()
		close(p.exited)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		p.conn, err = dialPlayer(endpoint)
		if err == nil {
			break
		}
		select {
		case <-p.exited:
			p.close()
			return nil, fmt.Errorf("mpv exited before opening its control socket: %s", diagnostics.String())
		default:
		}
		if time.Now().After(deadline) {
			p.close()
			return nil, fmt.Errorf("connecting to mpv: %w; %s", err, diagnostics.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	go p.read()
	if err := p.command("request_log_messages", "error"); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (p *mpvPlayer) events() <-chan playerEvent { return p.event }

func (p *mpvPlayer) emit(e playerEvent) {
	select {
	case p.event <- e:
	case <-p.done:
	}
}

// read separates asynchronous mpv events from command acknowledgements.
func (p *mpvPlayer) read() {
	decoder := json.NewDecoder(p.conn)
	lastError := ""
	for {
		var m mpvMessage
		if err := decoder.Decode(&m); err != nil {
			p.emit(playerEvent{err: "mpv disconnected: " + err.Error()})
			return
		}
		if m.RequestID != 0 {
			select {
			case p.reply <- m:
			case <-p.done:
				return
			}
			continue
		}
		switch m.Event {
		case "log-message":
			lastError = strings.TrimSpace(m.Text)
			if len(lastError) > 500 {
				lastError = lastError[:500]
			}
		case "file-loaded":
			lastError = ""
			p.emit(playerEvent{loaded: true})
		case "end-file":
			// Playlist/URL redirects end the intermediate file before mpv
			// starts its resolved target; they are not playback failures.
			if m.Reason == "stop" || m.Reason == "quit" || m.Reason == "redirect" {
				continue
			}
			reason := "stream ended"
			if m.FileError != "" {
				reason = m.FileError
			}
			if lastError != "" {
				reason += ": " + lastError
			}
			p.emit(playerEvent{err: reason})
		}
	}
}

func (p *mpvPlayer) command(args ...any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.id++
	p.conn.SetWriteDeadline(time.Now().Add(time.Second))
	if err := json.NewEncoder(p.conn).Encode(struct {
		Command []any `json:"command"`
		ID      int   `json:"request_id"`
	}{args, p.id}); err != nil {
		return fmt.Errorf("mpv command: %w", err)
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case m := <-p.reply:
			if m.RequestID != p.id {
				continue
			}
			if m.Error != "success" {
				return fmt.Errorf("mpv: %s", m.Error)
			}
			return nil
		case <-p.done:
			return fmt.Errorf("mpv stopped")
		case <-timer.C:
			return fmt.Errorf("mpv command timed out")
		}
	}
}

func (p *mpvPlayer) close() {
	p.once.Do(func() {
		close(p.done)
		if p.conn != nil {
			p.conn.Close()
		}
		p.tree.kill()
		<-p.exited
		os.RemoveAll(p.dir)
	})
}
