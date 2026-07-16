# Caching

hubCDN caches in process memory: no disk I/O on the hot path, no external
store to operate. The cache is designed to be safe at both ends of the
hardware spectrum — it sizes itself from the machine's memory and keeps
adapting while running.

## Structure

- **256 shards**, each with its own lock, hash map and LRU list, so
  concurrent requests almost never contend.
- **Byte-accounted**: every entry is charged its real footprint (body +
  headers + overhead). Eviction is LRU per shard whenever a shard exceeds
  its share of the global budget. Admission only rejects an object bigger
  than the *entire* budget — a large-but-legitimate entry (an unresized
  photo, say) is still cached even if it alone exceeds its shard's usual
  fair share; that shard just runs over its nominal share until its own
  older entries age out, rather than the object silently never being
  cached at all.
- **SHA-256 keys** derived from `host + method + URI + encoding bucket`.
  Host is part of the hash, so entries can never collide or leak across
  domains, and the in-memory key space carries no readable request data.
- **Encoding buckets**: `Accept-Encoding` is normalized to three variants
  (`br`, `gzip`, identity) before keying and before forwarding, so a URL is
  stored at most three times instead of once per client header spelling.

## Memory management

| Setting | Env var | Default |
| --- | --- | --- |
| Total budget | `HUBCDN_CACHE_MAX_BYTES` | auto: 40% of available RAM, clamped to 64 MB – 8 GB |
| Max object size | `HUBCDN_CACHE_MAX_OBJECT_BYTES` | 32 MB |
| Pressure headroom | `HUBCDN_CACHE_MEM_HEADROOM_BYTES` | 256 MB |

A watchdog samples `/proc/meminfo` every 15 seconds:

- available memory **below the headroom** → the budget shrinks 25% per tick
  (evicting immediately), down to a 64 MB floor;
- pressure clears (available > 2× headroom) → the budget grows back 10% per
  tick, up to its original target.

This means a node cohabiting with other processes on a small VPS backs off
before the OOM killer gets involved, and reclaims the memory when it frees
up.

Bodies larger than the per-domain object limit are **streamed, never
buffered**: the tee into the cache is abandoned the moment a body crosses
the limit, so large downloads cost no cache memory.

## What gets cached

Requests are cacheable when they are `GET`/`HEAD` without `Authorization`,
`Range`, or `Cache-Control: no-store`. Everything else — `POST`, WebSocket,
authenticated traffic — is proxied straight through (`X-Hubcdn-Cache:
BYPASS`).

Responses are stored according to the domain's mode (`cache=` in the config
TXT record):

| | `off` | `standard` (default) | `aggressive` |
| --- | --- | --- | --- |
| Origin says `max-age`/`s-maxage`/`Expires` (positive) | — | cached for that long (capped at 7 days) | same |
| Origin sends no freshness info | — | static assets (by extension) cached for `ttl` | *everything* cached for `ttl` |
| Origin says `no-cache` / `max-age=0` | — | not cached | cached for `ttl` |
| Requests with cookies | — | bypass | cached |

Never cached regardless of mode: `Set-Cookie` responses, `Cache-Control:
no-store/private`, `Vary: *`, and non-cacheable status codes (anything
outside 200/203/204/300/301/404/405/410/414/501).

**Aggressive mode and cookies.** Real browsers attach some cookie to nearly
every request (analytics, consent banners), so bypassing on request cookies
would defeat the cache for virtually all real traffic. Aggressive mode
therefore ignores *request* cookies and serves everyone the shared cached
copy — the `Set-Cookie` *response* guard still keeps session-issuing and
personalized responses out of the cache. Use aggressive mode only for sites
whose pages look the same for every visitor; keep `standard` for apps with
logged-in areas.

## Stale-while-revalidate

An expired entry is not thrown away immediately: for the domain's `swr`
window (default 10 minutes) it is served instantly with `X-Hubcdn-Cache:
STALE` while one background fetch — deduplicated per URL — refreshes the
entry from the origin. After warmup, no client ever waits on a slow origin
round trip because of TTL expiry. If the origin is briefly down, the stale
copy keeps the site up for the length of the window. Set `swr=0` to disable
and expire strictly at TTL.

## Serving

- Hits are served with an `Age` header and `X-Hubcdn-Cache: HIT` (or
  `STALE` during a background revalidation).
- Conditional requests (`If-None-Match`, `If-Modified-Since`) against a
  cached entry answer `304 Not Modified` from memory without contacting the
  origin.
- Hop-by-hop headers and `Set-Cookie` are stripped from stored entries.

## Operational visibility

`GET https://<node-hostname>/hubcdn/stats` returns entries, bytes, current
budget, hits, misses and evictions as JSON.
