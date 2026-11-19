package domain

import (
	"errors"
	"fmt"
	"time"
)

// RangeCode is one of the canonical date ranges of the FunctionalSpec:
// 1 day, 1 week, 2 weeks, 1 month, 3 months, custom.
type RangeCode string

const (
	Range1Day    RangeCode = "1d"
	Range1Week   RangeCode = "1w"
	Range2Weeks  RangeCode = "2w"
	Range1Month  RangeCode = "1m"
	Range3Months RangeCode = "3m"
	RangeCustom  RangeCode = "custom"
)

// RangeInfo describes a date range for the API.
type RangeInfo struct {
	Code  RangeCode `json:"code"`
	Label string    `json:"label"`
}

// Ranges lists every supported date range in display order.
var Ranges = []RangeInfo{
	{Range1Day, "1 day"},
	{Range1Week, "1 week"},
	{Range2Weeks, "2 weeks"},
	{Range1Month, "1 month"},
	{Range3Months, "3 months"},
	{RangeCustom, "Custom"},
}

// MaxCustomSpan bounds custom ranges so a single request cannot ask the bank
// for an unbounded history.
const MaxCustomSpan = 5 * 366 * 24 * time.Hour

// DateLayout is the wire format of calendar dates.
const DateLayout = "2006-01-02"

// DateRange is an inclusive interval of calendar days. From and To are always
// midnight UTC values representing a civil date (no time-of-day meaning).
type DateRange struct {
	Code RangeCode `json:"code"`
	From time.Time `json:"-"`
	To   time.Time `json:"-"`
}

// Days returns the number of calendar days in the range (inclusive).
func (r DateRange) Days() int {
	return int(r.To.Sub(r.From).Hours()/24) + 1
}

// Contains reports whether the civil date d is within the range.
func (r DateRange) Contains(d time.Time) bool {
	d = Civil(d)
	return !d.Before(r.From) && !d.After(r.To)
}

// String renders the range for logs and errors.
func (r DateRange) String() string {
	return fmt.Sprintf("%s[%s..%s]", r.Code, r.From.Format(DateLayout), r.To.Format(DateLayout))
}

// Civil truncates t to its calendar date, expressed as midnight UTC. The
// calendar date is read in t's own location, so callers should pass a time
// already converted to the user's time zone.
func Civil(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ParseDate parses a YYYY-MM-DD string into a civil date.
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(DateLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q, expected YYYY-MM-DD", s)
	}
	return t, nil
}

// RangeRequest carries the raw query parameters that select a date range.
type RangeRequest struct {
	Code string // required: one of the RangeCode values
	From string // custom only
	To   string // custom only
	End  string // optional for preset ranges: last day of the window (default today)
}

// ErrInvalidRange is returned for any malformed range request.
var ErrInvalidRange = errors.New("invalid date range")

// ResolveRange turns a RangeRequest into concrete calendar days.
//
// Preset ranges are rolling windows that end on End (default: today) and span
// exactly the named duration, e.g. "1w" ending on a Sunday covers Monday..Sunday.
// Custom ranges use From and To verbatim. Windows never extend past today,
// because the bank cannot report future bookings.
func ResolveRange(req RangeRequest, today time.Time) (DateRange, error) {
	today = Civil(today)
	code := RangeCode(req.Code)
	if code == "" {
		code = Range1Month
	}

	if code == RangeCustom {
		if req.From == "" || req.To == "" {
			return DateRange{}, fmt.Errorf("%w: custom range requires from and to", ErrInvalidRange)
		}
		from, err := ParseDate(req.From)
		if err != nil {
			return DateRange{}, fmt.Errorf("%w: %v", ErrInvalidRange, err)
		}
		to, err := ParseDate(req.To)
		if err != nil {
			return DateRange{}, fmt.Errorf("%w: %v", ErrInvalidRange, err)
		}
		if from.After(to) {
			return DateRange{}, fmt.Errorf("%w: from must not be after to", ErrInvalidRange)
		}
		if from.After(today) {
			return DateRange{}, fmt.Errorf("%w: range starts in the future", ErrInvalidRange)
		}
		if to.After(today) {
			to = today
		}
		if to.Sub(from) > MaxCustomSpan {
			return DateRange{}, fmt.Errorf("%w: custom range is longer than 5 years", ErrInvalidRange)
		}
		return DateRange{Code: RangeCustom, From: from, To: to}, nil
	}

	end := today
	if req.End != "" {
		e, err := ParseDate(req.End)
		if err != nil {
			return DateRange{}, fmt.Errorf("%w: %v", ErrInvalidRange, err)
		}
		if e.After(today) {
			e = today
		}
		end = e
	}

	var from time.Time
	switch code {
	case Range1Day:
		from = end
	case Range1Week:
		from = end.AddDate(0, 0, -6)
	case Range2Weeks:
		from = end.AddDate(0, 0, -13)
	case Range1Month:
		from = subMonthsClamped(end, 1).AddDate(0, 0, 1)
	case Range3Months:
		from = subMonthsClamped(end, 3).AddDate(0, 0, 1)
	default:
		return DateRange{}, fmt.Errorf("%w: unknown range %q", ErrInvalidRange, req.Code)
	}
	return DateRange{Code: code, From: from, To: end}, nil
}

// subMonthsClamped moves a civil date back n months, clamping the day to the
// last day of the target month (31 March minus one month is 28/29 February, not
// 3 March as time.AddDate would normalise it).
func subMonthsClamped(t time.Time, n int) time.Time {
	y, m, day := t.Date()
	firstOfTarget := time.Date(y, m-time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	lastDay := firstOfTarget.AddDate(0, 1, -1).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(firstOfTarget.Year(), firstOfTarget.Month(), day, 0, 0, 0, 0, time.UTC)
}
