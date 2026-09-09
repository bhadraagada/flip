package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type pair struct{ ui, backend *process }
type selection struct {
	name        string
	ui, backend *httputil.ReverseProxy
}
type manager struct {
	c         config
	mu        sync.Mutex // ponytail: serialize lifecycle commands; per-worktree locks if startup contention matters.
	processes map[string]*pair
	active    atomic.Pointer[selection]
	ctx       context.Context
}

func newManager(ctx context.Context, c config) *manager {
	return &manager{c: c, ctx: ctx, processes: map[string]*pair{}}
}
func proxy(port int, strip string) *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: address(port)}
	return &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.Out.Host = r.In.Host
		r.SetXForwarded()
		if strip != "" {
			r.Out.URL.Path = strings.TrimPrefix(r.Out.URL.Path, strip)
			r.Out.URL.RawPath = ""
			if r.Out.URL.Path == "" {
				r.Out.URL.Path = "/"
			}
		}
	}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "Selected service is unavailable. Check flip status and service logs.", 502)
	}}
}
func (m *manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		http.Error(w, "local hosts only", 403)
		return
	}
	a := m.active.Load()
	if a == nil {
		http.Error(w, "No worktree selected. Run flip use <name>.", 503)
		return
	}
	p := a.ui
	if r.URL.Path == m.c.APIPrefix || strings.HasPrefix(r.URL.Path, m.c.APIPrefix+"/") {
		p = a.backend
	}
	p.ServeHTTP(w, r)
}
func (m *manager) command(action, name, part string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if action == "status" {
		names := make([]string, 0, len(m.c.Worktrees))
		for n := range m.c.Worktrees {
			names = append(names, n)
		}
		sort.Strings(names)
		var out strings.Builder
		fmt.Fprintln(&out, "WORKTREE\tUI\tBACKEND\tSELECTED")
		for _, n := range names {
			p := m.processes[n]
			if p == nil {
				p = &pair{}
			}
			a := m.active.Load()
			fmt.Fprintf(&out, "%s\t%s\t%s\t%t\n", n, processStatus(p.ui), processStatus(p.backend), a != nil && a.name == n)
		}
		return out.String(), nil
	}
	w, ok := m.c.Worktrees[name]
	if !ok {
		return "", fmt.Errorf("unknown worktree %q", name)
	}
	p := m.processes[name]
	if p == nil {
		p = &pair{}
		m.processes[name] = p
	}
	if action == "down" {
		if a := m.active.Load(); a != nil && a.name == name {
			m.active.Store(nil)
		}
		e1, e2 := p.ui.stop(), p.backend.stop()
		if e1 != nil {
			return "", e1
		}
		if e2 != nil {
			return "", e2
		}
		p.ui = nil
		p.backend = nil
		return name + " stopped\n", nil
	}
	if action != "up" && action != "use" && action != "restart" {
		return "", fmt.Errorf("unknown action %q", action)
	}
	if action == "restart" && part != "backend" && part != "ui" {
		return "", fmt.Errorf("restart requires backend or ui")
	}
	if err := os.MkdirAll(filepath.Join(m.c.root, ".flip"), 0700); err != nil {
		return "", err
	}
	ensure := func(dst **process, s service, label string, restart bool) error {
		if restart || !(*dst).running() {
			if err := (*dst).stop(); err != nil {
				return err
			}
			*dst = nil
			ctx, cancel := context.WithTimeout(m.ctx, time.Duration(m.c.TimeoutSeconds)*time.Second)
			defer cancel()
			p, err := start(ctx, s, filepath.Join(m.c.root, ".flip", name+"-"+label+".log"))
			if err != nil {
				return err
			}
			*dst = p
		}
		return nil
	}
	if action != "restart" || part == "ui" {
		if err := ensure(&p.ui, w.UI, "ui", action == "restart"); err != nil {
			return "", err
		}
	}
	if action != "restart" || part == "backend" {
		if err := ensure(&p.backend, w.Backend, "backend", action == "use" || action == "restart"); err != nil {
			return "", err
		}
	}
	if action == "use" {
		if !p.ui.running() || !p.backend.running() {
			return "", fmt.Errorf("a service exited before selection; inspect logs")
		}
		strip := ""
		if m.c.StripPrefix {
			strip = m.c.APIPrefix
		}
		m.active.Store(&selection{name: name, ui: proxy(w.UI.Port, ""), backend: proxy(w.Backend.Port, strip)})
		return fmt.Sprintf("Selected %s at http://localhost:%d. Refresh your browser.\n", name, m.c.Port), nil
	}
	return name + " ready\n", nil
}
func processStatus(p *process) string {
	if p == nil {
		return "stopped"
	}
	if !p.running() {
		return "exited"
	}
	return fmt.Sprintf("running:%d", p.cmd.Process.Pid)
}
func (m *manager) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.processes {
		p.ui.stop()
		p.backend.stop()
	}
}

func control(m *manager, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", 403)
			return
		}
		var req struct{ Action, Name, Part string }
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if err := d.Decode(&req); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		out, err := m.command(req.Action, req.Name, req.Part)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		fmt.Fprint(w, out)
	})
}
func serve(c config) error {
	// Bind both listeners before touching the control token; another instance wins cleanly.
	admin, err := net.Listen("tcp", address(c.ControlPort))
	if err != nil {
		return err
	}
	defer admin.Close()
	public, err := net.Listen("tcp", address(c.Port))
	if err != nil {
		return err
	}
	defer public.Close()
	dir := filepath.Join(c.root, ".flip")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return err
	}
	token := hex.EncodeToString(b)
	tokenPath := filepath.Join(dir, "token")
	if err = os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		return err
	}
	defer os.Remove(tokenPath)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	m := newManager(ctx, c)
	defer m.close()
	front := &http.Server{Handler: m, ReadHeaderTimeout: 5 * time.Second}
	api := &http.Server{Handler: control(m, token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second}
	defer front.Close()
	defer api.Close()
	errs := make(chan error, 2)
	go func() { errs <- front.Serve(public) }()
	go func() { errs <- api.Serve(admin) }()
	fmt.Printf("Flip listening at http://localhost:%d. Use another terminal for flip commands. Ctrl+C stops owned services.\n", c.Port)
	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		cancel()
		return err
	}
}
