//go:build windows

package tracklog

import (
	"golang.org/x/sys/windows"
	"os"
)

type fileLock struct {
	file *os.File
	over windows.Overlapped
}

func openLock(path string) (*fileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	l := &fileLock{file: f}
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &l.over); err != nil {
		f.Close()
		return nil, err
	}
	return l, nil
}

// Close releases the interprocess lock.
func (l *fileLock) Close() error {
	_ = windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.over)
	return l.file.Close()
}
