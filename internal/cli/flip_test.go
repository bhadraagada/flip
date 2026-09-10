package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSingleServiceAndRestartPolicy(t *testing.T) {
	for _, role := range []string{"ui", "backend"} {
		t.Run(role, func(t *testing.T) {
			root := t.TempDir()
			exe, _ := os.Executable()
			os.WriteFile(filepath.Join(root, "response.txt"), []byte(role), 0600)
			no := false
			s := service{Dir: root, Command: []string{exe, "-test.run=^TestHelperProcess$"}, Port: freePort(t), Health: "/health", RestartOnUse: &no, Env: map[string]string{"FLIP_TEST_HELPER": "1", "FLIP_TEST_PORT": "{port}"}}
			w := worktree{}
			if role == "ui" {
				w.UI = s
			} else {
				w.Backend = s
			}
			// Validate through the public JSON configuration path as well as the manager.
			c := config{Port: freePort(t), ControlPort: freePort(t), APIPrefix: "/api", StripPrefix: true, TimeoutSeconds: 5, Worktrees: map[string]worktree{"one": w}}
			b, _ := json.Marshal(c)
			path := filepath.Join(root, "flip.json")
			os.WriteFile(path, b, 0600)
			c, err := readConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			m := newManager(context.Background(), c)
			defer m.close()
			if _, err := m.command("use", "one", ""); err != nil {
				t.Fatal(err)
			}
			proc := func() *process { return m.processes["one"][role] }
			old := proc().cmd.Process.Pid
			if _, err := m.command("use", "one", ""); err != nil {
				t.Fatal(err)
			}
			if proc().cmd.Process.Pid != old {
				t.Fatal("restart_on_use false ignored")
			}
			front := httptest.NewServer(m)
			defer front.Close()
			for _, path := range []string{"/", "/api/value", "/callback"} {
				resp, err := http.Get(front.URL + path)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				wantPath := path
				if role == "backend" && path == "/api/value" {
					wantPath = "/value"
				}
				if !strings.HasSuffix(string(body), ":"+wantPath) {
					t.Fatal("legacy prefix behavior changed", string(body))
				}

				if !strings.HasPrefix(string(body), role+":") {
					t.Fatal(string(body))
				}
			}
			if _, err := m.command("restart", "one", role); err != nil {
				t.Fatal(err)
			}
			if proc().cmd.Process.Pid == old {
				t.Fatal("explicit restart skipped")
			}
			yes := true
			w = m.c.Worktrees["one"]
			changed := w.Services[role]
			changed.RestartOnUse = &yes
			w.Services[role] = changed
			m.c.Worktrees["one"] = w
			old = proc().cmd.Process.Pid
			if _, err := m.command("use", "one", ""); err != nil {
				t.Fatal(err)
			}
			if proc().cmd.Process.Pid == old {
				t.Fatal("restart_on_use true ignored")
			}
		})
	}
}

func TestShorthand(t *testing.T) {
	for _, tc := range []struct{ in, want []string }{
		{[]string{"feature-a"}, []string{"use", "feature-a"}},
		{[]string{"status"}, []string{"status"}},
		{[]string{"use", "status"}, []string{"use", "status"}},
		{[]string{"restart", "a", "backend"}, []string{"restart", "a", "backend"}},
	} {
		got, err := normalizeCommand(tc.in)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatal(got, err)
		}
	}
	for _, args := range [][]string{nil, {"up"}, {"typo", "a"}, {"a", "b"}} {
		if _, err := normalizeCommand(args); err == nil {
			t.Fatal("accepted", args)
		}
	}
}

