package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// Stats holds the three FunctionalSpec metrics (Mean, Total, Max-Min) for a set
// of transactions, plus the transaction count they were computed from. Amounts
// are absolute values (an outflow of 12.50 is stored as 12.50).
type Stats struct {
	Count int64
	Total decimal.Decimal
	Max   decimal.Decimal
	Min   decimal.Decimal
}

// Mean is the arithmetic mean of the transaction amounts, rounded half-away
// from zero to cents. It is zero for an empty set.
func (s Stats) Mean() decimal.Decimal {
	if s.Count == 0 {
		return decimal.Zero
	}
	return s.Total.DivRound(decimal.NewFromInt(s.Count), 2)
}

// Spread is Max minus Min, i.e. the width of the amount range.
func (s Stats) Spread() decimal.Decimal {
	if s.Count == 0 {
		return decimal.Zero
	}
	return s.Max.Sub(s.Min)
}

// Combine merges per-class stats into the stats of their union. Total and Count
// add up, Max and Min take the extremes, and Mean is recomputed from the merged
// Total and Count, so it is exact rather than an average of averages.
func Combine(parts ...Stats) Stats {
	var out Stats
	for _, p := range parts {
		if p.Count == 0 {
			continue
		}
		if out.Count == 0 {
			out = p
			continue
		}
		out.Count += p.Count
		out.Total = out.Total.Add(p.Total)
		if p.Max.GreaterThan(out.Max) {
			out.Max = p.Max
		}
		if p.Min.LessThan(out.Min) {
			out.Min = p.Min
		}
	}
	return out
}

// Allocate converts per-class totals into percentages of their sum, rounded to
// two decimals with the largest-remainder method so the result always adds up
// to exactly 100.00 (or is all zeros when the sum is zero). Negative totals are
// treated as zero.
func Allocate(totals []decimal.Decimal) []decimal.Decimal {
	out := make([]decimal.Decimal, len(totals))
	sum := decimal.Zero
	for _, t := range totals {
		if t.IsPositive() {
			sum = sum.Add(t)
		}
	}
	for i := range out {
		out[i] = decimal.Zero
	}
	if !sum.IsPositive() {
		return out
	}

	// Work in basis points (1/100 of a percent): 100% = 10000 bp.
	const totalBP = 10000
	type rem struct {
		idx  int
		frac decimal.Decimal
	}
	floors := make([]int64, len(totals))
	rems := make([]rem, 0, len(totals))
	var assigned int64
	for i, t := range totals {
		if !t.IsPositive() {
			continue
		}
		exact := t.Mul(decimal.NewFromInt(totalBP)).Div(sum)
		fl := exact.Floor()
		floors[i] = fl.IntPart()
		assigned += floors[i]
		rems = append(rems, rem{idx: i, frac: exact.Sub(fl)})
	}
	// Hand the leftover basis points to the largest remainders; ties go to the
	// earlier index so the output is deterministic.
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac.GreaterThan(rems[b].frac) })
	for k := int64(0); k < totalBP-assigned && int(k) < len(rems); k++ {
		floors[rems[k].idx]++
	}
	for i, bp := range floors {
		out[i] = decimal.New(bp, -2)
	}
	return out
}

// Percent returns part/whole as a percentage rounded to two decimals, or zero
// when whole is not positive. It is used for budget consumption.
func Percent(part, whole decimal.Decimal) decimal.Decimal {
	if !whole.IsPositive() {
		return decimal.Zero
	}
	return part.Mul(decimal.NewFromInt(100)).DivRound(whole, 2)
}
