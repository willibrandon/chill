//go:build !windows

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// socketPath returns the path to the owner-scoped Unix socket used for IPC.
func socketPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("chill-%d", os.Getuid()), "daemon.sock")
}

func legacySocketPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("chill-%d.sock", os.Getuid()))
}

// listenSocket creates a Unix socket listener.
func listenSocket() (net.Listener, error) {
	sock := socketPath()
	if err := os.MkdirAll(filepath.Dir(sock), 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Dir(sock))
	if err != nil {
		return nil, err
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !owned || stat.Uid != uint32(os.Getuid()) {
		return nil, fmt.Errorf("daemon runtime directory is not owned by the current user")
	}
	if err := os.Chmod(filepath.Dir(sock), 0700); err != nil {
		return nil, err
	}
	_ = os.Remove(sock) // clean up an old socket in the owner-only directory
	listener, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sock, 0600); err != nil {
		listener.Close()
		os.Remove(sock)
		return nil, err
	}
	return listener, nil
}

// dialSocket connects to the daemon's Unix socket.
func dialSocket() (net.Conn, error) {
	connection, err := net.DialTimeout("unix", socketPath(), time.Second)
	if err == nil {
		return connection, nil
	}
	legacy, legacyErr := net.DialTimeout("unix", legacySocketPath(), time.Second)
	if legacyErr == nil {
		return legacy, nil
	}
	return nil, err
}

// cleanupSocket removes the Unix socket file.
func cleanupSocket() {
	_ = os.Remove(socketPath())
}
