//go:build !windows

package main

import (
	"net"
	"path/filepath"
	"time"
)

func playerEndpoint(dir string) string { return filepath.Join(dir, "ipc") }

func dialPlayer(endpoint string) (net.Conn, error) {
	return net.DialTimeout("unix", endpoint, 100*time.Millisecond)
}
