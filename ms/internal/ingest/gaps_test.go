package ingest

import (
	"testing"
	"time"
)

var rome, _ = time.LoadLocation("Europe/Rome")

func day(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func series(from string, n int) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		out[i] = day(from).AddDate(0, 0, i)
	}
	return out
}

func TestSatisfied(t *testing.T) {
	p := GapPolicy{FreshTTL: time.Hour, SettlementDays: 3, Location: rome}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, rome)
	cases := []struct {
		day     string
		fetched time.Time
		want    bool
	}{
		{"2026-11-10", time.Time{}, false},                                 // never fetched
		{"2026-11-10", time.Date(2026, 11, 14, 0, 0, 0, 0, rome), true},    // fetched exactly when final
		{"2026-11-10", time.Date(2026, 11, 13, 23, 0, 0, 0, rome), false},  // fetched before final, long ago
		{"2026-11-18", now.Add(-30 * time.Minute), true},                   // today, fresh
		{"2026-11-18", now.Add(-2 * time.Hour), false},                     // today, stale
		{"2026-11-16", now.Add(-59 * time.Minute), true},                   // unsettled but fresh
		{"2026-11-01", time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), true}, // final instant in UTC is after Rome midnight
	}
	for _, c := range cases {
		if got := p.Satisfied(day(c.day), c.fetched, now); got != c.want {
			t.Errorf("day %s fetched %s: got %v, want %v", c.day, c.fetched, got, c.want)
		}
	}
}

func TestComputeGaps(t *testing.T) {
	p := GapPolicy{FreshTTL: time.Hour, SettlementDays: 2, MaxBridgeDays: 2, Location: time.UTC}
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	final := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC) // final for days <= 11-17

	days := series("2026-11-01", 20) // 11-01 .. 11-20
	fetched := make([]time.Time, len(days))
	// Cached and final: 11-01..11-05, 11-08, 11-12..11-17.
	for _, i := range []int{0, 1, 2, 3, 4, 7, 11, 12, 13, 14, 15, 16} {
		fetched[i] = final
	}
	// 11-18..11-20 were fetched 10 minutes ago: fresh.
	for i := 17; i < 20; i++ {
		fetched[i] = now.Add(-10 * time.Minute)
	}

	gaps := ComputeGaps(days, fetched, now, p)
	// Missing: 11-06, 11-07, 11-09, 11-10, 11-11. The single cached day 11-08
	// is bridged, so one call covers 11-06..11-11.
	if len(gaps) != 1 {
		t.Fatalf("gaps = %v", gaps)
	}
	if gaps[0].From != day("2026-11-06") || gaps[0].To != day("2026-11-11") {
		t.Fatalf("gap = %v", gaps[0])
	}

	// With bridging disabled the same input yields two gaps.
	p.MaxBridgeDays = 0
	if gaps := ComputeGaps(days, fetched, now, p); len(gaps) != 2 {
		t.Fatalf("no-bridge gaps = %v", gaps)
	}

	// Everything never fetched: one gap covering the whole range.
	empty := make([]time.Time, len(days))
	gaps = ComputeGaps(days, empty, now, p)
	if len(gaps) != 1 || gaps[0].From != days[0] || gaps[0].To != days[len(days)-1] {
		t.Fatalf("cold gaps = %v", gaps)
	}

	// Everything final: no bank call.
	all := make([]time.Time, len(days))
	for i := range all {
		all[i] = now
	}
	if gaps := ComputeGaps(days[:17], all[:17], now, p); len(gaps) != 0 {
		t.Fatalf("warm gaps = %v", gaps)
	}
}
