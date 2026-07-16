package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hubfly-space/hubcdn/internal/dnsx"
)

// Registry is the concurrent set of known domains with JSON persistence and
// a background refresher that keeps origins and settings in sync with DNS.
type Registry struct {
	resolver *dnsx.Resolver
	path     string
	defaults Settings
	log      *slog.Logger

	activeEvery  time.Duration
	pendingEvery time.Duration

	mu      sync.RWMutex
	domains map[string]Snapshot
	dirty   bool
}

// Options configures a Registry.
type Options struct {
	Dir             string
	Resolver        *dnsx.Resolver
	Defaults        Settings
	RefreshInterval time.Duration
	PendingInterval time.Duration
	Log             *slog.Logger
}

// NewRegistry loads persisted domains from dir and returns a Registry.
func NewRegistry(opts Options) (*Registry, error) {
	r := &Registry{
		resolver:     opts.Resolver,
		path:         filepath.Join(opts.Dir, "domains.json"),
		defaults:     opts.Defaults,
		log:          opts.Log,
		activeEvery:  opts.RefreshInterval,
		pendingEvery: opts.PendingInterval,
		domains:      make(map[string]Snapshot),
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, fmt.Errorf("domain: reading %s: %w", r.path, err)
	}
	var stored []Snapshot
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("domain: parsing %s: %w", r.path, err)
	}
	for _, snap := range stored {
		if snap.OriginStr != "" {
			if u, err := dnsx.ParseOrigin(snap.OriginStr); err == nil {
				snap.Origin = u
			}
		}
		snap.Settings = ParseSettings(snap.ConfigText, r.defaults)
		r.domains[snap.Host] = snap
	}
	return r, nil
}

// Lookup returns the snapshot for host if it is registered.
func (r *Registry) Lookup(host string) (Snapshot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snap, ok := r.domains[host]
	return snap, ok
}

// All returns snapshots of every registered domain.
func (r *Registry) All() []Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Snapshot, 0, len(r.domains))
	for _, snap := range r.domains {
		out = append(out, snap)
	}
	return out
}

// Count returns the number of registered domains.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.domains)
}

// Observe registers host on first sight and immediately resolves its origin,
// so a domain whose TXT record already exists goes active on its very first
// request. Subsequent calls return the existing snapshot.
func (r *Registry) Observe(ctx context.Context, host string) Snapshot {
	if snap, ok := r.Lookup(host); ok {
		return snap
	}
	snap := Snapshot{
		Host:      host,
		Apex:      dnsx.Apex(host),
		Status:    StatusPending,
		Settings:  r.defaults,
		FirstSeen: time.Now(),
	}
	snap = r.refreshSnapshot(ctx, snap)

	r.mu.Lock()
	if existing, ok := r.domains[host]; ok {
		r.mu.Unlock()
		return existing
	}
	r.domains[host] = snap
	r.dirty = true
	r.mu.Unlock()
	r.log.Info("domain registered", "host", host, "status", snap.Status)
	return snap
}

// Refresh re-resolves host's origin and config records right away and
// returns the updated snapshot. Used when a pending domain is visited.
func (r *Registry) Refresh(ctx context.Context, host string) (Snapshot, bool) {
	snap, ok := r.Lookup(host)
	if !ok {
		return Snapshot{}, false
	}
	updated := r.refreshSnapshot(ctx, snap)
	r.store(updated)
	return updated, true
}

// refreshSnapshot resolves DNS for one domain and returns the new snapshot.
func (r *Registry) refreshSnapshot(ctx context.Context, snap Snapshot) Snapshot {
	now := time.Now()
	snap.LastRefreshed = now

	origin, err := r.resolver.Origin(ctx, snap.Host)
	switch {
	case err == nil:
		snap.Origin = origin
		snap.OriginStr = origin.String()
		snap.Status = StatusActive
		snap.LastError = ""
	case errors.Is(err, dnsx.ErrNoOrigin):
		snap.Origin = nil
		snap.OriginStr = ""
		snap.Status = StatusPending
		snap.LastError = ""
	default:
		// Transient DNS failure: keep the last known origin so an
		// upstream resolver hiccup never takes a live site down.
		if snap.Origin == nil {
			snap.Status = StatusPending
		} else {
			snap.Status = StatusError
		}
		snap.LastError = err.Error()
		return snap
	}

	cfg, err := r.resolver.ConfigText(ctx, snap.Host)
	if err == nil {
		snap.ConfigText = cfg
	}
	snap.Settings = ParseSettings(snap.ConfigText, r.defaults)
	return snap
}

func (r *Registry) store(snap Snapshot) {
	r.mu.Lock()
	old, ok := r.domains[snap.Host]
	r.domains[snap.Host] = snap
	r.dirty = true
	r.mu.Unlock()
	if ok && old.Status != snap.Status {
		r.log.Info("domain status changed", "host", snap.Host,
			"from", old.Status, "to", snap.Status, "origin", snap.OriginStr)
	}
}

// Run drives periodic refreshes and persistence until ctx is done. Pending
// domains are polled on the faster pending interval so setup feels
// instantaneous; active domains follow the regular interval.
func (r *Registry) Run(ctx context.Context) {
	tick := r.pendingEvery
	if tick <= 0 || tick > r.activeEvery {
		tick = r.activeEvery
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.save()
			return
		case <-ticker.C:
			r.refreshDue(ctx)
			r.save()
		}
	}
}

func (r *Registry) refreshDue(ctx context.Context) {
	now := time.Now()
	for _, snap := range r.All() {
		var due time.Duration
		if snap.Status == StatusActive {
			due = r.activeEvery
		} else {
			due = r.pendingEvery
		}
		if now.Sub(snap.LastRefreshed) < due {
			continue
		}
		r.store(r.refreshSnapshot(ctx, snap))
		if ctx.Err() != nil {
			return
		}
	}
}

// save persists the registry if it changed since the last save.
func (r *Registry) save() {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return
	}
	snaps := make([]Snapshot, 0, len(r.domains))
	for _, snap := range r.domains {
		snaps = append(snaps, snap)
	}
	r.dirty = false
	r.mu.Unlock()

	data, err := json.MarshalIndent(snaps, "", "  ")
	if err != nil {
		return
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		r.log.Error("persisting domains", "err", err)
		return
	}
	if err := os.Rename(tmp, r.path); err != nil {
		r.log.Error("persisting domains", "err", err)
	}
}
