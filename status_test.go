package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDoctorCompatibility checks client and daemon version descriptions.
func TestDoctorCompatibility(t *testing.T) {
	for _, tt := range []struct {
		name          string
		status        *Status
		clientVersion string
		clientBuildID string
		want          string
	}{
		{"stopped", nil, "v0.6.0", "release", "not-running"},
		{"legacy", &Status{}, "v0.6.0", "release", "daemon-outdated"},
		{"older", &Status{Version: "v0.5.0", Protocol: daemonProtocol}, "v0.6.0", "release", "daemon-outdated"},
		{"same", &Status{Version: "v0.6.0", Protocol: daemonProtocol}, "v0.6.0", "release", "current"},
		{"newer", &Status{Version: "v0.7.0", Protocol: daemonProtocol}, "v0.6.0", "release", "client-outdated"},
		{"protocol", &Status{Version: "v0.7.0", Protocol: daemonProtocol + 1}, "v0.6.0", "release", "incompatible"},
		{"dev matching", &Status{Version: "dev", BuildID: "same", Protocol: daemonProtocol}, "dev", "same", "current"},
		{"dev rebuilt", &Status{Version: "dev", BuildID: "old", Protocol: daemonProtocol}, "dev", "new", "daemon-outdated"},
		{"dirty legacy", &Status{Version: "v0.6.0+dirty", Protocol: daemonProtocol}, "v0.6.0+dirty", "new", "daemon-outdated"},
		{"dirty matching", &Status{Version: "v0.6.0+dirty", BuildID: "same", Protocol: daemonProtocol}, "v0.6.0+dirty", "same", "current"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := daemonCompatibility(tt.status, tt.clientVersion, tt.clientBuildID); got != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

// TestJSONStatusStoppedAndStale checks stopped and unreachable daemon responses.
func TestJSONStatusStoppedAndStale(t *testing.T) {
	withConfigDir(t)
	out, err := statusJSON()
	var status machineStatus
	if err != nil || json.Unmarshal([]byte(out), &status) != nil || status.Running || status.State != "stopped" || status.Compatibility != "not-running" {
		t.Fatalf("stopped: %s %v", out, err)
	}
	if err := os.MkdirAll(filepath.Dir(socketPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socketPath(), []byte("127.0.0.1:1"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = statusJSON()
	if err == nil || json.Unmarshal([]byte(out), &status) != nil || status.State != "unknown" || status.Error == "" {
		t.Fatalf("stale discovery file was hidden: %s %v", out, err)
	}
}

// TestInspectionNeverUpgrades checks read-only status does not replace a daemon.
func TestInspectionNeverUpgrades(t *testing.T) {
	// macOS Unix socket paths must fit in 104 bytes, including the filename.
	runtimeDir, err := os.MkdirTemp("", "ci-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDir) })
	withConfigDir(t)
	t.Setenv("TMPDIR", runtimeDir)
	t.Setenv("TMP", runtimeDir)
	t.Setenv("TEMP", runtimeDir)
	ln, err := listenSocket()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close(); cleanupSocket() })
	commands := make(chan string, 10)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			func(conn net.Conn) {
				defer conn.Close()
				command, _ := bufio.NewReader(conn).ReadString('\n')
				commands <- strings.TrimSpace(command)
				// Legacy daemon with no version handshake, and paused playback.
				fmt.Fprintln(conn, `{"playing":false,"paused":true,"station":"sleep","volume":31,"muted":true}`)
			}(conn)
		}
	}()
	out, err := statusJSON()
	if err != nil {
		t.Fatal(err)
	}
	var status machineStatus
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.Compatibility != "daemon-outdated" || status.State != "paused" || !status.Paused || !status.Muted || status.Volume != 31 {
		t.Fatal(out)
	}
	if command := <-commands; command != "status" {
		t.Fatalf("inspection sent %s", command)
	}
	select {
	case command := <-commands:
		t.Fatalf("inspection modified daemon with %s", command)
	default:
	}
}
