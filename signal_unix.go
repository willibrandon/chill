//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// processTree is the player process. Unlike on Windows, "mpv" is the player
// itself rather than a launcher, so there are no descendants to track.
type processTree struct {
	p *os.Process
}

// startInTree starts cmd.
func startInTree(cmd *exec.Cmd) (*processTree, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &processTree{p: cmd.Process}, nil
}

// pause sends SIGSTOP to pause the process.
func (t *processTree) pause() error {
	return t.p.Signal(syscall.SIGSTOP)
}

// resume sends SIGCONT to resume the process.
func (t *processTree) resume() error {
	return t.p.Signal(syscall.SIGCONT)
}

// kill kills the process.
func (t *processTree) kill() error {
	return t.p.Kill()
}
