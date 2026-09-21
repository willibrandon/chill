//go:build windows

package notify

import (
	"testing"
	"unsafe"
)

// TestWindowsNativeStructureSizes verifies the 64-bit COM ABI layouts.
func TestWindowsNativeStructureSizes(t *testing.T) {
	if unsafe.Sizeof(propertyKey{}) != 20 {
		t.Fatalf("PROPERTYKEY size = %d", unsafe.Sizeof(propertyKey{}))
	}
	if unsafe.Sizeof(propertyVariant{}) != 24 {
		t.Fatalf("PROPVARIANT size = %d", unsafe.Sizeof(propertyVariant{}))
	}
}