// Run the test binary as a real child server so lifecycle checks need no Python or Node.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("FLIP_TEST_HELPER") != "1" {
		return
	}
	if os.Getenv("FLIP_TEST_FAIL") == "1" {
		os.Exit(2)
	}
	if os.Getenv("FLIP_TEST_WORKER") == "1" {
		for {
			time.Sleep(time.Second)
		}
	}
	port, _ := strconv.Atoi(os.Getenv("FLIP_TEST_PORT"))
	data, err := os.ReadFile("response.txt")
	if err != nil {
		os.Exit(3)
	}
	err = http.ListenAndServe(address(port), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if r.Header.Get("Upgrade") == "websocket" {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
			rw.Flush()
			io.Copy(conn, rw)
			return
		}
		if r.URL.Path == "/notready" {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/health" {
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/spawn" {
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
			cmd.Env = append(os.Environ(), "FLIP_TEST_PORT="+r.URL.Query().Get("port"))
			if err := cmd.Start(); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			go cmd.Wait()
			w.WriteHeader(204)
			return
		}
		fmt.Fprintf(w, "%s:%d:%s", data, os.Getpid(), r.URL.Path)
	}))
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
func TestLifecycleAndRouting(t *testing.T) {
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	used := map[int]bool{}
	newService := func(label string) service {
		dir := filepath.Join(root, label)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "response.txt"), []byte(label), 0600); err != nil {
			t.Fatal(err)
		}
		port := freePort(t)
		for used[port] {
			port = freePort(t)
		}
		used[port] = true
		return service{Dir: dir, Command: []string{exe, "-test.run=^TestHelperProcess$"}, Port: port, Health: "/health", Env: map[string]string{"FLIP_TEST_HELPER": "1", "FLIP_TEST_PORT": "{port}"}}
	}
	c := config{Port: freePort(t), ControlPort: freePort(t), root: root, APIPrefix: "/api", TimeoutSeconds: 5, Worktrees: map[string]worktree{
		"a": {UI: newService("a-ui"), Backend: newService("a-backend")},
		"b": {UI: newService("b-ui"), Backend: newService("b-backend")},
	}}
	c = checkedConfig(t, root, c)
	m := newManager(context.Background(), c)
	defer m.close()
	front := httptest.NewServer(m)
	defer front.Close()
	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(front.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(b)
	}
	command := func(a, n, p string) {
		t.Helper()
		if _, err := m.command(a, n, p); err != nil {
			t.Fatal(err)
		}
	}
	if status, _ := get("/"); status != 503 {
		t.Fatalf("unselected status %d", status)
	}
	command("up", "a", "")
	old := m.processes["a"]["backend"].cmd.Process.Pid
	command("up", "a", "")
	if old != m.processes["a"]["backend"].cmd.Process.Pid {
		t.Fatal("up restarted running process")
	}
	command("use", "a", "")
	if old == m.processes["a"]["backend"].cmd.Process.Pid {
		t.Fatal("use did not restart backend")
	}
	for path, want := range map[string]string{"/": "a-ui:", "/oauth/callback": "a-ui:", "/api/items": "a-backend:", "/apiculture": "a-ui:"} {
		if _, body := get(path); !strings.HasPrefix(body, want) {
			t.Fatalf("%s: %s", path, body)
		}
	}
	command("use", "b", "")
	if _, body := get("/api"); !strings.HasPrefix(body, "b-backend:") {
		t.Fatal(body)
	}
	if !m.processes["a"]["backend"].running() {
		t.Fatal("switch stopped unrelated backend")
	}
	old = m.processes["b"]["backend"].cmd.Process.Pid
	if err := os.WriteFile(filepath.Join(c.Worktrees["b"].Services["backend"].Dir, "response.txt"), []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, body := get("/api"); !strings.HasPrefix(body, "b-backend:") {
		t.Fatal("running code unexpectedly reloaded", body)
	}
	command("restart", "b", "backend")
	if old == m.processes["b"]["backend"].cmd.Process.Pid {
		t.Fatal("restart kept PID")
	}
	if _, body := get("/api"); !strings.HasPrefix(body, "edited:") {
		t.Fatal("restart did not load edited code", body)
	}
	w := m.c.Worktrees["a"]
	w.Services["backend"].Env["FLIP_TEST_FAIL"] = "1"
	m.c.Worktrees["a"] = w
	if _, err := m.command("use", "a", ""); err == nil {
		t.Fatal("failed startup succeeded")
	}
	if m.active.Load().name != "b" {
		t.Fatal("failed startup changed selection")
	}
	command("down", "b", "")
	if status, _ := get("/"); status != 503 {
		t.Fatal(status)
	}
	m.close()
	for _, w := range c.Worktrees {
		for _, s := range w.Services {
			l, e := net.Listen("tcp", address(s.Port))
			if e != nil {
				t.Errorf("port still occupied: %v", e)
			} else {
				l.Close()
			}
		}
	}
}

