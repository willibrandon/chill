//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const deepLinkInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDisplayName</key><string>Chill</string>
  <key>CFBundleExecutable</key><string>chill-url-handler</string>
  <key>CFBundleIdentifier</key><string>com.willibrandon.chill.link</string>
  <key>CFBundleName</key><string>Chill</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>CFBundleURLTypes</key>
  <array><dict>
    <key>CFBundleURLName</key><string>com.willibrandon.chill.link</string>
    <key>CFBundleURLSchemes</key><array><string>chill</string></array>
  </dict></array>
  <key>LSBackgroundOnly</key><true/>
  <key>LSUIElement</key><true/>
</dict>
</plist>
`

func deepLinkAppPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Applications", "Chill URL Handler.app")
}

func registerDeepLinks() (string, error) {
	if !nativeDeepLinkHandlerAvailable() {
		return "", errors.New("register chill links: native Apple-event support is unavailable in this build")
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if located, locateErr := exec.LookPath(os.Args[0]); locateErr == nil {
		if absolute, absoluteErr := filepath.Abs(located); absoluteErr == nil {
			executable = absolute
		}
	}
	app := deepLinkAppPath()
	if app == "" || app == "." || app == string(filepath.Separator) {
		return "", fmt.Errorf("could not determine the URL handler path")
	}
	if err := os.MkdirAll(filepath.Dir(app), 0700); err != nil {
		return "", err
	}
	if err := os.RemoveAll(app); err != nil {
		return "", err
	}
	contents := filepath.Join(app, "Contents")
	macOS := filepath.Join(contents, "MacOS")
	if err := os.MkdirAll(macOS, 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(deepLinkInfoPlist), 0600); err != nil {
		return "", err
	}
	if err := os.Symlink(executable, filepath.Join(macOS, "chill-url-handler")); err != nil {
		return "", err
	}
	lsregister := "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
	if output, err := exec.Command(lsregister, "-f", app).CombinedOutput(); err != nil {
		return "", fmt.Errorf("register chill links: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return "registered chill:// links", nil
}

func unregisterDeepLinks() (string, error) {
	app := deepLinkAppPath()
	if _, err := os.Stat(app); os.IsNotExist(err) {
		return "chill:// links are not registered", nil
	}
	lsregister := "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
	_, _ = exec.Command(lsregister, "-u", app).CombinedOutput()
	if err := os.RemoveAll(app); err != nil {
		return "", err
	}
	return "unregistered chill:// links", nil
}

func deepLinkRegistrationStatus() (bool, string) {
	app := deepLinkAppPath()
	if _, err := os.Stat(filepath.Join(app, "Contents", "Info.plist")); err == nil {
		if _, err := os.Stat(filepath.Join(app, "Contents", "MacOS", "chill-url-handler")); err == nil {
			return true, app
		}
	}
	return false, app
}
