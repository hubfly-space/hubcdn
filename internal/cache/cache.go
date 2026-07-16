// Package cache implements hubCDN's in-memory object cache.
//
// The cache is sharded 256 ways to keep lock contention negligible under
// high concurrency. Every entry is accounted by its real byte footprint and
// evicted LRU-first when the shard exceeds its share of the global budget.
// The budget itself is dynamic: a watchdog (see memory.go) shrinks it when
// the host machine runs low on memory and grows it back when pressure
// clears, so a node keeps working on small VPSes as well as large edges.
//
// Keys are derived with SHA-256 (see Key), so raw request URLs are never
// used as map keys and entries cannot collide or be forged across domains.
package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const shardCount = 256

// Object is a cached HTTP response.
type Object struct {
	Status   int
	Header   http.Header
	Body     []byte
	StoredAt time.Time
	TTL      time.Duration
}

// Expired reports whether the object outlived its TTL.
func (o *Object) Expired(now time.Time) bool {
	return now.After(o.StoredAt.Add(o.TTL))
}

// Age returns the object's current age, for the Age response header.
func (o *Object) Age(now time.Time) time.Duration {
	return now.Sub(o.StoredAt)
}

func (o *Object) size() int64 {
	n := int64(len(o.Body)) + 256 // struct + map overhead estimate
	for k, vs := range o.Header {
		n += int64(len(k))
		for _, v := range vs {
			n += int64(len(v))
		}
	}
	return n
}

// Stats is a point-in-time snapshot of cache activity.
type Stats struct {
	Entries   int64
	Bytes     int64
	Budget    int64
	Hits      uint64
	Misses    uint64
	Evictions uint64
}

type entry struct {
	key  string
	obj  *Object
	size int64
	elem *list.Element
}

type shard struct {
	mu    sync.Mutex
	items map[string]*entry
	lru   *list.List // front = most recently used
	bytes int64
}

// Cache is a sharded LRU byte-bounded cache. Safe for concurrent use.
type Cache struct {
	shards [shardCount]*shard
	budget atomic.Int64

	entries   atomic.Int64
	bytes     atomic.Int64
	hits      atomic.Uint64
	misses    atomic.Uint64
	evictions atomic.Uint64
}

// New creates a cache with the given byte budget.
func New(budget int64) *Cache {
	c := &Cache{}
	c.budget.Store(budget)
	for i := range c.shards {
		c.shards[i] = &shard{items: make(map[string]*entry), lru: list.New()}
	}
	return c
}

// Key derives the cache key for a request. It binds host, method, path,
// query and the encoding the client accepts, hashed so the in-memory key
// space carries no readable request data.
func Key(host, method, uri, acceptEncoding string) string {
	h := sha256.New()
	h.Write([]byte(host))
	h.Write([]byte{0})
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(uri))
	h.Write([]byte{0})
	h.Write([]byte(acceptEncoding))
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Cache) shardFor(key string) *shard {
	// Keys are uniformly distributed SHA-256 hex; the first byte pair
	// selects the shard.
	return c.shards[hexByte(key[0])<<4|hexByte(key[1])]
}

func hexByte(b byte) int {
	if b >= 'a' {
		return int(b-'a') + 10
	}
	return int(b - '0')
}

// Get returns the cached object for key if present and fresh.
func (c *Cache) Get(key string) (*Object, bool) {
	s := c.shardFor(key)
	s.mu.Lock()
	e, ok := s.items[key]
	if !ok {
		s.mu.Unlock()
		c.misses.Add(1)
		return nil, false
	}
	if e.obj.Expired(time.Now()) {
		s.removeLocked(e)
		s.mu.Unlock()
		c.entries.Add(-1)
		c.bytes.Add(-e.size)
		c.misses.Add(1)
		return nil, false
	}
	s.lru.MoveToFront(e.elem)
	obj := e.obj
	s.mu.Unlock()
	c.hits.Add(1)
	return obj, true
}

// Set stores obj under key, evicting LRU entries as needed. Objects larger
// than the whole per-shard budget are silently rejected.
func (c *Cache) Set(key string, obj *Object) {
	size := obj.size()
	shardBudget := c.budget.Load() / shardCount
	if shardBudget <= 0 || size > shardBudget {
		return
	}
	s := c.shardFor(key)
	s.mu.Lock()
	if old, ok := s.items[key]; ok {
		s.removeLocked(old)
		c.entries.Add(-1)
		c.bytes.Add(-old.size)
	}
	e := &entry{key: key, obj: obj, size: size}
	e.elem = s.lru.PushFront(e)
	s.items[key] = e
	s.bytes += size
	c.entries.Add(1)
	c.bytes.Add(size)
	c.evictOverLocked(s, shardBudget)
	s.mu.Unlock()
}

// Delete removes key if present.
func (c *Cache) Delete(key string) {
	s := c.shardFor(key)
	s.mu.Lock()
	if e, ok := s.items[key]; ok {
		s.removeLocked(e)
		c.entries.Add(-1)
		c.bytes.Add(-e.size)
	}
	s.mu.Unlock()
}

// SetBudget adjusts the global byte budget and immediately evicts entries
// from shards that exceed their new share.
func (c *Cache) SetBudget(budget int64) {
	if budget < 0 {
		budget = 0
	}
	c.budget.Store(budget)
	shardBudget := budget / shardCount
	for _, s := range c.shards {
		s.mu.Lock()
		c.evictOverLocked(s, shardBudget)
		s.mu.Unlock()
	}
}

// Budget returns the current global byte budget.
func (c *Cache) Budget() int64 { return c.budget.Load() }

// Stats returns a snapshot of cache counters.
func (c *Cache) Stats() Stats {
	return Stats{
		Entries:   c.entries.Load(),
		Bytes:     c.bytes.Load(),
		Budget:    c.budget.Load(),
		Hits:      c.hits.Load(),
		Misses:    c.misses.Load(),
		Evictions: c.evictions.Load(),
	}
}

func (c *Cache) evictOverLocked(s *shard, shardBudget int64) {
	for s.bytes > shardBudget {
		back := s.lru.Back()
		if back == nil {
			return
		}
		e := back.Value.(*entry)
		s.removeLocked(e)
		c.entries.Add(-1)
		c.bytes.Add(-e.size)
		c.evictions.Add(1)
	}
}

func (s *shard) removeLocked(e *entry) {
	delete(s.items, e.key)
	s.lru.Remove(e.elem)
	s.bytes -= e.size
}
