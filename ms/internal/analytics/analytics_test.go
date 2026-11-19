package analytics

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/cecinuga/pff/ms/internal/domain"
)

func day(s string) time.Time {
	t, _ := domain.ParseDate(s)
	return t
}

func TestBucketStart(t *testing.T) {
	// 2026-11-18 is a Wednesday.
	if got := BucketStart(day("2026-11-18"), BucketWeek); got != day("2026-11-16") {
		t.Errorf("week bucket = %s", got)
	}
	if got := BucketStart(day("2026-11-16"), BucketWeek); got != day("2026-11-16") {
		t.Errorf("monday must be its own bucket, got %s", got)
	}
	if got := BucketStart(day("2026-11-22"), BucketWeek); got != day("2026-11-16") {
		t.Errorf("sunday bucket = %s", got)
	}
	if got := BucketStart(day("2026-11-18"), BucketMonth); got != day("2026-11-01") {
		t.Errorf("month bucket = %s", got)
	}
}

func TestFillSeries(t *testing.T) {
	r := domain.DateRange{From: day("2026-11-12"), To: day("2026-11-18")}
	pts := FillSeries(r, BucketDay, map[string]SeriesPoint{"2026-11-14": {Start: "2026-11-14", Total: "12.00", Count: 2}})
	if len(pts) != 7 {
		t.Fatalf("got %d points", len(pts))
	}
	if pts[2].Total != "12.00" || pts[0].Total != "0.00" || pts[6].Start != "2026-11-18" {
		t.Fatalf("points = %+v", pts)
	}

	weeks := FillSeries(domain.DateRange{From: day("2026-08-19"), To: day("2026-11-18")}, BucketWeek, nil)
	if weeks[0].Start != "2026-08-17" || weeks[len(weeks)-1].Start != "2026-11-16" {
		t.Fatalf("week buckets %s..%s", weeks[0].Start, weeks[len(weeks)-1].Start)
	}
}

func TestBuildResultSortsAndAllocates(t *testing.T) {
	s := &Service{Currency: "EUR"}
	d := domain.DomainSpendingCategory
	r := domain.DateRange{Code: domain.Range1Month, From: day("2026-10-19"), To: day("2026-11-18")}
	res := s.buildResult(d, r, []classStats{
		{domain.CatGroceries, domain.Stats{Count: 2, Total: decimal.RequireFromString("100"), Max: decimal.RequireFromString("60"), Min: decimal.RequireFromString("40")}},
		{domain.CatHousing, domain.Stats{Count: 1, Total: decimal.RequireFromString("300"), Max: decimal.RequireFromString("300"), Min: decimal.RequireFromString("300")}},
	})
	if res.Classes[0].Class != string(domain.CatHousing) {
		t.Fatalf("classes not sorted by total: %+v", res.Classes)
	}
	if res.Classes[0].Share != "75.00" || res.Classes[1].Share != "25.00" {
		t.Fatalf("shares = %s, %s", res.Classes[0].Share, res.Classes[1].Share)
	}
	if res.Overall.Total != "400.00" || res.Overall.Count != 3 || res.Overall.Mean != "133.33" || res.Overall.Max != "300.00" || res.Overall.Min != "40.00" {
		t.Fatalf("overall = %+v", res.Overall)
	}
	alloc := AllocationOf(res)
	if len(alloc.Items) != 2 || alloc.Total != "400.00" {
		t.Fatalf("allocation = %+v", alloc)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	id := "0b8f6a3e-7a52-4e8a-9a55-3f2b8d1c9e10"
	d, got, err := decodeCursor(encodeCursor("2026-11-02", id))
	if err != nil || got != id || d != day("2026-11-02") {
		t.Fatalf("cursor round trip: %s %s %v", d, got, err)
	}
	for _, bad := range []string{"%%%", encodeCursor("nope", id), encodeCursor("2026-11-02", "short")} {
		if _, _, err := decodeCursor(bad); err == nil {
			t.Errorf("cursor %q accepted", bad)
		}
	}
}
