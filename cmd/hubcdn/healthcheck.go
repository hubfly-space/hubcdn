package main

import (
	"net/http"
	"os"
	"time"
)

// runHealthCheck is invoked as `hubcdn healthcheck`. It hits the node's own
// /hubcdn/health endpoint over plain HTTP so container runtimes can probe
// liveness without a shell or curl in the image (see the distroless final
// stage in the Dockerfile).
func runHealthCheck() int {
	addr := os.Getenv("HUBCDN_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1" + addr + "/hubcdn/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
