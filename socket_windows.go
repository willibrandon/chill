//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// socketPath returns the owner-scoped marker used for liveness and locking.
func socketPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "chill", "daemon.pipe")
}

func legacySocketPath() string { return filepath.Join(os.TempDir(), "chill.port") }

func namedPipe() (string, string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", "", err
	}
	sid := user.User.Sid.String()
	name := `\\.\pipe\chill-` + strings.ReplaceAll(sid, "-", "_")
	security := fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;%s)", sid)
	return name, security, nil
}

// listenSocket creates an owner-only Windows named pipe.
func listenSocket() (net.Listener, error) {
	name, security, err := namedPipe()
	if err != nil {
		return nil, err
	}
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: security, InputBufferSize: 64 << 10, OutputBufferSize: 64 << 10})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(socketPath()), 0700); err != nil {
		ln.Close()
		return nil, err
	}
	if err := os.WriteFile(socketPath(), []byte("named-pipe\n"), 0600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// dialSocket connects to the current user's named pipe.
func dialSocket() (net.Conn, error) {
	marker, markerErr := os.ReadFile(socketPath())
	if markerErr == nil && strings.TrimSpace(string(marker)) == "named-pipe" {
		name, _, err := namedPipe()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return winio.DialPipeContext(ctx, name)
	}
	if markerErr == nil {
		return dialLegacySocket(string(marker))
	}
	legacy, err := os.ReadFile(legacySocketPath())
	if err != nil {
		return nil, markerErr
	}
	return dialLegacySocket(string(legacy))
}

func dialLegacySocket(raw string) (net.Conn, error) {
	address := strings.TrimSpace(raw)
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid legacy daemon endpoint: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("legacy daemon endpoint is not loopback-only")
	}
	return net.DialTimeout("tcp", address, time.Second)
}

// cleanupSocket removes the named-pipe liveness marker.
func cleanupSocket() {
	_ = os.Remove(socketPath())
	_ = os.Remove(legacySocketPath())
}
