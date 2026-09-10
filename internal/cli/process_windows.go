package cli

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

// A job owns descendants too; closing it terminates the whole service tree.
var kernel = syscall.NewLazyDLL("kernel32.dll")
var createJob = kernel.NewProc("CreateJobObjectW")
var setJob = kernel.NewProc("SetInformationJobObject")
var assignJob = kernel.NewProc("AssignProcessToJobObject")

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008 | 0x00000200} // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
}

func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= 0x08000000 // CREATE_NO_WINDOW: no console to flash for background commands.
}

func lockFile(f *os.File) error {
	var overlapped syscall.Overlapped
	ok, _, err := kernel.NewProc("LockFileEx").Call(f.Fd(), 0x3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		return err
	}
	return nil
}

func stopSignals() []os.Signal { return []os.Signal{os.Interrupt} }

type jobLimits struct {
	ProcessTime, JobTime                                       int64
	Flags                                                      uint32
	MinWorkingSet, MaxWorkingSet                               uintptr
	ActiveProcesses                                            uint32
	Affinity                                                   uintptr
	Priority, Scheduling                                       uint32
	IO                                                         [6]uint64
	ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory uintptr
}

func launch(cmd *exec.Cmd) (func() error, error) {
	job, _, err := createJob.Call(0, 0)
	if job == 0 {
		return nil, err
	}
	closeJob := func() error { return syscall.CloseHandle(syscall.Handle(job)) }
	limits := jobLimits{Flags: 0x2000} // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	ok, _, err := setJob.Call(job, 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits))
	if ok == 0 {
		closeJob()
		return nil, err
	}
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		closeJob()
		return nil, err
	}
	h, err := syscall.OpenProcess(0x0100|0x0001, false, uint32(cmd.Process.Pid))
	if err == nil {
		ok, _, err = assignJob.Call(job, uintptr(h))
		syscall.CloseHandle(h)
		if ok != 0 {
			err = nil
		}
	}
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		closeJob()
		return nil, fmt.Errorf("assign process job: %w", err)
	}
	var once sync.Once
	var stopErr error
	return func() error { once.Do(func() { stopErr = closeJob() }); return stopErr }, nil
}
