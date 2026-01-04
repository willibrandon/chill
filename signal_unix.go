//go:build !windows

package main

import (
	"os"
	"syscall"
)

// pauseProcess sends SIGSTOP to pause the process.
func pauseProcess(p *os.Process) error {
	return p.Signal(syscall.SIGSTOP)
}

// resumeProcess sends SIGCONT to resume the process.
func resumeProcess(p *os.Process) error {
	return p.Signal(syscall.SIGCONT)
}
