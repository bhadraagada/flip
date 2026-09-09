//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func stopSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }

func launch(cmd *exec.Cmd) (func() error, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var once sync.Once
	var stopErr error
	return func() error {
		once.Do(func() {
			stopErr = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(stopErr, syscall.ESRCH) {
				stopErr = nil
			}
		})
		return stopErr
	}, nil
}
