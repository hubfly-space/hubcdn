// Package imgcdn serves hubCDN's direct image optimization endpoint:
//
//	https://<node>/img/<options>/<source-image-url>
//
// No DNS setup is involved — any public image URL can be wrapped and the
// node fetches, transforms (internal/imageproc) and caches the result.
// Optimized images live in the shared in-memory cache with a 7-day TTL;
// under memory pressure the LRU eviction and cache watchdog purge the
// least-recently used renditions automatically, and a purged image is
// simply re-optimized on its next request.
package imgcdn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/hubfly-space/hubcdn/internal/cache"
	"github.com/hubfly-space/hubcdn/internal/imageproc"
)

const (
	// PathPrefix is the URL prefix this handler owns on the node host.
	PathPrefix = "/img/"

	// retention is how long an optimized image stays cached. LRU eviction
	// can purge it earlier when the node fills up.
	retention = 7 * 24 * time.Hour

	// maxSourceBytes caps the size of a fetched source image.
	maxSourceBytes = 20 << 20
	// maxCacheableBytes caps a single optimized rendition admitted into the
	// shared cache. An output past this is still served, just re-optimized
	// on every request instead of cached, so one pathological request
	// (e.g. a huge explicit w=/h= on an already-large source) can't crowd
	// out the rest of the cache.
	maxCacheableBytes = 20 << 20

	fetchTimeout = 25 * time.Second
	maxRedirects = 3
)

// Handler implements the /img/ endpoint.
type Handler struct {
	cache  *cache.Cache
	log    *slog.Logger
	client *http.Client
	// selfHost reports whether a hostname refers to this node, to refuse
	// recursive /img/ chains through ourselves.
	selfHost func(host string) bool

	sf  singleflight.Group
	sem chan struct{}
}

// New builds the handler. client may be nil, in which case an SSRF-guarded
// client (public IPs only) is used — pass a custom client only in tests.
func New(c *cache.Cache, log *slog.Logger, selfHost func(string) bool, client *http.Client) *Handler {
	if client == nil {
		client = newSafeClient(selfHost)
	}
	return &Handler{
		cache:    c,
		log:      log,
		client:   client,
		selfHost: selfHost,
		sem:      make(chan struct{}, max(2, runtime.GOMAXPROCS(0))),
	}
}

// httpError is an error with a client-facing status code.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "hubCDN images: only GET is supported", http.StatusMethodNotAllowed)
		return
	}

	optStr, src, err := parsePath(r)
	if err != nil {
		http.Error(w, "hubCDN images: "+err.Error(), http.StatusBadRequest)
		return
	}
	params, err := imageproc.Parse(optStr)
	if err != nil {
		http.Error(w, "hubCDN images: "+err.Error(), http.StatusBadRequest)
		return
	}
	if h.selfHost != nil && h.selfHost(strings.ToLower(src.Hostname())) {
		http.Error(w, "hubCDN images: source may not point back at this node", http.StatusBadRequest)
		return
	}

	key := cache.Key("img.hubcdn.internal", "IMG", optStr+"|"+src.String(), "")
	if obj, freshness := h.cache.Get(key); freshness == cache.Fresh {
		h.serve(w, r, key, obj, "HIT")
		return
	}

	// One fetch+transform per key regardless of concurrent demand.
	v, err, _ := h.sf.Do(key, func() (any, error) {
		return h.optimize(r.Context(), key, src, params)
	})
	if err != nil {
		var he *httpError
		if errors.As(err, &he) {
			http.Error(w, "hubCDN images: "+he.msg, he.status)
			return
		}
		h.log.Warn("image optimization failed", "source", src.String(), "err", err)
		http.Error(w, "hubCDN images: could not fetch or process the source image", http.StatusBadGateway)
		return
	}
	h.serve(w, r, key, v.(*cache.Object), "MISS")
}

