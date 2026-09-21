//go:build windows

// Package notify presents optional track-change notifications through the
// host desktop's notification service.
package notify

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// RunHelper is unused on Windows, where notifications use WinRT directly.
func RunHelper(_ []string) (bool, error) { return false, nil }

const (
	toastAUMID              = "willibrandon.chill"
	toastTag                = "now-playing"
	toastGroup              = "chill"
	rpcEChangedMode uintptr = 0x80010106
)

var (
	toastCombase                = windows.NewLazySystemDLL("combase.dll")
	toastRoInitialize           = toastCombase.NewProc("RoInitialize")
	toastRoUninitialize         = toastCombase.NewProc("RoUninitialize")
	toastRoGetActivationFactory = toastCombase.NewProc("RoGetActivationFactory")
	toastRoActivateInstance     = toastCombase.NewProc("RoActivateInstance")
	toastWindowsCreateString    = toastCombase.NewProc("WindowsCreateString")
	toastWindowsDeleteString    = toastCombase.NewProc("WindowsDeleteString")
	toastOle32                  = windows.NewLazySystemDLL("ole32.dll")
	toastCoCreateInstance       = toastOle32.NewProc("CoCreateInstance")
	toastShell32                = windows.NewLazySystemDLL("shell32.dll")
	toastSetProcessAUMID        = toastShell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
)

