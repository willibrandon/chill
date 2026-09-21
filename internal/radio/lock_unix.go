//go:build !windows

package radio

import (
	"os"

	"golang.org/x/sys/unix"
)

type libraryLock struct{ file *os.File }

func openLibraryLock(path string) (*libraryLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return &libraryLock{file: f}, nil
}

// Close releases the interprocess lock.
func (l *libraryLock) Close() error {
	_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	return l.file.Close()
}
