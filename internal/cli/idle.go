package cli

import (
	"fmt"
	"sync"
	"time"
)

type activity struct {
	mu       sync.Mutex
	requests int
	last     time.Time
	stopped  bool
}

func (a *activity) begin() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return false
	}
	a.requests++
	return true
}

func (a *activity) end() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests--
	a.last = time.Now()
}

func (a *activity) touch() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.last, a.stopped = time.Now(), false
}

func (m *manager) reapIdle(now time.Time) {
	if m.c.IdleTimeoutSeconds == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, a := range m.activity {
		a.mu.Lock()
		expired := !a.stopped && !a.last.IsZero() && a.requests == 0 && now.Sub(a.last)/time.Second >= time.Duration(m.c.IdleTimeoutSeconds)
		if expired {
			a.stopped = true
		}
		a.mu.Unlock()
		if expired {
			if err := m.stopWorktree(name); err != nil {
				fmt.Printf("Idle shutdown %s: %v\n", name, err)
			} else {
				fmt.Printf("Idle worktree %s stopped\n", name)
			}
		}
	}
}

func (m *manager) idleLoop() {
	if m.c.IdleTimeoutSeconds == 0 {
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-ticker.C:
			m.reapIdle(now)
		}
	}
}
