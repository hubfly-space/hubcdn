# Self-hosting guide

## Requirements

- A server with ports **80 and 443** reachable from the internet (80 is
  needed for ACME HTTP-01 challenges and HTTP→HTTPS redirects).
- A public IP (v4 and/or v6).
- Optionally a hostname for the node itself (e.g. `cdn.example.net`) so
  users can CNAME to it instead of hardcoding your IP.

## Build and run

```sh
go build ./cmd/hubcdn

HUBCDN_ACME_EMAIL=you@example.com \
HUBCDN_PUBLIC_IPS=203.0.113.7 \
HUBCDN_HOSTNAME=cdn.example.net \
./hubcdn
```

Start with `HUBCDN_ACME_STAGING=true` until you've verified the setup —
staging certificates are untrusted but don't consume production ACME quota.

`HUBCDN_PUBLIC_IPS` is technically optional but **strongly recommended**:
without it the node skips verifying that a domain actually resolves to it
before requesting a certificate.

## Configuration reference

All configuration is environment variables. Sizes accept `kb`/`mb`/`gb`
suffixes; durations use Go syntax (`30s`, `5m`, `6h`).

### Core

| Variable | Default | Purpose |
| --- | --- | --- |
| `HUBCDN_DATA_DIR` | `./data` | Certificates, domain registry, issuance counters |
| `HUBCDN_HTTP_ADDR` | `:80` | HTTP listener (ACME + redirect) |
| `HUBCDN_HTTPS_ADDR` | `:443` | HTTPS listener |
| `HUBCDN_HOSTNAME` | — | This node's own hostname; serves the landing page |
| `HUBCDN_PUBLIC_IPS` | — | Comma-separated public IPs of this node |
| `HUBCDN_RESOLVER` | system | DNS server (`host:port`) for all lookups |

### TLS / ACME

| Variable | Default | Purpose |
| --- | --- | --- |
| `HUBCDN_ACME_EMAIL` | — | ACME account email (expiry notices) |
| `HUBCDN_ACME_STAGING` | `false` | Use Let's Encrypt staging |
| `HUBCDN_ACME_CA` | LE production | Custom ACME directory URL |
| `HUBCDN_CERTS_PER_APEX_PER_DAY` | `10` | Issuance budget — see [ssl.md](ssl.md) |
| `HUBCDN_CERTS_PER_APEX_PER_WEEK` | `30` | |
| `HUBCDN_CERTS_GLOBAL_PER_HOUR` | `60` | |
| `HUBCDN_CERTS_GLOBAL_PER_WEEK` | `1000` | |

### Cache

| Variable | Default | Purpose |
| --- | --- | --- |
| `HUBCDN_CACHE_MAX_BYTES` | auto | Total budget; auto = 40% of available RAM |
| `HUBCDN_CACHE_MAX_OBJECT_BYTES` | `32mb` | Largest cacheable body |
| `HUBCDN_CACHE_MEM_HEADROOM_BYTES` | `256mb` | Free-memory threshold for the watchdog |

### Domain refresh

| Variable | Default | Purpose |
| --- | --- | --- |
| `HUBCDN_REFRESH_INTERVAL` | `5m` | TXT re-resolution for active domains |
| `HUBCDN_PENDING_REFRESH_INTERVAL` | `30s` | Poll cadence for domains awaiting their origin record |

### Bunny DNS (multi-node)

| Variable | Default | Purpose |
| --- | --- | --- |
| `HUBCDN_BUNNY_API_KEY` | — | Bunny.net API key; enables the integration |
| `HUBCDN_BUNNY_ZONE_ID` | — | Bunny DNS zone ID containing `HUBCDN_HOSTNAME` |

## systemd

```ini
# /etc/systemd/system/hubcdn.service
[Unit]
Description=hubCDN node
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/hubcdn
Environment=HUBCDN_DATA_DIR=/var/lib/hubcdn
Environment=HUBCDN_ACME_EMAIL=you@example.com
Environment=HUBCDN_PUBLIC_IPS=203.0.113.7
Environment=HUBCDN_HOSTNAME=cdn.example.net
StateDirectory=hubcdn
DynamicUser=yes
AmbientCapabilities=CAP_NET_BIND_SERVICE
Restart=on-failure
# CAP_NET_BIND_SERVICE is only needed for the bare-metal defaults
# (HUBCDN_HTTP_ADDR=:80, HUBCDN_HTTPS_ADDR=:443). Drop it if you run on
# unprivileged ports as the Docker image does.

[Install]
WantedBy=multi-user.target
```

