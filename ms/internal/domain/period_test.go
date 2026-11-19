package domain

import "testing"

func TestBudgetPeriod(t *testing.T) {
	today := d("2026-11-18") // Wednesday
	cases := []struct {
		code     RangeCode
		from, to string
	}{
		{Range1Day, "2026-11-18", "2026-11-18"},
		{Range1Week, "2026-11-16", "2026-11-22"},
		{Range1Month, "2026-11-01", "2026-11-30"},
		{Range3Months, "2026-10-01", "2026-12-31"},
	}
	for _, c := range cases {
		p, err := BudgetPeriod(c.code, today, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if p.From.Format(DateLayout) != c.from || p.To.Format(DateLayout) != c.to {
			t.Errorf("%s: got %s, want %s..%s", c.code, p, c.from, c.to)
		}
	}

	// Fortnights are 14 days, start on a Monday and tile the calendar.
	p1, _ := BudgetPeriod(Range2Weeks, today, nil, nil)
	if p1.Days() != 14 || p1.From.Weekday().String() != "Monday" || !p1.Contains(today) {
		t.Fatalf("fortnight = %s", p1)
	}
	p2, _ := BudgetPeriod(Range2Weeks, p1.To.AddDate(0, 0, 1), nil, nil)
	if p2.From != p1.To.AddDate(0, 0, 1) {
		t.Fatalf("fortnights do not tile: %s then %s", p1, p2)
	}
	early, _ := BudgetPeriod(Range2Weeks, d("2023-12-31"), nil, nil)
	if !early.Contains(d("2023-12-31")) || early.Days() != 14 {
		t.Fatalf("fortnight before anchor = %s", early)
	}

	from, to := d("2026-12-01"), d("2026-12-24")
	if p, err := BudgetPeriod(RangeCustom, today, &from, &to); err != nil || p.From != from || p.To != to {
		t.Fatalf("custom = %s, %v", p, err)
	}
	if _, err := BudgetPeriod(RangeCustom, today, &to, &from); err == nil {
		t.Fatal("inverted custom budget accepted")
	}
}