var (
	iidToastManager = windows.GUID{Data1: 0x50ac103f, Data2: 0xd235, Data3: 0x4598, Data4: [8]byte{0xbb, 0xef, 0x98, 0xfe, 0x4d, 0x1a, 0x3a, 0xd4}}
	iidToastFactory = windows.GUID{Data1: 0x04124b20, Data2: 0x82c6, Data3: 0x4229, Data4: [8]byte{0xb1, 0x09, 0xfd, 0x9e, 0xd4, 0x66, 0x2b, 0x53}}
	iidToast2       = windows.GUID{Data1: 0x9dfb9fd1, Data2: 0x143a, Data3: 0x490e, Data4: [8]byte{0x90, 0xbf, 0xb9, 0xfb, 0xa7, 0x13, 0x2d, 0xe7}}
	iidXMLDocument  = windows.GUID{Data1: 0x6cd0e74e, Data2: 0xee65, Data3: 0x4489, Data4: [8]byte{0x9e, 0xbf, 0xca, 0x43, 0xe8, 0x7b, 0xa6, 0x37}}
	iidProperty     = windows.GUID{Data1: 0x629bdbc8, Data2: 0xd932, Data3: 0x4ff4, Data4: [8]byte{0x96, 0xb9, 0x8d, 0x96, 0xc5, 0xc1, 0xe8, 0x58}}

	clsidShellLink    = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidShellLink      = windows.GUID{Data1: 0x000214f9, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPropertyStore  = windows.GUID{Data1: 0x886d8eeb, Data2: 0x8cf2, Data3: 0x4446, Data4: [8]byte{0x8d, 0x02, 0xcd, 0xba, 0x1d, 0xbd, 0xcf, 0x99}}
	iidPersistFile    = windows.GUID{Data1: 0x0000010b, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	appUserModelIDKey = propertyKey{format: windows.GUID{Data1: 0x9f4c2855, Data2: 0x9f79, Data3: 0x4b39, Data4: [8]byte{0xa8, 0xd0, 0xe1, 0xd4, 0x2d, 0xe1, 0xd5, 0xf3}}, id: 5}
)

type propertyKey struct {
	format windows.GUID
	id     uint32
}

type propertyVariant struct {
	variantType uint16
	reserved1   uint16
	reserved2   uint16
	reserved3   uint16
	value       uintptr
	padding     uint64
}

var (
	identityMu    sync.Mutex
	identityReady bool
)

// Show presents a transient track-change notification using WinRT directly.
func Show(title, body, artwork string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := toastRoInitialize.Call(1)
	initialized := hr == 0 || hr == 1
	if !initialized && hr != rpcEChangedMode {
		return toastHRESULT(hr, "initializing Windows Runtime")
	}
	if initialized {
		defer toastRoUninitialize.Call()
	}
	if err := ensureToastIdentity(); err != nil {
		return err
	}

	manager, err := toastActivationFactory("Windows.UI.Notifications.ToastNotificationManager", &iidToastManager)
	if err != nil {
		return err
	}
	defer toastRelease(manager)

	aumid, err := toastHString(toastAUMID)
	if err != nil {
		return err
	}
	defer toastWindowsDeleteString.Call(aumid)
	var notifier uintptr
	if err := toastHRESULT(toastCall(manager, 7, aumid, uintptr(unsafe.Pointer(&notifier))), "creating toast notifier"); err != nil {
		return err
	}
	defer toastRelease(notifier)

	factory, err := toastActivationFactory("Windows.UI.Notifications.ToastNotification", &iidToastFactory)
	if err != nil {
		return err
	}
	defer toastRelease(factory)

	document, err := toastActivate("Windows.Data.Xml.Dom.XmlDocument")
	if err != nil {
		return err
	}
	defer toastRelease(document)
	documentIO, err := toastQueryInterface(document, &iidXMLDocument)
	if err != nil {
		return err
	}
	defer toastRelease(documentIO)
	xmlText, err := toastHString(buildToastXML(title, body, artwork))
	if err != nil {
		return err
	}
	defer toastWindowsDeleteString.Call(xmlText)
	if err := toastHRESULT(toastCall(documentIO, 6, xmlText), "loading toast XML"); err != nil {
		return err
	}

	var notification uintptr
	if err := toastHRESULT(toastCall(factory, 6, document, uintptr(unsafe.Pointer(&notification))), "creating toast notification"); err != nil {
		return err
	}
	defer toastRelease(notification)
	if err := setToastExpiration(notification, time.Now().Add(2*time.Minute)); err != nil {
		return err
	}
	if err := setToastIdentity(notification); err != nil {
		return err
	}
	return toastHRESULT(toastCall(notifier, 6, notification), "showing toast notification")
}

func setToastExpiration(notification uintptr, expires time.Time) error {
	factory, err := toastActivationFactory("Windows.Foundation.PropertyValue", &iidProperty)
	if err != nil {
		return err
	}
	defer toastRelease(factory)
	ticks := (expires.Unix() + 11644473600) * 10_000_000
	var reference uintptr
	if err := toastHRESULT(toastCall(factory, 21, uintptr(ticks), uintptr(unsafe.Pointer(&reference))), "creating toast expiration"); err != nil {
		return err
	}
	defer toastRelease(reference)
	return toastHRESULT(toastCall(notification, 7, reference), "setting toast expiration")
}

func setToastIdentity(notification uintptr) error {
	toast2, err := toastQueryInterface(notification, &iidToast2)
	if err != nil {
		return err
	}
	defer toastRelease(toast2)
	for _, value := range []struct {
		slot int
		text string
	}{{6, toastTag}, {8, toastGroup}} {
		text, err := toastHString(value.text)
		if err != nil {
			return err
		}
		hr := toastCall(toast2, value.slot, text)
		toastWindowsDeleteString.Call(text)
		if err := toastHRESULT(hr, "setting toast identity"); err != nil {
			return err
		}
	}
	return nil
}

func ensureToastIdentity() error {
	identityMu.Lock()
	defer identityMu.Unlock()
	if identityReady {
		return nil
	}
	aumid, err := windows.UTF16PtrFromString(toastAUMID)
	if err != nil {
		return err
	}
	// The shortcut association is what makes desktop toasts reliable. Setting
	// the process identifier too is useful when Show runs early enough in app
	// startup, but a late call must not prevent a correctly associated toast.
	toastSetProcessAUMID.Call(uintptr(unsafe.Pointer(aumid)))
	if err := createToastShortcut(); err != nil {
		return err
	}
	identityReady = true
	return nil
}

func createToastShortcut() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding Chill executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolving Chill executable: %w", err)
	}
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData, err = os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("finding Start Menu: %w", err)
		}
	}
	shortcut := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Chill.lnk")
	if err := os.MkdirAll(filepath.Dir(shortcut), 0o755); err != nil {
		return fmt.Errorf("creating Start Menu directory: %w", err)
	}

	var shellLink uintptr
	hr, _, _ := toastCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)), 0, 1,
		uintptr(unsafe.Pointer(&iidShellLink)), uintptr(unsafe.Pointer(&shellLink)),
	)
	if err := toastHRESULT(hr, "creating Start Menu shortcut"); err != nil {
		return err
	}
	defer toastRelease(shellLink)
	executableText, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return err
	}
	if err := toastHRESULT(toastCall(shellLink, 20, uintptr(unsafe.Pointer(executableText))), "setting shortcut target"); err != nil {
		return err
	}
	description, _ := windows.UTF16PtrFromString("Chill audio player")
	if err := toastHRESULT(toastCall(shellLink, 7, uintptr(unsafe.Pointer(description))), "setting shortcut description"); err != nil {
		return err
	}
	workingDirectory, _ := windows.UTF16PtrFromString(filepath.Dir(executable))
	if err := toastHRESULT(toastCall(shellLink, 9, uintptr(unsafe.Pointer(workingDirectory))), "setting shortcut directory"); err != nil {
		return err
	}

	propertyStore, err := toastQueryInterface(shellLink, &iidPropertyStore)
	if err != nil {
		return err
	}
	defer toastRelease(propertyStore)
	identifier, err := windows.UTF16FromString(toastAUMID)
	if err != nil {
		return err
	}
	variant := propertyVariant{variantType: 31, value: uintptr(unsafe.Pointer(&identifier[0]))}
	if err := toastHRESULT(toastCall(propertyStore, 6, uintptr(unsafe.Pointer(&appUserModelIDKey)), uintptr(unsafe.Pointer(&variant))), "setting shortcut application identifier"); err != nil {
		return err
	}
	commitErr := toastHRESULT(toastCall(propertyStore, 7), "saving shortcut application identifier")
	runtime.KeepAlive(identifier)
	if commitErr != nil {
		return commitErr
	}

	persistFile, err := toastQueryInterface(shellLink, &iidPersistFile)
	if err != nil {
		return err
	}
	defer toastRelease(persistFile)
	shortcutText, err := windows.UTF16PtrFromString(shortcut)
	if err != nil {
		return err
	}
	return toastHRESULT(toastCall(persistFile, 6, uintptr(unsafe.Pointer(shortcutText)), 1), "writing Start Menu shortcut")
}

