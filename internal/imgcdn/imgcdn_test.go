package imgcdn

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hubfly-space/hubcdn/internal/cache"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testHandler uses a plain HTTP client so it can reach loopback test
// servers; production wiring installs the SSRF-guarded client instead.
func testHandler() *Handler {
	return New(cache.New(64<<20), slog.New(slog.DiscardHandler), nil, http.DefaultClient)
}

func TestOptimizeMissThenHit(t *testing.T) {
	var fetches atomic.Int64
	src := pngBytes(t, 400, 200)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(src)
	}))
	defer origin.Close()

	h := testHandler()
	do := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "https://cdn.example/img/w=100,f=jpeg/"+origin.URL+"/photo.png", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	first := do()
	if first.Code != http.StatusOK {
		t.Fatalf("status %d: %s", first.Code, first.Body.String())
	}
	if got := first.Header().Get("X-Hubcdn-Cache"); got != "MISS" {
		t.Fatalf("want MISS, got %q", got)
	}
	if ct := first.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content type %q", ct)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(first.Body.Bytes()))
	if err != nil || cfg.Width != 100 || cfg.Height != 50 {
		t.Fatalf("output %dx%d err=%v, want 100x50", cfg.Width, cfg.Height, err)
	}

	second := do()
	if got := second.Header().Get("X-Hubcdn-Cache"); got != "HIT" {
		t.Fatalf("want HIT, got %q", got)
	}
	if fetches.Load() != 1 {
		t.Fatalf("origin fetched %d times, want 1", fetches.Load())
	}

	// Conditional revalidation from the edge.
	r := httptest.NewRequest(http.MethodGet, "https://cdn.example/img/w=100,f=jpeg/"+origin.URL+"/photo.png", nil)
	r.Header.Set("If-None-Match", second.Header().Get("Etag"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified {
		t.Fatalf("want 304, got %d", w.Code)
	}
}

func TestInvalidOptionsRejected(t *testing.T) {
	h := testHandler()
	r := httptest.NewRequest(http.MethodGet, "https://cdn.example/img/bogus=1/https://example.com/a.png", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestSelfHostRejected(t *testing.T) {
	h := New(cache.New(1<<20), slog.New(slog.DiscardHandler),
		func(host string) bool { return host == "cdn.example" }, http.DefaultClient)
	r := httptest.NewRequest(http.MethodGet, "https://cdn.example/img/_/https://cdn.example/img/_/x.png", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("recursive self-fetch not rejected: %d", w.Code)
	}
}

func TestNonImageSourceIs415(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer origin.Close()

	h := testHandler()
	r := httptest.NewRequest(http.MethodGet, "https://cdn.example/img/_/"+origin.URL+"/page", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("want 415, got %d: %s", w.Code, w.Body.String())
	}
}

func TestParsePathNormalization(t *testing.T) {
	tests := []struct {
		path    string
		query   string
		wantURL string
		wantOpt string
		wantErr bool
	}{
		{"/img/w=80/https://example.com/a.png", "", "https://example.com/a.png", "w=80", false},
		{"/img/_/https:/example.com/a.png", "", "https://example.com/a.png", "_", false},
		{"/img/_/example.com/a.png", "", "https://example.com/a.png", "_", false},
		{"/img/_/http://example.com/a.png", "", "http://example.com/a.png", "_", false},
		{"/img/w=80/example.com/a.png", "v=2", "https://example.com/a.png?v=2", "w=80", false},
		{"/img/w=80", "", "", "", true},
		{"/img/_/", "", "", "", true},
		{"/img/_/https://user:pass@example.com/a.png", "", "", "", true},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "https://cdn.example"+tt.path, nil)
		if tt.query != "" {
			r.URL.RawQuery = tt.query
		}
		opt, u, err := parsePath(r)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parsePath(%q) should fail, got %v", tt.path, u)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePath(%q): %v", tt.path, err)
			continue
		}
		if u.String() != tt.wantURL || opt != tt.wantOpt {
			t.Errorf("parsePath(%q) = %q %q, want %q %q", tt.path, opt, u, tt.wantOpt, tt.wantURL)
		}
	}
}

func TestIsPublicIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.0.0.5", "192.168.1.3", "172.16.0.1",
		"169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "fe80::1", "fc00::1", "224.0.0.1"}
	allowed := []string{"41.186.167.39", "1.1.1.1", "2606:4700:4700::1111"}
	for _, s := range blocked {
		if isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s must be blocked", s)
		}
	}
	for _, s := range allowed {
		if !isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s must be allowed", s)
		}
	}
}
