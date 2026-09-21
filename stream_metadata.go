package main

import (
	"strings"
	"sync"
	"time"
)

// metadataDiagnostics retains decoder errors while recognizing metadata that
// FFmpeg reports for HLS and other streams without interleaved ICY blocks.
type metadataDiagnostics struct {
	tail          *tailBuffer
	onTitle       func(string)
	mu            sync.Mutex
	partial       string
	artist, title string
	artistDirty   bool
	titleDirty    bool
	pending       *time.Timer
	last          string
}

func newMetadataDiagnostics(tail *tailBuffer, onTitle func(string)) *metadataDiagnostics {
	return &metadataDiagnostics{tail: tail, onTitle: onTitle}
}

// Write retains diagnostics and consumes complete metadata lines.
func (d *metadataDiagnostics) Write(p []byte) (int, error) {
	n, err := d.tail.Write(p)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.partial += string(p)
	for {
		line, rest, found := strings.Cut(d.partial, "\n")
		if !found {
			if len(d.partial) > diagnosticLimit {
				d.partial = d.partial[len(d.partial)-diagnosticLimit:]
			}
			break
		}
		d.partial = rest
		d.line(strings.TrimSpace(line))
	}
	return n, err
}

func (d *metadataDiagnostics) line(line string) {
	lower := strings.ToLower(line)
	if index := strings.Index(lower, "streamtitle"); index >= 0 {
		value := strings.Trim(strings.TrimSpace(line[index+len("streamtitle"):]), "=: '")
		d.emitDirect(value)
		return
	}
	key, value, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
	switch key {
	case "icy-title":
		d.emitDirect(value)
	case "title", "tit2":
		if value == "" {
			d.resetPair()
			d.emit("")
			return
		}
		d.title, d.titleDirty = value, true
		if d.artistDirty && d.artist != "" {
			d.emitPair()
		} else {
			d.scheduleTitle()
		}
	case "artist", "tpe1":
		d.artist, d.artistDirty = value, value != ""
		if d.artistDirty && d.titleDirty && d.title != "" {
			d.emitPair()
		}
	}
}

func (d *metadataDiagnostics) emitDirect(value string) {
	d.resetPair()
	d.emit(value)
}

func (d *metadataDiagnostics) emitPair() {
	value := d.artist + " - " + d.title
	d.resetPair()
	d.emit(value)
}

func (d *metadataDiagnostics) resetPair() {
	if d.pending != nil {
		d.pending.Stop()
		d.pending = nil
	}
	d.artist, d.title = "", ""
	d.artistDirty, d.titleDirty = false, false
}

func (d *metadataDiagnostics) scheduleTitle() {
	if d.pending != nil {
		d.pending.Stop()
	}
	title := d.title
	d.pending = time.AfterFunc(100*time.Millisecond, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.titleDirty && d.title == title {
			d.resetPair()
			d.emit(title)
		}
	})
}

func (d *metadataDiagnostics) emit(value string) {
	value = strings.TrimSpace(value)
	if value == d.last {
		return
	}
	d.last = value
	d.onTitle(value)
}
