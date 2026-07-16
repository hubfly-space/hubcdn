# TLS certificates

hubCDN issues certificates **on demand**: the first TLS handshake for a new
domain triggers an ACME order (Let's Encrypt by default, via
[certmagic](https://github.com/caddyserver/certmagic)). Nothing needs to be
provisioned ahead of time.

## Lifecycle

1. **Handshake for an unknown host** → the issuance gate runs (below). If it
   passes, an ACME order is placed and answered with the TLS-ALPN-01 or
   HTTP-01 challenge (both are served automatically; port 80 must be
   reachable for HTTP-01).
2. **Certificates are cached in memory and persisted** under
   `<data-dir>/certs/`, so restarts don't re-issue anything.
3. **Renewal is automatic.** certmagic renews in the background well before
   expiry; an expired or expiring certificate is re-issued without any
   action from the domain owner. Renewals are *exempt* from the issuance
   budgets — an abusive apex can be stopped from getting new certificates,
   but existing sites always keep renewing.

## The issuance gate

Every new order must pass, in this sequence:

1. **Host validation** — syntactically valid public DNS name; no IPs, no
   single-label hosts.
2. **Points-at-us check** — when `HUBCDN_PUBLIC_IPS` is set, the host's
   A/AAAA records must resolve to one of this node's addresses. This stops
   third parties from requesting certificates for domains that never
   delegated to you. **Always set this in production.**
3. **Issuance budgets** (`internal/certguard`) — sliding-window counters,
   persisted to `<data-dir>/certguard.json` so restarts don't reset them:

| Budget | Env var | Default |
| --- | --- | --- |
| Per apex domain per day | `HUBCDN_CERTS_PER_APEX_PER_DAY` | 10 |
| Per apex domain per week | `HUBCDN_CERTS_PER_APEX_PER_WEEK` | 30 |
| Node-wide per hour | `HUBCDN_CERTS_GLOBAL_PER_HOUR` | 60 |
| Node-wide per week | `HUBCDN_CERTS_GLOBAL_PER_WEEK` | 1000 |

Set a budget to `0` to disable it.

"Apex" means the registrable domain (eTLD+1): `a.example.com` and
`b.example.com` share the `example.com` budget, and `example.co.uk` is
handled correctly via the public-suffix list.

### Why these defaults

Let's Encrypt allows **50 new certificates per registered domain per week**
plus account-wide order limits. A single user pointing a wildcard's worth of
subdomains (`a.`, `b.`, `c.`, …) at your node would exhaust that within
minutes — and once the CA limit is hit, *legitimate* subdomains of that apex
are blocked for a week, and enough abusive apexes could exhaust
account-level limits for everyone. The per-apex weekly default (30) stays
comfortably under the CA's 50 so there is always headroom left for renewals
and retries; the global budgets cap the blast radius of many apexes abusing
at once.

When a budget is exhausted the handshake is refused (the browser shows a
TLS error), the event is logged with the offending apex, and issuance
resumes automatically as the window slides.

## Testing without burning production quota

```
HUBCDN_ACME_STAGING=true
```

uses the Let's Encrypt staging directory: same flow, untrusted certificates,
effectively unlimited quota. A custom ACME directory (another CA, or an
internal one like [Pebble](https://github.com/letsencrypt/pebble)) can be
set with `HUBCDN_ACME_CA=<directory-url>`.
