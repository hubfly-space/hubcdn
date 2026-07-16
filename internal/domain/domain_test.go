package domain

import (
	"testing"
	"time"

	"github.com/hubcdn/hubcdn/internal/cache"
)

func TestParseSettings(t *testing.T) {
	defaults := DefaultSettings(32 << 20)

	t.Run("empty keeps defaults", func(t *testing.T) {
		if got := ParseSettings("", defaults); got != defaults {
			t.Fatalf("got %+v, want defaults", got)
		}
	})

	t.Run("full config", func(t *testing.T) {
		got := ParseSettings("cache=aggressive; ttl=6h; websocket=off; preserve_host=off; max_object=16mb", defaults)
		want := Settings{
			CacheMode:      cache.ModeAggressive,
			TTL:            6 * time.Hour,
			Websocket:      false,
			PreserveHost:   false,
			MaxObjectBytes: 16 << 20,
		}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("ttl as bare seconds", func(t *testing.T) {
		got := ParseSettings("ttl=90", defaults)
		if got.TTL != 90*time.Second {
			t.Fatalf("got %v, want 90s", got.TTL)
		}
	})

	t.Run("max_object cannot exceed node limit", func(t *testing.T) {
		got := ParseSettings("max_object=10gb", defaults)
		if got.MaxObjectBytes != defaults.MaxObjectBytes {
			t.Fatalf("per-domain max_object escaped the node cap: %d", got.MaxObjectBytes)
		}
	})

	t.Run("garbage is ignored", func(t *testing.T) {
		got := ParseSettings("cache=; bogus; ttl=soon; websocket=maybe; =x", defaults)
		if got.TTL != defaults.TTL || got.Websocket != defaults.Websocket {
			t.Fatalf("malformed values changed settings: %+v", got)
		}
	})
}
