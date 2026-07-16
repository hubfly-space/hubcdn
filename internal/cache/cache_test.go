package cache

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

func obj(body string, ttl time.Duration) *Object {
	return &Object{
		Status:   http.StatusOK,
		Header:   http.Header{"Content-Type": {"text/plain"}},
		Body:     []byte(body),
		StoredAt: time.Now(),
		TTL:      ttl,
	}
}

func TestGetSet(t *testing.T) {
	c := New(1 << 20)
	key := Key("example.com", "GET", "/a", "gzip")
	if _, ok := c.Get(key); ok {
		t.Fatal("unexpected hit on empty cache")
	}
	c.Set(key, obj("hello", time.Minute))
	got, ok := c.Get(key)
	if !ok || string(got.Body) != "hello" {
		t.Fatalf("want hello, got %v %v", got, ok)
	}
}

func TestExpiry(t *testing.T) {
	c := New(1 << 20)
	key := Key("example.com", "GET", "/a", "")
	o := obj("x", time.Minute)
	o.StoredAt = time.Now().Add(-2 * time.Minute)
	c.Set(key, o)
	if _, ok := c.Get(key); ok {
		t.Fatal("expired object served")
	}
}

func TestKeyIsolation(t *testing.T) {
	c := New(1 << 20)
	c.Set(Key("a.com", "GET", "/x", ""), obj("from-a", time.Minute))
	if _, ok := c.Get(Key("b.com", "GET", "/x", "")); ok {
		t.Fatal("cache leaked an object across hosts")
	}
	if _, ok := c.Get(Key("a.com", "GET", "/x", "gzip")); ok {
		t.Fatal("cache leaked an object across encoding variants")
	}
}

func TestBudgetEviction(t *testing.T) {
	c := New(int64(shardCount) * 2048)
	// Fill well past the budget; total bytes must stay bounded.
	for i := 0; i < 5000; i++ {
		c.Set(Key("example.com", "GET", fmt.Sprintf("/%d", i), ""), obj("0123456789", time.Minute))
	}
	stats := c.Stats()
	if stats.Bytes > stats.Budget {
		t.Fatalf("cache over budget: %d > %d", stats.Bytes, stats.Budget)
	}
	if stats.Evictions == 0 {
		t.Fatal("expected evictions when over budget")
	}
}

func TestOversizeObjectRejected(t *testing.T) {
	c := New(shardCount * 512)
	key := Key("example.com", "GET", "/big", "")
	c.Set(key, obj(string(make([]byte, 4096)), time.Minute))
	if _, ok := c.Get(key); ok {
		t.Fatal("object larger than a shard budget was admitted")
	}
}

func TestSetBudgetShrinksImmediately(t *testing.T) {
	c := New(64 << 20)
	for i := 0; i < 1000; i++ {
		c.Set(Key("example.com", "GET", fmt.Sprintf("/%d", i), ""), obj("0123456789", time.Minute))
	}
	c.SetBudget(0)
	if got := c.Stats().Bytes; got != 0 {
		t.Fatalf("want empty cache after budget 0, have %d bytes", got)
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := New(8 << 20)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				key := Key("example.com", "GET", fmt.Sprintf("/%d/%d", w, i%50), "")
				c.Set(key, obj("data", time.Minute))
				c.Get(key)
			}
		}(w)
	}
	wg.Wait()
}
