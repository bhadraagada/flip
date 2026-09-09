package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
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
	defaultConfig := os.Getenv("FLIP_CONFIG")
	if defaultConfig == "" {
		defaultConfig = "flip.json"
	}
	path := f.String("config", defaultConfig, "configuration file; defaults to FLIP_CONFIG or flip.json")
	f.Usage = func() {
		fmt.Println("Flip — one address, several worktrees.\n\nflip [-config path] <name>\nflip [-config path] init|serve|status|doctor|picker\nflip [-config path] up|use|down <name>\nflip [-config path] restart <name> <service>\nflip [-config path] logs NAME SERVICE [-n 100] [-f]\n\nflip <name> is shorthand for flip use <name>. Services follow restart_on_use.\nConfig: -config, then FLIP_CONFIG, then flip.json in the current directory.")
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
	if action == "init" {
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
	c, err := readConfig(*path)
	if err != nil {
		if action == "doctor" {
			return fmt.Errorf("config check failed; fix %s before starting Flip: %w", *path, err)
		}
		return err
	}
	if action == "logs" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		return logs(ctx, c, a[1:], os.Stdout)
	}
	if action == "doctor" {
		return doctor(c, os.Stdout)
	}
	if action == "serve" {
		return serve(c)
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
	if args[0] == "logs" && len(args) >= 3 {
		return args, nil
	}
	expected := map[string]int{"init": 1, "serve": 1, "status": 1, "doctor": 1, "picker": 1, "up": 2, "use": 2, "down": 2, "restart": 3}
	n, known := expected[args[0]]
	if !known && len(args) == 1 {
		return []string{"use", args[0]}, nil
	}
	if !known || len(args) != n {
		return nil, fmt.Errorf("invalid command; run flip -h")
	}
	return args, nil
}
