//go:build windows

package radio

import (
	"os"

	"golang.org/x/sys/windows"
)

type libraryLock struct {
	file *os.File
	over windows.Overlapped
}

func openLibraryLock(path string) (*libraryLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	l := &libraryLock{file: f}
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &l.over); err != nil {
		f.Close()
		return nil, err
	}
	return l, nil
}

// Close releases the interprocess lock.
func (l *libraryLock) Close() error {
	_ = windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.over)
	return l.file.Close()
}
