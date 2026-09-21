//go:build !linux && !darwin && !windows

// Package notify presents optional track-change notifications through the
// host desktop's notification service.
package notify

// Show does nothing on unsupported platforms.
func Show(_, _, _ string) error { return nil }

// RunHelper reports that this platform has no notification helper.
func RunHelper(_ []string) (bool, error) { return false, nil }
