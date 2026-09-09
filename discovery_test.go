package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixtureGit(t *testing.T, root string, count int) []string {
	t.Helper()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init")
	if err := os.WriteFile(filepath.Join(repo, "response.txt"), []byte("discovered"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "response.txt")
	git("-c", "user.name=Flip Test", "-c", "user.email=flip@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	paths := []string{repo}
	for i := 1; i < count; i++ {
		path := filepath.Join(root, fmt.Sprintf("tree-%d", i))
		git("worktree", "add", "--detach", path)
		paths = append(paths, path)
	}
	return paths
}

func writeDiscoveryConfig(t *testing.T, path string, c config) {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func discoveryFixtureConfig(t *testing.T, repo string) config {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return config{Port: freePort(t), ControlPort: freePort(t), APIPrefix: "/api", TimeoutSeconds: 5,
		Discover: &discovery{Repo: repo, Preview: true, Services: map[string]service{
			"web": {Command: []string{exe, "-test.run=^TestHelperProcess$"}, Health: "/health", Env: map[string]string{"FLIP_TEST_HELPER": "1", "FLIP_TEST_PORT": "{port}"}},
		}},
	}
}

func TestDiscoveryFiveWorktrees(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FLIP_HOME", filepath.Join(root, "state"))
	t.Setenv("FLIP_CONFIG", "")
	paths := fixtureGit(t, root, 5)
	file := filepath.Join(paths[0], "flip.json")
	c := discoveryFixtureConfig(t, ".")
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	c.Discover.PortMin = occupied.Addr().(*net.TCPAddr).Port
	c.Discover.PortMax = 65535
	writeDiscoveryConfig(t, file, c)
	// Read-only diagnostics neither allocate nor create state.
	if _, err := readConfigMode(file, false); err == nil {
		t.Fatal("read-only config allocated ports")
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatal("read-only config wrote state", err)
	}
	loaded, err := readConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Worktrees) != 5 {
		t.Fatal(len(loaded.Worktrees))
	}
	ports := map[int]bool{c.Port: true, c.ControlPort: true, c.Discover.PortMin: true}
	for _, w := range loaded.Worktrees {
		for _, port := range []int{w.Services["web"].Port, w.PreviewPort} {
			if ports[port] {
				t.Fatal("duplicate or occupied assignment", port)
			}
			ports[port] = true
		}
	}
	m := newManager(context.Background(), loaded)
	defer m.close()
	for name, w := range loaded.Worktrees {
		if _, err := m.command("up", name, ""); err != nil {
			t.Fatal(err)
		}
		resp, err := http.Get("http://" + address(w.Services["web"].Port) + "/")
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || !strings.HasPrefix(string(b), "discovered:") {
			t.Fatal(string(b), err)
		}
	}
	for _, allocate := range []bool{true, false} {
		again, err := readConfigMode(file, allocate)
		if err != nil || !reflect.DeepEqual(loaded.Worktrees, again.Worktrees) {
			t.Fatal("assignments changed while running", err)
		}
	}
	m.close()
	again, err := readConfig(file)
	if err != nil || !reflect.DeepEqual(loaded.Worktrees, again.Worktrees) {
		t.Fatal("assignments changed after restart", err)
	}
	if err := run([]string{"-config", file, "register", "fixture"}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(paths[4])
	resolved, err := resolveConfig("", "")
	if err != nil || resolved != loaded.path {
		t.Fatal("Git common config discovery", resolved, err)
	}
	nested := filepath.Join(paths[0], "nested")
	os.Mkdir(nested, 0700)
	t.Chdir(nested)
	resolved, err = resolveConfig("", "")
	if err != nil || resolved != loaded.path {
		t.Fatal("ancestor lookup", resolved, err)
	}
	t.Chdir(root)
	resolved, err = resolveConfig("", "fixture")
	if err != nil || resolved != loaded.path {
		t.Fatal("project lookup", resolved, err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveConfig("", "fixture"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatal("stale registration", err)
	}
	if err := run([]string{"unregister", "fixture"}); err != nil {
		t.Fatal(err)
	}
}

// Separate processes exercise the OS lock, rather than a goroutine-only mutex.
func TestDiscoveryProcess(t *testing.T) {
	file := os.Getenv("FLIP_DISCOVERY_TEST_CONFIG")
	if file == "" {
		return
	}
	c, err := readConfig(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	b, _ := json.Marshal(c.Worktrees)
	fmt.Print(string(b))
	os.Exit(0)
}

func TestConcurrentDiscoveryAndOverrides(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FLIP_HOME", filepath.Join(root, "state"))
	paths := fixtureGit(t, root, 2)
	file := filepath.Join(paths[0], "flip.json")
	c := discoveryFixtureConfig(t, ".")
	writeDiscoveryConfig(t, file, c)
	exe, _ := os.Executable()
	var cmds []*exec.Cmd
	for i := 0; i < 6; i++ {
		cmd := exec.Command(exe, "-test.run=^TestDiscoveryProcess$")
		cmd.Env = append(os.Environ(), "FLIP_DISCOVERY_TEST_CONFIG="+file)
		cmds = append(cmds, cmd)
	}
	type result struct {
		b   []byte
		err error
	}
	results := make(chan result, len(cmds))
	for _, cmd := range cmds {
		go func(cmd *exec.Cmd) { b, e := cmd.CombinedOutput(); results <- result{b, e} }(cmd)
	}
	var first []byte
	for range cmds {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err, string(r.b))
		}
		if first == nil {
			first = r.b
		}
		if string(r.b) != string(first) {
			t.Fatal("concurrent assignments differ")
		}
	}
	loaded, err := readConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	// A fixed override replaces one generated worktree without changing its explicit port.
	w := loaded.Worktrees["repo"]
	c.Worktrees = map[string]worktree{"repo": w}
	writeDiscoveryConfig(t, file, c)
	loaded, err = readConfig(file)
	if err != nil || loaded.Worktrees["repo"].Services["web"].Port != w.Services["web"].Port {
		t.Fatal("explicit override", err)
	}
	// A deleted Git worktree is skipped even before Git's metadata is pruned.
	if err := os.RemoveAll(paths[1]); err != nil {
		t.Fatal(err)
	}
	loaded, err = readConfig(file)
	if err != nil || len(loaded.Worktrees) != 1 {
		t.Fatal("stale worktree", err)
	}
	// A failing config cannot change persisted reservations.
	statePath := filepath.Join(root, "state", "state.json")
	before, _ := os.ReadFile(statePath)
	c.TimeoutSeconds = -1
	writeDiscoveryConfig(t, file, c)
	if _, err := readConfig(file); err == nil {
		t.Fatal("invalid config accepted")
	}
	after, _ := os.ReadFile(statePath)
	if string(before) != string(after) {
		t.Fatal("invalid config changed saved state")
	}
}

func TestDiscoveredNames(t *testing.T) {
	paths := []string{filepath.Join("one", "same"), filepath.Join("two", "same"), "with spaces"}
	names := discoveredNames(paths)
	if names[paths[0]] == names[paths[1]] || names[paths[2]] != "with-spaces" {
		t.Fatal(names)
	}
}

func TestCrossProjectAllocation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FLIP_HOME", filepath.Join(root, "state"))
	firstRepo := fixtureGit(t, filepath.Join(root, "first"), 1)[0]
	secondRepo := fixtureGit(t, filepath.Join(root, "second"), 1)[0]
	firstFile, secondFile := filepath.Join(firstRepo, "flip.json"), filepath.Join(secondRepo, "flip.json")
	first := discoveryFixtureConfig(t, ".")
	second := discoveryFixtureConfig(t, ".")
	writeDiscoveryConfig(t, firstFile, first)
	writeDiscoveryConfig(t, secondFile, second)
	type result struct {
		c   config
		err error
	}
	results := make(chan result, 2)
	for _, file := range []string{firstFile, secondFile} {
		go func(file string) { c, err := readConfig(file); results <- result{c, err} }(file)
	}
	used := map[int]bool{}
	var loadedFirst config
	firstPath, _ := canonicalPath(firstFile)
	for range 2 {
		r := <-results
		c, err := r.c, r.err
		if err != nil {
			t.Fatal(err)
		}
		if c.path == firstPath {
			loadedFirst = c
		}
		w := c.Worktrees["repo"]
		for _, port := range []int{c.Port, c.ControlPort, w.PreviewPort, w.Services["web"].Port} {
			if used[port] {
				t.Fatal("cross-project duplicate", port)
			}
			used[port] = true
		}
	}
	// An explicit port cannot steal another project's inactive saved assignment.
	second.Port = loadedFirst.Worktrees["repo"].Services["web"].Port
	writeDiscoveryConfig(t, secondFile, second)
	if _, err := readConfig(secondFile); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatal("cross-project conflict accepted", err)
	}
}
