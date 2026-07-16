// Package bunny integrates a hubCDN node with Bunny DNS for multi-node
// routing. On startup the node upserts A/AAAA records for its public IPs on
// the CDN hostname inside a Bunny DNS zone; on graceful shutdown it removes
// them again. Running several nodes against the same zone yields DNS
// round-robin routing across the fleet, with dead nodes dropping out of the
// answer set as they shut down.
package bunny

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

const apiBase = "https://api.bunny.net"

// Bunny DNS record type identifiers.
const (
	recordA    = 0
	recordAAAA = 1
)

// Client talks to the Bunny DNS API for one zone.
type Client struct {
	apiKey string
	zoneID int64
	http   *http.Client
	log    *slog.Logger
}

// New returns a Bunny DNS client for the given zone.
func New(apiKey string, zoneID int64, log *slog.Logger) *Client {
	return &Client{
		apiKey: apiKey,
		zoneID: zoneID,
		http:   &http.Client{Timeout: 15 * time.Second},
		log:    log,
	}
}

type zone struct {
	Domain  string   `json:"Domain"`
	Records []record `json:"Records"`
}

type record struct {
	ID    int64  `json:"Id"`
	Type  int    `json:"Type"`
	Name  string `json:"Name"`
	Value string `json:"Value"`
	TTL   int    `json:"Ttl"`
}

// Register upserts A/AAAA records for ips on hostname. hostname must live
// inside the client's zone.
func (c *Client) Register(ctx context.Context, hostname string, ips []net.IP) error {
	z, err := c.zone(ctx)
	if err != nil {
		return err
	}
	name, err := recordName(hostname, z.Domain)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		typ := recordA
		if ip.To4() == nil {
			typ = recordAAAA
		}
		if hasRecord(z.Records, typ, name, ip.String()) {
			continue
		}
		if err := c.addRecord(ctx, typ, name, ip.String()); err != nil {
			return fmt.Errorf("bunny: adding %s record for %s: %w", ip, hostname, err)
		}
		c.log.Info("bunny: registered node in DNS", "hostname", hostname, "ip", ip.String())
	}
	return nil
}

// Deregister removes this node's A/AAAA records for ips from hostname so
// traffic routes to the remaining nodes.
func (c *Client) Deregister(ctx context.Context, hostname string, ips []net.IP) error {
	z, err := c.zone(ctx)
	if err != nil {
		return err
	}
	name, err := recordName(hostname, z.Domain)
	if err != nil {
		return err
	}
	for _, rec := range z.Records {
		if rec.Type != recordA && rec.Type != recordAAAA {
			continue
		}
		if rec.Name != name {
			continue
		}
		for _, ip := range ips {
			if rec.Value != ip.String() {
				continue
			}
			if err := c.deleteRecord(ctx, rec.ID); err != nil {
				return fmt.Errorf("bunny: deleting record %d: %w", rec.ID, err)
			}
			c.log.Info("bunny: deregistered node from DNS", "hostname", hostname, "ip", rec.Value)
		}
	}
	return nil
}

// Heartbeat re-registers the node every interval until ctx is done, healing
// records that were removed out-of-band.
func (c *Client) Heartbeat(ctx context.Context, hostname string, ips []net.IP, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Register(ctx, hostname, ips); err != nil {
				c.log.Warn("bunny: heartbeat failed", "err", err)
			}
		}
	}
}

func (c *Client) zone(ctx context.Context) (*zone, error) {
	var z zone
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/dnszone/%d", c.zoneID), nil, &z); err != nil {
		return nil, fmt.Errorf("bunny: fetching zone %d: %w", c.zoneID, err)
	}
	return &z, nil
}

func (c *Client) addRecord(ctx context.Context, typ int, name, value string) error {
	body := map[string]any{"Type": typ, "Name": name, "Value": value, "Ttl": 300}
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/dnszone/%d/records", c.zoneID), body, nil)
}

func (c *Client) deleteRecord(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/dnszone/%d/records/%d", c.zoneID, id), nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("AccessKey", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("bunny API %s %s: status %d", method, path, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// hasRecord reports whether records already contains an exact match.
func hasRecord(records []record, typ int, name, value string) bool {
	for _, r := range records {
		if r.Type == typ && r.Name == name && r.Value == value {
			return true
		}
	}
	return false
}

// recordName converts a FQDN into the record name Bunny expects: the label
// prefix relative to the zone, or "" for the zone apex.
func recordName(hostname, zoneDomain string) (string, error) {
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	zoneDomain = strings.TrimSuffix(strings.ToLower(zoneDomain), ".")
	if hostname == zoneDomain {
		return "", nil
	}
	if !strings.HasSuffix(hostname, "."+zoneDomain) {
		return "", fmt.Errorf("bunny: hostname %s is not inside zone %s", hostname, zoneDomain)
	}
	return strings.TrimSuffix(hostname, "."+zoneDomain), nil
}
