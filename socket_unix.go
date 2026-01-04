//go:build !windows

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// socketPath returns the path to the Unix socket used for IPC.
// The socket is user-specific to allow multiple users on the same system.
func socketPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("chill-%d.sock", os.Getuid()))
}

// listenSocket creates a Unix socket listener.
func listenSocket() (net.Listener, error) {
	sock := socketPath()
	os.Remove(sock) // clean up old socket
	return net.Listen("unix", sock)
}

// dialSocket connects to the daemon's Unix socket.
func dialSocket() (net.Conn, error) {
	return net.Dial("unix", socketPath())
}

// cleanupSocket removes the Unix socket file.
func cleanupSocket() {
	os.Remove(socketPath())
}
