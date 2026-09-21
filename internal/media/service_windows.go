//go:build windows

package media

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	sOK          = uintptr(0)
	eNoInterface = uintptr(0x80004002)
	ePointer     = uintptr(0x80004003)
)

var (
	combase                = windows.NewLazySystemDLL("combase.dll")
	roInitialize           = combase.NewProc("RoInitialize")
	roUninitialize         = combase.NewProc("RoUninitialize")
	roGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	roActivateInstance     = combase.NewProc("RoActivateInstance")
	windowsCreateString    = combase.NewProc("WindowsCreateString")
	windowsDeleteString    = combase.NewProc("WindowsDeleteString")
	user32Media            = windows.NewLazySystemDLL("user32.dll")
	createWindowEx         = user32Media.NewProc("CreateWindowExW")
	destroyWindow          = user32Media.NewProc("DestroyWindow")
)

var (
	iidUnknown          = windows.GUID{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidAgileObject      = windows.GUID{Data1: 0x94ea2b94, Data2: 0xe9cc, Data3: 0x49e0, Data4: [8]byte{0xc0, 0xff, 0xee, 0x64, 0xca, 0x8f, 0x5b, 0x90}}
	iidTransportInterop = windows.GUID{Data1: 0xddb0472d, Data2: 0xc911, Data3: 0x4a1f, Data4: [8]byte{0x86, 0xd9, 0xdc, 0x3d, 0x71, 0xa9, 0x5f, 0x5a}}
	iidTransport        = windows.GUID{Data1: 0x99fa3ff4, Data2: 0x1742, Data3: 0x42a6, Data4: [8]byte{0x90, 0x2e, 0x08, 0x7d, 0x41, 0xf9, 0x65, 0xec}}
	iidTransport2       = windows.GUID{Data1: 0xea98d2f6, Data2: 0x7f3c, Data3: 0x4af2, Data4: [8]byte{0xa5, 0x86, 0x72, 0x88, 0x98, 0x08, 0xef, 0xb1}}
	iidMusic2           = windows.GUID{Data1: 0x00368462, Data2: 0x97d3, Data3: 0x44b9, Data4: [8]byte{0xb0, 0x0f, 0x00, 0x8a, 0xfc, 0xef, 0xaf, 0x18}}
	iidURIFactory       = windows.GUID{Data1: 0x44a9796f, Data2: 0x723e, Data3: 0x4fdf, Data4: [8]byte{0xa2, 0x18, 0x03, 0x3e, 0x75, 0xb0, 0xc0, 0x84}}
	iidStreamReference  = windows.GUID{Data1: 0x857309dc, Data2: 0x3fbf, Data3: 0x4e7d, Data4: [8]byte{0x98, 0x6f, 0xef, 0x3b, 0x1a, 0x07, 0xa9, 0x64}}
	iidButtonHandler    = windows.GUID{Data1: 0x0557e996, Data2: 0x7b23, Data3: 0x5bae, Data4: [8]byte{0xaa, 0x81, 0xea, 0x0d, 0x67, 0x11, 0x43, 0xa4}}
	iidPositionHandler  = windows.GUID{Data1: 0x44e34f15, Data2: 0xbdc0, Data3: 0x50a7, Data4: [8]byte{0xac, 0xe4, 0x39, 0xe9, 0x1f, 0xb7, 0x53, 0xf1}}
)

type nativeDelegate struct {
	vtable     *[4]uintptr
	iid        windows.GUID
	references atomic.Int32
	service    *Service
	position   bool
}

var (
	delegateRegistryMu      sync.Mutex
	delegateRegistry        = map[uintptr]*nativeDelegate{}
	delegateQueryCallback   = syscall.NewCallback(delegateQueryInterface)
	delegateAddRefCallback  = syscall.NewCallback(delegateAddRef)
	delegateReleaseCallback = syscall.NewCallback(delegateRelease)
	delegateInvokeCallback  = syscall.NewCallback(delegateInvoke)
	delegateVTable          = [4]uintptr{delegateQueryCallback, delegateAddRefCallback, delegateReleaseCallback, delegateInvokeCallback}
)

// Service owns the Windows media transport session.
type Service struct {
	send             func(Command)
	updates          chan State
	closeChannel     chan struct{}
	closed           chan struct{}
	closeOnce        sync.Once
	transport        uintptr
	transport2       uintptr
	display          uintptr
	window           uintptr
	buttonDelegate   *nativeDelegate
	positionDelegate *nativeDelegate
	buttonToken      int64
	positionToken    int64
	last             State
}

func newDelegate(service *Service, iid windows.GUID, position bool) *nativeDelegate {
	d := &nativeDelegate{vtable: &delegateVTable, iid: iid, service: service, position: position}
	d.references.Store(1)
	delegateRegistryMu.Lock()
	delegateRegistry[uintptr(unsafe.Pointer(d))] = d
	delegateRegistryMu.Unlock()
	return d
}

func registeredDelegate(pointer uintptr) *nativeDelegate {
	delegateRegistryMu.Lock()
	defer delegateRegistryMu.Unlock()
	return delegateRegistry[pointer]
}

func delegateQueryInterface(this, iidPointer, resultPointer uintptr) uintptr {
	if resultPointer == 0 {
		return ePointer
	}
	result := (*uintptr)(pointerFromUintptr(resultPointer))
	*result = 0
	d := registeredDelegate(this)
	if d == nil || iidPointer == 0 {
		return eNoInterface
	}
	iid := *(*windows.GUID)(pointerFromUintptr(iidPointer))
	if iid != d.iid && iid != iidUnknown && iid != iidAgileObject {
		return eNoInterface
	}
	d.references.Add(1)
	*result = this
	return sOK
}

func delegateAddRef(this uintptr) uintptr {
	if d := registeredDelegate(this); d != nil {
		return uintptr(d.references.Add(1))
	}
	return 0
}

func delegateRelease(this uintptr) uintptr {
	d := registeredDelegate(this)
	if d == nil {
		return 0
	}
	remaining := d.references.Add(-1)
	if remaining == 0 {
		delegateRegistryMu.Lock()
		delete(delegateRegistry, this)
		delegateRegistryMu.Unlock()
	}
	return uintptr(remaining)
}

func delegateInvoke(this, _ uintptr, arguments uintptr) uintptr {
	d := registeredDelegate(this)
	if d == nil || arguments == 0 {
		return sOK
	}
	if d.position {
		var ticks int64
		callCOM(arguments, 6, uintptr(unsafe.Pointer(&ticks)))
		d.service.send(Command{Kind: SetPosition, Position: time.Duration(ticks * 100)})
		return sOK
	}
	var button int32
	callCOM(arguments, 6, uintptr(unsafe.Pointer(&button)))
	if command, ok := windowsButtonCommand(button); ok {
		d.service.send(command)
	}
	return sOK
}

func windowsButtonCommand(button int32) (Command, bool) {
	switch button {
	case 0:
		return Command{Kind: Play}, true
	case 1:
		return Command{Kind: Pause}, true
	case 2:
		return Command{Kind: Stop}, true
	case 4:
		return Command{Kind: Seek, Position: 30 * time.Second}, true
	case 5:
		return Command{Kind: Seek, Position: -15 * time.Second}, true
	case 6:
		return Command{Kind: Next}, true
	case 7:
		return Command{Kind: Previous}, true
	default:
		return Command{}, false
	}
}

func callCOM(object uintptr, slot int, arguments ...uintptr) uintptr {
	if object == 0 {
		return ePointer
	}
	vtable := *(*uintptr)(pointerFromUintptr(object))
	method := (*[64]uintptr)(pointerFromUintptr(vtable))[slot]
	callArguments := append([]uintptr{object}, arguments...)
	returnValue, _, _ := syscall.SyscallN(method, callArguments...)
	return returnValue
}

func pointerFromUintptr(value uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&value))
}

