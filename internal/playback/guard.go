package playback

import (
	"fmt"

	"github.com/gopxl/beep/v2"
)

// Third-party decoders run in-process. Contain failures at every entry point,
// including lazy decoding and seeking, not just at initialization.
type guardedDecoder struct {
	source beep.StreamSeekCloser
	err    error
}

func (d *guardedDecoder) recover() {
	if failure := recover(); failure != nil {
		d.err = fmt.Errorf("invalid audio: decoder panic: %v", failure)
	}
}

// Stream turns a codec panic into a terminal decoding error.
func (d *guardedDecoder) Stream(dst [][2]float64) (n int, ok bool) {
	if d.err != nil {
		return 0, false
	}
	defer d.recover()
	return d.source.Stream(dst)
}

// Err returns a contained panic or the underlying decoding error.
func (d *guardedDecoder) Err() (err error) {
	defer func() {
		if failure := recover(); failure != nil {
			d.err = fmt.Errorf("invalid audio: decoder panic: %v", failure)
		}
		if d.err != nil {
			err = d.err
		}
	}()
	if d.err != nil {
		return d.err
	}
	return d.source.Err()
}

// Len returns the decoded frame count when known.
func (d *guardedDecoder) Len() (n int) { defer d.recover(); return d.source.Len() }

// Position returns the current decoded frame position.
func (d *guardedDecoder) Position() (n int) { defer d.recover(); return d.source.Position() }

// Seek contains malformed indexes as well as decode failures.
func (d *guardedDecoder) Seek(frame int) (err error) {
	defer func() {
		if failure := recover(); failure != nil {
			d.err = fmt.Errorf("invalid audio seek: %v", failure)
			err = d.err
		}
	}()
	if d.err != nil {
		return d.err
	}
	return d.source.Seek(frame)
}

// Close releases codec resources without allowing cleanup panics to escape.
func (d *guardedDecoder) Close() (err error) {
	defer func() {
		if failure := recover(); failure != nil {
			err = fmt.Errorf("close audio decoder: %v", failure)
		}
	}()
	return d.source.Close()
}
