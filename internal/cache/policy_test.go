package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func stdPolicy(mode Mode) Policy {
	return Policy{Mode: mode, DefaultTTL: time.Hour, MaxObjectBytes: 1 << 20}
}

func TestCacheableRequest(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
		mut  func(*http.Request)
		want bool
	}{
		{"plain GET", ModeStandard, nil, true},
		{"HEAD", ModeStandard, func(r *http.Request) { r.Method = http.MethodHead }, true},
		{"POST", ModeStandard, func(r *http.Request) { r.Method = http.MethodPost }, false},
		{"mode off", ModeOff, nil, false},
		{"authorization", ModeStandard, func(r *http.Request) { r.Header.Set("Authorization", "Bearer x") }, false},
		{"range", ModeStandard, func(r *http.Request) { r.Header.Set("Range", "bytes=0-100") }, false},
		{"client no-store", ModeStandard, func(r *http.Request) { r.Header.Set("Cache-Control", "no-store") }, false},
		{"cookie standard", ModeStandard, func(r *http.Request) { r.Header.Set("Cookie", "s=1") }, false},
		{"cookie aggressive html", ModeAggressive, func(r *http.Request) { r.Header.Set("Cookie", "s=1") }, true},
		{"cookie aggressive static", ModeAggressive, func(r *http.Request) {
			r.Header.Set("Cookie", "s=1")
			r.URL.Path = "/app.css"
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "https://example.com/page", nil)
			if tt.mut != nil {
				tt.mut(r)
			}
			if got := stdPolicy(tt.mode).CacheableRequest(r); got != tt.want {
				t.Fatalf("want %v, got %v", tt.want, got)
			}
		})
	}
}

func TestResponseTTL(t *testing.T) {
	tests := []struct {
		name    string
		mode    Mode
		path    string
		status  int
		header  http.Header
		wantTTL time.Duration
		wantOK  bool
	}{
		{"s-maxage wins", ModeStandard, "/x", 200,
			http.Header{"Cache-Control": {"max-age=10, s-maxage=60"}}, time.Minute, true},
		{"max-age", ModeStandard, "/x", 200,
			http.Header{"Cache-Control": {"max-age=120"}}, 2 * time.Minute, true},
		{"no-store", ModeStandard, "/x", 200,
			http.Header{"Cache-Control": {"no-store"}}, 0, false},
		{"no-store aggressive", ModeAggressive, "/x", 200,
			http.Header{"Cache-Control": {"no-store"}}, 0, false},
		{"private", ModeStandard, "/x", 200,
			http.Header{"Cache-Control": {"private, max-age=60"}}, 0, false},
		{"private aggressive", ModeAggressive, "/x", 200,
			http.Header{"Cache-Control": {"private, max-age=60"}}, 0, false},
		{"no-cache standard", ModeStandard, "/x", 200,
			http.Header{"Cache-Control": {"no-cache"}}, 0, false},
		{"no-cache aggressive overridden", ModeAggressive, "/x", 200,
			http.Header{"Cache-Control": {"no-cache"}}, time.Hour, true},
		{"max-age=0 standard", ModeStandard, "/x", 200,
			http.Header{"Cache-Control": {"max-age=0"}}, 0, false},
		{"max-age=0 aggressive overridden", ModeAggressive, "/x", 200,
			http.Header{"Cache-Control": {"max-age=0"}}, time.Hour, true},
		{"no-cache standard static not heuristic", ModeStandard, "/app.js", 200,
			http.Header{"Cache-Control": {"no-cache"}}, 0, false},
		{"set-cookie", ModeStandard, "/x", 200,
			http.Header{"Set-Cookie": {"s=1"}, "Cache-Control": {"max-age=60"}}, 0, false},
		{"set-cookie aggressive", ModeAggressive, "/x", 200,
			http.Header{"Set-Cookie": {"s=1"}, "Cache-Control": {"max-age=60"}}, 0, false},
		{"vary star", ModeStandard, "/x", 200,
			http.Header{"Vary": {"*"}, "Cache-Control": {"max-age=60"}}, 0, false},
		{"500 not cacheable", ModeStandard, "/x", 500,
			http.Header{"Cache-Control": {"max-age=60"}}, 0, false},
		{"no headers dynamic standard", ModeStandard, "/api/data", 200, http.Header{}, 0, false},
		{"no headers static standard", ModeStandard, "/app.js", 200, http.Header{}, time.Hour, true},
		{"no headers dynamic aggressive", ModeAggressive, "/api/data", 200, http.Header{}, time.Hour, true},
		{"404 cacheable", ModeStandard, "/app.js", 404, http.Header{}, time.Hour, true},
		{"ttl capped", ModeStandard, "/x", 200,
			http.Header{"Cache-Control": {"max-age=99999999"}}, maxTTL, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ttl, ok := stdPolicy(tt.mode).ResponseTTL(tt.path, tt.status, tt.header)
			if ok != tt.wantOK || ttl != tt.wantTTL {
				t.Fatalf("want (%v, %v), got (%v, %v)", tt.wantTTL, tt.wantOK, ttl, ok)
			}
		})
	}
}
