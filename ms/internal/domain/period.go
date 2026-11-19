package domain

import (
	"fmt"
	"time"
)

// fortnightAnchor is the Monday that starts fortnight number zero.
var fortnightAnchor = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// BudgetPeriod returns the calendar-aligned period containing today for a
// budget's date range. Unlike the rolling analysis windows, budget periods
// reset on fixed boundaries, so "1 month" means the current calendar month and
// an alert fires at most once per period:
//
//	1d: today · 1w: ISO week (Mon–Sun) · 2w: fortnight starting on a Monday
//	1m: calendar month · 3m: calendar quarter · custom: the fixed dates.
//
// The returned To may lie in the future (the end of the current period).
func BudgetPeriod(code RangeCode, today time.Time, customFrom, customTo *time.Time) (DateRange, error) {
	today = Civil(today)
	switch code {
	case Range1Day:
		return DateRange{Code: code, From: today, To: today}, nil
	case Range1Week:
		from := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
		return DateRange{Code: code, From: from, To: from.AddDate(0, 0, 6)}, nil
	case Range2Weeks:
		days := int(today.Sub(fortnightAnchor).Hours() / 24)
		idx := days / 14
		if days < 0 && days%14 != 0 {
			idx--
		}
		from := fortnightAnchor.AddDate(0, 0, idx*14)
		return DateRange{Code: code, From: from, To: from.AddDate(0, 0, 13)}, nil
	case Range1Month:
		from := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
		return DateRange{Code: code, From: from, To: from.AddDate(0, 1, -1)}, nil
	case Range3Months:
		q := (int(today.Month()) - 1) / 3
		from := time.Date(today.Year(), time.Month(q*3+1), 1, 0, 0, 0, 0, time.UTC)
		return DateRange{Code: code, From: from, To: from.AddDate(0, 3, -1)}, nil
	case RangeCustom:
		if customFrom == nil || customTo == nil || customFrom.After(*customTo) {
			return DateRange{}, fmt.Errorf("%w: custom budget needs from <= to", ErrInvalidRange)
		}
		return DateRange{Code: code, From: Civil(*customFrom), To: Civil(*customTo)}, nil
	}
	return DateRange{}, fmt.Errorf("%w: unknown range %q", ErrInvalidRange, code)
}
