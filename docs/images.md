# Image CDN (direct caching)

Every hubCDN node doubles as an image optimizer that needs **no DNS setup at
all**: wrap any public image URL in the node's `/img/` endpoint and get back
a compressed, transformed, edge-cached rendition.

```
https://<node-hostname>/img/<options>/<image-url>
```

Examples:

```
https://cdn.example.net/img/w=800,q=75/https://your-site.com/photo.jpg
https://cdn.example.net/img/w=400,h=400,fit=cover,f=jpeg/your-site.com/avatar.png
https://cdn.example.net/img/_/https://your-site.com/hero.png
```

- `<options>` is a comma-separated list of `key=value` pairs; a single `_`
  means "defaults only" (re-encode at quality 80, metadata stripped, longest
  side capped to 2048px if larger — see below).
- `<image-url>` may include a query string and may omit the scheme, which
  defaults to `https://`. Proxies that collapse `//` in paths are handled.
- Invalid options return `400` with the reason — the URL is an API surface,
  so it fails fast instead of silently serving the wrong rendition.

## Options

| Key | Values | Effect |
| --- | --- | --- |
| `w`, `h` | 1–4096 | Target size; a single dimension preserves aspect ratio. With neither given, the longest side is capped to 2048px if the source is larger — pass an explicit `w`/`h` (up to 4096) to bypass this and get the exact size you asked for |
| `fit` | `scaledown` (default), `contain`, `cover`, `fill` | `scaledown` never upscales; `cover` fills and center-crops; `fill` distorts |
| `dpr` | 0.5–3 | Multiplies `w`/`h` for high-density screens (`srcset`-style) |
| `q` | 1–100 | JPEG quality (default 80) |
| `f` | `auto` (default), `jpeg`, `png`, `gif` | Output format; `auto` picks JPEG for opaque images and PNG when transparency is present |
| `gray` | `on` | Grayscale (Rec. 601 luma, alpha preserved) |
| `blur` | 1–50 | Gaussian-approximating blur radius |
| `flip` | `h`, `v`, `hv` | Mirror horizontally and/or vertically |

Accepted input formats: JPEG, PNG, GIF, WebP, BMP, TIFF. Re-encoding always
strips EXIF/metadata, which both shrinks files and removes location data.

**Why the default 2048px cap exists:** JPEG file size is dominated by pixel
count, not the quality setting. A quality-only re-encode of an untouched
camera or stock-photo original (routinely 6000px+, 20+ megapixels) can
still come back several megabytes, because there's simply that much detail
to encode — the quality knob alone doesn't help much. Capping the longest side by default turns "just optimize this" into an
actually small file, which is what nearly everyone wants; pass an explicit
`w=` (up to 4096) matching the source if you genuinely want to preserve its
full resolution.

**Animated GIFs are passed through untouched** — resizing them would freeze
the animation, so all options are ignored for multi-frame GIFs.

## Retention and purging

Optimized renditions are cached in memory for **up to 7 days**. The cache is
byte-budgeted with LRU eviction and a memory-pressure watchdog (see
[caching.md](caching.md)), so when images pile up the least-recently-used
renditions are purged automatically. A purged image is simply re-fetched and
re-optimized on its next request — hotlinking generated URLs is always safe.

Responses carry `Cache-Control: public, max-age=604800, immutable` and a
weak `ETag`, so browsers and downstream caches revalidate with `304`s
instead of re-downloading.

## Limits and safety

- Source images are capped at **20 MB** and **50 megapixels** (decoded), with
  a 25-second fetch budget and at most 3 redirects.
- The fetcher refuses non-public addresses on every connection, including
  each redirect hop: loopback, RFC 1918, link-local (cloud metadata),
  CGNAT and multicast ranges are all blocked, so the endpoint cannot be
  used to probe the node's network.
- Source URLs pointing back at the node itself are rejected, preventing
  recursive optimization chains.
- Concurrent transforms are bounded by CPU count; identical in-flight
  requests are coalesced into a single fetch+transform.
