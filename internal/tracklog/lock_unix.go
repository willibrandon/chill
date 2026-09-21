//go:build !windows

package tracklog

import (
	"golang.org/x/sys/unix"
	"os"
)

type fileLock struct{ file *os.File }

func openLock(path string) (*fileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return &fileLock{file: f}, nil
}

// Close releases the interprocess lock.
func (l *fileLock) Close() error {
	_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	return l.file.Close()
}
