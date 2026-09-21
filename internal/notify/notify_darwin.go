//go:build darwin

// Package notify presents optional track-change notifications through the
// host desktop's notification service.
package notify

import (
	"context"
	"os/exec"
	"time"
)

// Show presents a transient track-change notification.
func Show(title, body, _ string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	script := `on run argv
display notification (item 2 of argv) with title (item 1 of argv)
end run`
	return exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script, title, body).Run()
}
