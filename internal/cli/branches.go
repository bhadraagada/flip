package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

type branchView struct {
	Name, Worktree string
	LastActivity   int64
}

func worktreeRoot(w worktree) string {
	for _, name := range serviceNames(w) {
		for dir := w.Services[name].Dir; dir != ""; dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				if root, err := canonicalPath(dir); err == nil {
					return root
				}
				break
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return ""
}

// Activity is local edits, branch checkout/commits, and preview use, not remote fetch time.
func gitActivity(root string) (string, int64) {
	if root == "" {
		return "", 0
	}
	branch, _ := gitOutput(root, "symbolic-ref", "--quiet", "--short", "HEAD")
	stamp, _ := gitOutput(root, "log", "-1", "--format=%ct")
	latest, _ := strconv.ParseInt(stamp, 10, 64)
	if log, err := gitOutput(root, "rev-parse", "--git-path", "logs/HEAD"); err == nil {
		if !filepath.IsAbs(log) {
			log = filepath.Join(root, log)
		}
		if info, err := os.Stat(log); err == nil && info.ModTime().Unix() > latest {
			latest = info.ModTime().Unix()
		}
	}
	// Git's ignore rules keep dependencies and build outputs out of the edit scan.
	files, err := gitOutput(root, "ls-files", "-z", "--modified", "--others", "--exclude-standard", "--", ".", ":(exclude).flip")
	staged, _ := gitOutput(root, "diff", "--cached", "--name-only", "-z")
	files += staged
	if err == nil {
		for _, file := range strings.Split(files, "\x00") {
			if file != "" {
				if info, e := os.Stat(filepath.Join(root, file)); e == nil && info.ModTime().Unix() > latest {
					latest = info.ModTime().Unix()
				}
			}
		}
	}
	return branch, latest
}

func branchList(roots map[string]string) []branchView {
	branches := []branchView{}
	repos := map[string]bool{}
	for _, root := range roots {
		common, err := gitCommon(root)
		if err != nil {
			continue
		}
		if repos[common] {
			continue
		}
		repos[common] = true
		out, err := gitOutput(root, "for-each-ref", "--format=%(refname:short)%09%(committerdate:unix)%09%(worktreepath)", "refs/heads/")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			parts := strings.Split(line, "\t")
			if len(parts) != 3 {
				continue
			}
			stamp, _ := strconv.ParseInt(parts[1], 10, 64)
			entry := branchView{Name: parts[0], LastActivity: stamp}
			if parts[2] != "" {
				path, _ := canonicalPath(parts[2])
				for name, root := range roots {
					if path == root {
						entry.Worktree = name
						break
					}
				}
			}
			branches = append(branches, entry)
		}
	}
	// Configurations spanning repositories are intentionally not offered ambiguous branch creation.
	if len(repos) != 1 {
		return nil
	}
	sort.Slice(branches, func(i, j int) bool {
		if branches[i].LastActivity != branches[j].LastActivity {
			return branches[i].LastActivity > branches[j].LastActivity
		}
		return branches[i].Name < branches[j].Name
	})
	return branches
}

