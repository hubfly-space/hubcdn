# hubCDN

A self-hosted CDN that is configured entirely through DNS. No accounts, no
dashboard, no API keys for your users — putting a site behind hubCDN takes
two DNS records.

```
your-domain.com                  A      →  your hubCDN node
hubcdn.v1.origin.your-domain.com TXT    →  "https://your-actual-server.com"
```

hubCDN terminates TLS with automatically issued (and renewed) Let's Encrypt
certificates, caches responses in a memory-adaptive in-process cache, and
reverse-proxies everything else — including WebSockets — to the origin.

## How it works

1. **A user points their domain at your node** (A/AAAA record, or CNAME to
   your node's hostname). When the first HTTPS request arrives, hubCDN
   issues a certificate on demand — gated by a rate-limit guard so runaway
   subdomain issuance can never exhaust the CA's weekly limits.
2. **hubCDN serves a setup page** on their domain explaining the one
   remaining step: publish the origin TXT record.
3. **The user adds `hubcdn.v1.origin.<domain>`** with the address of their
   real server. hubCDN picks it up within seconds and starts proxying and
   caching. An optional `hubcdn.v1.config.<domain>` record tunes caching,
   WebSockets and more — see [docs/dns-records.md](docs/dns-records.md).

Origins and configuration are re-resolved periodically, so users change
settings by editing DNS — never by touching your server.

## Quick start

```sh
go build ./cmd/hubcdn

HUBCDN_ACME_EMAIL=you@example.com \
HUBCDN_PUBLIC_IPS=203.0.113.7 \
HUBCDN_HOSTNAME=cdn.example.net \
./hubcdn
```

Visiting `https://cdn.example.net` shows the instructions page your users
follow — hubCDN is TLS-only, so there's no plain-HTTP fallback, not even for
its own landing page; the first HTTPS request issues its certificate
on-demand just like any customer domain. Use `HUBCDN_ACME_STAGING=true`
while testing so you never touch production ACME limits.

### With Docker

```sh
cp .env.example .env   # fill in HUBCDN_ACME_EMAIL / HUBCDN_PUBLIC_IPS / HUBCDN_HOSTNAME
make up                 # builds the hardened distroless image and starts it
make logs
```

Full deployment guide (systemd, Docker, hardening, multi-node, `make deploy`): [docs/self-hosting.md](docs/self-hosting.md).

## Features

- **Automatic TLS** — on-demand issuance on the first request for a domain,
  automatic renewal before expiry, and a persistent issuance guard with
  per-domain and node-wide budgets ([docs/ssl.md](docs/ssl.md)).
- **DNS-only onboarding** — origin and per-domain configuration live in TXT
  records under the versioned `hubcdn.v1.*` namespace
  ([docs/dns-records.md](docs/dns-records.md)).
- **Adaptive in-memory cache** — sharded 256 ways, byte-accounted, LRU
  eviction, SHA-256 keying, and a watchdog that shrinks the cache when the
  host runs low on memory and grows it back when pressure clears
  ([docs/caching.md](docs/caching.md)).
- **WebSockets & streaming** — Upgrade requests tunnel straight through;
  SSE and chunked responses stream with periodic flushing.
- **Multi-node routing via Bunny DNS** — each node registers itself in a
  Bunny DNS zone on startup and removes itself on shutdown, giving you
  DNS-distributed traffic across a fleet ([docs/self-hosting.md](docs/self-hosting.md#multiple-nodes-with-bunny-dns)).

## Documentation

| Document | Contents |
| --- | --- |
| [docs/architecture.md](docs/architecture.md) | Components, request flow, design decisions |
| [docs/self-hosting.md](docs/self-hosting.md) | Deployment, every `HUBCDN_*` variable, ops endpoints |
| [docs/dns-records.md](docs/dns-records.md) | The `hubcdn.v1.*` record specification |
| [docs/caching.md](docs/caching.md) | Cache behavior, modes, memory management |
| [docs/ssl.md](docs/ssl.md) | Certificate lifecycle and the issuance guard |

## Development

```sh
make dev      # templ generate + go run, staging ACME, local ./data
make test     # go test ./...
make check    # fmt-check + vet + test
make build    # templ generate + go build
```

Run `make help` for the full target list, including the Docker and
`make deploy` workflows.

## License

MIT — see [LICENSE](LICENSE).
