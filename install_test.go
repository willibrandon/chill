package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPackageInstallDetection checks package-manager executable paths.
func TestPackageInstallDetection(t *testing.T) {
	for _, tt := range []struct{ path, command string }{
		{"/opt/homebrew/Cellar/chill/0.5.0/bin/chill", "brew upgrade willibrandon/tap/chill"},
		{"/home/linuxbrew/.linuxbrew/Cellar/chill/0.5.0/bin/chill", "brew upgrade willibrandon/tap/chill"},
		{`C:\Users\name\scoop\apps\chill\current\chill.exe`, "scoop update chill"},
		{`D:\custom\apps\chill\0.5.0\chill.exe`, "scoop update chill"},
		{"/usr/local/bin/chill", ""},
		{"/home/name/go/bin/chill", ""},
		{"/opt/homebrew/bin/chill", ""}, // resolve the symlink first
		{"/opt/homebrew/Cellar/chilly/0.5.0/bin/chill", ""},
	} {
		if got := packageUpdateCommand(tt.path); got != tt.command {
			t.Errorf("%s: %q, want %q", tt.path, got, tt.command)
		}
	}
}

// TestPackageUpdateDoesNotDownloadOrReplace checks managed installs use their package manager.
func TestPackageUpdateDoesNotDownloadOrReplace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Cellar", "chill", "0.5.0", "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "chill")
	if err := os.WriteFile(path, []byte("original"), 0755); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("package update attempted a download") }))
	defer server.Close()
	u := releaseUpdater{client: server.Client(), url: server.URL}
	_, changed, err := u.install(path, "v0.5.0")
	if changed || err == nil || !strings.Contains(err.Error(), "brew upgrade") {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatal("modified package-managed binary")
	}
}
