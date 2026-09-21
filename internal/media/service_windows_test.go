//go:build windows

package media

import (
	"testing"
	"time"
)

// TestWindowsButtonCommand verifies native button translation.
func TestWindowsButtonCommand(t *testing.T) {
	tests := []struct {
		button   int32
		kind     CommandKind
		position time.Duration
	}{
		{0, Play, 0}, {1, Pause, 0}, {2, Stop, 0}, {4, Seek, 30 * time.Second},
		{5, Seek, -15 * time.Second}, {6, Next, 0}, {7, Previous, 0},
	}
	for _, test := range tests {
		command, ok := windowsButtonCommand(test.button)
		if !ok || command.Kind != test.kind || command.Position != test.position {
			t.Fatalf("button %d: %#v, %v", test.button, command, ok)
		}
	}
	if _, ok := windowsButtonCommand(99); ok {
		t.Fatal("unknown button accepted")
	}
}
