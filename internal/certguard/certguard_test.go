package certguard

import (
	"errors"
	"testing"
	"time"
)

func newTestGuard(t *testing.T, limits Limits) *Guard {
	t.Helper()
	g, err := New(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestPerApexDailyLimit(t *testing.T) {
	g := newTestGuard(t, Limits{PerApexPerDay: 3})
	for i := 0; i < 3; i++ {
		if err := g.Check("example.com"); err != nil {
			t.Fatalf("issuance %d unexpectedly limited: %v", i, err)
		}
		g.Record("example.com")
	}
	if err := g.Check("example.com"); !errors.Is(err, ErrLimited) {
		t.Fatalf("want ErrLimited after 3 issuances, got %v", err)
	}
	// Other apexes are unaffected.
	if err := g.Check("other.org"); err != nil {
		t.Fatalf("unrelated apex limited: %v", err)
	}
}

func TestLimitWindowSlides(t *testing.T) {
	g := newTestGuard(t, Limits{PerApexPerDay: 1})
	now := time.Now()
	g.now = func() time.Time { return now }
	g.Record("example.com")
	if err := g.Check("example.com"); !errors.Is(err, ErrLimited) {
		t.Fatalf("want ErrLimited, got %v", err)
	}
	// 25 hours later the window has passed.
	g.now = func() time.Time { return now.Add(25 * time.Hour) }
	if err := g.Check("example.com"); err != nil {
		t.Fatalf("issuance still limited after window passed: %v", err)
	}
}

func TestGlobalLimit(t *testing.T) {
	g := newTestGuard(t, Limits{GlobalPerHour: 2})
	g.Record("a.com")
	g.Record("b.com")
	if err := g.Check("c.com"); !errors.Is(err, ErrLimited) {
		t.Fatalf("want global ErrLimited, got %v", err)
	}
}

func TestZeroLimitsDisable(t *testing.T) {
	g := newTestGuard(t, Limits{})
	for i := 0; i < 100; i++ {
		g.Record("example.com")
	}
	if err := g.Check("example.com"); err != nil {
		t.Fatalf("zero limits must disable the guard, got %v", err)
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	g1, err := New(dir, Limits{PerApexPerDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	g1.Record("example.com")

	g2, err := New(dir, Limits{PerApexPerDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := g2.Check("example.com"); !errors.Is(err, ErrLimited) {
		t.Fatalf("issuance history lost across restart: %v", err)
	}
}
