package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
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
		fmt.Println("Flip — one address, several worktrees.\n\nflip [-config path] <name>\nflip [-config path] init|serve|status|discover|doctor|picker\nflip [-config path] supervisor status|stop\nflip [-config path] logs NAME SERVICE [-n 100] [-f]\nflip [-config path] up|use|down <name>\nflip [-config path] restart <name> <service>\nflip [-config path] register <project>\nflip projects|unregister <project>\nflip -project <project> <command>\n\nflip <name> is shorthand for flip use <name>. Services follow restart_on_use.\nConfig: -config, -project, FLIP_CONFIG, ancestor flip.json, then registered Git repository or main checkout flip.json.")
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
		fmt.Println("Created", *path, "— edit worktree paths and commands, then run flip NAME.")
		return nil
	}
	*path, err = resolveConfig(*path, *project)
	if err != nil {
		return err
	}
	if action == "register" {
		return projectCommand(action, a[1], *path)
	}
	c, err := readConfigMode(*path, action != "doctor" && action != "logs")
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
	if action == "supervisor" {
		if name == "stop" {
			return stopSupervisor(c)
		}
		info, err := probeSupervisor(c)
		if err != nil {
			return fmt.Errorf("supervisor unavailable: %w", err)
		}
		fmt.Printf("Supervisor running:%d at http://localhost:%d\n", info.PID, c.Port)
		return nil
	}
	if action == "use" || action == "up" || action == "restart" {
		if _, ok := c.Worktrees[name]; !ok {
			return fmt.Errorf("unknown worktree %q", name)
		}
		if err := ensureSupervisor(c); err != nil {
			return err
		}
	}
	if _, err := probeSupervisor(c); err != nil {
		return fmt.Errorf("supervisor unavailable: %w", err)
	}
	data, err := controlRequest(c, action, name, part, time.Duration(c.TimeoutSeconds*len(c.Worktrees[name].Services)+20)*time.Second)
	if err != nil {
		return err
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
	expected := map[string]int{"init": 1, "serve": 1, "status": 1, "doctor": 1, "picker": 1, "logs": 3, "up": 2, "use": 2, "down": 2, "restart": 3, "supervisor": 2, "register": 2, "unregister": 2, "projects": 1, "discover": 1}
	n, known := expected[args[0]]
	if !known && len(args) == 1 {
		return []string{"use", args[0]}, nil
	}
	if !known || len(args) != n {
		return nil, fmt.Errorf("invalid command; run flip -h")
	}
	if args[0] == "supervisor" && args[1] != "status" && args[1] != "stop" {
		return nil, fmt.Errorf("supervisor requires status or stop")
	}
	return args, nil
}
