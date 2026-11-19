package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func d(s string) time.Time {
	t, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return t
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestResolveRangePresets(t *testing.T) {
	today := d("2026-11-18")
	cases := []struct {
		code, end string
		from, to  string
	}{
		{"1d", "", "2026-11-18", "2026-11-18"},
		{"1w", "", "2026-11-12", "2026-11-18"},
		{"2w", "", "2026-11-05", "2026-11-18"},
		{"1m", "", "2026-10-19", "2026-11-18"},
		{"3m", "", "2026-08-19", "2026-11-18"},
		{"1m", "2026-03-31", "2026-03-01", "2026-03-31"},
		{"", "", "2026-10-19", "2026-11-18"},             // default is 1 month
		{"1w", "2030-01-01", "2026-11-12", "2026-11-18"}, // end clamped to today
	}
	for _, c := range cases {
		r, err := ResolveRange(RangeRequest{Code: c.code, End: c.end}, today)
		if err != nil {
			t.Fatalf("%s/%s: %v", c.code, c.end, err)
		}
		if got := r.From.Format(DateLayout); got != c.from {
			t.Errorf("%s/%s from = %s, want %s", c.code, c.end, got, c.from)
		}
		if got := r.To.Format(DateLayout); got != c.to {
			t.Errorf("%s/%s to = %s, want %s", c.code, c.end, got, c.to)
		}
	}
}

func TestResolveRangeWeekSpansSevenDays(t *testing.T) {
	r, err := ResolveRange(RangeRequest{Code: "1w"}, d("2026-11-18"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Days() != 7 {
		t.Fatalf("1w spans %d days", r.Days())
	}
}

func TestResolveRangeCustom(t *testing.T) {
	today := d("2026-11-18")
	r, err := ResolveRange(RangeRequest{Code: "custom", From: "2026-01-01", To: "2026-12-31"}, today)
	if err != nil {
		t.Fatal(err)
	}
	if r.To != today {
		t.Errorf("custom to should clamp to today, got %s", r.To)
	}

	bad := []RangeRequest{
		{Code: "custom"},
		{Code: "custom", From: "2026-02-01", To: "2026-01-01"},
		{Code: "custom", From: "2027-01-01", To: "2027-02-01"},
		{Code: "custom", From: "2015-01-01", To: "2026-01-01"},
		{Code: "custom", From: "nope", To: "2026-01-01"},
		{Code: "5y"},
		{Code: "1w", End: "yesterday"},
	}
	for _, b := range bad {
		if _, err := ResolveRange(b, today); !errors.Is(err, ErrInvalidRange) {
			t.Errorf("%+v: expected ErrInvalidRange, got %v", b, err)
		}
	}
}

func TestStatsMeanAndSpread(t *testing.T) {
	s := Stats{Count: 3, Total: dec("10.00"), Max: dec("5.00"), Min: dec("1.00")}
	if got := s.Mean(); !got.Equal(dec("3.33")) {
		t.Errorf("mean = %s", got)
	}
	if got := s.Spread(); !got.Equal(dec("4.00")) {
		t.Errorf("spread = %s", got)
	}
	var empty Stats
	if !empty.Mean().IsZero() || !empty.Spread().IsZero() {
		t.Error("empty stats must be zero")
	}
}

func TestCombine(t *testing.T) {
	a := Stats{Count: 2, Total: dec("30"), Max: dec("20"), Min: dec("10")}
	b := Stats{Count: 1, Total: dec("5"), Max: dec("5"), Min: dec("5")}
	c := Combine(a, Stats{}, b)
	if c.Count != 3 || !c.Total.Equal(dec("35")) || !c.Max.Equal(dec("20")) || !c.Min.Equal(dec("5")) {
		t.Fatalf("combine = %+v", c)
	}
	if !c.Mean().Equal(dec("11.67")) {
		t.Errorf("combined mean = %s, want exact 35/3", c.Mean())
	}
}

func TestAllocateSumsTo100(t *testing.T) {
	cases := [][]string{
		{"1", "1", "1"},
		{"10", "20", "30.5", "0.01"},
		{"99.99", "0.01"},
		{"5", "0", "-3", "5"},
	}
	for _, c := range cases {
		in := make([]decimal.Decimal, len(c))
		for i, s := range c {
			in[i] = dec(s)
		}
		out := Allocate(in)
		sum := decimal.Zero
		for i, p := range out {
			sum = sum.Add(p)
			if !in[i].IsPositive() && !p.IsZero() {
				t.Errorf("%v: non-positive input %d got %s", c, i, p)
			}
		}
		if !sum.Equal(dec("100")) {
			t.Errorf("%v: percentages sum to %s (%v)", c, sum, out)
		}
	}
	thirds := Allocate([]decimal.Decimal{dec("1"), dec("1"), dec("1")})
	if !thirds[0].Equal(dec("33.34")) || !thirds[1].Equal(dec("33.33")) {
		t.Errorf("thirds = %v", thirds)
	}
	zero := Allocate([]decimal.Decimal{dec("0"), dec("0")})
	if !zero[0].IsZero() || !zero[1].IsZero() {
		t.Errorf("all-zero input must allocate zeros, got %v", zero)
	}
}

func TestPercent(t *testing.T) {
	if got := Percent(dec("80"), dec("200")); !got.Equal(dec("40")) {
		t.Errorf("percent = %s", got)
	}
	if got := Percent(dec("1"), dec("0")); !got.IsZero() {
		t.Errorf("percent of zero whole = %s", got)
	}
}

func TestCatalog(t *testing.T) {
	for _, info := range Catalog {
		seen := map[MetricClass]bool{}
		for _, c := range info.Classes {
			if seen[c.Class] {
				t.Errorf("%s: duplicate class %s", info.Domain, c.Class)
			}
			seen[c.Class] = true
		}
		if _, err := ParseDomain(string(info.Domain)); err != nil {
			t.Error(err)
		}
	}
	if _, err := ParseDomain("savings"); err == nil {
		t.Error("unknown domain must fail")
	}
	if !DomainSpendingCategory.HasClass(CatGroceries) || DomainIncome.HasClass(CatGroceries) {
		t.Error("HasClass mismatch")
	}
}