func hresultError(value uintptr, operation string) error {
	if int32(value) < 0 {
		return fmt.Errorf("%s: HRESULT 0x%08x", operation, uint32(value))
	}
	return nil
}

func hstring(value string) (uintptr, error) {
	utf16, err := windows.UTF16FromString(value)
	if err != nil {
		return 0, err
	}
	var result uintptr
	hr, _, _ := windowsCreateString.Call(uintptr(unsafe.Pointer(&utf16[0])), uintptr(len(utf16)-1), uintptr(unsafe.Pointer(&result)))
	runtime.KeepAlive(utf16)
	if err := hresultError(hr, "creating Windows string"); err != nil {
		return 0, err
	}
	return result, nil
}

func withHString(value string, function func(uintptr) uintptr) error {
	text, err := hstring(value)
	if err != nil {
		return err
	}
	defer windowsDeleteString.Call(text)
	return hresultError(function(text), "setting media metadata")
}

func activationFactory(className string, iid *windows.GUID) (uintptr, error) {
	name, err := hstring(className)
	if err != nil {
		return 0, err
	}
	defer windowsDeleteString.Call(name)
	var result uintptr
	hr, _, _ := roGetActivationFactory.Call(name, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&result)))
	return result, hresultError(hr, "opening media transport factory")
}

