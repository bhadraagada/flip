package cli

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed web/picker.html
var pickerHTML string

//go:embed web/picker.js
var pickerJS string

//go:embed web/picker.css
var pickerCSS string

type serviceView struct {
	Name, Status string
	Port         int
}
type worktreeView struct {
	Name, URL    string
	Branch       string
	LastActivity int64
	Selected     bool
	Services     []serviceView
}
type previewState struct {
	URL       string
	Worktrees []worktreeView
	Branches  []branchView
}

func (m *manager) previewState() previewState {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := previewState{URL: fmt.Sprintf("http://localhost:%d", m.c.Port), Worktrees: []worktreeView{}}
	names := make([]string, 0, len(m.c.Worktrees))
	for name := range m.c.Worktrees {
		names = append(names, name)
	}
	sort.Strings(names)
	roots := map[string]string{}
	for _, name := range names {
		w := m.c.Worktrees[name]
		row := worktreeView{Name: name, Services: []serviceView{}}
		root := worktreeRoot(w)
		if root != "" {
			roots[name] = root
		}
		if a := m.activity[name]; a != nil {
			a.mu.Lock()
			if a.last.Unix() > row.LastActivity {
				row.LastActivity = a.last.Unix()
			}
			a.mu.Unlock()
		}
		if a := m.active.Load(); a != nil {
			row.Selected = a.name == name
		}
		if w.PreviewPort != 0 {
			row.URL = fmt.Sprintf("http://localhost:%d", w.PreviewPort)
		}
		for _, label := range serviceNames(w) {
			s := w.Services[label]
			status := processStatus(m.processes[name][label])
			if !s.enabled() {
				status = "disabled"
			}
			row.Services = append(row.Services, serviceView{label, status, s.Port})
		}
		state.Worktrees = append(state.Worktrees, row)
	}
	// Independent Git scans keep a project with many worktrees responsive.
	var scans sync.WaitGroup
	for i := range state.Worktrees {
		scans.Go(func() {
			row := &state.Worktrees[i]
			branch, last := gitActivity(roots[row.Name])
			row.Branch = branch
			if last > row.LastActivity {
				row.LastActivity = last
			}
		})
	}
	scans.Wait()
	sort.SliceStable(state.Worktrees, func(i, j int) bool {
		a, b := state.Worktrees[i], state.Worktrees[j]
		if a.Selected != b.Selected {
			return a.Selected
		}
		return a.LastActivity > b.LastActivity
	})
	state.Branches = branchList(roots)
	return state
}

type picker struct {
	m                *manager
	mu               sync.Mutex
	grants, sessions map[string]time.Time
}

func newPicker(m *manager) *picker {
	return &picker{m: m, grants: map[string]time.Time{}, sessions: map[string]time.Time{}}
}

func (p *picker) credential(entries map[string]time.Time, ttl time.Duration) (string, error) {
	for key, expiry := range entries {
		if !time.Now().Before(expiry) {
			delete(entries, key)
		}
	}
	if len(entries) >= 32 {
		return "", fmt.Errorf("too many picker sessions; wait for an existing session to expire")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := hex.EncodeToString(b)
	entries[key] = time.Now().Add(ttl)
	return key, nil
}

func (p *picker) link() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	grant, err := p.credential(p.grants, time.Minute)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://%s/picker#%s\n", address(p.m.c.ControlPort), grant), nil
}

func (p *picker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	origin := "http://" + address(p.m.c.ControlPort)
	if r.Host != address(p.m.c.ControlPort) {
		http.Error(w, "local picker host required", 403)
		return
	}
	if r.Method == "GET" {
		switch r.URL.Path {
		case "/picker":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, pickerHTML)
		case "/picker.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			fmt.Fprint(w, pickerJS)
		case "/picker.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			fmt.Fprint(w, pickerCSS)
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.Method != "POST" || r.Header.Get("Origin") != origin || r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "unauthorized picker request", 403)
		return
	}
	var req struct{ Grant, Action, Name string }
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if r.URL.Path == "/picker/session" {
		p.mu.Lock()
		expiry, ok := p.grants[req.Grant]
		delete(p.grants, req.Grant)
		if !ok || !time.Now().Before(expiry) {
			p.mu.Unlock()
			http.Error(w, "Login link expired or already used. Run flip picker for a new link.", 403)
			return
		}
		session, err := p.credential(p.sessions, time.Hour)
		p.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), 429)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"session": session})
		return
	}
	if r.URL.Path != "/picker/api" {
		http.NotFound(w, r)
		return
	}
	p.mu.Lock()
	expiry, ok := p.sessions[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	p.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || !ok || !time.Now().Before(expiry) {
		http.Error(w, "Session expired. Run flip picker for a new link.", 403)
		return
	}
	if req.Action == "use" || req.Action == "branch" {
		if _, err := p.m.command(req.Action, req.Name, ""); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	} else if req.Action != "status" {
		http.Error(w, "picker supports status, use and branch only", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p.m.previewState())
}
