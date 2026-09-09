package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "flip:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	f := flag.NewFlagSet("flip", flag.ContinueOnError)
	path := f.String("config", "", "configuration file")
	project := f.String("project", "", "registered project name")
	f.Usage = func() {
		fmt.Println("Flip — one address, several worktrees.\n\nflip [-config path] <name>\nflip [-config path] init|serve|status|discover\nflip [-config path] up|use|down <name>\nflip [-config path] restart <name> <service>\nflip [-config path] register <project>\nflip projects|unregister <project>\nflip -project <project> <command>\n\nflip <name> is shorthand for flip use <name>. Services follow restart_on_use.\nConfig: -config, -project, FLIP_CONFIG, ancestor flip.json, then registered Git repository or main checkout flip.json.")
	}
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	a := f.Args()
	if len(a) == 0 {
		f.Usage()
		return nil
	}
	a, err := normalizeCommand(a)
	if err != nil {
		return err
	}
	action := a[0]
	if action == "projects" || action == "unregister" {
		name := ""
		if len(a) > 1 {
			name = a[1]
		}
		return projectCommand(action, name, "")
	}
	if action == "init" {
		if *project != "" {
			return fmt.Errorf("init requires -config PATH or the current directory; -project selects existing configs")
		}
		if *path == "" {
			*path = os.Getenv("FLIP_CONFIG")
		}
		if *path == "" {
			*path = "flip.json"
		}
		f, err := os.OpenFile(*path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(example)
		ce := f.Close()
		if err != nil {
			return err
		}
		if ce != nil {
			return ce
		}
		fmt.Println("Created", *path, "— edit worktree paths and commands, then run flip serve.")
		return nil
	}
	*path, err = resolveConfig(*path, *project)
	if err != nil {
		return err
	}
	if action == "register" {
		return projectCommand(action, a[1], *path)
	}
	c, err := readConfig(*path)
	if err != nil {
		return err
	}
	if action == "serve" {
		return serve(c)
	}
	if action == "discover" {
		return printDiscovery(c)
	}
	name, part := "", ""
	if len(a) > 1 {
		name = a[1]
	}
	if len(a) > 2 {
		part = a[2]
	}
	token, err := os.ReadFile(filepath.Join(c.root, ".flip", "token"))
	if err != nil {
		return fmt.Errorf("start flip serve first: %w", err)
	}
	body, _ := json.Marshal(map[string]string{"Action": action, "Name": name, "Part": part})
	req, _ := http.NewRequest("POST", "http://"+address(c.ControlPort), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: time.Duration(c.TimeoutSeconds*len(c.Worktrees[name].Services)+20) * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s", bytes.TrimSpace(data))
	}
	fmt.Print(string(data))
	return nil
}

func normalizeCommand(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("missing command; run flip -h")
	}
	expected := map[string]int{"init": 1, "serve": 1, "status": 1, "up": 2, "use": 2, "down": 2, "restart": 3, "register": 2, "unregister": 2, "projects": 1, "discover": 1}
	n, known := expected[args[0]]
	if !known && len(args) == 1 {
		return []string{"use", args[0]}, nil
	}
	if !known || len(args) != n {
		return nil, fmt.Errorf("invalid command; run flip -h")
	}
	return args, nil
}
