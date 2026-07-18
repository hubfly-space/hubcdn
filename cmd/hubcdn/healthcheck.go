package main

import (
	"net"
	"os"
	"strings"
	"time"
)

// defaultHTTPSAddr is the container-image default. Keep in sync with:
//   - Dockerfile ENV HUBCDN_HTTPS_ADDR
//   - .env.example HUBCDN_HTTPS_PORT (compose maps PORT → ADDR)
const defaultHTTPSAddr = ":4403"

// runHealthCheck is invoked as `hubcdn healthcheck`. hubCDN is TLS-only, and
// on-demand issuance means a plain TLS handshake against 127.0.0.1 (no SNI
// matching a real domain) can't complete - so liveness is a TCP-level check
// that the HTTPS listener is up and accepting connections, rather than an
// HTTP request. That's all a container runtime needs to know the process
// hasn't wedged (see the distroless final stage in the Dockerfile, which has
// no curl/shell to probe with anyway).
func runHealthCheck() int {
	addr := resolveHTTPSAddr()
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return 1
	}
	conn.Close()
	return 0
}

// resolveHTTPSAddr prefers HUBCDN_HTTPS_ADDR (same as internal/config), then
// HUBCDN_HTTPS_PORT from .env.example/compose, then the image default.
func resolveHTTPSAddr() string {
	if addr := os.Getenv("HUBCDN_HTTPS_ADDR"); addr != "" {
		return addr
	}
	if port := strings.TrimSpace(os.Getenv("HUBCDN_HTTPS_PORT")); port != "" {
		port = strings.TrimPrefix(port, ":")
		return ":" + port
	}
	return defaultHTTPSAddr
}
