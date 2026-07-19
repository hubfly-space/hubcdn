package proxy

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hubfly-space/hubcdn/internal/cache"
	"github.com/hubfly-space/hubcdn/internal/domain"
	"github.com/hubfly-space/hubcdn/internal/metrics"
)

func testSnapshot(t *testing.T, origin string, settings domain.Settings) domain.Snapshot {
	t.Helper()
	u, err := url.Parse(origin)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Snapshot{
		Host:     "site.example.com",
		Apex:     "example.com",
		Status:   domain.StatusActive,
		Origin:   u,
		Settings: settings,
	}
}

func testProxy() *Proxy {
	return New(cache.New(64<<20), slog.New(slog.DiscardHandler), &metrics.Metrics{})
}

func TestStaleWhileRevalidate(t *testing.T) {
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Cache-Control", "max-age=60")
		fmt.Fprintf(w, "v%d", n)
	}))
	defer origin.Close()

	c := cache.New(64 << 20)
	p := New(c, slog.New(slog.DiscardHandler), &metrics.Metrics{})
	snap := testSnapshot(t, origin.URL, domain.DefaultSettings(32<<20))

	do := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "https://site.example.com/page", nil)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r, snap)
		return w
	}

	if got := do().Body.String(); got != "v1" {
		t.Fatalf("first fetch: %q", got)
	}

	// Age the entry past its TTL but within the stale window.
	key := cache.Key("site.example.com", http.MethodGet, "/page", "")
	obj, _ := c.Get(key)
	obj.StoredAt = time.Now().Add(-2 * time.Minute)

	stale := do()
	if got := stale.Header().Get("X-Hubcdn-Cache"); got != "STALE" {
		t.Fatalf("want STALE, got %q", got)
	}
	if got := stale.Body.String(); got != "v1" {
		t.Fatalf("stale response must serve the old copy instantly, got %q", got)
	}

	// The background revalidation should replace the entry with v2.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if obj, f := c.Get(key); f == cache.Fresh && string(obj.Body) == "v2" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	refreshed := do()
	if got := refreshed.Header().Get("X-Hubcdn-Cache"); got != "HIT" {
		t.Fatalf("want HIT after revalidation, got %q", got)
	}
	if got := refreshed.Body.String(); got != "v2" {
		t.Fatalf("want v2 after revalidation, got %q", got)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("origin hit %d times, want 2 (initial + one revalidation)", n)
	}
}

func TestAggressiveCachesDespiteCookies(t *testing.T) {
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "shared page")
	}))
	defer origin.Close()

	settings := domain.DefaultSettings(32 << 20)
	settings.CacheMode = cache.ModeAggressive
	p := testProxy()
	snap := testSnapshot(t, origin.URL, settings)

	for i := 0; i < 3; i++ {
		r := httptest.NewRequest(http.MethodGet, "https://site.example.com/", nil)
		r.Header.Set("Cookie", "_ga=GA1.2.123; consent=yes")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r, snap)
		if w.Body.String() != "shared page" {
			t.Fatalf("bad body: %q", w.Body.String())
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("origin hit %d times, want 1 - cookies must not bypass aggressive caching", n)
	}
}

func TestProxyMissThenHit(t *testing.T) {
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = io.WriteString(w, "origin says hi")
	}))
	defer origin.Close()

	p := testProxy()
	snap := testSnapshot(t, origin.URL, domain.DefaultSettings(32<<20))

	do := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "https://site.example.com/page", nil)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r, snap)
		return w
	}

	first := do()
	if first.Body.String() != "origin says hi" {
		t.Fatalf("unexpected body: %q", first.Body.String())
	}
	second := do()
	if got := second.Header().Get("X-Hubcdn-Cache"); got != "HIT" {
		t.Fatalf("want HIT on second request, got %q", got)
	}
	if second.Body.String() != "origin says hi" {
		t.Fatalf("cached body mismatch: %q", second.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("origin hit %d times, want 1", hits.Load())
	}
}

func TestProxyDoesNotCachePrivate(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private")
		_, _ = io.WriteString(w, "secret")
	}))
	defer origin.Close()

	p := testProxy()
	snap := testSnapshot(t, origin.URL, domain.DefaultSettings(32<<20))
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodGet, "https://site.example.com/me", nil)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r, snap)
		if got := w.Header().Get("X-Hubcdn-Cache"); got == "HIT" {
			t.Fatal("private response was cached")
		}
	}
}

func TestProxyOversizeBodyNotCached(t *testing.T) {
	big := make([]byte, 2048)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write(big)
	}))
	defer origin.Close()

	settings := domain.DefaultSettings(32 << 20)
	settings.MaxObjectBytes = 1024
	p := testProxy()
	snap := testSnapshot(t, origin.URL, settings)

	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodGet, "https://site.example.com/big", nil)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r, snap)
		if w.Body.Len() != len(big) {
			t.Fatalf("body truncated: %d bytes", w.Body.Len())
		}
		if got := w.Header().Get("X-Hubcdn-Cache"); got == "HIT" {
			t.Fatal("oversize body was cached")
		}
	}
}

func TestWebsocketDisabled(t *testing.T) {
	settings := domain.DefaultSettings(32 << 20)
	settings.Websocket = false
	p := testProxy()
	snap := testSnapshot(t, "http://127.0.0.1:1", settings)

	r := httptest.NewRequest(http.MethodGet, "https://site.example.com/ws", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r, snap)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for disabled websocket, got %d", w.Code)
	}
}

func TestConditionalRevalidation(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Etag", `"v1"`)
		_, _ = io.WriteString(w, "body")
	}))
	defer origin.Close()

	p := testProxy()
	snap := testSnapshot(t, origin.URL, domain.DefaultSettings(32<<20))

	r := httptest.NewRequest(http.MethodGet, "https://site.example.com/a", nil)
	p.ServeHTTP(httptest.NewRecorder(), r, snap)

	r = httptest.NewRequest(http.MethodGet, "https://site.example.com/a", nil)
	r.Header.Set("If-None-Match", `"v1"`)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r, snap)
	if w.Code != http.StatusNotModified {
		t.Fatalf("want 304, got %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatal("304 must not carry a body")
	}
}

func TestOriginPathPrefix(t *testing.T) {
	var gotPath string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	defer origin.Close()

	p := testProxy()
	snap := testSnapshot(t, origin.URL+"/base/", domain.DefaultSettings(32<<20))
	r := httptest.NewRequest(http.MethodGet, "https://site.example.com/page?x=1", nil)
	p.ServeHTTP(httptest.NewRecorder(), r, snap)

	deadline := time.Now().Add(time.Second)
	for gotPath == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if gotPath != "/base/page" {
		t.Fatalf("origin saw path %q, want /base/page", gotPath)
	}
}
