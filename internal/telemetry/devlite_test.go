package telemetry

import (
	"log/slog"
	"os"
	"testing"
)

func TestInitWithoutKeyIsNoOp(t *testing.T) {
	old, had := os.LookupEnv("DEVLITE_API_KEY")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("DEVLITE_API_KEY", old)
		} else {
			_ = os.Unsetenv("DEVLITE_API_KEY")
		}
	})
	_ = os.Unsetenv("DEVLITE_API_KEY")

	base := slog.New(slog.NewTextHandler(os.Stderr, nil))
	got, closeTelemetry := Init(base)
	closeTelemetry()
	if got != base {
		t.Fatal("telemetry changed the logger without an API key")
	}
}
