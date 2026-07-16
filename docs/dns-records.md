# DNS record specification

Everything a domain owner configures lives in their own DNS zone, under the
versioned `hubcdn.v1.*` namespace. Versioning the prefix means the format
can evolve (a future `hubcdn.v2.*`) without breaking zones that were set up
earlier.

## 1. Routing record (required)

Point the domain at a hubCDN node:

```
www.example.com     CNAME   cdn.your-hub.net.
```

or directly at the node's addresses (required at the zone apex unless your
provider supports ALIAS/ANAME):

```
example.com         A       203.0.113.7
example.com         AAAA    2001:db8::7
```

## 2. Origin record (required)

```
hubcdn.v1.origin.example.com    TXT    "https://origin.example.com"
```

The value tells hubCDN where the real server lives. Accepted forms:

| Value | Meaning |
| --- | --- |
| `https://origin.example.com` | HTTPS origin on port 443 |
| `http://203.0.113.50:8080` | plain HTTP origin with explicit port |
| `origin.example.com` | bare host — defaults to `https://` |
| `https://origin.example.com/base/` | origin with a path prefix; `/page` is fetched as `/base/page` |

Credentials, query strings and fragments in the value are stripped. If
several strings are present, the first parseable one wins.

**Important:** the origin record must be resolvable by the hubCDN node, and
the origin host itself must *not* point back at hubCDN (that would loop).

## 3. Configuration record (optional)

```
hubcdn.v1.config.example.com    TXT    "cache=aggressive; ttl=6h; websocket=on"
```

The value is a semicolon-separated list of `key=value` pairs. Unknown keys
and malformed values are ignored — a typo can never take a domain down, it
just falls back to the default for that key.

| Key | Values | Default | Effect |
| --- | --- | --- | --- |
| `cache` | `off`, `standard`, `aggressive` | `standard` | Caching mode — see [caching.md](caching.md) |
| `ttl` | Go duration (`90s`, `6h`) or bare seconds (`3600`) | `1h` | TTL for responses without origin freshness headers |
| `websocket` | `on`, `off` | `on` | Allow WebSocket/Upgrade tunneling |
| `preserve_host` | `on`, `off` | `on` | `on` sends the visitor's Host header to the origin; `off` sends the origin's own hostname |
| `max_object` | size (`16mb`, `512kb`) | node limit | Largest response body cached for this domain (cannot exceed the node's limit) |

## Propagation

- New domains poll their records every 30 seconds (configurable) until the
  origin appears, and the setup page also triggers an immediate re-check on
  each visit — activation typically happens within one TXT TTL.
- Active domains re-resolve origin and config every 5 minutes
  (configurable). Keep the TXT records' own TTL low (60–300s) if you plan
  to switch origins often.
- If the origin record is *removed*, the domain returns to pending and
  serves the setup page again. If lookups merely *fail* (resolver outage),
  hubCDN keeps proxying to the last known origin.
