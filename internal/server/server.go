// Package server wires every hubCDN component into a single HTTPS listener.
// Every domain hubCDN serves - including its own landing page - is TLS
// only; there is no plaintext HTTP anywhere. Certificates are issued
// on-demand via the TLS-ALPN-01 ACME challenge, which is validated entirely
// within the TLS handshake on the HTTPS port itself, so no separate HTTP
// port is ever needed.
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/caddyserver/certmagic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/hubfly-space/hubcdn/internal/cache"
	"github.com/hubfly-space/hubcdn/internal/certguard"
	"github.com/hubfly-space/hubcdn/internal/config"
	"github.com/hubfly-space/hubcdn/internal/dnsx"
	"github.com/hubfly-space/hubcdn/internal/domain"
	"github.com/hubfly-space/hubcdn/internal/imgcdn"
	"github.com/hubfly-space/hubcdn/internal/proxy"
	"github.com/hubfly-space/hubcdn/internal/web"
)

// Server owns the HTTPS listener and all shared components.
type Server struct {
	cfg      *config.Config
	log      *slog.Logger
	resolver *dnsx.Resolver
	registry *domain.Registry
	guard    *certguard.Guard
	cache    *cache.Cache
	proxy    *proxy.Proxy
	images   *imgcdn.Handler
	magic    *certmagic.Config
	started  time.Time
}

// New assembles a Server from configuration. Callers must also start the
// background loops via Run.
func New(cfg *config.Config, log *slog.Logger) (*Server, error) {
	resolver := dnsx.New(cfg.Resolver)

	guard, err := certguard.New(cfg.DataDir, certguard.Limits{
		PerApexPerDay:  cfg.CertsPerApexPerDay,
		PerApexPerWeek: cfg.CertsPerApexPerWeek,
		GlobalPerHour:  cfg.CertsGlobalPerHour,
		GlobalPerWeek:  cfg.CertsGlobalPerWeek,
	})
	if err != nil {
		return nil, err
	}

	registry, err := domain.NewRegistry(domain.Options{
		Dir:             cfg.DataDir,
		Resolver:        resolver,
		Defaults:        domain.DefaultSettings(cfg.CacheMaxObjectBytes),
		RefreshInterval: cfg.RefreshInterval,
		PendingInterval: cfg.PendingRefreshInterval,
		Log:             log,
	})
	if err != nil {
		return nil, err
	}

	budget := cfg.CacheMaxBytes
	if budget == 0 {
		budget = cache.AutoBudget()
	}
	objCache := cache.New(budget)
	log.Info("cache initialized", "budget_mb", budget>>20)

	s := &Server{
		cfg:      cfg,
		log:      log,
		resolver: resolver,
		registry: registry,
		guard:    guard,
		cache:    objCache,
		proxy:    proxy.New(objCache, log),
		started:  time.Now(),
	}
	s.images = imgcdn.New(objCache, log, s.isSelfHost, nil)
	if err := s.setupTLS(); err != nil {
		return nil, err
	}
	return s, nil
}

// isSelfHost reports whether host names this node, so the image endpoint
// can refuse recursive fetches through itself.
func (s *Server) isSelfHost(host string) bool {
	if host == s.cfg.Hostname && host != "" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		for _, own := range s.cfg.PublicIPs {
			if ip.Equal(own) {
				return true
			}
		}
	}
	return false
}

// setupTLS configures certmagic for on-demand issuance gated by the guard.
func (s *Server) setupTLS() error {
	storage := &certmagic.FileStorage{Path: filepath.Join(s.cfg.DataDir, "certs")}

	certCache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			return s.magic, nil
		},
	})

	magicCfg := certmagic.Config{
		Storage: storage,
		OnDemand: &certmagic.OnDemandConfig{
			DecisionFunc: s.decideIssuance,
		},
		OnEvent: s.onCertEvent,
	}
	if s.cfg.ACMEDebug {
		// certmagic's default logger runs at Info level, which silently
		// drops most of the detail acmez logs while placing an ACME order
		// (challenge setup, authorization polling) - and some failure
		// paths in acmez only log at Error for structured ACME "problem"
		// responses, so a plain network/timeout error can propagate all
		// the way up without ever being logged. Debug level surfaces all
		// of it.
		magicCfg.Logger = zap.New(zapcore.NewCore(
			zapcore.NewConsoleEncoder(zap.NewProductionEncoderConfig()),
			zapcore.AddSync(os.Stderr),
			zap.DebugLevel,
		))
	}
	s.magic = certmagic.New(certCache, magicCfg)

	ca := s.cfg.ACMECA
	if ca == "" {
		ca = certmagic.LetsEncryptProductionCA
		if s.cfg.ACMEStaging {
			ca = certmagic.LetsEncryptStagingCA
		}
	}
	// TLS-ALPN-01 only: it validates entirely within the TLS handshake on
	// the HTTPS port, so hubCDN never needs a plaintext HTTP listener or
	// port 80 reachable from the internet.
	acme := certmagic.NewACMEIssuer(s.magic, certmagic.ACMEIssuer{
		CA:                   ca,
		Email:                s.cfg.ACMEEmail,
		Agreed:               true,
		DisableHTTPChallenge: true,
	})
	s.magic.Issuers = []certmagic.Issuer{acme}
	return nil
}

