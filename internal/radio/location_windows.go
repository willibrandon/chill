//go:build windows

package radio

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var getUserDefaultLocaleName = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")

func platformCountry() string {
	var locale [85]uint16
	length, _, _ := getUserDefaultLocaleName.Call(uintptr(unsafe.Pointer(&locale[0])), uintptr(len(locale)))
	if length == 0 {
		return ""
	}
	return countryFromLocale(windows.UTF16ToString(locale[:]))
}
