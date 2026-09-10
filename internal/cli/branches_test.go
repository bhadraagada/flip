package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBranchSwitchingLive(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FLIP_HOME", filepath.Join(root, "state"))
	repo := fixtureGit(t, root, 1)[0]
	git := func(args ...string) string {
		t.Helper()
		out, err := gitOutput(repo, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git("branch", "-M", "main")
	for _, branch := range []string{"feature/a", "feature/b", "feature/c", "feature/d"} {
		git("checkout", "-b", branch, "main")
		os.WriteFile(filepath.Join(repo, "response.txt"), []byte(branch), 0600)
		git("add", "response.txt")
		git("-c", "user.name=Flip Test", "-c", "user.email=flip@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", branch)
	}
	git("checkout", "main")
	os.WriteFile(filepath.Join(repo, "response.txt"), []byte("main dirty edits"), 0600)
	exe, _ := os.Executable()
	restart := true
	services := map[string]service{}
	for _, label := range []string{"web", "api"} {
		services[label] = service{Dir: repo, Command: []string{exe, "-test.run=^TestHelperProcess$"}, Port: freePort(t), Health: "/health", RestartOnUse: &restart, Env: map[string]string{"FLIP_TEST_HELPER": "1", "FLIP_TEST_PORT": "{port}"}}
	}
	path := filepath.Join(root, "flip.json")
	writeDiscoveryConfig(t, path, config{Port: freePort(t), ControlPort: freePort(t), TimeoutSeconds: 5, APIPrefix: "/api", Worktrees: map[string]worktree{"main": {Services: services, Routes: []route{{Prefix: "/api", Service: "api"}, {Prefix: "/", Service: "web"}}}}})
	c, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newManager(context.Background(), c)
	defer m.close()
	front := httptest.NewServer(m)
	defer front.Close()
	check := func(branch, want string) {
		t.Helper()
		if _, err := m.command("branch", branch, ""); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/callback", "/api/value"} {
			resp, err := http.Get(front.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || !strings.HasPrefix(string(data), want+":") {
				t.Fatalf("%s: %d %s", path, resp.StatusCode, data)
			}
		}
		state := m.previewState()
		if !state.Worktrees[0].Selected || state.Worktrees[0].Branch != branch {
			t.Fatalf("selected branch not first: %+v", state.Worktrees[0])
		}
	}
	for _, branch := range []string{"feature/a", "feature/b", "feature/c", "feature/d"} {
		check(branch, branch)
	}
	if got := git("branch", "--show-current"); got != "main" {
		t.Fatal("changed caller branch", got)
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "response.txt")); string(data) != "main dirty edits" {
		t.Fatal("lost caller edits")
	}
	check("main", "main dirty edits")
	check("feature/a", "feature/a")
	if len(m.c.Worktrees) != 5 {
		t.Fatal("did not reuse checkout")
	}
	current := m.active.Load().name
	os.WriteFile(filepath.Join(m.c.Worktrees[current].Services["api"].Dir, "response.txt"), []byte("updated backend"), 0600)
	check("feature/a", "updated backend") // no-reload services must load edits on reselection.
	// A freshly edited inactive checkout outranks older inactive checkouts, with current pinned first.
	edited := m.c.Worktrees["main"].Services["web"].Dir
	future := time.Now().Add(time.Minute)
	os.Chtimes(filepath.Join(edited, "response.txt"), future, future)
	state := m.previewState()
	if state.Worktrees[1].Name != "main" {
		t.Fatal("local edit recency ignored", state.Worktrees)
	}
	if _, err := m.command("branch", "--orphan", ""); err == nil {
		t.Fatal("accepted unknown branch")
	}
	if m.active.Load().name != current {
		t.Fatal("failed branch changed selection")
	}
	reloaded, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Worktrees) != 5 {
		t.Fatal("branch registration not persistent")
	}
	for name, w := range reloaded.Worktrees {
		if name != "main" && w.PreviewPort != 0 {
			t.Fatal("branch changed public origin")
		}
	}
	// Startup failure must leave the current preview healthy.
	target, err := m.prepareBranch("feature/b")
	if err != nil {
		t.Fatal(err)
	}
	m.command("down", target, "")
	s := m.c.Worktrees[target].Services["api"]
	s.Command = []string{"flip-nonexistent-test-executable"}
	m.c.Worktrees[target].Services["api"] = s
	if _, err := m.command("branch", "feature/b", ""); err == nil {
		t.Fatal("accepted failed backend")
	}
	if m.active.Load().name != current {
		t.Fatal("failed backend changed selection")
	}
}

func TestBranchDiscoveryRegistration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FLIP_HOME", filepath.Join(root, "state"))
	repo := fixtureGit(t, root, 1)[0]
	if _, err := gitOutput(repo, "branch", "-M", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(repo, "branch", "feature"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "flip.json")
	c := discoveryFixtureConfig(t, repo)
	writeDiscoveryConfig(t, path, c)
	c, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newManager(context.Background(), c)
	if _, err := m.prepareBranch("feature"); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external-checkout")
	if _, err := gitOutput(repo, "worktree", "add", "-b", "external", external, "main"); err != nil {
		t.Fatal(err)
	}
	name, err := m.prepareBranch("external")
	if err != nil {
		t.Fatal(err)
	}
	if name != "external-checkout" {
		t.Fatal("did not use discovery name", name)
	}
	c, err = readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Worktrees) != 3 {
		t.Fatal("duplicate discovery entry", len(c.Worktrees))
	}
	if got := worktreeRoot(c.Worktrees[name]); got != strings.ToLower(external) && got != external {
		t.Fatal("did not reuse external checkout", got)
	}
}
