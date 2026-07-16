package main

import (
	"net"
	"os"
	"strings"
	"time"
)

// runHealthCheck is invoked as `hubcdn healthcheck`. hubCDN is TLS-only, and
// on-demand issuance means a plain TLS handshake against 127.0.0.1 (no SNI
// matching a real domain) can't complete — so liveness is a TCP-level check
// that the HTTPS listener is up and accepting connections, rather than an
// HTTP request. That's all a container runtime needs to know the process
// hasn't wedged (see the distroless final stage in the Dockerfile, which has
// no curl/shell to probe with anyway).
func runHealthCheck() int {
	addr := os.Getenv("HUBCDN_HTTPS_ADDR")
	if addr == "" {
		addr = ":4403"
	}
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
