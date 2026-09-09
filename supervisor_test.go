package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIdleTimer(t *testing.T) {
	m := newManager(context.Background(), config{Worktrees: map[string]worktree{"one": {}}})
	a := m.activity["one"]
	now := time.Now()
	a.last = now.Add(-time.Hour)
	m.reapIdle(now)
	if a.stopped {
		t.Fatal("idle shutdown must be opt-in")
	}
	m.c.IdleTimeoutSeconds = 1
	a.begin()
	m.reapIdle(now)
	if a.stopped {
		t.Fatal("active request was stopped")
	}
	a.end()
	m.reapIdle(now)
	if a.stopped {
		t.Fatal("request completion did not reset idle time")
	}
	m.reapIdle(now.Add(2 * time.Second))
	if !a.stopped || a.begin() {
		t.Fatal("expired worktree still accepts requests")
	}
}

// Exercise detached ownership through actual, short-lived CLI processes.
func TestDetachedSupervisor(t *testing.T) {
	root := t.TempDir()
	bin := os.Getenv("FLIP_TEST_BINARY")
	if bin == "" {
		bin = filepath.Join(root, "flip")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
			t.Fatalf("build: %s: %v", out, err)
		}
	}
	exe, _ := os.Executable()
	no := false
	// Hold the sockets together while choosing ports, then release before starting.
	var listeners []net.Listener
	for range 5 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, l)
	}
	c := config{Port: listeners[0].Addr().(*net.TCPAddr).Port, ControlPort: listeners[1].Addr().(*net.TCPAddr).Port, APIPrefix: "/api", TimeoutSeconds: 3, IdleTimeoutSeconds: 1,
		Worktrees: map[string]worktree{"one": {UI: service{Dir: root, Command: []string{exe, "-test.run=^TestHelperProcess$"}, Port: listeners[2].Addr().(*net.TCPAddr).Port, Health: "/health", RestartOnUse: &no, Env: map[string]string{"FLIP_TEST_HELPER": "1", "FLIP_TEST_PORT": "{port}"}}}}}
	one := c.Worktrees["one"]
	one.PreviewPort = listeners[3].Addr().(*net.TCPAddr).Port
	c.Worktrees["one"] = one
	two := one
	two.PreviewPort = 0
	two.UI.Port = listeners[4].Addr().(*net.TCPAddr).Port
	c.Worktrees["two"] = two
	for _, l := range listeners {
		l.Close()
	}
	os.WriteFile(filepath.Join(root, "response.txt"), []byte("detached"), 0600)
	path := filepath.Join(root, "flip.json")
	data, _ := json.Marshal(c)
	os.WriteFile(path, data, 0600)
	c, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cli := func(args ...string) (string, error) {
		out, err := exec.Command(bin, append([]string{"-config", path}, args...)...).CombinedOutput()
		return string(out), err
	}
	defer func() { cli("supervisor", "stop") }()
	if out, err := cli("supervisor", "status"); err == nil {
		t.Fatalf("absent status: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, ".flip", "token")); !os.IsNotExist(err) {
		t.Fatal("status started a supervisor")
	}
	os.MkdirAll(filepath.Join(root, ".flip"), 0700)
	os.WriteFile(filepath.Join(root, ".flip", "token"), []byte("stale-token"), 0600)
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if out, err := cli("one"); err != nil {
				t.Errorf("concurrent start: %s: %v", out, err)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	info, err := probeSupervisor(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("six CLI processes share detached supervisor PID %d", info.PID)
	if lock, err := acquireLock(c, "supervisor"); err == nil {
		lock.Close()
		t.Fatal("supervisor lock not held")
	}
	before, _ := os.ReadFile(filepath.Join(root, ".flip", "token"))
	if string(before) == "stale-token" {
		t.Fatal("stale token retained")
	}
	if out, err := cli("serve"); err == nil {
		t.Fatalf("duplicate serve succeeded: %s", out)
	}
	after, _ := os.ReadFile(filepath.Join(root, ".flip", "token"))
	if string(after) != string(before) {
		t.Fatal("duplicate serve changed token")
	}
	url := "http://" + address(c.Port)
	resp, err := http.Get(url + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	time.Sleep(1500 * time.Millisecond)
	if out, err := cli("status"); err != nil || !strings.Contains(out, "running:") {
		t.Fatalf("active stream reaped: %s %v", out, err)
	}
	conn, err := net.Dial("tcp", address(c.Worktrees["one"].PreviewPort))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprint(conn, "GET /socket HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	reader := bufio.NewReader(conn)
	upgrade, err := http.ReadResponse(reader, nil)
	if err != nil || upgrade.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", upgrade, err)
	}
	if out, err := cli("two"); err != nil {
		t.Fatal(out, err)
	}
	resp.Body.Close() // The socket alone must keep the worktree alive.
	time.Sleep(1500 * time.Millisecond)
	if out, err := cli("status"); err != nil || !strings.Contains(out, "running:") {
		t.Fatalf("active socket reaped: %s %v", out, err)
	}
	fmt.Fprint(conn, "ping")
	b := make([]byte, 4)
	if _, err := io.ReadFull(reader, b); err != nil || string(b) != "ping" {
		t.Fatalf("socket: %q %v", b, err)
	}
	conn.Close()
	deadline := time.Now().Add(4 * time.Second)
	for {
		out, err := cli("status")
		if err != nil {
			t.Fatal(out, err)
		}
		if !strings.Contains(out, "running:") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed socket prevented idle shutdown", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if again, err := probeSupervisor(c); err != nil || again.PID != info.PID {
		t.Fatal("idle shutdown stopped supervisor", again, err)
	}
	// Kill only our authenticated test supervisor after all its services are idle.
	owned, err := os.FindProcess(info.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := owned.Kill(); err != nil {
		t.Fatal(err)
	}
	owned.Release()
	deadline = time.Now().Add(3 * time.Second)
	for {
		lock, err := acquireLock(c, "supervisor")
		if err == nil {
			lock.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("crashed supervisor retained OS lock", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if out, err := cli("one"); err != nil {
		t.Fatalf("crash recovery: %s %v", out, err)
	}
	if out, err := cli("supervisor", "stop"); err != nil {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".flip", "token")); !os.IsNotExist(err) {
		t.Fatal("token survived stop", err)
	}
	for _, port := range []int{c.Port, c.ControlPort, c.Worktrees["one"].Services["ui"].Port, c.Worktrees["two"].Services["ui"].Port, c.Worktrees["one"].PreviewPort} {
		l, err := net.Listen("tcp", address(port))
		if err != nil {
			t.Fatalf("owned port %d survived shutdown: %v", port, err)
		}
		l.Close()
	}
	if out, err := cli("one"); err != nil {
		t.Fatalf("restart after shutdown: %s %v", out, err)
	}
	// A readiness failure through another CLI must stop its child on timeout.
	if out, err := cli("supervisor", "stop"); err != nil {
		t.Fatal(out, err)
	}
	c.TimeoutSeconds = 1
	one = c.Worktrees["one"]
	svc := one.Services["ui"]
	svc.Health = "/notready"
	one.Services["ui"] = svc
	c.Worktrees["one"] = one
	data, _ = json.Marshal(c)
	os.WriteFile(path, data, 0600)
	if out, err := cli("one"); err == nil || !strings.Contains(out, "context deadline exceeded") {
		t.Fatalf("readiness timeout: %s %v", out, err)
	}
	if out, err := cli("supervisor", "stop"); err != nil {
		t.Fatal(out, err)
	}
	// An occupied public port belongs to someone else; auto-start must leave it alone.
	occupied, err := net.Listen("tcp", address(c.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	os.WriteFile(filepath.Join(root, ".flip", "token"), []byte("stale-before-collision"), 0600)
	if out, err := cli("one"); err == nil {
		t.Fatalf("occupied public port accepted: %s", out)
	}
	token, _ := os.ReadFile(filepath.Join(root, ".flip", "token"))
	if string(token) != "stale-before-collision" {
		t.Fatal("failed startup modified token")
	}
	t.Log("stale recovery, stream/socket protection across preview switching, timeout and shutdown cleanup passed")
}
