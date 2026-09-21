//go:build linux

// Package notify presents optional track-change notifications through the
// host desktop's notification service.
package notify

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

// Show presents a transient track-change notification.
func Show(title, body, artwork string) error {
	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("desktop notification: %w", err)
	}
	defer connection.Close()
	hints := map[string]dbus.Variant{"desktop-entry": dbus.MakeVariant("chill")}
	if artwork != "" {
		hints["image-path"] = dbus.MakeVariant(artwork)
	}
	call := connection.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications").Call(
		"org.freedesktop.Notifications.Notify", 0, "Chill", uint32(0), "", title, body,
		[]string{}, hints, int32(6000),
	)
	return call.Err
}