// decideIssuance is the certificate gate. It runs before every new ACME
// order and rejects hosts that do not point at this node or would blow the
// issuance budgets - this is what keeps one abusive user with unlimited
// subdomains from exhausting the CA's weekly rate limit for everyone.
func (s *Server) decideIssuance(ctx context.Context, name string) error {
	name = strings.ToLower(name)
	if !dnsx.ValidHost(name) {
		err := fmt.Errorf("refusing certificate for invalid host %q", name)
		s.log.Warn("certificate refused: invalid host", "host", name, "err", err)
		return err
	}
	if len(s.cfg.PublicIPs) > 0 {
		ok, err := s.resolver.PointsTo(ctx, name, s.cfg.PublicIPs)
		if err != nil {
			err = fmt.Errorf("verifying %s points here: %w", name, err)
			s.log.Warn("certificate refused: points-at-us check errored", "host", name, "public_ips", s.cfg.PublicIPs, "err", err)
			return err
		}
		if !ok {
			err := fmt.Errorf("%s does not resolve to this node", name)
			s.log.Warn("certificate refused: host does not resolve to this node", "host", name, "public_ips", s.cfg.PublicIPs, "err", err)
			return err
		}
	}
	if err := s.guard.Check(dnsx.Apex(name)); err != nil {
		s.log.Warn("certificate refused by guard", "host", name, "err", err)
		return err
	}
	s.log.Info("certificate issuance approved, requesting from CA", "host", name)
	return nil
}

// onCertEvent records successful new issuances against the guard's budgets
// and surfaces failures. certmagic's own failure log fires only when the
// obtain attempt returns an error; hooking cert_failed here means the reason
// is always visible in hubCDN's log at the same level as everything else.
// Renewals are exempt from the guard: they replace an existing certificate
// and must never be starved by a noisy apex.
func (s *Server) onCertEvent(_ context.Context, event string, data map[string]any) error {
	switch event {
	case "cert_obtained":
		renewal, _ := data["renewal"].(bool)
		if renewal {
			return nil
		}
		name, _ := data["identifier"].(string)
		if name == "" {
			return nil
		}
		apex := dnsx.Apex(name)
		s.guard.Record(apex)
		s.log.Info("certificate issued", "host", name, "apex", apex)
	case "cert_failed":
		name, _ := data["identifier"].(string)
		s.log.Error("certificate issuance failed",
			"host", name,
			"issuers", data["issuers"],
			"err", data["error"])
	}
	return nil
}

// Run starts the HTTPS listener and the background loops, blocking until
// ctx is canceled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	go s.registry.Run(ctx)
	go s.cache.Watchdog(ctx, s.cache.Budget(), s.cfg.CacheMemHeadroomBytes, s.log)

	tlsCfg := s.magic.TLSConfig()
	tlsCfg.MinVersion = tls.VersionTLS12
	tlsCfg.NextProtos = append([]string{"h2", "http/1.1"}, tlsCfg.NextProtos...)

	httpsSrv := &http.Server{
		Addr:              s.cfg.HTTPSAddr,
		Handler:           http.HandlerFunc(s.route),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}

	errCh := make(chan error, 1)
	go func() {
		ln, err := net.Listen("tcp", httpsSrv.Addr)
		if err != nil {
			errCh <- fmt.Errorf("https listen %s: %w", httpsSrv.Addr, err)
			return
		}
		s.log.Info("https listening", "addr", httpsSrv.Addr)
		errCh <- httpsSrv.ServeTLS(ln, "", "")
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpsSrv.Shutdown(shutdownCtx)
	return nil
}

// route dispatches an HTTPS request: node pages for the node's own
// hostname, setup or proxy for customer domains.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)

	if host == "" || host == s.cfg.Hostname || !dnsx.ValidHost(host) {
		s.serveNode(w, r)
		return
	}

	snap, ok := s.registry.Lookup(host)
	if !ok {
		snap = s.registry.Observe(r.Context(), host)
	}

	switch snap.Status {
	case domain.StatusActive:
		s.proxy.ServeHTTP(w, r, snap)
	case domain.StatusError:
		if snap.Origin != nil {
			// Keep serving through the last known origin during DNS
			// hiccups.
			s.proxy.ServeHTTP(w, r, snap)
			return
		}
		web.RenderSetupError(w, snap)
	default:
		// Pending: re-check DNS opportunistically so a fresh TXT record
		// activates on the next page load, not the next refresh tick.
		if time.Since(snap.LastRefreshed) > 5*time.Second {
			if updated, ok := s.registry.Refresh(r.Context(), host); ok {
				snap = updated
			}
			if snap.Status == domain.StatusActive {
				s.proxy.ServeHTTP(w, r, snap)
				return
			}
		}
		web.RenderSetup(w, snap)
	}
}

// serveNode handles requests addressed to the node itself.
func (s *Server) serveNode(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, imgcdn.PathPrefix) {
		s.images.ServeHTTP(w, r)
		return
	}
	switch r.URL.Path {
	case "/", "/index.html":
		web.RenderLanding(w, s.cfg.Hostname, s.cfg.PublicIPs)
	case "/hubcdn/health":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	case "/hubcdn/stats":
		s.serveStats(w)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveStats(w http.ResponseWriter) {
	stats := s.cache.Stats()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"uptime_seconds": int64(time.Since(s.started).Seconds()),
		"domains":        s.registry.Count(),
		"cache": map[string]any{
			"entries":   stats.Entries,
			"bytes":     stats.Bytes,
			"budget":    stats.Budget,
			"hits":      stats.Hits,
			"misses":    stats.Misses,
			"evictions": stats.Evictions,
		},
	})
}

func hostOnly(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}
