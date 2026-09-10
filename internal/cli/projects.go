package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

type savedState struct {
	Projects map[string]string         `json:"projects"`
	Ports    map[string]map[string]int `json:"ports"`
}

// The separate lock file stays in place; closing its handle releases the OS lock even after a crash.
func withStateMode(update func(*savedState) error, write bool) error {
	dir := os.Getenv("FLIP_HOME")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(base, "flip")
	}
	if write {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	flags := os.O_RDWR
	if write {
		flags |= os.O_CREATE
	}
	lock, err := os.OpenFile(filepath.Join(dir, "state.lock"), flags, 0600)
	if err == nil {
		defer lock.Close()
		if err = lockState(lock); err != nil {
			return err
		}
	} else if write || !os.IsNotExist(err) {
		return err
	}
	path := filepath.Join(dir, "state.json")
	s := savedState{Projects: map[string]string{}, Ports: map[string]map[string]int{}}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("read Flip state: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if s.Projects == nil {
		s.Projects = map[string]string{}
	}
	if s.Ports == nil {
		s.Ports = map[string]map[string]int{}
	}
	if err = update(&s); err != nil {
		return err
	}
	if !write {
		return nil
	}
	data, err = json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "state-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp.Name(), path)
}

func canonicalPath(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if real, e := filepath.EvalSymlinks(p); e == nil {
		p = real
	}
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return filepath.Clean(p), nil
}

func gitOutput(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(e.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r"), nil
}

func gitCommon(dir string) (string, error) {
	s, err := gitOutput(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return canonicalPath(s)
}

func resolveConfig(explicit, project string) (string, error) {
	if explicit != "" {
		return canonicalPath(explicit)
	}
	if project != "" {
		var path string
		err := withStateMode(func(s *savedState) error { path = s.Projects[project]; return nil }, false)
		if err != nil {
			return "", err
		}
		if path == "" {
			return "", fmt.Errorf("unknown project %q; run flip projects", project)
		}
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("project %q config unavailable: %w; register it again or unregister it", project, err)
		}
		return path, nil
	}
	if path := os.Getenv("FLIP_CONFIG"); path != "" {
		return canonicalPath(path)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, "flip.json")
		if _, err := os.Stat(path); err == nil {
			return canonicalPath(path)
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	common, err := gitCommon(cwd)
	if err == nil {
		var matches []string
		err = withStateMode(func(s *savedState) error {
			seen := map[string]bool{}
			for _, path := range s.Projects {
				data, e := os.ReadFile(path)
				if e != nil {
					continue
				}
				var c config
				if json.Unmarshal(data, &c) != nil {
					continue
				}
				repo := filepath.Dir(path)
				if c.Discover != nil {
					if filepath.IsAbs(c.Discover.Repo) {
						repo = c.Discover.Repo
					} else {
						repo = filepath.Join(repo, c.Discover.Repo)
					}
				}
				root, e := gitCommon(repo)
				if e == nil && root == common && !seen[path] {
					matches = append(matches, path)
					seen[path] = true
				}
			}
			return nil
		}, false)
		if err != nil {
			return "", err
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return "", fmt.Errorf("multiple registered configs for this repository; use -project NAME")
		}
		path := filepath.Join(filepath.Dir(common), "flip.json")
		if _, err := os.Stat(path); err == nil {
			return canonicalPath(path)
		}
	}
	return "", fmt.Errorf("no flip.json found; use -config PATH or -project NAME, or run flip init")
}

func projectCommand(action, name, path string) error {
	if action != "projects" && !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`).MatchString(name) {
		return fmt.Errorf("invalid project name %q", name)
	}
	if action == "register" {
		if _, err := readConfig(path); err != nil {
			return err
		}
	}
	return withStateMode(func(s *savedState) error {
		switch action {
		case "register":
			if old := s.Projects[name]; old != "" && old != path {
				if _, err := os.Stat(old); !os.IsNotExist(err) {
					return fmt.Errorf("project %q already registered; unregister it first", name)
				}
			}
			s.Projects[name] = path
			fmt.Printf("Registered %s: %s\n", name, path)
		case "unregister":
			if s.Projects[name] == "" {
				return fmt.Errorf("unknown project %q", name)
			}
			delete(s.Projects, name)
			fmt.Println("Unregistered", name)
		case "projects":
			names := make([]string, 0, len(s.Projects))
			for n := range s.Projects {
				names = append(names, n)
			}
			sort.Strings(names)
			fmt.Println("PROJECT\tCONFIG\tSTATE")
			for _, n := range names {
				state := "available"
				if _, err := os.Stat(s.Projects[n]); err != nil {
					state = "unavailable"
				}
				fmt.Printf("%s\t%s\t%s\n", n, s.Projects[n], state)
			}
		}
		return nil
	}, action != "projects")
}