// optimize fetches the source, transforms it and stores the result.
func (h *Handler) optimize(ctx context.Context, key string, src *url.URL, params imageproc.Params) (*cache.Object, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	data, err := h.fetch(ctx, src)
	if err != nil {
		return nil, err
	}

	// Bound concurrent CPU-heavy transforms; fetches above run unthrottled
	// since they are I/O.
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	res, err := imageproc.Transform(data, params)
	if err != nil {
		var unsupported imageproc.ErrUnsupported
		if errors.As(err, &unsupported) {
			return nil, &httpError{http.StatusUnsupportedMediaType, "the source is not a supported image format"}
		}
		return nil, err
	}

	obj := &cache.Object{
		Status: http.StatusOK,
		Header: http.Header{
			"Content-Type":  {res.ContentType},
			"Cache-Control": {"public, max-age=604800, immutable"},
		},
		Body:     res.Data,
		StoredAt: time.Now(),
		TTL:      retention,
	}
	if len(obj.Body) <= maxCacheableBytes {
		h.cache.Set(key, obj)
	}
	return obj, nil
}

func (h *Handler) fetch(ctx context.Context, src *url.URL) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.String(), nil)
	if err != nil {
		return nil, &httpError{http.StatusBadRequest, "invalid source URL"}
	}
	req.Header.Set("User-Agent", "hubCDN-image-optimizer/1.0")
	req.Header.Set("Accept", "image/*")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching source: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpError{http.StatusBadGateway,
			fmt.Sprintf("source responded with status %d", resp.StatusCode)}
	}
	if resp.ContentLength > maxSourceBytes {
		return nil, &httpError{http.StatusRequestEntityTooLarge, "source image exceeds the 20 MB limit"}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading source: %w", err)
	}
	if len(data) > maxSourceBytes {
		return nil, &httpError{http.StatusRequestEntityTooLarge, "source image exceeds the 20 MB limit"}
	}
	return data, nil
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request, key string, obj *cache.Object, status string) {
	etag := `W/"` + key[:24] + `"`
	hdr := w.Header()
	for k, vs := range obj.Header {
		hdr[k] = vs
	}
	hdr.Set("Etag", etag)
	hdr.Set("X-Hubcdn-Cache", status)
	hdr.Set("Age", strconv.FormatInt(int64(obj.Age(time.Now()).Seconds()), 10))

	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hdr.Set("Content-Length", strconv.Itoa(len(obj.Body)))
	w.WriteHeader(obj.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(obj.Body)
	}
}

// parsePath splits "/img/<options>/<source-url>" and normalizes the source.
// The request's own query string belongs to the source URL. Proxies and
// clients often collapse "//" inside paths, so "https:/host/..." is
// repaired to "https://host/...", and scheme-less sources default to https.
func parsePath(r *http.Request) (string, *url.URL, error) {
	rest := strings.TrimPrefix(r.URL.EscapedPath(), PathPrefix)
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return "", nil, errors.New("expected /img/<options>/<image-url>; use _ for default options")
	}
	optStr, raw := rest[:slash], rest[slash+1:]
	if raw == "" {
		return "", nil, errors.New("missing source image URL")
	}
	if r.URL.RawQuery != "" {
		raw += "?" + r.URL.RawQuery
	}

	switch {
	case strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://"):
	case strings.HasPrefix(raw, "https:/"):
		raw = "https://" + raw[len("https:/"):]
	case strings.HasPrefix(raw, "http:/"):
		raw = "http://" + raw[len("http:/"):]
	default:
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", nil, errors.New("invalid source image URL")
	}
	if u.User != nil {
		return "", nil, errors.New("source URLs with credentials are not allowed")
	}
	return optStr, u, nil
}

// newSafeClient builds the SSRF-guarded HTTP client used for source
// fetches: every connection (including each redirect hop) re-resolves the
// destination and refuses anything that is not a public unicast address, so
// the endpoint cannot be used to reach loopback, LAN, link-local metadata
// or carrier-grade NAT ranges.
func newSafeClient(selfHost func(string) bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if !isPublicIP(ip) {
					return nil, fmt.Errorf("source resolves to non-public address %s", ip)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		MaxIdleConns:          64,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			if selfHost != nil && selfHost(strings.ToLower(req.URL.Hostname())) {
				return errors.New("redirect points back at this node")
			}
			return nil
		},
	}
}

// isPublicIP reports whether ip is a publicly routable unicast address.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// Carrier-grade NAT (100.64.0.0/10) is not covered by IsPrivate.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}
