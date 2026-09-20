package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const diagnosticLimit = 8 << 10

// tailBuffer bounds subprocess diagnostics even when an extractor is noisy.
type tailBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if n >= diagnosticLimit {
		b.data = append(b.data[:0], p[n-diagnosticLimit:]...)
	} else {
		if overflow := len(b.data) + n - diagnosticLimit; overflow > 0 {
			b.data = b.data[overflow:]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}

func daemonLogPath() string {
	if path := configPath(); path != "" {
		return filepath.Join(filepath.Dir(path), "daemon.log")
	}
	return ""
}

// Each launch replaces the previous startup log. The daemon writes only its
// startup diagnostics here; playback errors remain in its structured status.
func openDaemonLog() (*os.File, error) {
	path := daemonLogPath()
	if path == "" {
		return nil, fmt.Errorf("no config directory for daemon diagnostics")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
}

func readDaemonLog() string {
	f, err := os.Open(daemonLogPath())
	if err != nil {
		return ""
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > diagnosticLimit {
		f.Seek(-diagnosticLimit, io.SeekEnd)
	}
	data, _ := io.ReadAll(io.LimitReader(f, diagnosticLimit))
	return strings.TrimSpace(string(data))
}

func daemonStartError(reason string) error {
	message := fmt.Sprintf("daemon failed to start: %s (log: %s)", reason, daemonLogPath())
	if output := readDaemonLog(); output != "" {
		message += "\n" + output
	}
	return fmt.Errorf("%s", message)
}