func TestDescendantCleanupAndReadinessTimeout(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "response.txt"), []byte("child"), 0600)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := service{Dir: dir, Command: []string{exe, "-test.run=^TestHelperProcess$"}, Port: freePort(t), Health: "/health", Env: map[string]string{"FLIP_TEST_HELPER": "1", "FLIP_TEST_PORT": "{port}"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := start(ctx, s, filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop()
	childPort := freePort(t)
	resp, err := http.Get(fmt.Sprintf("http://%s/spawn?port=%d", address(s.Port), childPort))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, e := net.DialTimeout("tcp", address(childPort), 100*time.Millisecond)
		if e == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := p.stop(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		l, e := net.Listen("tcp", address(childPort))
		if e == nil {
			l.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child survived stop")
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.Health = "/notready"
	cancelled, stop := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer stop()
	if _, err := start(cancelled, s, filepath.Join(dir, "timeout.log")); err == nil {
		t.Fatal("cancelled startup succeeded")
	}
	l, err := net.Listen("tcp", address(s.Port))
	if err != nil {
		t.Fatal("cancelled process retained port", err)
	}
	l.Close()
}
func TestControlAuthentication(t *testing.T) {
	m := newManager(context.Background(), config{})
	for _, tc := range []struct {
		method, token, origin string
		want                  int
	}{{"POST", "", "", 403}, {"POST", "Bearer secret", "https://evil.example", 403}, {"GET", "Bearer secret", "", 403}, {"POST", "Bearer secret", "", 200}} {
		for _, action := range []string{"status", "supervisor-status", "supervisor-stop"} {
			cancelled := false
			m.cancel = func() { cancelled = true }
			r := httptest.NewRequest(tc.method, "http://localhost", strings.NewReader(fmt.Sprintf(`{"Action":%q}`, action)))
			r.Header.Set("Authorization", tc.token)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			control(m, "secret").ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatal(w.Code, tc)
			}
			if cancelled != (action == "supervisor-stop" && tc.want == 200) {
				t.Fatal("unauthorized stop or missing cancellation", action, tc)
			}
		}
	}
}
func TestProxyUpgradeAndPrefix(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
			rw.Flush()
			buf := make([]byte, 4)
			io.ReadFull(rw, buf)
			rw.Write(buf)
			rw.Flush()
			return
		}
		fmt.Fprint(w, r.URL.RequestURI())
	}))
	defer up.Close()
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(up.URL, "http://"))
	port, _ := strconv.Atoi(p)
	front := httptest.NewServer(proxy(port, "/api"))
	defer front.Close()
	resp, err := http.Get(front.URL + "/api/items?q=hello")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(data) != "/items?q=hello" {
		t.Fatal(string(data))
	}
	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /socket HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	r := bufio.NewReader(conn)
	response, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 101 {
		t.Fatal(response.StatusCode)
	}
	fmt.Fprint(conn, "ping")
	b := make([]byte, 4)
	if _, err := io.ReadFull(r, b); err != nil || string(b) != "ping" {
		t.Fatal(string(b), err)
	}
}
func TestConfigAndPortCollision(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "flip.json")
	if err := run([]string{"-config", file, "init"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-config", file, "init"}); err == nil {
		t.Fatal("init overwrote config")
	}
	if _, err := readConfig(file); err == nil {
		t.Fatal("example missing directory accepted")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := start(context.Background(), service{Port: l.Addr().(*net.TCPAddr).Port}, filepath.Join(dir, "log")); err == nil {
		t.Fatal("occupied port accepted")
	}
}

func checkedConfig(t *testing.T, root string, c config) config {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "flip.json")
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	c, err = readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
