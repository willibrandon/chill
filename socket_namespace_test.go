package main

import (
	"regexp"
	"testing"
)

// TestIPCNamespaceSuffix checks stable, safe endpoint isolation names.
func TestIPCNamespaceSuffix(t *testing.T) {
	t.Setenv(ipcNamespaceEnvironment, "")
	if suffix := ipcNamespaceSuffix(); suffix != "" {
		t.Fatalf("empty namespace suffix = %q", suffix)
	}
	t.Setenv(ipcNamespaceEnvironment, `capture/one\\pipe`)
	first := ipcNamespaceSuffix()
	if !regexp.MustCompile(`^-[0-9a-f]{16}$`).MatchString(first) {
		t.Fatalf("unsafe namespace suffix = %q", first)
	}
	if again := ipcNamespaceSuffix(); again != first {
		t.Fatalf("namespace suffix changed from %q to %q", first, again)
	}
	t.Setenv(ipcNamespaceEnvironment, "capture-two")
	if second := ipcNamespaceSuffix(); second == first {
		t.Fatalf("distinct namespaces shared suffix %q", first)
	}
}
