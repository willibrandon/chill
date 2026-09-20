//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procSuspendThread    = kernel32.NewProc("SuspendThread")
	procResumeThread     = kernel32.NewProc("ResumeThread")
	procCreateToolhelp32 = kernel32.NewProc("CreateToolhelp32Snapshot")
	procThread32First    = kernel32.NewProc("Thread32First")
	procThread32Next     = kernel32.NewProc("Thread32Next")
)

const (
	// TH32CS_SNAPTHREAD requests thread records in a Toolhelp snapshot.
	TH32CS_SNAPTHREAD = 0x00000004
)

// THREADENTRY32 matches the Windows Toolhelp thread enumeration structure.
type THREADENTRY32 struct {
	// Size is the structure size in bytes required by the Windows API.
	Size uint32
	// Usage is reserved by Windows.
	Usage uint32
	// ThreadID identifies the enumerated thread.
	ThreadID uint32
	// OwnerProcessID identifies the process owning the thread.
	OwnerProcessID uint32
	// BasePri is the thread's base priority.
	BasePri int32
	// DeltaPri is reserved by Windows.
	DeltaPri int32
	// Flags is reserved by Windows.
	Flags uint32
}

// JOBOBJECT_BASIC_PROCESS_ID_LIST with room for far more processes than an
// mpv tree will ever have.
type JOBOBJECT_BASIC_PROCESS_ID_LIST struct {
	// NumberOfAssignedProcesses is the total number of processes in the job.
	NumberOfAssignedProcesses uint32
	// NumberOfProcessIdsInList counts valid entries in ProcessIdList.
	NumberOfProcessIdsInList uint32
	// ProcessIdList holds the process identifiers returned by Windows.
	ProcessIdList [64]uintptr
}

// processTree is a process and all of its descendants, tracked with a job
// object. On Windows "mpv" usually resolves to a launcher (mpv.com, or a
// scoop/chocolatey shim) that runs the real player as a child process, so
// acting on the launcher alone leaves the music playing.
type processTree struct {
	job windows.Handle
}

// startInTree starts cmd inside a new job object so that everything it
// spawns can be paused, resumed, and killed together.
func startInTree(cmd *exec.Cmd) (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}

	// Take the whole tree down if the daemon dies without stopping it.
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}

	// Start suspended so the process can't spawn children before it joins the job.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}

	t := &processTree{job: job}
	err = t.assign(uint32(cmd.Process.Pid))
	if err == nil {
		err = t.resume()
	}
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		windows.CloseHandle(job)
		return nil, err
	}
	return t, nil
}

// assign adds the process to the job.
func (t *processTree) assign(pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)

	return windows.AssignProcessToJobObject(t.job, handle)
}

// pause suspends all threads of every process in the tree.
func (t *processTree) pause() error {
	return t.forEachThread(func(threadID uint32) error {
		return suspendThread(threadID)
	})
}

// resume resumes all threads of every process in the tree.
func (t *processTree) resume() error {
	return t.forEachThread(func(threadID uint32) error {
		return resumeThread(threadID)
	})
}

// kill terminates every process in the tree and releases the job.
func (t *processTree) kill() error {
	err := windows.TerminateJobObject(t.job, 1)
	windows.CloseHandle(t.job)
	return err
}

// pids returns the IDs of the processes currently in the job.
func (t *processTree) pids() (map[uint32]bool, error) {
	var list JOBOBJECT_BASIC_PROCESS_ID_LIST
	err := windows.QueryInformationJobObject(t.job, windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&list)), uint32(unsafe.Sizeof(list)), nil)
	if err != nil && err != windows.ERROR_MORE_DATA {
		return nil, err
	}

	pids := make(map[uint32]bool)
	for _, pid := range list.ProcessIdList[:list.NumberOfProcessIdsInList] {
		pids[uint32(pid)] = true
	}
	return pids, nil
}

// forEachThread iterates over all threads belonging to the processes in the tree.
func (t *processTree) forEachThread(fn func(threadID uint32) error) error {
	pids, err := t.pids()
	if err != nil {
		return err
	}

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
		if pids[entry.OwnerProcessID] {
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
	if err == windows.ERROR_INVALID_PARAMETER {
		return nil // thread exited after the snapshot was taken
	}
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
	if err == windows.ERROR_INVALID_PARAMETER {
		return nil // thread exited after the snapshot was taken
	}
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
