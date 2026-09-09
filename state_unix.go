//go:build !windows

package main

import (
	"os"
	"syscall"
)

func lockState(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
