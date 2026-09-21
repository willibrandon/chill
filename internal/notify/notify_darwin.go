//go:build darwin && cgo

// Package notify presents optional track-change notifications through the
// host desktop's notification service.
package notify

/*
#cgo CFLAGS: -x objective-c -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Foundation

#include <stdlib.h>
#import <Foundation/Foundation.h>

static int chillNotifyShow(const char *rawTitle, const char *rawBody) {
	@autoreleasepool {
		NSUserNotificationCenter *center = [NSUserNotificationCenter defaultUserNotificationCenter];
		if (center == nil) {
			return 0;
		}
		NSString *title = rawTitle ? [NSString stringWithUTF8String:rawTitle] : @"";
		NSString *body = rawBody ? [NSString stringWithUTF8String:rawBody] : @"";
		NSUserNotification *notification = [[[NSUserNotification alloc] init] autorelease];
		notification.title = title;
		notification.informativeText = body;
		notification.identifier = @"willibrandon.chill.now-playing";
		notification.hasActionButton = NO;

		for (NSUserNotification *delivered in center.deliveredNotifications) {
			if ([delivered.identifier isEqualToString:notification.identifier]) {
				[center removeDeliveredNotification:delivered];
			}
		}
		[center deliverNotification:notification];
		return 1;
	}
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"unsafe"
)

const (
	notificationHelperArgument = "__chill_native_notification"
	notificationInfoPlist      = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleDisplayName</key><string>Chill</string>
<key>CFBundleExecutable</key><string>chill-notification-helper</string>
<key>CFBundleIdentifier</key><string>com.willibrandon.chill.notifications</string>
<key>CFBundleName</key><string>Chill</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>LSBackgroundOnly</key><true/>
<key>LSUIElement</key><true/>
</dict></plist>`
)

// Show presents a transient track-change notification through macOS.
func Show(title, body, _ string) error {
	if showNative(title, body) {
		return nil
	}
	helper, err := installNotificationHelper()
	if err != nil {
		return err
	}
	if err := exec.Command(helper, notificationHelperArgument, title, body).Run(); err != nil {
		return fmt.Errorf("native notification helper: %w", err)
	}
	return nil
}

// RunHelper handles the private invocation used by the bundled notification process.
func RunHelper(args []string) (bool, error) {
	if len(args) == 0 || filepath.Base(args[0]) != "chill-notification-helper" {
		return false, nil
	}
	if len(args) != 4 || args[1] != notificationHelperArgument {
		return true, nil
	}
	if !showNative(args[2], args[3]) {
		return true, errors.New("native notification center is unavailable")
	}
	return true, nil
}

func showNative(title, body string) bool {
	cTitle := C.CString(title)
	defer C.free(unsafe.Pointer(cTitle))
	cBody := C.CString(body)
	defer C.free(unsafe.Pointer(cBody))
	return C.chillNotifyShow(cTitle, cBody) != 0
}

func installNotificationHelper() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	return installNotificationHelperAt(cache, executable)
}

func installNotificationHelperAt(cache, executable string) (string, error) {
	app := filepath.Join(cache, "chill", "Chill Notifications.app")
	helper := filepath.Join(app, "Contents", "MacOS", "chill-notification-helper")
	if target, linkErr := filepath.EvalSymlinks(helper); linkErr == nil && target == executable {
		return helper, nil
	}
	if app == "" || app == "." || app == string(filepath.Separator) {
		return "", errors.New("could not determine the notification helper path")
	}
	if err := os.RemoveAll(app); err != nil {
		return "", err
	}
	contents := filepath.Join(app, "Contents")
	if err := os.MkdirAll(filepath.Dir(helper), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(notificationInfoPlist), 0600); err != nil {
		return "", err
	}
	if err := os.Symlink(executable, helper); err != nil {
		return "", err
	}
	return helper, nil
}
