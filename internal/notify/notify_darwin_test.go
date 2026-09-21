//go:build darwin && cgo

package notify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNotificationHelperBundleIsValid checks the identity used by standalone binaries.
func TestNotificationHelperBundleIsValid(t *testing.T) {
	command := exec.Command("/usr/bin/plutil", "-lint", "-")
	command.Stdin = strings.NewReader(notificationInfoPlist)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Info.plist: %v: %s", err, output)
	}
	executable := filepath.Join(t.TempDir(), "chill")
	if err := os.WriteFile(executable, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	helper, err := installNotificationHelperAt(t.TempDir(), executable)
	if err != nil {
		t.Fatal(err)
	}
	target, err := filepath.EvalSymlinks(helper)
	expected, expectedErr := filepath.EvalSymlinks(executable)
	if err != nil || expectedErr != nil || target != expected {
		t.Fatalf("helper target = %q, %v", target, err)
	}
	if handled, err := RunHelper([]string{helper}); !handled || err != nil {
		t.Fatalf("helper activation = %t, %v", handled, err)
	}
}
