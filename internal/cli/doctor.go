package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func doctor(c config, out io.Writer) error {
	failures := 0
	check := func(ok bool, format string, args ...any) {
		label := "OK"
		if !ok {
			label = "FAIL"
			failures++
		}
		fmt.Fprintf(out, "%s  %s\n", label, fmt.Sprintf(format, args...))
	}
	fmt.Fprintln(out, "OK  Config parsed and directories validated. Checks never start or stop apps.")
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	state := previewState{}
	_, err := probeSupervisor(c)
	if err == nil {
		var data []byte
		data, err = controlRequest(c, "inspect", "", "", time.Second)
		if err == nil {
			err = json.Unmarshal(data, &state)
		}
	}
	online := err == nil
	if online {
		fmt.Fprintln(out, "OK  Authenticated supervisor answered.")
	} else {
		fmt.Fprintln(out, "WARN  Supervisor unavailable or busy; inspect flip supervisor status, or run flip serve to enable live checks.")
	}
	checkPort := func(label string, port int, listening bool) {
		if listening {
			conn, err := net.DialTimeout("tcp", address(port), time.Second)
			if err != nil {
				check(false, "%s port %d is unreachable; inspect flip supervisor status", label, port)
				return
			}
			conn.Close()
			check(true, "%s listening at http://%s", label, address(port))
			return
		}
		listener, err := net.Listen("tcp", address(port))
		if err != nil {
			check(false, "%s port %d is unavailable; inspect its owner or choose another port: %v", label, port, err)
			return
		}
		listener.Close()
		check(true, "%s port %d is available", label, port)
	}
	for _, item := range []struct {
		name string
		port int
	}{{"shared preview", c.Port}, {"control", c.ControlPort}} {
		checkPort(item.name, item.port, online)
	}
	running := map[string]bool{}
	for _, tree := range state.Worktrees {
		for _, svc := range tree.Services {
			running[tree.Name+"/"+svc.Name] = strings.HasPrefix(svc.Status, "running:")
		}
	}
	names := make([]string, 0, len(c.Worktrees))
	for n := range c.Worktrees {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		w := c.Worktrees[name]
		for _, serviceName := range serviceNames(w) {
			s := w.Services[serviceName]
			if !s.enabled() {
				fmt.Fprintf(out, "SKIP  %s/%s is disabled\n", name, serviceName)
				continue
			}
			label := name + "/" + serviceName
			command := strings.ReplaceAll(s.Command[0], "{port}", strconv.Itoa(s.Port))
			if strings.ContainsAny(command, `/\`) && !filepath.IsAbs(command) {
				command = filepath.Join(s.Dir, command)
			}
			_, e := exec.LookPath(command)
			if e != nil {
				check(false, "%s command lookup failed; install the executable or correct command[0] and PATH: %v", label, e)
			} else {
				check(true, "%s command executable found", label)
			}
			if s.Type == "worker" {
				fmt.Fprintf(out, "SKIP  %s readiness: worker has no HTTP probe\n", label)
				continue
			}
			if !running[label] {
				checkPort(label, s.Port, false)
				fmt.Fprintf(out, "SKIP  %s readiness: service is stopped or ownership is unverified\n", label)
				continue
			}
			resp, e := client.Get("http://" + address(s.Port) + s.Health)
			if e != nil {
				check(false, "%s readiness unreachable; inspect flip logs %s %s", label, name, serviceName)
				continue
			}
			resp.Body.Close()
			check(resp.StatusCode >= 200 && resp.StatusCode < 400, "%s readiness returned HTTP %d; on failure inspect health path and flip logs %s %s", label, resp.StatusCode, name, serviceName)
		}
		for _, route := range w.Routes {
			fmt.Fprintf(out, "OK  %s route %s -> %s, strip=%t. No preview request sent.\n", name, route.Prefix, route.Service, route.StripPrefix)
		}
		if w.PreviewPort != 0 {
			checkPort(name+" preview", w.PreviewPort, online)
		}
	}
	if online {
		selected := "none"
		for _, tree := range state.Worktrees {
			if tree.Selected {
				selected = tree.Name
			}
		}
		fmt.Fprintf(out, "OK  Selected worktree: %s. Routing was inspected without waking services.\n", selected)
	}
	if failures > 0 {
		return fmt.Errorf("doctor found %d failed checks; resolve the FAIL lines and rerun", failures)
	}
	return nil
}
