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
	if _, f := c.Get(key); f != Miss {
		t.Fatal("unexpected hit on empty cache")
	}
	c.Set(key, obj("hello", time.Minute))
	got, f := c.Get(key)
	if f != Fresh || string(got.Body) != "hello" {
		t.Fatalf("want fresh hello, got %v %v", got, f)
	}
}

func TestExpiry(t *testing.T) {
	c := New(1 << 20)
	key := Key("example.com", "GET", "/a", "")
	o := obj("x", time.Minute)
	o.StoredAt = time.Now().Add(-2 * time.Minute)
	c.Set(key, o)
	if _, f := c.Get(key); f != Miss {
		t.Fatal("expired object without a stale window was served")
	}
}

func TestStaleWindow(t *testing.T) {
	c := New(1 << 20)
	key := Key("example.com", "GET", "/a", "")

	o := obj("x", time.Minute)
	o.StaleFor = 10 * time.Minute
	o.StoredAt = time.Now().Add(-2 * time.Minute)
	c.Set(key, o)
	got, f := c.Get(key)
	if f != Stale || string(got.Body) != "x" {
		t.Fatalf("want stale x within window, got %v %v", got, f)
	}

	// Past TTL + stale window: gone entirely.
	o2 := obj("y", time.Minute)
	o2.StaleFor = 10 * time.Minute
	o2.StoredAt = time.Now().Add(-12 * time.Minute)
	c.Set(key, o2)
	if _, f := c.Get(key); f != Miss {
		t.Fatal("object served past its stale window")
	}
}

func TestKeyIsolation(t *testing.T) {
	c := New(1 << 20)
	c.Set(Key("a.com", "GET", "/x", ""), obj("from-a", time.Minute))
	if _, f := c.Get(Key("b.com", "GET", "/x", "")); f != Miss {
		t.Fatal("cache leaked an object across hosts")
	}
	if _, f := c.Get(Key("a.com", "GET", "/x", "gzip")); f != Miss {
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

// TestLargeObjectAdmittedOverShardShare guards against the bug where any
// object bigger than budget/shardCount (a shard's "fair share") was
// silently dropped from the cache even though the aggregate cache had
// plenty of room - e.g. a small VPS with a ~700MB budget gives each of the
// 256 shards only ~2.7MB, which a single unresized photo can easily
// exceed. Such an object must still be cached, and retrievable, as long as
// it fits under the *total* budget.
func TestLargeObjectAdmittedOverShardShare(t *testing.T) {
	budget := int64(64 << 20) // 64MB total, so ~256KB per shard
	c := New(budget)
	big := make([]byte, 4<<20) // 4MB: far more than one shard's fair share
	key := Key("example.com", "GET", "/photo.jpg", "")
	c.Set(key, obj(string(big), time.Minute))

	got, f := c.Get(key)
	if f != Fresh {
		t.Fatalf("large-but-under-total-budget object was not cached, freshness=%v", f)
	}
	if len(got.Body) != len(big) {
		t.Fatalf("body truncated: got %d bytes, want %d", len(got.Body), len(big))
	}
}

func TestObjectBiggerThanTotalBudgetRejected(t *testing.T) {
	c := New(1 << 20) // 1MB total
	key := Key("example.com", "GET", "/huge", "")
	c.Set(key, obj(string(make([]byte, 2<<20)), time.Minute)) // 2MB, over budget
	if _, f := c.Get(key); f != Miss {
		t.Fatal("object bigger than the entire cache budget was admitted")
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
