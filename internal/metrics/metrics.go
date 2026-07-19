// Package metrics collects global request counters for hubCDN using
// lock-free atomic operations. Every increment is a single atomic add
// on a uint64, which costs roughly 5–15 ns on modern hardware with no
// mutex contention regardless of goroutine count – the same pattern the
// existing cache Stats already uses.
//
// Counters are cumulative since process start and never reset. They are
// cheap enough to read on every /hubcdn/stats poll without any caching
// layer.
package metrics

import "sync/atomic"

// Metrics holds global request counters. The zero value is ready to use.
type Metrics struct {
	// TotalRequests counts every request that enters the node, including
	// the node's own pages and health checks.
	TotalRequests atomic.Uint64

	// --- proxy (site CDN) ---
	// ProxyRequests counts requests forwarded to an origin (cache miss
	// or bypass). This does not include stale-while-revalidate
	// background refreshes – those are internal.
	ProxyRequests atomic.Uint64
	// CacheHits counts fresh cache hits served synchronously.
	CacheHits atomic.Uint64
	// CacheStales counts stale-while-revalidate serves (expired but
	// within the SWR window, revalidated in the background).
	CacheStales atomic.Uint64
	// CacheMisses counts cache misses that triggered an origin fetch.
	CacheMisses atomic.Uint64
	// CacheBypass counts requests that skipped the cache entirely
	// (non-cacheable methods, no-store, cookies in standard mode, etc.).
	CacheBypass atomic.Uint64
	// WebSocketUpgrades counts WebSocket (Upgrade) connections tunneled
	// to the origin.
	WebSocketUpgrades atomic.Uint64
	// NotModified counts 304 Not Modified responses served from cache.
	NotModified atomic.Uint64

	// --- image CDN ---
	// ImageRequests counts total /img/ requests.
	ImageRequests atomic.Uint64
	// ImageCacheHits counts /img/ requests served from cache.
	ImageCacheHits atomic.Uint64
	// ImageCacheMisses counts /img/ requests that required
	// fetch + transform from the origin.
	ImageCacheMisses atomic.Uint64
}

// Snapshot is a point-in-time copy of all counters, safe to serialize.
type Snapshot struct {
	TotalRequests     uint64 `json:"total_requests"`
	ProxyRequests     uint64 `json:"proxy_requests"`
	CacheHits         uint64 `json:"cache_hits"`
	CacheStales       uint64 `json:"cache_stales"`
	CacheMisses       uint64 `json:"cache_misses"`
	CacheBypass       uint64 `json:"cache_bypass"`
	WebSocketUpgrades uint64 `json:"websocket_upgrades"`
	NotModified       uint64 `json:"not_modified"`
	ImageRequests     uint64 `json:"image_requests"`
	ImageCacheHits    uint64 `json:"image_cache_hits"`
	ImageCacheMisses  uint64 `json:"image_cache_misses"`
}

// Snap returns an atomic snapshot of all counters.
func (m *Metrics) Snap() Snapshot {
	return Snapshot{
		TotalRequests:     m.TotalRequests.Load(),
		ProxyRequests:     m.ProxyRequests.Load(),
		CacheHits:         m.CacheHits.Load(),
		CacheStales:       m.CacheStales.Load(),
		CacheMisses:       m.CacheMisses.Load(),
		CacheBypass:       m.CacheBypass.Load(),
		WebSocketUpgrades: m.WebSocketUpgrades.Load(),
		NotModified:       m.NotModified.Load(),
		ImageRequests:     m.ImageRequests.Load(),
		ImageCacheHits:    m.ImageCacheHits.Load(),
		ImageCacheMisses:  m.ImageCacheMisses.Load(),
	}
}
