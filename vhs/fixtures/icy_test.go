package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestIsolatedEnvironmentReplacesIPCNamespace checks recordings cannot inherit
// or omit the endpoint namespace used by the child daemon.
func TestIsolatedEnvironmentReplacesIPCNamespace(t *testing.T) {
	t.Setenv(ipcNamespaceEnvironment, "normal-session")
	t.Setenv("NO_COLOR", "1")
	root := filepath.Join(t.TempDir(), "recording-session")
	environment, _ := isolatedEnvironment(root)
	want := ipcNamespaceEnvironment + "=" + filepath.Base(root)
	count := 0
	for _, entry := range environment {
		if entry == want {
			count++
		}
		if strings.HasPrefix(entry, "NO_COLOR=") || entry == ipcNamespaceEnvironment+"=normal-session" {
			t.Fatalf("recording inherited %q", entry)
		}
	}
	if count != 1 {
		t.Fatalf("recording namespace appeared %d times in environment", count)
	}
}
