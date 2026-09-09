package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamedServicesWorkersAndPreviews(t *testing.T) {
	root := t.TempDir()
	exe, _ := os.Executable()
	yes, no := true, false
	used := map[int]bool{}
	port := func() int {
		p := freePort(t)
		for used[p] {
			p = freePort(t)
		}
		used[p] = true
		return p
	}
	httpService := func(label string) service {
		dir := filepath.Join(root, label)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "response.txt"), []byte(label), 0600); err != nil {
			t.Fatal(err)
		}
		return service{Dir: dir, Port: port(), Health: "/health", Command: []string{exe, "-test.run=^TestHelperProcess$"}, Env: map[string]string{"FLIP_TEST_HELPER": "1", "FLIP_TEST_PORT": "{port}"}}
	}
	worker := service{Type: "worker", Dir: root, Command: []string{exe, "-test.run=^TestHelperProcess$"}, Env: map[string]string{"FLIP_TEST_HELPER": "1", "FLIP_TEST_WORKER": "1"}, Enabled: &yes, RestartOnUse: &yes}
	disabled := worker
	disabled.Enabled = nil
	c := checkedConfig(t, root, config{Port: port(), ControlPort: port(), APIPrefix: "/api", TimeoutSeconds: 3, Worktrees: map[string]worktree{
		"a": {Services: map[string]service{"web": httpService("a-web"), "catalog": httpService("a-catalog"), "jobs": worker, "off": disabled}, PreviewPort: port(), Routes: []route{{Prefix: "/", Service: "web"}, {Prefix: "/api", Service: "catalog", StripPrefix: true}, {Prefix: "/api/admin", Service: "web", StripPrefix: true}}},
		"b": {Services: map[string]service{"solo": httpService("b-solo")}, PreviewPort: port()},
	}})
	m := newManager(context.Background(), c)
	defer m.close()
	front := httptest.NewServer(m)
	defer front.Close()
	a := httptest.NewServer(m.previewHandler("a"))
	defer a.Close()
	b := httptest.NewServer(m.previewHandler("b"))
	defer b.Close()
	get := func(base, path string, status int, want string) {
		t.Helper()
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != status || !strings.Contains(string(body), want) {
			t.Fatalf("%s%s: %d %s, want %d %s", base, path, resp.StatusCode, body, status, want)
		}
	}
	command := func(action, name, part string) {
		t.Helper()
		if _, err := m.command(action, name, part); err != nil {
			t.Fatal(err)
		}
	}
	get(a.URL, "/", 503, "")
	command("use", "b", "")
	command("up", "a", "")
	get(front.URL, "/", 200, "b-solo:")
	get(a.URL, "/", 200, "a-web:")
	get(a.URL, "/api/items", 200, ":/items")
	get(a.URL, "/api", 200, "a-catalog:")
	get(a.URL, "/apiculture", 200, "a-web:")
	get(a.URL, "/api/admin/users", 200, "a-web:")
	get(a.URL, "/api/admin/users", 200, ":/users")
	get(a.URL, "/oauth/callback", 200, "a-web:")
	get(b.URL, "/api", 200, "b-solo:")
	if m.processes["a"]["off"] != nil {
		t.Fatal("worker started without opt-in")
	}
	if !m.processes["a"]["jobs"].running() {
		t.Fatal("enabled worker missing")
	}
	oldWorker, oldWeb := m.processes["a"]["jobs"], m.processes["a"]["web"]
	command("use", "a", "")
	if m.processes["a"]["jobs"] == oldWorker || oldWorker.running() {
		t.Fatal("worker restart_on_use ignored")
	}
	if m.processes["a"]["web"] != oldWeb {
		t.Fatal("named HTTP default should not restart")
	}
	get(b.URL, "/", 200, "b-solo:")
	command("restart", "a", "catalog")
	if _, err := m.command("restart", "a", "off"); err == nil {
		t.Fatal("restarted disabled worker")
	}
	if _, err := m.command("restart", "a", "missing"); err == nil {
		t.Fatal("restarted unknown service")
	}
	status, err := m.command("status", "", "")
	if err != nil || !strings.Contains(status, "off\tdisabled") || !strings.Contains(status, fmt.Sprintf("http://localhost:%d", c.Worktrees["a"].PreviewPort)) {
		t.Fatal(status, err)
	}
	command("use", "b", "")
	command("down", "a", "")
	get(a.URL, "/", 503, "")
	get(front.URL, "/", 200, "b-solo:")
	if oldWeb.running() {
		t.Fatal("down left HTTP alive")
	}
	failing := m.c.Worktrees["a"].Services["jobs"]
	failing.Env["FLIP_TEST_FAIL"] = "1"
	failing.RestartOnUse = &no
	m.c.Worktrees["a"].Services["jobs"] = failing
	if _, err = m.command("use", "a", ""); err == nil {
		t.Fatal("immediately exiting worker considered ready")
	}
	get(a.URL, "/", 503, "")
	get(front.URL, "/", 200, "b-solo:")
	request := httptest.NewRequest("GET", "http://evil.example/", nil)
	result := httptest.NewRecorder()
	m.previewHandler("b").ServeHTTP(result, request)
	if result.Code != 403 {
		t.Fatal("preview allowed external host", result.Code)
	}
	command("down", "a", "")
	command("down", "b", "")
	for _, wt := range c.Worktrees {
		for _, s := range wt.Services {
			if s.Type == "worker" {
				continue
			}
			l, err := net.Listen("tcp", address(s.Port))
			if err != nil {
				t.Fatal(err)
			}
			l.Close()
		}
	}
}

