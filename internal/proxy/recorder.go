package proxy

import (
	"net/http"
	"time"

	"github.com/hubfly-space/hubcdn/internal/cache"
)

// recorder wraps the client's ResponseWriter on a cache miss. It streams the
// origin response through untouched while buffering cacheable bodies, and
// abandons the buffer the moment it grows past the policy's object limit so
// large downloads never pile up in memory.
type recorder struct {
	http.ResponseWriter
	reqPath string
	policy  cache.Policy

	wroteHeader bool
	store       bool
	status      int
	header      http.Header
	ttl         time.Duration
	body        []byte
}

// hopHeaders are connection-scoped and must not be replayed from the cache.
var hopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade", "Age", "Set-Cookie",
}

func (rec *recorder) WriteHeader(status int) {
	if !rec.wroteHeader {
		rec.wroteHeader = true
		rec.status = status
		if ttl, ok := rec.policy.ResponseTTL(rec.reqPath, status, rec.ResponseWriter.Header()); ok {
			rec.store = true
			rec.ttl = ttl
			rec.header = rec.ResponseWriter.Header().Clone()
			for _, h := range hopHeaders {
				rec.header.Del(h)
			}
		}
	}
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *recorder) Write(b []byte) (int, error) {
	if !rec.wroteHeader {
		rec.WriteHeader(http.StatusOK)
	}
	if rec.store {
		if int64(len(rec.body)+len(b)) > rec.policy.MaxObjectBytes {
			rec.store = false
			rec.body = nil
		} else {
			rec.body = append(rec.body, b...)
		}
	}
	return rec.ResponseWriter.Write(b)
}

// Flush keeps streaming responses (SSE, chunked) working through the tee.
func (rec *recorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (rec *recorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// object returns the finished cache object, or nil when the response was not
// cacheable or exceeded the size limit.
func (rec *recorder) object() *cache.Object {
	if !rec.store {
		return nil
	}
	return &cache.Object{
		Status:   rec.status,
		Header:   rec.header,
		Body:     rec.body,
		StoredAt: time.Now(),
		TTL:      rec.ttl,
		StaleFor: rec.policy.StaleFor,
	}
}

// discardWriter satisfies http.ResponseWriter for background revalidation
// fetches: the recorder wrapping it captures the response for the cache and
// nothing goes to a client.
type discardWriter struct {
	header http.Header
}

func newDiscardWriter() *discardWriter {
	return &discardWriter{header: make(http.Header)}
}

func (d *discardWriter) Header() http.Header         { return d.header }
func (d *discardWriter) WriteHeader(int)             {}
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
