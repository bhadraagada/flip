package main

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

//go:embed picker.html
var pickerHTML string

//go:embed picker.js
var pickerJS string

//go:embed picker.css
var pickerCSS string

type serviceView struct {
	Name, Status string
	Port         int
}
type worktreeView struct {
	Name, URL string
	Selected  bool
	Services  []serviceView
}
type previewState struct {
	URL       string
	Worktrees []worktreeView
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
	for _, name := range names {
		w := m.c.Worktrees[name]
		row := worktreeView{Name: name, Services: []serviceView{}}
		if a := m.active.Load(); a != nil {
			row.Selected = a.name == name
		}
		p := m.processes[name]
		if p == nil {
			p = &pair{}
		}
		for _, item := range []struct {
			name string
			s    service
			p    *process
		}{{"ui", w.UI, p.ui}, {"backend", w.Backend, p.backend}} {
			if item.s.configured() {
				row.Services = append(row.Services, serviceView{item.name, processStatus(item.p), item.s.Port})
			}
		}
		state.Worktrees = append(state.Worktrees, row)
	}
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
	if !ok || !time.Now().Before(expiry) {
		http.Error(w, "Session expired. Run flip picker for a new link.", 403)
		return
	}
	if req.Action == "use" {
		if _, err := p.m.command("use", req.Name, ""); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	} else if req.Action != "status" {
		http.Error(w, "picker supports status and use only", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p.m.previewState())
}
