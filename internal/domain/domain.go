// Package domain tracks every customer domain served by the node: its
// origin, its per-domain settings, and its lifecycle state. Domains are
// discovered passively — the first request for an unknown host registers it
// — and everything about them is declared in the owner's DNS zone, so there
// are no accounts and no write API.
package domain

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hubfly-space/hubcdn/internal/cache"
)

// Status is a domain's lifecycle state.
type Status string

const (
	// StatusPending: the domain points at us but has not published an
	// origin TXT record yet; requests get the setup instructions page.
	StatusPending Status = "pending"
	// StatusActive: origin known, traffic is proxied.
	StatusActive Status = "active"
	// StatusError: the last refresh failed; the previous origin (if any)
	// keeps serving until DNS recovers.
	StatusError Status = "error"
)

// Settings are the per-domain knobs owners control through their
// hubcdn.v1.config TXT record.
type Settings struct {
	CacheMode      cache.Mode
	TTL            time.Duration
	Websocket      bool
	PreserveHost   bool
	MaxObjectBytes int64
}

// DefaultSettings returns the settings applied when a domain has no config
// record. maxObject is the node-wide cap from the server configuration.
func DefaultSettings(maxObject int64) Settings {
	return Settings{
		CacheMode:      cache.ModeStandard,
		TTL:            time.Hour,
		Websocket:      true,
		PreserveHost:   true,
		MaxObjectBytes: maxObject,
	}
}

// Policy converts the settings into a cache policy.
func (s Settings) Policy() cache.Policy {
	return cache.Policy{
		Mode:           s.CacheMode,
		DefaultTTL:     s.TTL,
		MaxObjectBytes: s.MaxObjectBytes,
	}
}

// ParseSettings parses a config TXT value like
//
//	"cache=aggressive; ttl=6h; websocket=off; preserve_host=on; max_object=16mb"
//
// on top of the given defaults. Unknown keys and malformed values are
// ignored so a typo can never take a domain down.
func ParseSettings(raw string, defaults Settings) Settings {
	s := defaults
	for _, pair := range strings.Split(raw, ";") {
		key, val, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.ToLower(strings.TrimSpace(val))
		switch key {
		case "cache":
			s.CacheMode = cache.ParseMode(val)
		case "ttl":
			if d, ok := parseTTL(val); ok {
				s.TTL = d
			}
		case "websocket":
			if b, ok := parseSwitch(val); ok {
				s.Websocket = b
			}
		case "preserve_host":
			if b, ok := parseSwitch(val); ok {
				s.PreserveHost = b
			}
		case "max_object":
			if n, ok := parseSize(val); ok && n > 0 && n <= defaults.MaxObjectBytes {
				s.MaxObjectBytes = n
			}
		}
	}
	return s
}

// parseTTL accepts Go durations ("90s", "6h") or bare seconds ("3600").
func parseTTL(v string) (time.Duration, bool) {
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, true
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return d, true
	}
	return 0, false
}

func parseSwitch(v string) (bool, bool) {
	switch v {
	case "on", "true", "yes", "1":
		return true, true
	case "off", "false", "no", "0":
		return false, true
	}
	return false, false
}

func parseSize(v string) (int64, bool) {
	mult := int64(1)
	switch {
	case strings.HasSuffix(v, "gb"):
		mult, v = 1<<30, strings.TrimSuffix(v, "gb")
	case strings.HasSuffix(v, "mb"):
		mult, v = 1<<20, strings.TrimSuffix(v, "mb")
	case strings.HasSuffix(v, "kb"):
		mult, v = 1<<10, strings.TrimSuffix(v, "kb")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, false
	}
	return n * mult, true
}

// Snapshot is an immutable view of one domain, safe to hold across a
// request without locking.
type Snapshot struct {
	Host          string    `json:"host"`
	Apex          string    `json:"apex"`
	Status        Status    `json:"status"`
	Origin        *url.URL  `json:"-"`
	OriginStr     string    `json:"origin,omitempty"`
	Settings      Settings  `json:"-"`
	ConfigText    string    `json:"config,omitempty"`
	FirstSeen     time.Time `json:"first_seen"`
	LastRefreshed time.Time `json:"last_refreshed"`
	LastError     string    `json:"last_error,omitempty"`
}
