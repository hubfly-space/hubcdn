// Command hubcdn runs a hubCDN node: a self-hosted CDN edge that terminates
// TLS with automatic certificates, caches responses in memory, and proxies
// traffic to origins declared in the customer's own DNS zone.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hubfly-space/hubcdn/internal/bunny"
	"github.com/hubfly-space/hubcdn/internal/config"
	"github.com/hubfly-space/hubcdn/internal/server"
)

// version, commit and date are set via -ldflags at release build time (see
// .goreleaser.yaml); they stay at these defaults for `go build`/`go run`.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(runHealthCheck())
		case "version", "--version", "-v":
			fmt.Printf("hubcdn %s (commit %s, built %s)\n", version, commit, date)
			os.Exit(0)
		}
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Error("creating data directory", "dir", cfg.DataDir, "err", err)
		os.Exit(1)
	}

	srv, err := server.New(cfg, log)
	if err != nil {
		log.Error("initializing server", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var dns *bunny.Client
	if cfg.BunnyAPIKey != "" {
		dns = bunny.New(cfg.BunnyAPIKey, cfg.BunnyZoneID, log)
		if err := dns.Register(ctx, cfg.Hostname, cfg.PublicIPs); err != nil {
			log.Error("bunny DNS registration failed", "err", err)
			os.Exit(1)
		}
		go dns.Heartbeat(ctx, cfg.Hostname, cfg.PublicIPs, 5*time.Minute)
	}

	log.Info("hubcdn starting",
		"version", version,
		"hostname", cfg.Hostname,
		"data_dir", cfg.DataDir,
		"acme_staging", cfg.ACMEStaging,
	)
	err = srv.Run(ctx)

	if dns != nil {
		// Pull this node out of DNS so peers absorb the traffic.
		deregCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if derr := dns.Deregister(deregCtx, cfg.Hostname, cfg.PublicIPs); derr != nil {
			log.Warn("bunny DNS deregistration failed", "err", derr)
		}
		cancel()
	}

	if err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
	log.Info("hubcdn stopped")
}
