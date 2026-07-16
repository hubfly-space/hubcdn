package cache

import (
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// Policy decides what gets cached and for how long for one domain. It is
// derived from the domain's TXT configuration (see internal/domain).
type Policy struct {
	// Mode: ModeOff caches nothing, ModeStandard follows origin headers,
	// ModeAggressive additionally caches responses that lack caching
	// headers and ignores cookies on static assets.
	Mode Mode
	// DefaultTTL applies when the origin sends no freshness information.
	DefaultTTL time.Duration
	// MaxObjectBytes caps the body size admitted into the cache.
	MaxObjectBytes int64
}

// Mode is a domain's caching mode.
type Mode int

const (
	ModeOff Mode = iota
	ModeStandard
	ModeAggressive
)

// ParseMode maps TXT config values to a Mode. Unknown values default to
// standard.
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "none", "disabled":
		return ModeOff
	case "aggressive":
		return ModeAggressive
	default:
		return ModeStandard
	}
}

func (m Mode) String() string {
	switch m {
	case ModeOff:
		return "off"
	case ModeAggressive:
		return "aggressive"
	default:
		return "standard"
	}
}

// staticExt lists extensions treated as static assets under aggressive mode.
var staticExt = map[string]bool{
	".css": true, ".js": true, ".mjs": true, ".map": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".avif": true, ".svg": true, ".ico": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".mp4": true, ".webm": true, ".mp3": true, ".ogg": true, ".wav": true,
	".pdf": true, ".txt": true, ".xml": true, ".json": true,
	".zip": true, ".gz": true, ".br": true, ".tar": true, ".wasm": true,
}

// cacheableStatus lists response codes eligible for caching (RFC 9111 §3
// heuristically cacheable statuses).
var cacheableStatus = map[int]bool{
	http.StatusOK:                   true,
	http.StatusNonAuthoritativeInfo: true,
	http.StatusNoContent:            true,
	http.StatusPartialContent:       false, // ranges are proxied, not cached
	http.StatusMultipleChoices:      true,
	http.StatusMovedPermanently:     true,
	http.StatusNotFound:             true,
	http.StatusMethodNotAllowed:     true,
	http.StatusGone:                 true,
	http.StatusRequestURITooLong:    true,
	http.StatusNotImplemented:       true,
}

// CacheableRequest reports whether the request may be served from or admit
// into the cache.
func (p Policy) CacheableRequest(r *http.Request) bool {
	if p.Mode == ModeOff {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.Header.Get("Authorization") != "" {
		return false
	}
	if r.Header.Get("Range") != "" {
		return false
	}
	cc := parseCacheControl(r.Header.Get("Cache-Control"))
	if cc.noStore {
		return false
	}
	if r.Header.Get("Cookie") != "" {
		// Cookies usually mean personalized content; aggressive mode
		// still caches static assets since those never vary per user.
		return p.Mode == ModeAggressive && staticExt[strings.ToLower(path.Ext(r.URL.Path))]
	}
	return true
}

// ResponseTTL reports whether a response with the given status and headers,
// for a request to reqPath, may be cached and for how long. A false return
// means do not cache. Body size is enforced separately while streaming.
func (p Policy) ResponseTTL(reqPath string, status int, header http.Header) (time.Duration, bool) {
	if p.Mode == ModeOff || !cacheableStatus[status] {
		return 0, false
	}
	if header.Get("Set-Cookie") != "" {
		return 0, false
	}
	if strings.Contains(strings.ToLower(header.Get("Vary")), "*") {
		return 0, false
	}

	cc := parseCacheControl(header.Get("Cache-Control"))
	if cc.noStore || cc.private {
		return 0, false
	}
	if cc.noCache {
		return 0, false
	}
	if cc.sMaxAge >= 0 {
		return capTTL(time.Duration(cc.sMaxAge) * time.Second), cc.sMaxAge > 0
	}
	if cc.maxAge >= 0 {
		return capTTL(time.Duration(cc.maxAge) * time.Second), cc.maxAge > 0
	}
	if exp := header.Get("Expires"); exp != "" {
		if t, err := http.ParseTime(exp); err == nil {
			ttl := time.Until(t)
			if ttl <= 0 {
				return 0, false
			}
			return capTTL(ttl), true
		}
	}

	// No explicit freshness from the origin.
	switch p.Mode {
	case ModeAggressive:
		return p.DefaultTTL, p.DefaultTTL > 0
	default:
		// Standard mode only applies the heuristic TTL to static assets.
		if staticExt[strings.ToLower(path.Ext(reqPath))] {
			return p.DefaultTTL, p.DefaultTTL > 0
		}
		return 0, false
	}
}

const maxTTL = 24 * time.Hour * 7

func capTTL(ttl time.Duration) time.Duration {
	if ttl > maxTTL {
		return maxTTL
	}
	return ttl
}

type cacheControl struct {
	noStore bool
	noCache bool
	private bool
	maxAge  int
	sMaxAge int
}

func parseCacheControl(v string) cacheControl {
	cc := cacheControl{maxAge: -1, sMaxAge: -1}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		switch {
		case part == "no-store":
			cc.noStore = true
		case part == "no-cache":
			cc.noCache = true
		case part == "private":
			cc.private = true
		case strings.HasPrefix(part, "max-age="):
			if n, err := strconv.Atoi(part[len("max-age="):]); err == nil {
				cc.maxAge = n
			}
		case strings.HasPrefix(part, "s-maxage="):
			if n, err := strconv.Atoi(part[len("s-maxage="):]); err == nil {
				cc.sMaxAge = n
			}
		}
	}
	return cc
}
