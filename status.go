package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"golang.org/x/mod/semver"
)

// inspectDaemon is read-only: it must not run the upgrade handshake, start a
// daemon, or mask a stale discovery file as a normally stopped daemon.
func inspectDaemon() (*Status, error) {
	return inspectDaemonContext(context.Background())
}

func inspectDaemonContext(ctx context.Context) (*Status, error) {
	raw, err := sendRawCommandContext(ctx, "status")
	if err != nil {
		if _, statErr := os.Stat(socketPath()); os.IsNotExist(statErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("daemon unreachable at %s (possibly stale): %w", socketPath(), err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, fmt.Errorf("invalid daemon status: %w", err)
	}
	if fields["playing"] == nil && fields["state"] == nil {
		return nil, fmt.Errorf("unrecognized daemon status: %s", raw)
	}
	var status Status
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return nil, fmt.Errorf("invalid daemon status: %w", err)
	}
	if status.State == "" { // normalize status from pre-state-machine daemons
		status.State = "idle"
		if status.Playing {
			status.State = "playing"
		} else if status.Paused {
			status.State = "paused"
		}
	}
	return &status, nil
}

func daemonCompatibility(s *Status, clientVersion, clientBuildID string) (string, string) {
	if s == nil {
		return "not-running", "no daemon running; the next playback command starts one"
	}
	upgrade, err := daemonNeedsUpgrade(*s, clientVersion, clientBuildID)
	if err != nil {
		return "incompatible", err.Error()
	}
	if upgrade {
		return "daemon-outdated", "stale daemon; the next normal playback/control command upgrades it and preserves playback settings"
	}
	if !semver.IsValid(clientVersion) || !semver.IsValid(s.Version) || strings.Contains(clientVersion, "+dirty") || strings.Contains(s.Version, "+dirty") {
		if clientBuildID != "" && s.BuildID == clientBuildID {
			return "current", "client and daemon executable identities match"
		}
		return "unverifiable", "development/unversioned build; executable identity is unavailable"
	}
	if semver.Compare(s.Version, clientVersion) > 0 {
		return "client-outdated", "daemon is newer but protocol-compatible (the daemon will not be downgraded)"
	}
	return "current", "client and daemon versions and protocols match"
}

// machineStatus keeps playback fields at the top level for status-bar tools.
// Version/protocol describe the daemon; client_* identify the invoking binary.
type machineStatus struct {
	Status
	// Running distinguishes a connected daemon from a stopped one.
	Running bool `json:"running"`
	// ClientVersion identifies the binary issuing this status request.
	ClientVersion string `json:"client_version"`
	// ClientBuildID identifies the exact client executable.
	ClientBuildID string `json:"client_build_id,omitempty"`
	// ClientProtocol is the client's supported daemon protocol.
	ClientProtocol int `json:"client_protocol"`
	// Compatibility describes the client/daemon version relationship.
	Compatibility string `json:"compatibility"`
}

func statusJSON() (string, error) {
	s, err := inspectDaemon()
	result := machineStatus{Status: Status{Queue: []MediaItem{}, PlayNext: []MediaItem{}, Repeat: "off"}, ClientVersion: buildVersion(), ClientBuildID: buildIdentity(), ClientProtocol: daemonProtocol}
	result.Compatibility, _ = daemonCompatibility(s, result.ClientVersion, result.ClientBuildID)
	result.State = "stopped"
	if s != nil {
		result.Status, result.Running = *s, true
	}
	if err != nil {
		result.State, result.Compatibility, result.Error = "unknown", "unreachable", err.Error()
	}
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return "", marshalErr
	}
	return string(data), err
}
