//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
)

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
