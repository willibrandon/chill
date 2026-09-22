//go:build !windows

package main

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// processTree owns an external helper's process group and all descendants.
type processTree struct {
	p       *os.Process
	once    sync.Once
	killErr error
}

// startInTree starts cmd.
func startInTree(cmd *exec.Cmd) (*processTree, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &processTree{p: cmd.Process}, nil
}

// kill terminates the whole group, even if its original process has exited.
func (t *processTree) kill() error {
	t.once.Do(func() { t.killErr = syscall.Kill(-t.p.Pid, syscall.SIGKILL) })
	return t.killErr
}
