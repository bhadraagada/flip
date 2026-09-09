package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type process struct {
	cmd      *exec.Cmd
	done     chan struct{}
	stopTree func() error
}

func (p *process) running() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}
func (p *process) stop() error {
	if p == nil {
		return nil
	}
	err := p.stopTree()
	select {
	case <-p.done:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("process %d did not stop: %v", p.cmd.Process.Pid, err)
	}
}
func address(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }

func start(ctx context.Context, s service, logPath string) (*process, error) {
	l, err := net.Listen("tcp", address(s.Port))
	if err != nil {
		return nil, fmt.Errorf("port %d is occupied: %w", s.Port, err)
	}
	l.Close()
	args := make([]string, len(s.Command))
	for i, a := range s.Command {
		args[i] = strings.ReplaceAll(a, "{port}", strconv.Itoa(s.Port))
	}
	if strings.ContainsAny(args[0], `/\`) && !filepath.IsAbs(args[0]) {
		args[0] = filepath.Join(s.Dir, args[0])
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = s.Dir
	cmd.Env = os.Environ()
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+strings.ReplaceAll(v, "{port}", strconv.Itoa(s.Port)))
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	cmd.Stdout = f
	cmd.Stderr = f
	stop, err := launch(cmd)
	if err != nil {
		f.Close()
		return nil, err
	}
	p := &process{cmd: cmd, done: make(chan struct{}), stopTree: stop}
	go func() { cmd.Wait(); f.Close(); close(p.done) }()
	client := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+address(s.Port)+s.Health, nil)
		resp, e := client.Do(req)
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 && p.running() {
				return p, nil
			}
		}
		select {
		case <-p.done:
			p.stopTree()
			return nil, fmt.Errorf("process exited; see %s", logPath)
		case <-ctx.Done():
			p.stop()
			return nil, fmt.Errorf("readiness failed: %w; see %s", ctx.Err(), logPath)
		case <-ticker.C:
		}
	}
}