func activate(className string) (uintptr, error) {
	name, err := hstring(className)
	if err != nil {
		return 0, err
	}
	defer windowsDeleteString.Call(name)
	var result uintptr
	hr, _, _ := roActivateInstance.Call(name, uintptr(unsafe.Pointer(&result)))
	return result, hresultError(hr, "creating media timeline")
}

func queryInterface(object uintptr, iid *windows.GUID) (uintptr, error) {
	var result uintptr
	hr := callCOM(object, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&result)))
	return result, hresultError(hr, "querying media interface")
}

func releaseCOM(object uintptr) {
	if object != 0 {
		callCOM(object, 2)
	}
}

// New creates a Windows media transport session and its hidden owner window.
func New(send func(Command)) (*Service, error) {
	s := &Service{send: send, updates: make(chan State, 1), closeChannel: make(chan struct{}), closed: make(chan struct{})}
	ready := make(chan error, 1)
	go s.run(ready)
	if err := <-ready; err != nil {
		<-s.closed
		return nil, err
	}
	return s, nil
}

func (s *Service) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(s.closed)
	hr, _, _ := roInitialize.Call(1)
	if err := hresultError(hr, "initializing Windows Runtime"); err != nil {
		ready <- err
		return
	}
	defer roUninitialize.Call()
	className, _ := windows.UTF16PtrFromString("Static")
	windowName, _ := windows.UTF16PtrFromString("Chill media session")
	s.window, _, _ = createWindowEx.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(windowName)), 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if s.window == 0 {
		ready <- fmt.Errorf("creating media owner window")
		return
	}
	defer destroyWindow.Call(s.window)
	if err := s.open(); err != nil {
		ready <- err
		return
	}
	defer s.release()
	ready <- nil
	for {
		select {
		case state := <-s.updates:
			s.apply(state)
		case <-s.closeChannel:
			return
		}
	}
}

func (s *Service) open() error {
	interop, err := activationFactory("Windows.Media.SystemMediaTransportControls", &iidTransportInterop)
	if err != nil {
		return err
	}
	defer releaseCOM(interop)
	var transport uintptr
	if err := hresultError(callCOM(interop, 6, s.window, uintptr(unsafe.Pointer(&iidTransport)), uintptr(unsafe.Pointer(&transport))), "creating media transport"); err != nil {
		return err
	}
	s.transport = transport
	if s.transport2, err = queryInterface(transport, &iidTransport2); err != nil {
		s.transport2 = 0
	}
	for _, slot := range []int{11, 13, 15, 17, 25, 27} {
		if err := hresultError(callCOM(transport, slot, 1), "enabling media control"); err != nil {
			return err
		}
	}
	if err := hresultError(callCOM(transport, 8, uintptr(unsafe.Pointer(&s.display))), "opening media display"); err != nil {
		return err
	}
	s.buttonDelegate = newDelegate(s, iidButtonHandler, false)
	if err := hresultError(callCOM(transport, 32, uintptr(unsafe.Pointer(s.buttonDelegate)), uintptr(unsafe.Pointer(&s.buttonToken))), "registering media buttons"); err != nil {
		return err
	}
	if s.transport2 != 0 {
		s.positionDelegate = newDelegate(s, iidPositionHandler, true)
		if err := hresultError(callCOM(s.transport2, 13, uintptr(unsafe.Pointer(s.positionDelegate)), uintptr(unsafe.Pointer(&s.positionToken))), "registering media seek control"); err != nil {
			return err
		}
	}
	return hresultError(callCOM(transport, 11, 1), "enabling media session")
}

func (s *Service) release() {
	if s.transport != 0 && s.buttonToken != 0 {
		callCOM(s.transport, 33, uintptr(s.buttonToken))
	}
	if s.transport2 != 0 && s.positionToken != 0 {
		callCOM(s.transport2, 14, uintptr(s.positionToken))
	}
	if s.buttonDelegate != nil {
		delegateRelease(uintptr(unsafe.Pointer(s.buttonDelegate)))
	}
	if s.positionDelegate != nil {
		delegateRelease(uintptr(unsafe.Pointer(s.positionDelegate)))
	}
	if s.transport != 0 {
		callCOM(s.transport, 11, 0)
	}
	releaseCOM(s.display)
	releaseCOM(s.transport2)
	releaseCOM(s.transport)
}