func toastActivationFactory(className string, iid *windows.GUID) (uintptr, error) {
	name, err := toastHString(className)
	if err != nil {
		return 0, err
	}
	defer toastWindowsDeleteString.Call(name)
	var result uintptr
	hr, _, _ := toastRoGetActivationFactory.Call(name, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&result)))
	return result, toastHRESULT(hr, "opening Windows Runtime factory")
}

func toastActivate(className string) (uintptr, error) {
	name, err := toastHString(className)
	if err != nil {
		return 0, err
	}
	defer toastWindowsDeleteString.Call(name)
	var result uintptr
	hr, _, _ := toastRoActivateInstance.Call(name, uintptr(unsafe.Pointer(&result)))
	return result, toastHRESULT(hr, "creating Windows Runtime object")
}

func toastHString(value string) (uintptr, error) {
	utf16, err := windows.UTF16FromString(value)
	if err != nil {
		return 0, err
	}
	var result uintptr
	hr, _, _ := toastWindowsCreateString.Call(uintptr(unsafe.Pointer(&utf16[0])), uintptr(len(utf16)-1), uintptr(unsafe.Pointer(&result)))
	runtime.KeepAlive(utf16)
	return result, toastHRESULT(hr, "creating Windows string")
}

func toastQueryInterface(object uintptr, iid *windows.GUID) (uintptr, error) {
	var result uintptr
	hr := toastCall(object, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&result)))
	return result, toastHRESULT(hr, "querying Windows interface")
}

func toastCall(object uintptr, slot int, arguments ...uintptr) uintptr {
	if object == 0 {
		return 0x80004003
	}
	vtable := *(*uintptr)(toastPointerFromUintptr(object))
	method := (*[64]uintptr)(toastPointerFromUintptr(vtable))[slot]
	callArguments := append([]uintptr{object}, arguments...)
	result, _, _ := syscall.SyscallN(method, callArguments...)
	return result
}

func toastPointerFromUintptr(value uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&value))
}

func toastRelease(object uintptr) {
	if object != 0 {
		toastCall(object, 2)
	}
}

func toastHRESULT(value uintptr, operation string) error {
	if int32(value) < 0 {
		return fmt.Errorf("%s: HRESULT 0x%08x", operation, uint32(value))
	}
	return nil
}
