//go:build darwin

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestDeepLinkBundlePlistIsValid keeps the native URL handler discoverable.
func TestDeepLinkBundlePlistIsValid(t *testing.T) {
	command := exec.Command("/usr/bin/plutil", "-lint", "-")
	command.Stdin = strings.NewReader(deepLinkInfoPlist)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Info.plist: %v: %s", err, output)
	}
}
