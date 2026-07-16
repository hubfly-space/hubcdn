// Package config loads and validates the hubCDN runtime configuration.
//
// Every option is set through an environment variable prefixed with HUBCDN_.
// All options have safe defaults so a bare `hubcdn` starts a working node;
// see docs/self-hosting.md for the full reference.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every tunable of a hubCDN node.
type Config struct {
	// DataDir is where certificates, the domain registry and issuance
	// counters are persisted.
	DataDir string

	// HTTPSAddr is the listen address. hubCDN is TLS-only: certificates are
	// issued on-demand via TLS-ALPN-01, which is validated entirely within
	// the TLS handshake, so there is no HTTP listener at all.
	HTTPSAddr string

	// Hostname is the public hostname of this hubCDN deployment, used on
	// the landing page (e.g. "cdn.example.net"). Optional.
	Hostname string

	// ACMEEmail is the account email for the ACME CA.
	ACMEEmail string
	// ACMECA is the ACME directory URL. Empty selects Let's Encrypt
	// production; set ACMEStaging for the staging directory instead.
	ACMECA      string
	ACMEStaging bool

	// PublicIPs are the addresses this node is reachable on. When set, a
	// certificate is only issued for a domain whose A/AAAA records point
	// at one of them. Strongly recommended in production.
	PublicIPs []net.IP

	// Resolver is an optional "host:port" of the DNS server used for all
	// lookups. Empty uses the system resolver.
	Resolver string

	// RefreshInterval controls how often origin and configuration TXT
	// records of active domains are re-resolved.
	RefreshInterval time.Duration
	// PendingRefreshInterval is the faster cadence used for domains that
	// have pointed at us but not yet published an origin record.
	PendingRefreshInterval time.Duration

	// Certificate issuance guard limits. Zero disables a limit.
	CertsPerApexPerDay  int
	CertsPerApexPerWeek int
	CertsGlobalPerHour  int
	CertsGlobalPerWeek  int

	// CacheMaxBytes caps the in-memory cache. 0 means automatic sizing
	// from available system memory.
	CacheMaxBytes int64
	// CacheMaxObjectBytes is the largest single response body the cache
	// will store.
	CacheMaxObjectBytes int64
	// CacheMemHeadroomBytes is how much system memory must stay available
	// before the watchdog starts shrinking the cache.
	CacheMemHeadroomBytes int64

	// Bunny DNS integration (optional). When both are set the node
	// registers its public IPs as A/AAAA records on Hostname inside the
	// zone, enabling DNS-based multi-node routing.
	BunnyAPIKey string
	BunnyZoneID int64
}

// Load reads the configuration from the environment.
func Load() (*Config, error) {
	cfg := &Config{
		DataDir:                envStr("HUBCDN_DATA_DIR", "./data"),
		HTTPSAddr:              envStr("HUBCDN_HTTPS_ADDR", ":443"),
		Hostname:               envStr("HUBCDN_HOSTNAME", ""),
		ACMEEmail:              envStr("HUBCDN_ACME_EMAIL", ""),
		ACMECA:                 envStr("HUBCDN_ACME_CA", ""),
		ACMEStaging:            envBool("HUBCDN_ACME_STAGING", false),
		Resolver:               envStr("HUBCDN_RESOLVER", ""),
		RefreshInterval:        envDuration("HUBCDN_REFRESH_INTERVAL", 5*time.Minute),
		PendingRefreshInterval: envDuration("HUBCDN_PENDING_REFRESH_INTERVAL", 30*time.Second),
		CertsPerApexPerDay:     envInt("HUBCDN_CERTS_PER_APEX_PER_DAY", 10),
		CertsPerApexPerWeek:    envInt("HUBCDN_CERTS_PER_APEX_PER_WEEK", 30),
		CertsGlobalPerHour:     envInt("HUBCDN_CERTS_GLOBAL_PER_HOUR", 60),
		CertsGlobalPerWeek:     envInt("HUBCDN_CERTS_GLOBAL_PER_WEEK", 1000),
		CacheMaxBytes:          envBytes("HUBCDN_CACHE_MAX_BYTES", 0),
		CacheMaxObjectBytes:    envBytes("HUBCDN_CACHE_MAX_OBJECT_BYTES", 32<<20),
		CacheMemHeadroomBytes:  envBytes("HUBCDN_CACHE_MEM_HEADROOM_BYTES", 256<<20),
		BunnyAPIKey:            envStr("HUBCDN_BUNNY_API_KEY", ""),
		BunnyZoneID:            int64(envInt("HUBCDN_BUNNY_ZONE_ID", 0)),
	}

	for _, raw := range splitList(os.Getenv("HUBCDN_PUBLIC_IPS")) {
		ip := net.ParseIP(raw)
		if ip == nil {
			return nil, fmt.Errorf("HUBCDN_PUBLIC_IPS: invalid IP %q", raw)
		}
		cfg.PublicIPs = append(cfg.PublicIPs, ip)
	}

	if cfg.BunnyAPIKey != "" && cfg.BunnyZoneID == 0 {
		return nil, fmt.Errorf("HUBCDN_BUNNY_ZONE_ID is required when HUBCDN_BUNNY_API_KEY is set")
	}
	if cfg.BunnyAPIKey != "" && cfg.Hostname == "" {
		return nil, fmt.Errorf("HUBCDN_HOSTNAME is required when Bunny DNS integration is enabled")
	}
	if cfg.BunnyAPIKey != "" && len(cfg.PublicIPs) == 0 {
		return nil, fmt.Errorf("HUBCDN_PUBLIC_IPS is required when Bunny DNS integration is enabled")
	}
	return cfg, nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// envBytes parses sizes like "512mb", "2gb" or plain byte counts.
func envBytes(key string, def int64) int64 {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return def
	}
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
		return def
	}
	return n * mult
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
