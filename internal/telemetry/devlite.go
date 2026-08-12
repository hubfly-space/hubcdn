// Package telemetry wires the optional DevLite SDK into hubCDN.
//
// The integration is opt-in: without DEVLITE_API_KEY, hubCDN behaves exactly
// as before. SDK delivery is asynchronous and deliberately configured so a
// full queue is dropped/trimmed rather than causing a request-thread flush.
package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	devlite "github.com/Ishimwe-Kevin/devlite-go"
)

const maxBatchWithoutSyncFlush = 1 << 30

// Init enables DevLite when DEVLITE_API_KEY is set. It never makes a network
// request during startup. The returned logger preserves the normal stderr
// output and additionally sends structured logs to DevLite. The shutdown
// function is safe to defer and is a no-op when telemetry is disabled.
func Init(base *slog.Logger) (*slog.Logger, func()) {
	key := strings.TrimSpace(os.Getenv("DEVLITE_API_KEY"))
	if key == "" {
		return base, func() {}
	}

	if err := devlite.Init(
		devlite.WithAPIKey(key),
		devlite.WithServiceName("hubcdn"),
		devlite.WithEnvironment(envOr("DEVLITE_ENVIRONMENT", "production")),
		devlite.WithRelease(os.Getenv("DEVLITE_RELEASE")),
		devlite.WithCaptureBody(false),
		devlite.WithMaxQueueSize(5000),
		// The SDK otherwise calls Flush synchronously when the queue reaches
		// MaxBatchSize. Keep that threshold above the bounded queue so only
		// the background interval performs network I/O.
		devlite.WithMaxBatchSize(maxBatchWithoutSyncFlush),
		devlite.WithRequestTimeoutMs(2000),
		devlite.WithMaxRetries(1),
		devlite.WithOnError(func(err error) {
			base.Warn("devlite delivery failed", "err", err)
		}),
	); err != nil {
		base.Warn("devlite disabled", "err", err)
		return base, func() {}
	}

	return slog.New(&logHandler{next: base.Handler()}), devlite.Close
}

// Middleware is a no-op when Init did not enable DevLite.
func Middleware(next http.Handler) http.Handler { return devlite.Middleware(next) }

// StartMetrics periodically invokes collect while DevLite is enabled. The
// callback should only read local counters and call ReportMetric; the SDK
// queues those points without doing network I/O on this goroutine's caller.
func StartMetrics(ctx context.Context, collect func()) {
	if devlite.DefaultClient() == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				collect()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// ReportMetric is a no-op when DevLite is disabled or shutting down.
func ReportMetric(name string, value float64, unit string, tags map[string]string) {
	if devlite.DefaultClient() == nil {
		return
	}
	devlite.ReportMetric(name, value, unit, tags)
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// logHandler mirrors the existing slog handler and forwards only structured
// log fields. DevLite performs its own sensitive-data scrubbing before send.
type logHandler struct {
	next   slog.Handler
	attrs  []slog.Attr
	groups []string
}

func (h *logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *logHandler) Handle(ctx context.Context, record slog.Record) error {
	err := h.next.Handle(ctx, record)
	fields := make(map[string]any, len(h.attrs)+record.NumAttrs())
	for _, attr := range h.attrs {
		addAttr(fields, h.groups, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		addAttr(fields, h.groups, attr)
		return true
	})
	devlite.CaptureLog(record.Level.String(), record.Message, fields)
	return err
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &clone
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.groups = append(append([]string(nil), h.groups...), name)
	return &clone
}

func addAttr(fields map[string]any, groups []string, attr slog.Attr) {
	if attr.Equal(slog.Attr{}) || attr.Key == "" {
		return
	}
	value := attr.Value
	if value.Kind() == slog.KindGroup {
		nested := make(map[string]any)
		for _, child := range value.Group() {
			addAttr(nested, nil, child)
		}
		fields[attr.Key] = nested
		return
	}
	key := attr.Key
	for i := len(groups) - 1; i >= 0; i-- {
		key = groups[i] + "." + key
	}
	fields[key] = value.Any()
}
