package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
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

type proxyRoute struct {
	prefix string
	proxy  *httputil.ReverseProxy
}
type selection struct {
	name   string
	routes []proxyRoute
}
type manager struct {
	c         config
	mu        sync.Mutex // ponytail: serialize lifecycle commands; per-worktree locks if startup contention matters.
	processes map[string]map[string]*process
	previews  map[string]*atomic.Pointer[selection]
	active    atomic.Pointer[selection]
	ctx       context.Context
	cancel    context.CancelFunc
	activity  map[string]*activity
}

func newManager(ctx context.Context, c config) *manager {
	m := &manager{c: c, ctx: ctx, processes: map[string]map[string]*process{}, previews: map[string]*atomic.Pointer[selection]{}, activity: map[string]*activity{}}
	for name := range c.Worktrees {
		m.previews[name] = &atomic.Pointer[selection]{}
		m.activity[name] = &activity{}
	}
	return m
}
func routesFor(name string, w worktree) *selection {
	a := &selection{name: name}
	for _, r := range w.Routes {
		strip := ""
		if r.StripPrefix && r.Prefix != "/" {
			strip = r.Prefix
		}
		a.routes = append(a.routes, proxyRoute{prefix: r.Prefix, proxy: proxy(w.Services[r.Service].Port, strip)})
	}
	sort.Slice(a.routes, func(i, j int) bool { return len(a.routes[i].prefix) > len(a.routes[j].prefix) })
	return a
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
	m.serveSelection(m.active.Load(), w, r)
}
func (m *manager) previewHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { m.serveSelection(m.previews[name].Load(), w, r) })
}
func (m *manager) serveSelection(a *selection, w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		http.Error(w, "local hosts only", 403)
		return
	}
	if a == nil {
		http.Error(w, "No worktree ready. Run flip use <name> for the shared preview or flip up <name> for its own preview.", 503)
		return
	}
	activity := m.activity[a.name]
	if !activity.begin() {
		http.Error(w, "Worktree stopped. Run flip use <name>.", 503)
		return
	}
	defer activity.end() // ReverseProxy returns after streams and upgraded sockets close.
	for _, route := range a.routes {
		if route.prefix == "/" || r.URL.Path == route.prefix || strings.HasPrefix(r.URL.Path, route.prefix+"/") {
			route.proxy.ServeHTTP(w, r)
			return
		}
	}
	http.NotFound(w, r)
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
		fmt.Fprintln(&out, "WORKTREE\tSERVICE\tSTATE\tSELECTED\tPREVIEW")
		for _, n := range names {
			wt := m.c.Worktrees[n]
			preview := "-"
			if wt.PreviewPort != 0 {
				preview = fmt.Sprintf("http://localhost:%d", wt.PreviewPort)
			}
			for _, label := range serviceNames(wt) {
				state := processStatus(m.processes[n][label])
				if !wt.Services[label].enabled() {
					state = "disabled"
				}
				a := m.active.Load()
				fmt.Fprintf(&out, "%s\t%s\t%s\t%t\t%s\n", n, label, state, a != nil && a.name == n, preview)
			}
		}
		return out.String(), nil
	}
	wt, ok := m.c.Worktrees[name]
	if !ok {
		return "", fmt.Errorf("unknown worktree %q", name)
	}
	if action == "down" {
		return name + " stopped\n", m.stopWorktree(name)
	}
	if action != "up" && action != "use" && action != "restart" {
		return "", fmt.Errorf("unknown action %q", action)
	}
	if action == "restart" {
		s, ok := wt.Services[part]
		if !ok || !s.enabled() {
			return "", fmt.Errorf("%s has no enabled %s service", name, part)
		}
	}
	defer m.activity[name].touch()
	if err := os.MkdirAll(filepath.Join(m.c.root, ".flip"), 0700); err != nil {
		return "", err
	}
	procs := m.processes[name]
	if procs == nil {
		procs = map[string]*process{}
		m.processes[name] = procs
	}
	for _, label := range serviceNames(wt) {
		s := wt.Services[label]
		if !s.enabled() || action == "restart" && label != part {
			continue
		}
		if action == "restart" || action == "use" && s.restartOnUse(false) || !procs[label].running() {
			if err := procs[label].stop(); err != nil {
				return "", err
			}
			delete(procs, label)
			ctx, cancel := context.WithTimeout(m.ctx, time.Duration(m.c.TimeoutSeconds)*time.Second)
			p, err := start(ctx, s, filepath.Join(m.c.root, ".flip", name+"-"+label+".log"))
			cancel()
			if err != nil {
				return "", fmt.Errorf("%s/%s: %w", name, label, err)
			}
			procs[label] = p
		}
	}
	// Explicit restart affects only its service and never publishes a partial worktree.
	if action != "restart" {
		for label, s := range wt.Services {
			if s.enabled() && !procs[label].running() {
				return "", fmt.Errorf("%s exited before selection; inspect logs", label)
			}
		}
		a := routesFor(name, wt)
		m.previews[name].Store(a)
		if action == "use" {
			m.active.Store(a)
			return fmt.Sprintf("Selected %s at http://localhost:%d. Refresh your browser.\n", name, m.c.Port), nil
		}
	}
	if action == "restart" {
		return fmt.Sprintf("%s/%s restarted\n", name, part), nil
	}
	if wt.PreviewPort != 0 {
		return fmt.Sprintf("%s ready at http://localhost:%d\n", name, wt.PreviewPort), nil
	}
	return name + " ready\n", nil
}

