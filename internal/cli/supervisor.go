package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Keep lock files in place: unlinking a locked file permits a second lock inode.
// OS locks are released on exit, including crashes, so stale PIDs need no killing.
func acquireLock(c config, name string) (*os.File, error) {
	dir := filepath.Join(c.root, ".flip")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

type supervisorInfo struct {
	PID    int    `json:"pid"`
	Config string `json:"config"`
}

func controlRequest(c config, action, name, part string, timeout time.Duration) ([]byte, error) {
	token, err := os.ReadFile(filepath.Join(c.root, ".flip", "token"))
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]string{"Action": action, "Name": name, "Part": part})
	req, err := http.NewRequest("POST", "http://"+address(c.ControlPort), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("control HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(data))
	}
	return data, nil
}

func probeSupervisor(c config) (supervisorInfo, error) {
	var info supervisorInfo
	data, err := controlRequest(c, "supervisor-status", "", "", 500*time.Millisecond)
	if err != nil {
		return info, err
	}
	if err = json.Unmarshal(data, &info); err != nil {
		return info, err
	}
	if info.Config != c.path {
		return info, fmt.Errorf("supervisor uses another config: %s", info.Config)
	}
	return info, nil
}

func ensureSupervisor(c config) error {
	if _, err := probeSupervisor(c); err == nil {
		return nil
	}
	deadline := time.Now().Add(10 * time.Second)
	var lock *os.File
	var err error
	for {
		lock, err = acquireLock(c, "startup")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("supervisor startup lock: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer lock.Close()
	if _, err = probeSupervisor(c); err == nil {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(c.root, ".flip", "supervisor.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(exe, "-config", c.path, "serve")
	cmd.Dir = c.root
	cmd.Stdout, cmd.Stderr = log, log
	detach(cmd)
	if err = cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		if _, err = probeSupervisor(c); err == nil {
			return nil
		}
		select {
		case exitErr := <-done:
			// A foreground serve may have won the lock between our probes.
			if _, err = probeSupervisor(c); err == nil {
				return nil
			}
			return fmt.Errorf("supervisor exited (%v); inspect %s: %w", exitErr, logPath, err)
		default:
		}
		if time.Now().After(deadline) {
			// Only terminate the child this invocation created, never a stored PID.
			cmd.Process.Kill()
			<-done
			return fmt.Errorf("supervisor startup timed out; inspect %s: %w", logPath, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func stopSupervisor(c config) error {
	if _, err := probeSupervisor(c); err != nil {
		return fmt.Errorf("supervisor unavailable: %w", err)
	}
	if _, err := controlRequest(c, "supervisor-stop", "", "", 5*time.Second); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		lock, err := acquireLock(c, "supervisor")
		if err == nil {
			lock.Close()
			fmt.Println("Supervisor stopped; owned services stopped.")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("supervisor shutdown timed out: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
