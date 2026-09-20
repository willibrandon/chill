package main

import (
	"path/filepath"
	"strings"
)

// packageUpdateCommand recognizes package-owned executable locations, including
// custom Homebrew prefixes and Scoop roots. Resolve symlinks before calling it.
func packageUpdateCommand(executable string) string {
	path := strings.ToLower(strings.ReplaceAll(executable, "\\", "/"))
	if strings.Contains(path, "/cellar/chill/") {
		return "brew upgrade willibrandon/tap/chill"
	}
	if strings.Contains(path, "/apps/chill/") && strings.HasSuffix(path, "/chill.exe") {
		return "scoop update chill"
	}
	return ""
}

func resolvedExecutable(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
