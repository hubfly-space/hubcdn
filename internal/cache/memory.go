package cache

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// AutoBudget picks an initial cache budget from the machine's currently
// available memory: 40% of MemAvailable, clamped to [64 MiB, 8 GiB]. Used
// when no explicit budget is configured.
func AutoBudget() int64 {
	avail := memAvailable()
	if avail <= 0 {
		return 256 << 20
	}
	budget := avail * 4 / 10
	if budget < 64<<20 {
		budget = 64 << 20
	}
	if budget > 8<<30 {
		budget = 8 << 30
	}
	return budget
}

// Watchdog adapts the cache budget to system memory pressure until ctx is
// done. When available memory falls below headroom the budget shrinks 25%
// per tick (evicting immediately); when memory recovers it grows back 10%
// per tick, never exceeding target.
func (c *Cache) Watchdog(ctx context.Context, target, headroom int64, log *slog.Logger) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		avail := memAvailable()
		if avail <= 0 {
			continue
		}
		budget := c.Budget()
		switch {
		case avail < headroom && budget > 64<<20:
			next := budget * 3 / 4
			if next < 64<<20 {
				next = 64 << 20
			}
			c.SetBudget(next)
			log.Warn("memory pressure: shrinking cache",
				"available_mb", avail>>20, "budget_mb", next>>20)
		case avail > headroom*2 && budget < target:
			next := budget + budget/10
			if next > target {
				next = target
			}
			c.SetBudget(next)
		}
	}
}

// memAvailable returns MemAvailable from /proc/meminfo in bytes, or 0 when
// it cannot be determined (non-Linux hosts).
func memAvailable() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb << 10
	}
	return 0
}
