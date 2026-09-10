package cli

import (
	"os"
	"syscall"
	"unsafe"
)

func lockState(f *os.File) error {
	var overlapped syscall.Overlapped
	ok, _, err := syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx").Call(f.Fd(), 2, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		return err
	}
	return nil
}
