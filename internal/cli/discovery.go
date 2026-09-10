package cli

import (
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

type discovery struct {
	Repo     string             `json:"repo"`
	Services map[string]service `json:"services"`
	Routes   []route            `json:"routes"`
	Preview  bool               `json:"preview,omitempty"`
	PortMin  int                `json:"port_min,omitempty"`
	PortMax  int                `json:"port_max,omitempty"`
}

func gitWorktrees(repo string) ([]string, error) {
	b, err := gitOutput(repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("discover Git worktrees: %w", err)
	}
	var paths []string
	for _, record := range strings.Split(b, "\x00\x00") {
		fields := strings.Split(record, "\x00")
		path, skip := "", false
		for _, field := range fields {
			if strings.HasPrefix(field, "worktree ") {
				path = strings.TrimPrefix(field, "worktree ")
			}
			if field == "bare" || strings.HasPrefix(field, "prunable") {
				skip = true
			}
		}
		if path == "" || skip {
			continue
		}
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		path, err = canonicalPath(path)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

// Path-derived suffixes keep colliding directory names independent of Git's listing order.
func discoveredNames(paths []string) map[string]string {
	re := regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	names := map[string]string{}
	counts := map[string]int{}
	for _, path := range paths {
		name := strings.Trim(re.ReplaceAllString(filepath.Base(path), "-"), "-_")
		if name == "" {
			name = "worktree"
		}
		names[path] = name
		counts[name]++
	}
	for path, name := range names {
		if counts[name] > 1 {
			sum := sha256.Sum256([]byte(path))
			names[path] = fmt.Sprintf("%s-%x", name, sum[:6])
		}
	}
	return names
}

func expandDiscovery(c *config, state *savedState, allocate bool) error {
	d := c.Discover
	if d.PortMin == 0 {
		d.PortMin = 20000
	}
	if d.PortMax == 0 {
		d.PortMax = 40000
	}
	if d.PortMin < 1 || d.PortMax > 65535 || d.PortMin > d.PortMax {
		return fmt.Errorf("discover port range must be within 1..65535")
	}
	if len(d.Services) == 0 {
		return fmt.Errorf("discover.services requires a service template")
	}
	for _, s := range d.Services {
		if s.Port != 0 {
			return fmt.Errorf("discover service ports must be omitted; set explicit ports in worktrees overrides")
		}
		if filepath.IsAbs(s.Dir) || filepath.Clean(s.Dir) == ".." || strings.HasPrefix(filepath.Clean(s.Dir), ".."+string(filepath.Separator)) {
			return fmt.Errorf("discover service directories must be relative paths inside each worktree")
		}
	}
	repo := d.Repo
	if !filepath.IsAbs(repo) {
		repo = filepath.Join(c.root, repo)
	}
	paths, err := gitWorktrees(repo)
	if err != nil {
		return err
	}
	names := discoveredNames(paths)
	if c.Worktrees == nil {
		c.Worktrees = map[string]worktree{}
	}
	used := map[int]string{}
	// Other config allocations also reserve ports while their config files exist.
	for path, ports := range state.Ports {
		if path == c.path {
			continue
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			delete(state.Ports, path)
			continue
		}
		for _, port := range ports {
			used[port] = "another project"
		}
	}
	fixed := map[string]int{"public": c.Port, "control": c.ControlPort}
	for name, w := range c.Worktrees {
		fixed["explicit/"+name+"/preview"] = w.PreviewPort
		fixed["explicit/"+name+"/ui"] = w.UI.Port
		fixed["explicit/"+name+"/backend"] = w.Backend.Port
		for label, s := range w.Services {
			fixed["explicit/"+name+"/"+label] = s.Port
		}
	}
	next := map[string]int{}
	for key, port := range fixed {
		if port == 0 {
			continue
		}
		if owner := used[port]; owner != "" {
			return fmt.Errorf("port %d for %s conflicts with %s", port, key, owner)
		}
		used[port] = key
		next[key] = port
	}
	// Reserve surviving assignments first. Removed worktrees must not exhaust a full range.
	old := state.Ports[c.path]
	wanted := map[string]bool{}
	for _, path := range paths {
		if _, explicit := c.Worktrees[names[path]]; explicit {
			continue
		}
		for label, s := range d.Services {
			if s.Type != "worker" {
				wanted["auto/"+path+"/service/"+label] = true
			}
		}
		if d.Preview {
			wanted["auto/"+path+"/preview"] = true
		}
	}
	for key, port := range old {
		if wanted[key] && used[port] == "" {
			used[port] = "saved assignment"
		}
	}
	portFor := func(key string) (int, error) {
		if port := old[key]; port != 0 {
			if port < 1 || port > 65535 {
				return 0, fmt.Errorf("invalid saved port %d", port)
			}
			if used[port] != "saved assignment" {
				return 0, fmt.Errorf("saved port %d conflicts with %s", port, used[port])
			}
			for fixedKey, p := range next {
				if p == port && fixedKey != key {
					return 0, fmt.Errorf("saved port %d conflicts with %s; keep existing assignments or change the explicit port", port, fixedKey)
				}
			}
			next[key] = port
			return port, nil
		}
		if !allocate {
			return 0, fmt.Errorf("new worktree ports need allocation; run flip discover first")
		}
		for port := d.PortMin; port <= d.PortMax; port++ {
			if used[port] != "" {
				continue
			}
			l, err := net.Listen("tcp", address(port))
			if err != nil {
				continue
			}
			l.Close()
			used[port] = key
			next[key] = port
			return port, nil
		}
		return 0, fmt.Errorf("no available ports in discover range %d..%d", d.PortMin, d.PortMax)
	}
	seenNames := map[string]bool{}
	for _, path := range paths {
		name := names[path]
		if seenNames[name] {
			return fmt.Errorf("discovered worktree name collision %q; rename a checkout directory", name)
		}
		seenNames[name] = true
		if _, explicit := c.Worktrees[name]; explicit {
			continue
		}
		w := worktree{Services: map[string]service{}, Routes: slices.Clone(d.Routes)}
		labels := make([]string, 0, len(d.Services))
		for label := range d.Services {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		for _, label := range labels {
			s := d.Services[label]
			s.Dir = filepath.Join(path, s.Dir)
			if s.Type != "worker" {
				s.Port, err = portFor("auto/" + path + "/service/" + label)
				if err != nil {
					return err
				}
			}
			w.Services[label] = s
		}
		if d.Preview {
			w.PreviewPort, err = portFor("auto/" + path + "/preview")
			if err != nil {
				return err
			}
		}
		c.Worktrees[name] = w
	}
	state.Ports[c.path] = next
	return nil
}

func printDiscovery(c config) error {
	names := make([]string, 0, len(c.Worktrees))
	for name := range c.Worktrees {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Println("WORKTREE\tSERVICE\tPORT\tPREVIEW\tDIRECTORY")
	for _, name := range names {
		w := c.Worktrees[name]
		labels := serviceNames(w)
		for _, label := range labels {
			s := w.Services[label]
			fmt.Printf("%s\t%s\t%d\t%d\t%s\n", name, label, s.Port, w.PreviewPort, s.Dir)
		}
	}
	return nil
}