## Docker

The image is a multi-stage build ending in `gcr.io/distroless/static-debian12:nonroot`
— no shell, no package manager, non-root by default. The container listens
on `:8080` (HTTP/ACME) and `:4403` (HTTPS) internally; both are unprivileged
ports, so the container never needs `root` or `CAP_NET_BIND_SERVICE`. Map
your real port 80/443 to these from outside — via `docker run -p`,
docker-compose, a firewall NAT rule, or a reverse proxy in front of it.

```sh
docker build -t hubcdn .
docker run -d --name hubcdn \
  -p 80:8080 -p 443:4403 \
  -v hubcdn-data:/data \
  -e HUBCDN_ACME_EMAIL=you@example.com \
  -e HUBCDN_PUBLIC_IPS=203.0.113.7 \
  -e HUBCDN_HOSTNAME=cdn.example.net \
  hubcdn
```

### docker-compose

```sh
cp .env.example .env   # fill in HUBCDN_ACME_EMAIL / HUBCDN_PUBLIC_IPS / HUBCDN_HOSTNAME
make up                # docker compose up -d --build
make logs               # follow logs
make down                # stop
```

`docker-compose.yml` publishes ports via `HUBCDN_HOST_HTTP_PORT` /
`HUBCDN_HOST_HTTPS_PORT` (defaulting to the same `8080`/`4403` as the
container) and runs the container hardened: `read_only` root filesystem
with a small `tmpfs` at `/tmp`, `cap_drop: ALL`, `no-new-privileges`, and
bounded JSON log rotation. All ports are configurable through `.env` — see
[.env.example](../.env.example).

### Deploying

`make deploy` rsyncs the source tree to a remote Docker host over SSH and
runs `docker compose up -d --build` there — see `scripts/deploy.sh`. It
targets `dev@192.168.1.3` by default and writes a `.env` with hubCDN's
standard production values (`ops@hubcdn.space` / `41.186.167.39` /
`cdn.hubcdn.space`) on first deploy only, so it never clobbers a `.env`
you've since customized on the server. Override the target or any value by
exporting it before the call:

```sh
make deploy                                   # deploy to the default server
DEPLOY_HOST=dev@10.0.0.9 make deploy          # deploy elsewhere
HUBCDN_ACME_STAGING=true make deploy          # override a default value
```

`make remote-logs`, `make remote-status` and `make remote-down` manage the
deployed stack afterwards.

## Multiple nodes with Bunny DNS

Host your CDN hostname's zone on [Bunny DNS](https://bunny.net/dns/) and
give every node the same hostname plus its own IPs:

```sh
HUBCDN_HOSTNAME=cdn.example.net \
HUBCDN_PUBLIC_IPS=<this node's IP> \
HUBCDN_BUNNY_API_KEY=... \
HUBCDN_BUNNY_ZONE_ID=12345 \
./hubcdn
```

Each node **upserts its A/AAAA records on startup**, re-asserts them every
5 minutes, and **removes them on graceful shutdown**, so
`cdn.example.net` always resolves to the set of live nodes and users'
CNAMEs spread across the fleet. Nodes are independent — each issues its own
certificates and fills its own cache.

In a fleet, set `HUBCDN_PUBLIC_IPS` on every node to the **union of all
fleet IPs**. A user's CNAME resolves to any node in the set, and a TLS
handshake can land on any of them — the points-at-us check passes as long
as the domain resolves to one of the listed addresses, so certificates are
issued no matter which node handles the first request.

## Operations

- `GET /hubcdn/health` on the node hostname → `ok` (readiness probe).
- `GET /hubcdn/stats` → JSON: uptime, domain count, cache counters.
- Logs are structured JSON on stderr: certificate issuances and refusals,
  domain state changes, memory-pressure events, origin errors.
- Back up `HUBCDN_DATA_DIR` if you want restarts to preserve certificates
  and issuance history; everything in it can be regenerated, at the cost of
  re-issuing certificates (which consumes ACME quota).