func (m *manager) prepareBranch(branch string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	roots := map[string]string{}
	for name, w := range m.c.Worktrees {
		if root := worktreeRoot(w); root != "" {
			roots[name] = root
		}
	}
	found := false
	for _, b := range branchList(roots) {
		if b.Name == branch {
			if b.Worktree != "" {
				return b.Worktree, nil
			}
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("local branch %q not found in this project's repository", branch)
	}
	names := make([]string, 0, len(roots))
	for name := range roots {
		names = append(names, name)
	}
	sort.Strings(names)
	source := names[0]
	if _, ok := roots["main"]; ok {
		source = "main"
	}
	if a := m.active.Load(); a != nil && roots[a.name] != "" {
		source = a.name
	}
	base := roots[source]
	template := m.c.Worktrees[source]
	sum := sha256.Sum256([]byte(branch))
	name := fmt.Sprintf("branch-%x", sum[:6])
	dir := filepath.Join(m.c.root, ".flip", "worktrees", name)
	existing := false
	paths, err := gitWorktrees(base)
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		if b, _ := gitOutput(path, "symbolic-ref", "--quiet", "--short", "HEAD"); b == branch {
			dir = path
			existing = true
			break
		}
	}
	if existing && m.c.Discover != nil {
		name = discoveredNames(paths)[dir]
	}
	if _, exists := m.c.Worktrees[name]; exists {
		return "", fmt.Errorf("generated worktree name %s already exists", name)
	}
	if !existing {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			return "", fmt.Errorf("worktree directory already exists: %s", dir)
		}
	}
	used := map[int]bool{m.c.Port: true, m.c.ControlPort: true}
	for _, tree := range m.c.Worktrees {
		used[tree.PreviewPort] = true
		for _, s := range tree.Services {
			used[s.Port] = true
		}
	}
	if err := withStateMode(func(state *savedState) error {
		for _, ports := range state.Ports {
			for _, port := range ports {
				used[port] = true
			}
		}
		return nil
	}, false); err != nil {
		return "", err
	}
	w := worktree{Services: map[string]service{}, Routes: template.Routes}
	sockets := []net.Listener{}
	defer func() {
		for _, l := range sockets {
			l.Close()
		}
	}()
	for _, label := range serviceNames(template) {
		s := template.Services[label]
		rel, err := filepath.Rel(base, s.Dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("service %s directory is outside its worktree", label)
		}
		s.Dir = filepath.Join(dir, rel)
		// Absolute executable/argument paths inside the source checkout must follow its code.
		s.Command = append([]string(nil), s.Command...)
		for i, arg := range s.Command {
			if filepath.IsAbs(arg) {
				if rel, e := filepath.Rel(base, arg); e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					s.Command[i] = filepath.Join(dir, rel)
				}
			}
		}
		if s.Type == "worker" {
			disabled := false
			s.Enabled = &disabled
		} else {
			s.Port = 0
			for port := 20000; port <= 40000; port++ {
				if used[port] {
					continue
				}
				l, e := net.Listen("tcp", address(port))
				if e != nil {
					continue
				}
				sockets = append(sockets, l)
				s.Port = port
				used[port] = true
				break
			}
			if s.Port == 0 {
				return "", fmt.Errorf("no available branch service port in 20000..40000")
			}
		}
		w.Services[label] = s
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return "", err
	}
	if !existing {
		if _, err := gitOutput(base, "worktree", "add", "--", dir, branch); err != nil {
			return "", err
		}
	}
	for label, s := range w.Services {
		if info, e := os.Stat(s.Dir); e != nil || !info.IsDir() {
			return "", fmt.Errorf("branch checkout at %s lacks service directory %s (%s); configure this branch before retrying", dir, label, s.Dir)
		}
	}
	// Preserve unrelated configuration fields and write atomically before exposing the new entry.
	err = withStateMode(func(_ *savedState) error {
		data, err := os.ReadFile(m.c.path)
		if err != nil {
			return err
		}
		raw := map[string]json.RawMessage{}
		if err = json.Unmarshal(data, &raw); err != nil {
			return err
		}
		entries := map[string]json.RawMessage{}
		if b := raw["worktrees"]; len(b) > 0 {
			if err = json.Unmarshal(b, &entries); err != nil {
				return err
			}
		}
		if entries == nil {
			entries = map[string]json.RawMessage{}
		}
		if _, ok := entries[name]; ok {
			return fmt.Errorf("worktree name was added concurrently: %s", name)
		}
		entries[name], err = json.Marshal(w)
		if err != nil {
			return err
		}
		raw["worktrees"], err = json.Marshal(entries)
		if err != nil {
			return err
		}
		data, err = json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(m.c.root, "flip-branch-*.json")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		_, err = f.Write(data)
		ce := f.Close()
		if err != nil {
			return err
		}
		if ce != nil {
			return ce
		}
		return os.Rename(f.Name(), m.c.path)
	}, false)
	if err != nil {
		return "", fmt.Errorf("worktree created at %s but registration failed: %w", dir, err)
	}
	m.registryMu.Lock()
	m.c.Worktrees[name] = w
	m.previews[name] = &atomic.Pointer[selection]{}
	m.activity[name] = &activity{}
	m.registryMu.Unlock()
	return name, nil
}