func (s *Service) apply(state State) {
	status := uintptr(2)
	if state.Status == StatusPlaying {
		status = 3
	} else if state.Status == StatusPaused {
		status = 4
	}
	if state.Status != s.last.Status {
		callCOM(s.transport, 7, status)
	}
	callCOM(s.transport, 25, boolWord(state.CanGoPrevious))
	callCOM(s.transport, 27, boolWord(state.CanGoNext))
	callCOM(s.transport, 21, boolWord(state.Seekable))
	callCOM(s.transport, 23, boolWord(state.Seekable))
	if state.Status == StatusStopped {
		if s.last.Status != StatusStopped || s.last.Track != (Track{}) {
			callCOM(s.display, 16)
			callCOM(s.display, 17)
		}
		s.last = state
		s.last.Track = Track{}
		return
	}
	if state.Track != s.last.Track {
		callCOM(s.display, 7, 0)
		_ = withHString(state.Track.URL, func(value uintptr) uintptr { return callCOM(s.display, 9, value) })
		var music uintptr
		if int32(callCOM(s.display, 12, uintptr(unsafe.Pointer(&music)))) >= 0 && music != 0 {
			_ = withHString(state.Track.Title, func(value uintptr) uintptr { return callCOM(music, 7, value) })
			_ = withHString(state.Track.Artist, func(value uintptr) uintptr { return callCOM(music, 11, value) })
			music2, err := queryInterface(music, &iidMusic2)
			if err == nil {
				_ = withHString(state.Track.Album, func(value uintptr) uintptr { return callCOM(music2, 7, value) })
				releaseCOM(music2)
			}
			releaseCOM(music)
		}
		s.applyArtwork(state.Track.ArtURL)
		callCOM(s.display, 17)
	}
	if s.transport2 != 0 && (state.Seekable || s.last.Seekable) {
		rate := 0.0
		if state.Status == StatusPlaying {
			rate = 1
		}
		callCOM(s.transport2, 11, uintptrFromFloat64(rate))
		timeline, err := activate("Windows.Media.SystemMediaTransportControlsTimelineProperties")
		if err == nil {
			start, end, position := int64(0), int64(0), int64(0)
			if state.Seekable {
				end, position = state.Track.Duration.Nanoseconds()/100, state.Position.Nanoseconds()/100
			}
			callCOM(timeline, 7, uintptr(start))
			callCOM(timeline, 9, uintptr(end))
			callCOM(timeline, 11, uintptr(start))
			callCOM(timeline, 13, uintptr(end))
			callCOM(timeline, 15, uintptr(position))
			callCOM(s.transport2, 12, timeline)
			releaseCOM(timeline)
		}
	}
	s.last = state
}

func (s *Service) applyArtwork(rawURL string) {
	if rawURL == "" {
		callCOM(s.display, 11, 0)
		return
	}
	uriFactory, err := activationFactory("Windows.Foundation.Uri", &iidURIFactory)
	if err != nil {
		return
	}
	defer releaseCOM(uriFactory)
	text, err := hstring(rawURL)
	if err != nil {
		return
	}
	defer windowsDeleteString.Call(text)
	var uri uintptr
	if int32(callCOM(uriFactory, 6, text, uintptr(unsafe.Pointer(&uri)))) < 0 || uri == 0 {
		return
	}
	defer releaseCOM(uri)
	streamFactory, err := activationFactory("Windows.Storage.Streams.RandomAccessStreamReference", &iidStreamReference)
	if err != nil {
		return
	}
	defer releaseCOM(streamFactory)
	var reference uintptr
	if int32(callCOM(streamFactory, 7, uri, uintptr(unsafe.Pointer(&reference)))) < 0 || reference == 0 {
		return
	}
	defer releaseCOM(reference)
	callCOM(s.display, 11, reference)
}

func uintptrFromFloat64(value float64) uintptr { return *(*uintptr)(unsafe.Pointer(&value)) }

func boolWord(value bool) uintptr {
	if value {
		return 1
	}
	return 0
}

// Run executes work while the media service uses its own event thread.
func Run(_ *Service, work func() error) error { return work() }

// Update publishes a complete playback snapshot.
func (s *Service) Update(state State) {
	if s == nil {
		return
	}
	select {
	case s.updates <- state:
	default:
		select {
		case <-s.updates:
		default:
		}
		s.updates <- state
	}
}

// Seeked publishes a playhead discontinuity through the next state update.
func (s *Service) Seeked(_ time.Duration) {}

// Close disables the media transport session and releases its owner window.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() { close(s.closeChannel); <-s.closed })
}
