package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func logs(ctx context.Context, c config, args []string, out io.Writer) error {
	f := flag.NewFlagSet("logs NAME SERVICE", flag.ContinueOnError)
	n := f.Int("n", 100, "recent lines, 0 for new output only")
	follow := f.Bool("f", false, "follow appended output until Ctrl+C")
	if len(args) < 2 {
		return fmt.Errorf("usage: flip logs NAME SERVICE [-n 100] [-f]")
	}
	if err := f.Parse(args[2:]); err != nil {
		return err
	}
	if f.NArg() != 0 || *n < 0 || *n > 100000 {
		return fmt.Errorf("logs: -n must be 0..100000; flags follow NAME SERVICE")
	}
	w, ok := c.Worktrees[args[0]]
	if !ok {
		return fmt.Errorf("unknown worktree %q", args[0])
	}
	s, ok := map[string]service{"ui": w.UI, "backend": w.Backend}[args[1]]
	if !ok || !s.configured() {
		return fmt.Errorf("%s has no %s service", args[0], args[1])
	}
	path := filepath.Join(c.root, ".flip", args[0]+"-"+args[1]+".log")
	fp, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("no readable log for %s/%s; start it with flip up %s: %w", args[0], args[1], args[0], err)
	}
	defer fp.Close()
	if err := recentLines(fp, *n, out); err != nil {
		return err
	}
	if !*follow {
		return nil
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			info, err := fp.Stat()
			if err != nil {
				return err
			}
			offset, err := fp.Seek(0, io.SeekCurrent)
			if err != nil {
				return err
			}
			if info.Size() < offset {
				if _, err = fp.Seek(0, io.SeekStart); err != nil {
					return err
				}
			}
			if _, err := io.Copy(out, fp); err != nil {
				return err
			}
		}
	}
}

// Scan backwards in blocks so recent output does not load an entire log into RAM.
func recentLines(f *os.File, n int, out io.Writer) error {
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil || n == 0 || end == 0 {
		return err
	}
	pos, start, count := end, int64(0), 0
	buf := make([]byte, 8192)
	for pos > 0 {
		size := min(int64(len(buf)), pos)
		pos -= size
		if _, err := f.ReadAt(buf[:size], pos); err != nil {
			return err
		}
		for i := int(size) - 1; i >= 0; i-- {
			if buf[i] == '\n' && pos+int64(i) != end-1 {
				count++
				if count == n {
					start = pos + int64(i) + 1
					break
				}
			}
		}
		if count == n {
			break
		}
	}
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	_, err = io.CopyN(out, f, end-start)
	return err
}
