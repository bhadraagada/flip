package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
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
	token, err := os.ReadFile(filepath.Join(c.root, ".flip", "token"))
	if err == nil {
		req, _ := http.NewRequest("POST", "http://"+address(c.ControlPort), strings.NewReader(`{"Action":"inspect"}`))
		req.Header.Set("Authorization", "Bearer "+string(token))
		req.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(req)
		if e == nil {
			if resp.StatusCode == 200 {
				err = json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&state)
			} else {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			resp.Body.Close()
		} else {
			err = e
		}
	}
	online := err == nil
	if online {
		fmt.Fprintln(out, "OK  Authenticated supervisor answered.")
	} else {
		fmt.Fprintln(out, "WARN  Supervisor unavailable; run flip serve to enable live ownership and routing checks.")
	}
	available := func(port int) bool {
		l, e := net.Listen("tcp", address(port))
		if e != nil {
			return false
		}
		l.Close()
		return true
	}
	for _, item := range []struct {
		name string
		port int
	}{{"shared preview", c.Port}, {"control", c.ControlPort}} {
		if online {
			fmt.Fprintf(out, "OK  %s configured at http://%s\n", item.name, address(item.port))
		} else {
			check(available(item.port), "%s port %d must be free; if occupied, choose another port or inspect its owner", item.name, item.port)
		}
	}
	names := make([]string, 0, len(c.Worktrees))
	for n := range c.Worktrees {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		w := c.Worktrees[name]
		for _, item := range []struct {
			name string
			s    service
		}{{"ui", w.UI}, {"backend", w.Backend}} {
			s := item.s
			if !s.configured() {
				continue
			}
			label := name + "/" + item.name
			command := strings.ReplaceAll(s.Command[0], "{port}", strconv.Itoa(s.Port))
			if strings.ContainsAny(command, `/\`) && !filepath.IsAbs(command) {
				command = filepath.Join(s.Dir, command)
			}
			_, e := exec.LookPath(command)
			check(e == nil, "%s command executable lookup%s", label, diagnosticHint(e, "; install the executable or correct command[0] and PATH"))
			running := false
			for _, tree := range state.Worktrees {
				if tree.Name == name {
					for _, svc := range tree.Services {
						if svc.Name == item.name {
							running = strings.HasPrefix(svc.Status, "running:")
						}
					}
				}
			}
			if !running {
				check(available(s.Port), "%s port %d is available for startup; occupied ports are never taken over", label, s.Port)
				fmt.Fprintf(out, "SKIP  %s readiness: service is stopped or ownership is unverified\n", label)
				continue
			}
			resp, e := client.Get("http://" + address(s.Port) + s.Health)
			if e != nil {
				check(false, "%s readiness unreachable; inspect flip logs %s %s", label, name, item.name)
				continue
			}
			resp.Body.Close()
			check(resp.StatusCode >= 200 && resp.StatusCode < 400, "%s readiness returned HTTP %d; on failure inspect health path and flip logs %s %s", label, resp.StatusCode, name, item.name)
		}
		fmt.Fprintf(out, "OK  %s routing: API prefix %s, strip=%t; configured service fallback applies. No preview request sent.\n", name, c.APIPrefix, c.StripPrefix)
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

func diagnosticHint(err error, hint string) string {
	if err != nil {
		return " failed" + hint
	}
	return " passed"
}
