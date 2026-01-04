//go:build windows

package main

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procSuspendThread     = kernel32.NewProc("SuspendThread")
	procResumeThread      = kernel32.NewProc("ResumeThread")
	procCreateToolhelp32  = kernel32.NewProc("CreateToolhelp32Snapshot")
	procThread32First     = kernel32.NewProc("Thread32First")
	procThread32Next      = kernel32.NewProc("Thread32Next")
)

const (
	TH32CS_SNAPTHREAD = 0x00000004
)

type THREADENTRY32 struct {
	Size           uint32
	Usage          uint32
	ThreadID       uint32
	OwnerProcessID uint32
	BasePri        int32
	DeltaPri       int32
	Flags          uint32
}

// pauseProcess suspends all threads in the process.
func pauseProcess(p *os.Process) error {
	return forEachThread(uint32(p.Pid), func(threadID uint32) error {
		return suspendThread(threadID)
	})
}

// resumeProcess resumes all threads in the process.
func resumeProcess(p *os.Process) error {
	return forEachThread(uint32(p.Pid), func(threadID uint32) error {
		return resumeThread(threadID)
	})
}

// forEachThread iterates over all threads belonging to the given process.
func forEachThread(pid uint32, fn func(threadID uint32) error) error {
	snapshot, _, err := procCreateToolhelp32.Call(TH32CS_SNAPTHREAD, 0)
	if snapshot == uintptr(windows.InvalidHandle) {
		return err
	}
	defer windows.CloseHandle(windows.Handle(snapshot))

	var entry THREADENTRY32
	entry.Size = uint32(unsafe.Sizeof(entry))

	ret, _, err := procThread32First.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return err
	}

	for {
		if entry.OwnerProcessID == pid {
			if err := fn(entry.ThreadID); err != nil {
				return err
			}
		}

		ret, _, err = procThread32Next.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}

	return nil
}

// suspendThread suspends a single thread by its ID.
func suspendThread(threadID uint32) error {
	handle, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, threadID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)

	ret, _, err := procSuspendThread.Call(uintptr(handle))
	if ret == 0xFFFFFFFF {
		return err
	}
	return nil
}

// resumeThread resumes a single suspended thread by its ID.
func resumeThread(threadID uint32) error {
	handle, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, threadID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)

	ret, _, err := procResumeThread.Call(uintptr(handle))
	if ret == 0xFFFFFFFF {
		return err
	}
	return nil
}
