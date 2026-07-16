// Package dnsx performs the DNS lookups hubCDN relies on: the origin and
// configuration TXT records that customers publish under their domain, and
// the A/AAAA check used to verify a domain actually points at this node.
//
// Record naming follows a versioned scheme so the format can evolve without
// breaking existing zones:
//
//	hubcdn.v1.origin.<domain>  TXT  "https://origin.example.com"
//	hubcdn.v1.config.<domain>  TXT  "cache=standard; ttl=3600; websocket=on"
package dnsx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

const (
	// OriginRecordPrefix is prepended to a domain to form the name of its
	// origin TXT record.
	OriginRecordPrefix = "hubcdn.v1.origin."
	// ConfigRecordPrefix is prepended to a domain to form the name of its
	// configuration TXT record.
	ConfigRecordPrefix = "hubcdn.v1.config."

	lookupTimeout = 5 * time.Second
)

// ErrNoOrigin is returned when a domain has no origin TXT record yet.
var ErrNoOrigin = errors.New("dnsx: no origin record")

// Resolver wraps a net.Resolver with hubCDN-specific lookups.
type Resolver struct {
	r *net.Resolver
}

// New returns a Resolver. If server is non-empty ("host:port") every lookup
// goes to that DNS server; otherwise the system resolver is used.
func New(server string) *Resolver {
	if server == "" {
		return &Resolver{r: net.DefaultResolver}
	}
	return &Resolver{r: &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: lookupTimeout}
			return d.DialContext(ctx, network, server)
		},
	}}
}

// Origin resolves the origin URL for domain from its
// hubcdn.v1.origin.<domain> TXT record. Accepted values are full URLs
// ("https://origin.example.com[:port]") or bare hosts, which default to
// https. Returns ErrNoOrigin when the record does not exist.
func (rs *Resolver) Origin(ctx context.Context, domain string) (*url.URL, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	records, err := rs.r.LookupTXT(ctx, OriginRecordPrefix+domain)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, ErrNoOrigin
		}
		return nil, fmt.Errorf("dnsx: origin lookup for %s: %w", domain, err)
	}
	for _, rec := range records {
		if u, err := ParseOrigin(rec); err == nil {
			return u, nil
		}
	}
	return nil, ErrNoOrigin
}

// ParseOrigin validates and normalizes an origin TXT record value.
func ParseOrigin(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("dnsx: empty origin")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("dnsx: invalid origin %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("dnsx: origin scheme must be http or https, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("dnsx: origin %q has no host", raw)
	}
	// Origins are a host (and optional path prefix), never credentials or
	// query strings.
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}

// ConfigText returns the raw value of the hubcdn.v1.config.<domain> TXT
// record, or "" when absent. Multiple strings in one record are joined, as
// DNS TXT semantics prescribe.
func (rs *Resolver) ConfigText(ctx context.Context, domain string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	records, err := rs.r.LookupTXT(ctx, ConfigRecordPrefix+domain)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return "", nil
		}
		return "", fmt.Errorf("dnsx: config lookup for %s: %w", domain, err)
	}
	if len(records) == 0 {
		return "", nil
	}
	return strings.Join(records, ""), nil
}

// PointsTo reports whether domain has an A/AAAA record resolving to one of
// ips. With an empty ips list it only verifies the domain resolves at all.
func (rs *Resolver) PointsTo(ctx context.Context, domain string, ips []net.IP) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	addrs, err := rs.r.LookupIPAddr(ctx, domain)
	if err != nil {
		return false, fmt.Errorf("dnsx: resolving %s: %w", domain, err)
	}
	if len(ips) == 0 {
		return len(addrs) > 0, nil
	}
	for _, addr := range addrs {
		for _, ip := range ips {
			if addr.IP.Equal(ip) {
				return true, nil
			}
		}
	}
	return false, nil
}

// Apex returns the registrable domain (eTLD+1) for host, which is the unit
// certificate rate limits are enforced on. Falls back to host itself when it
// cannot be determined.
func Apex(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	apex, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return host
	}
	return apex
}

// ValidHost reports whether host looks like a DNS hostname hubCDN should
// serve: lowercase, no port, no IP literals, at least two labels.
func ValidHost(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return false
		}
		for i, r := range label {
			ok := r == '-' && i > 0 && i < len(label)-1 ||
				r >= 'a' && r <= 'z' ||
				r >= '0' && r <= '9'
			if !ok {
				return false
			}
		}
	}
	return true
}
