package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type service struct {
	Type         string            `json:"type,omitempty"`
	Enabled      *bool             `json:"enabled,omitempty"`
	Dir          string            `json:"dir"`
	Command      []string          `json:"command"`
	Port         int               `json:"port"`
	Health       string            `json:"health"`
	Env          map[string]string `json:"env,omitempty"`
	RestartOnUse *bool             `json:"restart_on_use,omitempty"`
}

func (s service) configured() bool { return len(s.Command) > 0 }
func (s service) enabled() bool {
	if s.Enabled != nil {
		return *s.Enabled
	}
	return s.Type != "worker"
}
func (s service) specified() bool {
	return s.configured() || s.Port != 0 || s.Dir != "" || s.Health != "" || len(s.Env) > 0 || s.RestartOnUse != nil || s.Type != "" || s.Enabled != nil
}
func (s service) restartOnUse(fallback bool) bool {
	if s.RestartOnUse != nil {
		return *s.RestartOnUse
	}
	return fallback
}

type route struct {
	Prefix      string `json:"prefix"`
	Service     string `json:"service"`
	StripPrefix bool   `json:"strip_prefix,omitempty"`
}
type worktree struct {
	Services    map[string]service `json:"services,omitempty"`
	Routes      []route            `json:"routes,omitempty"`
	PreviewPort int                `json:"preview_port,omitempty"`
	UI          service            `json:"ui"`
	Backend     service            `json:"backend"`
}
type config struct {
	Port               int                 `json:"port"`
	ControlPort        int                 `json:"control_port"`
	APIPrefix          string              `json:"api_prefix"`
	StripPrefix        bool                `json:"strip_api_prefix"`
	TimeoutSeconds     int                 `json:"timeout_seconds"`
	IdleTimeoutSeconds int                 `json:"idle_timeout_seconds,omitempty"`
	Worktrees          map[string]worktree `json:"worktrees"`
	Discover           *discovery          `json:"discover,omitempty"`
	root               string
	path               string
}

// normalizeLegacy is the only place that assigns meaning to ui/backend roles.
func (w *worktree) normalizeLegacy(c config) error {
	if w.Services != nil {
		if w.UI.specified() || w.Backend.specified() {
			return fmt.Errorf("services cannot be mixed with ui/backend")
		}
		return nil
	}
	w.Services = map[string]service{}
	for name, s := range map[string]service{"ui": w.UI, "backend": w.Backend} {
		if !s.specified() {
			continue
		}
		if s.Type != "" && s.Type != "http" {
			return fmt.Errorf("legacy %s must be HTTP", name)
		}
		if s.RestartOnUse == nil {
			restart := name == "backend"
			s.RestartOnUse = &restart
		}
		w.Services[name] = s
	}
	if w.Routes == nil {
		root := "ui"
		if !w.UI.configured() || !w.UI.enabled() {
			root = "backend"
		}
		if w.Backend.configured() && w.Backend.enabled() {
			w.Routes = append(w.Routes, route{Prefix: c.APIPrefix, Service: "backend", StripPrefix: c.StripPrefix})
		}
		if s, ok := w.Services[root]; ok && s.enabled() {
			w.Routes = append(w.Routes, route{Prefix: "/", Service: root})
		}
	}
	w.UI, w.Backend = service{}, service{}
	return nil
}
func serviceNames(w worktree) []string {
	names := make([]string, 0, len(w.Services))
	for name := range w.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func readConfig(path string) (config, error) {
	return readConfigMode(path, true)
}

func readConfigMode(path string, allocate bool) (config, error) {
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
	c.path, err = canonicalPath(path)
	if err != nil {
		return c, err
	}
	c.root = filepath.Dir(c.path)
	if c.Discover == nil {
		return validateConfig(c)
	}
	err = withStateMode(func(state *savedState) error {
		if err := expandDiscovery(&c, state, allocate); err != nil {
			return err
		}
		c, err = validateConfig(c)
		return err
	}, allocate)
	return c, err
}

func validateConfig(c config) (config, error) {
	if c.IdleTimeoutSeconds < 0 {
		return c, fmt.Errorf("idle_timeout_seconds must be nonnegative")
	}
	var err error
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
		if err = w.normalizeLegacy(c); err != nil {
			return c, fmt.Errorf("%s: %w", name, err)
		}
		if len(w.Services) == 0 {
			return c, fmt.Errorf("%s: configure at least one service", name)
		}
		if w.PreviewPort != 0 {
			if err = checkPort(w.PreviewPort); err != nil {
				return c, fmt.Errorf("%s preview: %w", name, err)
			}
		}
		httpNames := []string{}
		for _, label := range serviceNames(w) {
			s := w.Services[label]
			if !namePattern.MatchString(label) {
				return c, fmt.Errorf("invalid service name %q", label)
			}
			if s.Type == "" {
				s.Type = "http"
			}
			if s.Type != "http" && s.Type != "worker" {
				return c, fmt.Errorf("%s/%s: type must be http or worker", name, label)
			}
			if !s.configured() || s.Command[0] == "" {
				return c, fmt.Errorf("%s/%s: command is required", name, label)
			}
			if s.Type == "worker" {
				if s.Port != 0 || s.Health != "" {
					return c, fmt.Errorf("%s/%s: workers cannot have port or health", name, label)
				}
			} else {
				if err = checkPort(s.Port); err != nil {
					return c, fmt.Errorf("%s/%s: %w", name, label, err)
				}
				if s.Health == "" {
					s.Health = "/"
				}
				u, e := url.ParseRequestURI(s.Health)
				if e != nil || !strings.HasPrefix(s.Health, "/") || strings.HasPrefix(s.Health, "//") || u.Host != "" || strings.Contains(s.Health, "#") {
					return c, fmt.Errorf("%s/%s: health must be a local path", name, label)
				}
				if s.enabled() {
					httpNames = append(httpNames, label)
				}
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
			w.Services[label] = s
		}
		if w.Routes == nil {
			if len(httpNames) > 1 {
				return c, fmt.Errorf("%s: multiple HTTP services require routes", name)
			}
			if len(httpNames) == 1 {
				w.Routes = []route{{Prefix: "/", Service: httpNames[0]}}
			}
		}
		prefixes := map[string]bool{}
		for i, r := range w.Routes {
			if !strings.HasPrefix(r.Prefix, "/") || strings.HasPrefix(r.Prefix, "//") || strings.ContainsAny(r.Prefix, "?#%\\") {
				return c, fmt.Errorf("%s: invalid route prefix %q", name, r.Prefix)
			}
			if r.Prefix != "/" {
				r.Prefix = strings.TrimRight(r.Prefix, "/")
			}
			if prefixes[r.Prefix] {
				return c, fmt.Errorf("%s: duplicate route prefix %q", name, r.Prefix)
			}
			prefixes[r.Prefix] = true
			s, ok := w.Services[r.Service]
			if !ok || s.Type != "http" || !s.enabled() {
				return c, fmt.Errorf("%s: route must target an enabled HTTP service: %q", name, r.Service)
			}
			w.Routes[i] = r
		}
		if w.PreviewPort != 0 && len(w.Routes) == 0 {
			return c, fmt.Errorf("%s: preview requires HTTP routes", name)
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
