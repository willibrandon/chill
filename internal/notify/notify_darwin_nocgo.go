//go:build darwin && !cgo

// Package notify presents optional track-change notifications through the
// host desktop's notification service.
package notify

import "errors"

// Show reports when native macOS integration is unavailable.
func Show(_, _, _ string) error {
	return errors.New("native macOS notifications require a cgo-enabled build")
}

// RunHelper reports that this build has no native notification helper.
func RunHelper(_ []string) (bool, error) { return false, nil }
