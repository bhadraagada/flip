package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type service struct {
	Dir          string            `json:"dir"`
	Command      []string          `json:"command"`
	Port         int               `json:"port"`
	Health       string            `json:"health"`
	Env          map[string]string `json:"env,omitempty"`
	RestartOnUse *bool             `json:"restart_on_use,omitempty"`
}

func (s service) configured() bool { return len(s.Command) > 0 }
func (s service) restartOnUse(fallback bool) bool {
	if s.RestartOnUse != nil {
		return *s.RestartOnUse
	}
	return fallback
}

type worktree struct {
	UI      service `json:"ui"`
	Backend service `json:"backend"`
}
type config struct {
	Port               int                 `json:"port"`
	ControlPort        int                 `json:"control_port"`
	APIPrefix          string              `json:"api_prefix"`
	StripPrefix        bool                `json:"strip_api_prefix"`
	TimeoutSeconds     int                 `json:"timeout_seconds"`
	IdleTimeoutSeconds int                 `json:"idle_timeout_seconds,omitempty"`
	Worktrees          map[string]worktree `json:"worktrees"`
	root               string
	path               string
}

func readConfig(path string) (config, error) {
	c := config{Port: 8080, ControlPort: 18080, APIPrefix: "/api", TimeoutSeconds: 30}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	c.root, err = filepath.Abs(filepath.Dir(path))
	if err != nil {
		return c, err
	}
	c.path, err = filepath.Abs(path)
	if err != nil {
		return c, err
	}
	if c.IdleTimeoutSeconds < 0 {
		return c, fmt.Errorf("idle_timeout_seconds must be nonnegative")
	}
	ports := map[int]bool{}
	checkPort := func(p int) error {
		if p < 1 || p > 65535 || ports[p] {
			return fmt.Errorf("invalid or duplicate port %d", p)
		}
		ports[p] = true
		return nil
	}
	if err = checkPort(c.Port); err != nil {
		return c, err
	}
	if err = checkPort(c.ControlPort); err != nil {
		return c, err
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 300 {
		return c, fmt.Errorf("timeout_seconds must be 1..300")
	}
	if !strings.HasPrefix(c.APIPrefix, "/") || c.APIPrefix == "/" || strings.ContainsAny(c.APIPrefix, "?#") {
		return c, fmt.Errorf("api_prefix must be a path such as /api")
	}
	c.APIPrefix = strings.TrimRight(c.APIPrefix, "/")
	if len(c.Worktrees) == 0 {
		return c, fmt.Errorf("configure at least one worktree")
	}
	namePattern := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
	for name, w := range c.Worktrees {
		if !namePattern.MatchString(name) {
			return c, fmt.Errorf("invalid worktree name %q", name)
		}
		if !w.UI.configured() && !w.Backend.configured() {
			return c, fmt.Errorf("%s: configure at least one ui or backend command", name)
		}
		for _, s := range []*service{&w.UI, &w.Backend} {
			if !s.configured() {
				if s.Port != 0 || s.Dir != "" || s.Health != "" || len(s.Env) > 0 || s.RestartOnUse != nil {
					return c, fmt.Errorf("%s: service command is required", name)
				}
				continue
			}
			if err = checkPort(s.Port); err != nil {
				return c, fmt.Errorf("%s: %w", name, err)
			}
			if len(s.Command) == 0 || s.Command[0] == "" {
				return c, fmt.Errorf("%s: command is required", name)
			}
			if s.Health == "" {
				s.Health = "/"
			}
			if !strings.HasPrefix(s.Health, "/") || strings.HasPrefix(s.Health, "//") {
				return c, fmt.Errorf("%s: health must be a local path", name)
			}
			if !filepath.IsAbs(s.Dir) {
				s.Dir = filepath.Join(c.root, s.Dir)
			}
			info, e := os.Stat(s.Dir)
			if e != nil {
				return c, e
			}
			if !info.IsDir() {
				return c, fmt.Errorf("%s is not a directory", s.Dir)
			}
		}
		c.Worktrees[name] = w
	}
	return c, nil
}

const example = `{
  "port": 8080,
  "control_port": 18080,
  "api_prefix": "/api",
  "strip_api_prefix": false,
  "timeout_seconds": 30,
  "worktrees": {
    "main": {
      "ui": {
        "dir": "../my-app",
        "command": ["your-dev-server", "--port", "{port}"],
        "port": 8081,
        "health": "/",
        "restart_on_use": false
      }
    }
  }
}
`
