// Package certguard protects the node from exhausting ACME CA rate limits.
//
// Let's Encrypt caps issuance at 50 certificates per registered domain per
// week, plus global account limits. A single misbehaving user pointing
// wildcard subdomains (a.example.com, b.example.com, ...) at the node could
// burn through that in minutes. certguard enforces sliding-window budgets
// before any order is placed:
//
//   - per apex domain (eTLD+1) per day and per week
//   - globally per hour and per week
//
// Issuance events are persisted to disk so restarts do not reset the
// windows.
package certguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Limits configures the guard. A zero value disables that limit.
type Limits struct {
	PerApexPerDay  int
	PerApexPerWeek int
	GlobalPerHour  int
	GlobalPerWeek  int
}

// ErrLimited is wrapped by every rejection so callers can detect it with
// errors.Is.
var ErrLimited = errors.New("certificate issuance limit reached")

// Guard tracks certificate issuance events and answers whether a new
// certificate may be requested.
type Guard struct {
	limits Limits
	path   string

	mu     sync.Mutex
	events map[string][]time.Time // apex -> issuance times, newest last
	now    func() time.Time
}

const retention = 8 * 24 * time.Hour

// New loads persisted issuance history from dir and returns a ready Guard.
func New(dir string, limits Limits) (*Guard, error) {
	g := &Guard{
		limits: limits,
		path:   filepath.Join(dir, "certguard.json"),
		events: make(map[string][]time.Time),
		now:    time.Now,
	}
	data, err := os.ReadFile(g.path)
	if err != nil {
		if os.IsNotExist(err) {
			return g, nil
		}
		return nil, fmt.Errorf("certguard: reading %s: %w", g.path, err)
	}
	if err := json.Unmarshal(data, &g.events); err != nil {
		return nil, fmt.Errorf("certguard: parsing %s: %w", g.path, err)
	}
	g.prune()
	return g, nil
}

// Check reports whether a certificate for a host under apex may be issued
// right now. It does not record anything; call Record once issuance actually
// happens.
func (g *Guard) Check(apex string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked()

	now := g.now()
	if n := g.limits.PerApexPerDay; n > 0 && g.countSince(apex, now.Add(-24*time.Hour)) >= n {
		return fmt.Errorf("%w: %s used its budget of %d certificates per day", ErrLimited, apex, n)
	}
	if n := g.limits.PerApexPerWeek; n > 0 && g.countSince(apex, now.Add(-7*24*time.Hour)) >= n {
		return fmt.Errorf("%w: %s used its budget of %d certificates per week", ErrLimited, apex, n)
	}
	if n := g.limits.GlobalPerHour; n > 0 && g.countAllSince(now.Add(-time.Hour)) >= n {
		return fmt.Errorf("%w: node-wide budget of %d certificates per hour", ErrLimited, n)
	}
	if n := g.limits.GlobalPerWeek; n > 0 && g.countAllSince(now.Add(-7*24*time.Hour)) >= n {
		return fmt.Errorf("%w: node-wide budget of %d certificates per week", ErrLimited, n)
	}
	return nil
}

// Record notes that a certificate was issued for a host under apex and
// persists the updated history.
func (g *Guard) Record(apex string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events[apex] = append(g.events[apex], g.now())
	g.saveLocked()
}

func (g *Guard) countSince(apex string, cutoff time.Time) int {
	n := 0
	for _, t := range g.events[apex] {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}

func (g *Guard) countAllSince(cutoff time.Time) int {
	n := 0
	for apex := range g.events {
		n += g.countSince(apex, cutoff)
	}
	return n
}

func (g *Guard) prune() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked()
}

func (g *Guard) pruneLocked() {
	cutoff := g.now().Add(-retention)
	for apex, times := range g.events {
		kept := times[:0]
		for _, t := range times {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(g.events, apex)
		} else {
			g.events[apex] = kept
		}
	}
}

func (g *Guard) saveLocked() {
	data, err := json.Marshal(g.events)
	if err != nil {
		return
	}
	tmp := g.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, g.path)
}
