//go:build windows

package main

import (
	"net"
	"os"
	"path/filepath"
)

// socketPath returns the path to a lock file used to store the port number.
// On Windows, we use TCP on localhost instead of Unix sockets.
func socketPath() string {
	return filepath.Join(os.TempDir(), "chill.port")
}

// listenSocket creates a TCP listener on localhost.
// The port is written to a file so clients can find it.
func listenSocket() (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	// Write the port to a file so clients can find it
	addr := ln.Addr().(*net.TCPAddr)
	err = os.WriteFile(socketPath(), []byte(addr.String()), 0600)
	if err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// dialSocket connects to the daemon's TCP socket.
func dialSocket() (net.Conn, error) {
	data, err := os.ReadFile(socketPath())
	if err != nil {
		return nil, err
	}
	return net.Dial("tcp", string(data))
}

// cleanupSocket removes the port file.
func cleanupSocket() {
	os.Remove(socketPath())
}
