// Package web renders hubCDN's HTML pages from the templ views.
package web

import (
	"context"
	"log/slog"
	"net"
	"net/http"

	"github.com/hubfly-space/hubcdn/internal/dnsx"
	"github.com/hubfly-space/hubcdn/internal/domain"
	"github.com/hubfly-space/hubcdn/internal/web/views"
)

// RenderLanding writes the node landing page with onboarding instructions.
func RenderLanding(w http.ResponseWriter, hostname string, ips []net.IP) {
	strs := make([]string, len(ips))
	for i, ip := range ips {
		strs[i] = ip.String()
	}
	writeHTML(w, http.StatusOK)
	if err := views.Landing(hostname, strs).Render(context.Background(), w); err != nil {
		slog.Error("failed to render landing page", "error", err)
	}
}

// RenderSetup writes the "add your origin TXT record" page for a pending
// domain.
func RenderSetup(w http.ResponseWriter, snap domain.Snapshot) {
	writeHTML(w, http.StatusOK)
	if err := views.Setup(
		snap.Host,
		dnsx.OriginRecordPrefix+snap.Host,
		dnsx.ConfigRecordPrefix+snap.Host,
	).Render(context.Background(), w); err != nil {
		slog.Error("failed to render setup page", "host", snap.Host, "error", err)
	}
}

// RenderSetupError writes the page shown when a domain's DNS configuration
// keeps failing to resolve.
func RenderSetupError(w http.ResponseWriter, snap domain.Snapshot) {
	writeHTML(w, http.StatusServiceUnavailable)
	if err := views.SetupError(
		snap.Host,
		dnsx.OriginRecordPrefix+snap.Host,
		snap.LastError,
	).Render(context.Background(), w); err != nil {
		slog.Error("failed to render setup error page", "host", snap.Host, "error", err)
	}
}

func writeHTML(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
}
