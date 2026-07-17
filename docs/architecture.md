# Architecture

hubCDN is a single Go binary. Every node is self-contained: it terminates
TLS, caches in its own memory, and reads all customer configuration from
DNS. Nodes share nothing with each other, which is what makes horizontal
scaling trivial - routing between nodes is plain DNS.

## Components

```
cmd/hubcdn            entrypoint: config, signals, Bunny DNS registration
internal/
  config              HUBCDN_* environment configuration
  server              HTTPS listener, routing, certmagic wiring
  dnsx                TXT lookups, points-at-us checks, host validation
  domain              domain registry: lifecycle, settings, persistence, refresher
  certguard           certificate issuance budgets (persistent sliding windows)
  cache               sharded in-memory LRU + memory watchdog + HTTP policy
  proxy               caching reverse proxy, WebSocket passthrough
  bunny               optional Bunny DNS fleet registration
  web, web/views      templ-rendered landing/setup pages
```

## Request flow

hubCDN is TLS-only - there is exactly one listener, on port 443. There is
no HTTP port, no port 80, and no plaintext fallback anywhere, including for
the node's own landing page.

```
    :443 (TLS handshake, TLS-ALPN-01 for unknown hosts)
      │
      ├─ known certificate? serve it
      └─ unknown host → on-demand issuance
             │  certguard: apex/day, apex/week, global budgets
             │  dnsx: does the host resolve to this node?
             ▼  ACME order (TLS-ALPN-01, validated on this same port)
                → certificate cached + persisted
    HTTPS request
      │
      ├─ host == node hostname → landing page / health / stats
      │
      └─ customer domain → registry lookup
             ├─ pending (no origin TXT yet) → setup page (auto-refreshing)
             ├─ error, no known origin      → error page (503)
             └─ active ──────────────┐
                                     ▼
                          Upgrade/WebSocket? → tunnel to origin, no cache
                                     │
                          cacheable request?
                             ├─ no  → proxy (X-Hubcdn-Cache: BYPASS)
                             ├─ HIT → serve from memory (Age, 304 support)
                             └─ MISS→ stream from origin, tee into cache
```

Because TLS-ALPN-01 validates within the handshake itself, the ACME CA's
connection lands on the same port real traffic uses - no separate port 80
listener, HTTP→HTTPS redirect, or ACME challenge handler exists in the
codebase at all. Container/orchestrator liveness probes use a plain TCP
dial against the HTTPS port instead of an HTTP request, for the same
reason (see `cmd/hubcdn/healthcheck.go`).

## Domain lifecycle

Domains are discovered passively: the first request for an unknown host
registers it in the registry (`internal/domain`). A domain is always in one
of three states:

- **pending** - points at the node, no origin TXT record yet. Requests get
  the setup page. Polled on the fast refresh interval (default 30s), and
  additionally re-checked opportunistically when a request arrives, so
  publishing the record activates the domain on the next page load.
- **active** - origin known; traffic proxies. Origin and config TXT records
  are re-resolved every `HUBCDN_REFRESH_INTERVAL` (default 5m), so owners
  move origins or change settings by editing DNS.
- **error** - the last refresh failed. If a previous origin is known the
  node keeps proxying to it (a resolver hiccup must never take a live site
  down); otherwise an auto-refreshing error page is served.

The registry persists to `domains.json` in the data directory so state
survives restarts.

## Design decisions

**Why DNS as the only control plane?** It removes the entire account/auth
surface: proof of DNS control *is* the authentication. It also makes hubCDN
stateless with respect to users - a node can be rebuilt from an empty data
directory and reconverge purely from incoming traffic and DNS.

**Why on-demand certificates with a local guard?** Issuing at first request
means zero-step onboarding, but exposes the node to issuance abuse (one
hostile apex with unlimited subdomains). The guard (`internal/certguard`)
enforces persistent sliding-window budgets *before* any ACME order and is
deliberately conservative by default - see [ssl.md](docs/ssl.md).

**Why an in-process memory cache instead of disk/Redis?** Latency and
operational simplicity. The cache is rebuilt from origin traffic after a
restart, which is the standard CDN trade-off. The watchdog makes the same
binary safe on a 1 GB VPS and effective on a 128 GB edge box - see
[caching.md](docs/caching.md).

**Why one shared reverse proxy?** `internal/proxy` uses a single
`httputil.ReverseProxy` with per-request origin data carried in the request
context, so thousands of domains share one connection pool and one
transport configuration.
