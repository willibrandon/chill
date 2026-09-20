//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// processTree owns mpv's process group, including an in-flight yt-dlp resolver.
type processTree struct {
	p *os.Process
}

// startInTree starts cmd.
func startInTree(cmd *exec.Cmd) (*processTree, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &processTree{p: cmd.Process}, nil
}

// kill terminates the whole group, even if mpv itself has already exited.
func (t *processTree) kill() error {
	return syscall.Kill(-t.p.Pid, syscall.SIGKILL)
}
