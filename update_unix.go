//go:build !windows

package main

import "os"

func replaceUpdate(staged, target string) error {
	return os.Rename(staged, target)
}

func cleanupUpdateBackups() {}
