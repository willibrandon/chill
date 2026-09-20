//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Windows permits renaming a running executable, but cannot overwrite it.
// Keep a uniquely named backup until the old client and daemon have exited.
func replaceUpdate(staged, target string) error {
	backup, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".old-*")
	if err != nil {
		return err
	}
	backup.Close()
	os.Remove(backup.Name())
	if err := os.Rename(target, backup.Name()); err != nil {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		if restoreErr := os.Rename(backup.Name(), target); restoreErr != nil {
			return fmt.Errorf("%w; rollback failed: %v (previous binary: %s)", err, restoreErr, backup.Name())
		}
		return err
	}
	os.Remove(backup.Name()) // succeeds once no process has the old image open
	return nil
}

func cleanupUpdateBackups() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(exe), "."+filepath.Base(exe)+".old-*"))
	if err != nil {
		return
	}
	for _, file := range files {
		os.Remove(file) // an old REPL/daemon still using it keeps the file locked
	}
}
