//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func deepLinkDesktopPath() string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "applications", "chill-url.desktop")
}

func registerDeepLinks() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	path := deepLinkDesktopPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	body := "[Desktop Entry]\nType=Application\nName=chill\nNoDisplay=true\nExec=" + strconv.Quote(executable) + " %u\nMimeType=x-scheme-handler/chill;\nTerminal=false\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		return "", err
	}
	if command, err := exec.LookPath("xdg-mime"); err == nil {
		if output, commandErr := exec.Command(command, "default", filepath.Base(path), "x-scheme-handler/chill").CombinedOutput(); commandErr != nil {
			return "", fmt.Errorf("register chill links: %w: %s", commandErr, strings.TrimSpace(string(output)))
		}
	}
	if command, err := exec.LookPath("update-desktop-database"); err == nil {
		_, _ = exec.Command(command, filepath.Dir(path)).CombinedOutput()
	}
	return "registered chill:// links", nil
}

func unregisterDeepLinks() (string, error) {
	path := deepLinkDesktopPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return "unregistered chill:// links", nil
}

func deepLinkRegistrationStatus() (bool, string) {
	path := deepLinkDesktopPath()
	if _, err := os.Stat(path); err == nil {
		return true, path
	}
	return false, path
}
