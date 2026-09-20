//go:build windows

package main

import (
	"net"
	"path/filepath"
	"time"

	"github.com/Microsoft/go-winio"
)

func playerEndpoint(dir string) string { return `\\.\pipe\` + filepath.Base(dir) }

func dialPlayer(endpoint string) (net.Conn, error) {
	timeout := 100 * time.Millisecond
	return winio.DialPipe(endpoint, &timeout)
}
