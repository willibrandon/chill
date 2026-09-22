//go:build windows

package main

import (
	"strings"
	"testing"
)

// TestNamedPipeUsesIPCNamespace checks recordings cannot collide with the user's daemon.
func TestNamedPipeUsesIPCNamespace(t *testing.T) {
	t.Setenv(ipcNamespaceEnvironment, "")
	base, baseSecurity, err := namedPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(ipcNamespaceEnvironment, `vhs/private\\pipe`)
	isolated, isolatedSecurity, err := namedPipe()
	if err != nil {
		t.Fatal(err)
	}
	if isolated == base || !strings.HasPrefix(isolated, base+"-") {
		t.Fatalf("isolated pipe %q did not extend %q", isolated, base)
	}
	if strings.Contains(isolated, "vhs") || strings.Contains(isolated, "private") {
		t.Fatalf("isolated pipe exposed namespace text: %q", isolated)
	}
	if isolatedSecurity != baseSecurity {
		t.Fatal("IPC namespace changed the owner-only security descriptor")
	}
}
