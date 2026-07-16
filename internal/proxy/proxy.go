// Package proxy is the data plane of hubCDN: a caching reverse proxy that
// forwards requests for active domains to their origin.
//
// Request flow:
//
//	client → cache lookup → HIT: serve from memory (with 304 revalidation)
//	                      → MISS: stream from origin to the client while
//	                              teeing cacheable bodies into the cache
//
// WebSocket (and any Upgrade) requests bypass the cache entirely and are
// tunneled by httputil.ReverseProxy.
package proxy

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/hubfly-space/hubcdn/internal/cache"
	"github.com/hubfly-space/hubcdn/internal/domain"
)

type ctxKey int

const snapKey ctxKey = 0

// Proxy forwards requests for active domains to their origins.
type Proxy struct {
	cache *cache.Cache
	rp    *httputil.ReverseProxy
	log   *slog.Logger
}

// New builds the shared reverse proxy. One instance serves every domain;
// the per-request origin travels in the request context.
func New(c *cache.Cache, log *slog.Logger) *Proxy {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	p := &Proxy{cache: c, log: log}
	p.rp = &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: 100 * time.Millisecond,
		Rewrite: func(pr *httputil.ProxyRequest) {
			snap := pr.In.Context().Value(snapKey).(domain.Snapshot)
			origin := snap.Origin
			pr.Out.URL.Scheme = origin.Scheme
			pr.Out.URL.Host = origin.Host
			if prefix := strings.TrimSuffix(origin.Path, "/"); prefix != "" {
				pr.Out.URL.Path = prefix + pr.Out.URL.Path
				pr.Out.URL.RawPath = ""
			}
			if snap.Settings.PreserveHost {
				pr.Out.Host = pr.In.Host
			} else {
				pr.Out.Host = origin.Host
			}
			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("origin error", "host", r.Host, "path", r.URL.Path, "err", err)
			if r.Context().Err() != nil {
				return
			}
			http.Error(w, "hubCDN: origin unreachable", http.StatusBadGateway)
		},
	}
	return p
}

// ServeHTTP handles one request for an active domain.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request, snap domain.Snapshot) {
	if isUpgrade(r) {
		if !snap.Settings.Websocket {
			http.Error(w, "hubCDN: websockets disabled for this domain", http.StatusForbidden)
			return
		}
		p.forward(w, r, snap)
		return
	}

	policy := snap.Settings.Policy()
	if !policy.CacheableRequest(r) {
		w.Header().Set("X-Hubcdn-Cache", "BYPASS")
		p.forward(w, r, snap)
		return
	}

	enc := normalizeEncoding(r.Header.Get("Accept-Encoding"))
	key := cache.Key(snap.Host, r.Method, r.URL.RequestURI(), enc)

	if obj, ok := p.cache.Get(key); ok {
		p.serveCached(w, r, obj)
		return
	}

	// Miss: fetch a consistent encoding variant from the origin and tee
	// the body into the cache while streaming it to the client.
	r.Header.Set("Accept-Encoding", enc)
	rec := &recorder{
		ResponseWriter: w,
		reqPath:        r.URL.Path,
		policy:         policy,
	}
	p.forward(rec, r, snap)
	if obj := rec.object(); obj != nil {
		p.cache.Set(key, obj)
	}
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, snap domain.Snapshot) {
	ctx := context.WithValue(r.Context(), snapKey, snap)
	p.rp.ServeHTTP(w, r.WithContext(ctx))
}

// serveCached writes a cache hit, honoring conditional request headers.
func (p *Proxy) serveCached(w http.ResponseWriter, r *http.Request, obj *cache.Object) {
	h := w.Header()
	for k, vs := range obj.Header {
		h[k] = vs
	}
	h.Set("Age", strconv.FormatInt(int64(obj.Age(time.Now()).Seconds()), 10))
	h.Set("X-Hubcdn-Cache", "HIT")

	if notModified(r, obj) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(obj.Body)))
	w.WriteHeader(obj.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(obj.Body)
	}
}

func notModified(r *http.Request, obj *cache.Object) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		etag := obj.Header.Get("Etag")
		if etag == "" {
			return false
		}
		for _, cand := range strings.Split(inm, ",") {
			cand = strings.TrimSpace(cand)
			if cand == etag || cand == "*" || strings.TrimPrefix(cand, "W/") == strings.TrimPrefix(etag, "W/") {
				return true
			}
		}
		return false
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		lm := obj.Header.Get("Last-Modified")
		if lm == "" {
			return false
		}
		imsT, err1 := http.ParseTime(ims)
		lmT, err2 := http.ParseTime(lm)
		return err1 == nil && err2 == nil && !lmT.After(imsT)
	}
	return false
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// normalizeEncoding buckets Accept-Encoding values so the cache stores at
// most three variants per URL instead of one per client header spelling.
func normalizeEncoding(ae string) string {
	ae = strings.ToLower(ae)
	switch {
	case strings.Contains(ae, "br"):
		return "br, gzip"
	case strings.Contains(ae, "gzip"):
		return "gzip"
	default:
		return ""
	}
}
