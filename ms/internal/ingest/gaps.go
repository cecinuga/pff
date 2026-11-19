package ingest

import (
	"time"

	"github.com/cecinuga/pff/ms/internal/warehouse"
)

// GapPolicy decides when cached days can be served without calling the bank.
type GapPolicy struct {
	// FreshTTL: a day fetched less than FreshTTL ago is served from cache even
	// if it is not final yet (today, recent days still receiving bookings).
	FreshTTL time.Duration
	// SettlementDays: a day is final, and never refetched, once it was fetched
	// after the end of day+SettlementDays. Banks book card payments a few days
	// late, so recent days stay open for a while.
	SettlementDays int
	// MaxBridgeDays: two gaps separated by at most this many cached days are
	// fetched in one bank call, trading a little refetching for fewer calls.
	MaxBridgeDays int
	// Location is the user's time zone, used to decide when a day has ended.
	Location *time.Location
}

// Satisfied reports whether a day can be served from cache.
func (p GapPolicy) Satisfied(day, lastFetched, now time.Time) bool {
	if lastFetched.IsZero() {
		return false
	}
	y, m, d := day.Date()
	finalAt := time.Date(y, m, d+1+p.SettlementDays, 0, 0, 0, 0, p.Location)
	if !lastFetched.Before(finalAt) {
		return true
	}
	return now.Sub(lastFetched) < p.FreshTTL
}

// ComputeGaps returns the windows that must be fetched from the bank, given
// each day of the requested range and when it was last fetched (zero = never).
// days must be consecutive and ascending.
func ComputeGaps(days, lastFetched []time.Time, now time.Time, p GapPolicy) []warehouse.Window {
	var gaps []warehouse.Window
	for i, day := range days {
		if p.Satisfied(day, lastFetched[i], now) {
			continue
		}
		if n := len(gaps); n > 0 {
			last := &gaps[n-1]
			// Days strictly between the previous gap and this day are cached.
			cachedBetween := int(day.Sub(last.To).Hours()/24) - 1
			if cachedBetween <= p.MaxBridgeDays {
				last.To = day
				continue
			}
		}
		gaps = append(gaps, warehouse.Window{From: day, To: day})
	}
	return gaps
}