// Caller holds mu. A failed stop retains its handle so cleanup can be retried.
func (m *manager) stopWorktree(name string) error {
	activity := m.activity[name]
	activity.mu.Lock()
	activity.stopped = true
	activity.mu.Unlock()
	if a := m.active.Load(); a != nil && a.name == name {
		m.active.Store(nil)
	}
	m.previews[name].Store(nil)
	var errs []error
	for label, p := range m.processes[name] {
		if err := p.stop(); err != nil {
			errs = append(errs, err)
		} else {
			delete(m.processes[name], label)
		}
	}
	return errors.Join(errs...)
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
	for name := range m.processes {
		m.stopWorktree(name)
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
		if req.Action == "supervisor-status" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(supervisorInfo{PID: os.Getpid(), Config: m.c.path})
			return
		}
		if req.Action == "supervisor-stop" && m.cancel != nil {
			fmt.Fprintln(w, "Stopping supervisor")
			m.cancel()
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
	lock, err := acquireLock(c, "supervisor")
	if err != nil {
		return fmt.Errorf("supervisor already running or lock unavailable: %w", err)
	}
	defer lock.Close()
	// Bind every listener before touching the control token; collisions leave no partial supervisor.
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
	previews := map[string]net.Listener{}
	defer func() {
		for _, l := range previews {
			l.Close()
		}
	}()
	for name, wt := range c.Worktrees {
		if wt.PreviewPort == 0 {
			continue
		}
		l, e := net.Listen("tcp", address(wt.PreviewPort))
		if e != nil {
			return fmt.Errorf("%s preview: %w", name, e)
		}
		previews[name] = l
	}
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
	ctx, cancel := signal.NotifyContext(context.Background(), stopSignals()...)
	defer cancel()
	m := newManager(ctx, c)
	m.cancel = cancel
	defer m.close()
	front := &http.Server{Handler: m, ReadHeaderTimeout: 5 * time.Second}
	api := &http.Server{Handler: control(m, token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second}
	defer front.Close()
	defer func() {
		cancel()
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := api.Shutdown(ctx); err != nil {
			api.Close()
		}
	}()
	errs := make(chan error, 2+len(previews))
	for name, l := range previews {
		server := &http.Server{Handler: m.previewHandler(name), ReadHeaderTimeout: 5 * time.Second}
		defer server.Close()
		go func() { errs <- server.Serve(l) }()
	}
	go func() { errs <- front.Serve(public) }()
	go func() { errs <- api.Serve(admin) }()
	go m.idleLoop()
	fmt.Printf("Flip listening at http://localhost:%d. Use another terminal for flip commands. Ctrl+C stops owned services.\n", c.Port)
	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		cancel()
		return err
	}
}