func TestNamedConfigValidation(t *testing.T) {
	root := t.TempDir()
	yes := true
	web := service{Command: []string{"unused"}, Port: 21001}
	worker := service{Type: "worker", Command: []string{"unused"}, Enabled: &yes}
	cases := []struct {
		name string
		wt   worktree
		want string
	}{
		{"mixed", worktree{UI: web, Services: map[string]service{"web": web}}, "cannot be mixed"},
		{"multiple without routes", worktree{Services: map[string]service{"web": web, "api": {Command: []string{"unused"}, Port: 21002}}}, "require routes"},
		{"worker route", worktree{Services: map[string]service{"job": worker}, Routes: []route{{Prefix: "/", Service: "job"}}}, "enabled HTTP"},
		{"unknown route", worktree{Services: map[string]service{"web": web}, Routes: []route{{Prefix: "/", Service: "missing"}}}, "enabled HTTP"},
		{"duplicate route", worktree{Services: map[string]service{"web": web}, Routes: []route{{Prefix: "/api", Service: "web"}, {Prefix: "/api/", Service: "web"}}}, "duplicate route"},
		{"invalid prefix", worktree{Services: map[string]service{"web": web}, Routes: []route{{Prefix: "//host", Service: "web"}}}, "invalid route"},
		{"unsafe name", worktree{Services: map[string]service{"../oops": web}}, "invalid service name"},
		{"preview collision", worktree{Services: map[string]service{"web": web}, PreviewPort: 21001}, "duplicate port"},
		{"worker port", worktree{Services: map[string]service{"job": {Type: "worker", Command: []string{"unused"}, Port: 21002}}}, "cannot have port or health"},
		{"worker health", worktree{Services: map[string]service{"job": {Type: "worker", Command: []string{"unused"}, Health: "/"}}}, "cannot have port or health"},
		{"worker preview", worktree{Services: map[string]service{"job": worker}, PreviewPort: 21003}, "preview requires HTTP"},
		{"bad health", worktree{Services: map[string]service{"web": {Command: []string{"unused"}, Port: 21001, Health: "/%zz"}}}, "health must be a local path"},
		{"no command", worktree{Services: map[string]service{"web": {Port: 21001}}}, "command is required"},
		{"bad type", worktree{Services: map[string]service{"web": {Command: []string{"unused"}, Type: "unknown"}}}, "type must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := config{Port: 21010, ControlPort: 21011, APIPrefix: "/api", TimeoutSeconds: 3, Worktrees: map[string]worktree{"one": tc.wt}}
			data, _ := json.Marshal(c)
			path := filepath.Join(root, "flip.json")
			os.WriteFile(path, data, 0600)
			if _, err := readConfig(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v want %s", err, tc.want)
			}
		})
	}
	// Workers can run without any HTTP route; explicit routes can leave paths unmatched.
	c := checkedConfig(t, root, config{Port: 21010, ControlPort: 21011, APIPrefix: "/api", TimeoutSeconds: 3, Worktrees: map[string]worktree{"jobs": {Services: map[string]service{"job": worker}}, "web": {Services: map[string]service{"web": web}, Routes: []route{{Prefix: "/only", Service: "web"}}}}})
	result := httptest.NewRecorder()
	serveSelection(routesFor("web", c.Worktrees["web"]), result, httptest.NewRequest("GET", "http://localhost/unmatched", nil))
	if result.Code != 404 {
		t.Fatal(result.Code)
	}
}

func TestPreviewPortCollisionPreservesToken(t *testing.T) {
	root := t.TempDir()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	token := filepath.Join(root, ".flip", "token")
	os.Mkdir(filepath.Dir(token), 0700)
	os.WriteFile(token, []byte("existing"), 0600)
	c := config{root: root, Port: freePort(t), ControlPort: freePort(t), Worktrees: map[string]worktree{"a": {PreviewPort: occupied.Addr().(*net.TCPAddr).Port}}}
	if err = serve(c); err == nil {
		t.Fatal("occupied preview accepted")
	}
	data, err := os.ReadFile(token)
	if err != nil || string(data) != "existing" {
		t.Fatal("token modified on failed bind", err)
	}
	for _, port := range []int{c.Port, c.ControlPort} {
		l, err := net.Listen("tcp", address(port))
		if err != nil {
			t.Fatal("partial listener survived", err)
		}
		l.Close()
	}
}
