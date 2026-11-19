// Package analytics is the read side of PFF: metrics (Mean, Total, Max-Min)
// per metric domain and date range, allocation percentages per metric class,
// financial history and its detail. All numbers are computed here; the client
// only renders them.
package analytics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/cecinuga/pff/ms/internal/domain"
)

// Service answers analytical queries.
type Service struct {
	Pool     *pgxpool.Pool
	Currency string // reporting currency; metrics only aggregate this currency
}

// ---- DTOs ---------------------------------------------------------------------

// RangeDTO describes the resolved date range.
type RangeDTO struct {
	Code string `json:"code"`
	From string `json:"from"`
	To   string `json:"to"`
	Days int    `json:"days"`
}

// NewRangeDTO renders a DateRange.
func NewRangeDTO(r domain.DateRange) RangeDTO {
	return RangeDTO{Code: string(r.Code), From: r.From.Format(domain.DateLayout), To: r.To.Format(domain.DateLayout), Days: r.Days()}
}

// StatsDTO renders domain.Stats with amounts as fixed 2-decimal strings.
type StatsDTO struct {
	Count  int64  `json:"count"`
	Total  string `json:"total"`
	Mean   string `json:"mean"`
	Max    string `json:"max"`
	Min    string `json:"min"`
	Spread string `json:"max_min_spread"`
}

func newStatsDTO(s domain.Stats) StatsDTO {
	return StatsDTO{
		Count:  s.Count,
		Total:  s.Total.StringFixed(2),
		Mean:   s.Mean().StringFixed(2),
		Max:    s.Max.StringFixed(2),
		Min:    s.Min.StringFixed(2),
		Spread: s.Spread().StringFixed(2),
	}
}

// ClassMetrics are the metrics of one metric class.
type ClassMetrics struct {
	Class string   `json:"class"`
	Label string   `json:"label"`
	Stats StatsDTO `json:"stats"`
	// Share is the class's percentage of the domain total (allocation).
	Share string `json:"share"`
}

// MetricsResult is the answer for one domain and date range.
type MetricsResult struct {
	Domain      string         `json:"domain"`
	DomainLabel string         `json:"domain_label"`
	Range       RangeDTO       `json:"range"`
	Currency    string         `json:"currency"`
	Overall     StatsDTO       `json:"overall"`
	Classes     []ClassMetrics `json:"classes"`
}

// ---- metrics (GetOrSet over fact_metric) -------------------------------------

type classStats struct {
	class domain.MetricClass
	stats domain.Stats
}

// Metrics returns the metrics of a domain over a date range. Per-class results
// are cached in fact_metric and reused while the user's data version is
// unchanged.
func (s *Service) Metrics(ctx context.Context, userID string, d domain.MetricDomain, r domain.DateRange) (MetricsResult, error) {
	res, err := s.MetricsOf(ctx, userID, []domain.MetricDomain{d}, r)
	if err != nil {
		return MetricsResult{}, err
	}
	return res[0], nil
}

// MetricsOf returns the metrics of several domains over one date range in at
// most two round trips: one read of the cached aggregates and, for domains
// without a valid cache, one statement that computes and caches them all.
func (s *Service) MetricsOf(ctx context.Context, userID string, domains []domain.MetricDomain, r domain.DateRange) ([]MetricsResult, error) {
	names := make([]string, len(domains))
	for i, d := range domains {
		names[i] = string(d)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT m.metric_domain, m.metric_class, m.tx_count, m.total::text, m.max_amount::text, m.min_amount::text
		FROM fact_metric m JOIN app_user u ON u.id = m.user_id AND u.data_version = m.data_version
		WHERE m.user_id = $1 AND m.metric_domain = ANY($2) AND m.range_from = $3 AND m.range_to = $4 AND m.currency = $5`,
		userID, names, r.From, r.To, s.Currency)
	if err != nil {
		return nil, err
	}
	byDomain, err := collectClassStats(rows)
	if err != nil {
		return nil, err
	}

	// A domain is cached as a whole, so any row means its result is complete.
	// Empty ranges have no rows and are recomputed, which costs nothing.
	var missing []string
	for _, n := range names {
		if len(byDomain[n]) == 0 {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		computed, err := s.computeMetrics(ctx, userID, missing, r)
		if err != nil {
			return nil, err
		}
		maps.Copy(byDomain, computed)
	}

	out := make([]MetricsResult, len(domains))
	for i, d := range domains {
		out[i] = s.buildResult(d, r, byDomain[string(d)])
	}
	return out, nil
}

// computeMetrics aggregates the facts of the given domains and stores the
// result in fact_metric with a single INSERT … SELECT … RETURNING.
func (s *Service) computeMetrics(ctx context.Context, userID string, domains []string, r domain.DateRange) (map[string][]classStats, error) {
	var out map[string][]classStats
	// REPEATABLE READ: the data version and the facts come from one snapshot,
	// so a concurrent ingest can only make this result stale, never wrong for
	// the version it is stored under.
	err := pgx.BeginTxFunc(ctx, s.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		// Drop rows of older versions, including classes that no longer occur.
		if _, err := tx.Exec(ctx, `
			DELETE FROM fact_metric WHERE user_id = $1 AND metric_domain = ANY($2) AND range_from = $3 AND range_to = $4`,
			userID, domains, r.From, r.To); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			INSERT INTO fact_metric (user_id, metric_domain, range_from, range_to, metric_class, currency,
			                         tx_count, total, mean, max_amount, min_amount, data_version)
			SELECT $1::uuid, metric_domain, $3::date, $4::date, metric_class, $5::text,
			       count(*), sum(amount), round(avg(amount), 4), max(amount), min(amount),
			       (SELECT data_version FROM app_user WHERE id = $1::uuid)
			FROM fact_transaction
			WHERE user_id = $1::uuid AND metric_domain = ANY($2::text[]) AND booked_on BETWEEN $3::date AND $4::date
			  AND currency = $5::text
			GROUP BY metric_domain, metric_class
			ON CONFLICT (user_id, metric_domain, range_from, range_to, metric_class) DO UPDATE SET
				currency = EXCLUDED.currency, tx_count = EXCLUDED.tx_count, total = EXCLUDED.total,
				mean = EXCLUDED.mean, max_amount = EXCLUDED.max_amount, min_amount = EXCLUDED.min_amount,
				data_version = EXCLUDED.data_version, computed_at = now()
			RETURNING metric_domain, metric_class, tx_count, total::text, max_amount::text, min_amount::text`,
			userID, domains, r.From, r.To, s.Currency)
		if err != nil {
			return err
		}
		out, err = collectClassStats(rows)
		return err
	})
	// Two concurrent computations may collide on the cache write; recompute
	// read-only in that case rather than failing the request.
	if err != nil && isSerializationFailure(err) {
		rows, qerr := s.Pool.Query(ctx, `
			SELECT metric_domain, metric_class, count(*), sum(amount)::text, max(amount)::text, min(amount)::text
			FROM fact_transaction
			WHERE user_id = $1 AND metric_domain = ANY($2) AND booked_on BETWEEN $3 AND $4 AND currency = $5
			GROUP BY metric_domain, metric_class`, userID, domains, r.From, r.To, s.Currency)
		if qerr != nil {
			return nil, qerr
		}
		return collectClassStats(rows)
	}
	return out, err
}

func isSerializationFailure(err error) bool {
	return strings.Contains(err.Error(), "SQLSTATE 40001") || strings.Contains(err.Error(), "SQLSTATE 23505")
}

// collectClassStats reads (domain, class, count, total, max, min) rows,
// grouped by domain.
func collectClassStats(rows pgx.Rows) (map[string][]classStats, error) {
	out := map[string][]classStats{}
	defer rows.Close()
	for rows.Next() {
		cs, d, err := scanClassStats(rows)
		if err != nil {
			return nil, err
		}
		out[d] = append(out[d], cs)
	}
	return out, rows.Err()
}

func scanClassStats(r pgx.Rows) (classStats, string, error) {
	var cs classStats
	var d, class, total, max, min string
	if err := r.Scan(&d, &class, &cs.stats.Count, &total, &max, &min); err != nil {
		return cs, d, err
	}
	cs.class = domain.MetricClass(class)
	var err error
	if cs.stats.Total, err = decimal.NewFromString(total); err != nil {
		return cs, d, err
	}
	if cs.stats.Max, err = decimal.NewFromString(max); err != nil {
		return cs, d, err
	}
	cs.stats.Min, err = decimal.NewFromString(min)
	return cs, d, err
}

func (s *Service) buildResult(d domain.MetricDomain, r domain.DateRange, parts []classStats) MetricsResult {
	sort.Slice(parts, func(i, j int) bool {
		if c := parts[i].stats.Total.Cmp(parts[j].stats.Total); c != 0 {
			return c > 0
		}
		return parts[i].class < parts[j].class
	})
	totals := make([]decimal.Decimal, len(parts))
	all := make([]domain.Stats, len(parts))
	for i, p := range parts {
		totals[i] = p.stats.Total
		all[i] = p.stats
	}
	shares := domain.Allocate(totals)
	res := MetricsResult{
		Domain:      string(d),
		DomainLabel: d.Info().Label,
		Range:       NewRangeDTO(r),
		Currency:    s.Currency,
		Overall:     newStatsDTO(domain.Combine(all...)),
		Classes:     make([]ClassMetrics, len(parts)),
	}
	for i, p := range parts {
		res.Classes[i] = ClassMetrics{
			Class: string(p.class),
			Label: d.ClassLabel(p.class),
			Stats: newStatsDTO(p.stats),
			Share: shares[i].StringFixed(2),
		}
	}
	return res
}

// ---- allocation -------------------------------------------------------------

// AllocationItem is one slice of the allocation of a domain.
type AllocationItem struct {
	Class      string `json:"class"`
	Label      string `json:"label"`
	Total      string `json:"total"`
	Percentage string `json:"percentage"`
}

// Allocation is the split of a domain's total by metric class.
type Allocation struct {
	Domain      string           `json:"domain"`
	DomainLabel string           `json:"domain_label"`
	Range       RangeDTO         `json:"range"`
	Currency    string           `json:"currency"`
	Total       string           `json:"total"`
	Items       []AllocationItem `json:"items"`
}

// AllocationOf derives the allocation from a metrics result.
func AllocationOf(m MetricsResult) Allocation {
	a := Allocation{Domain: m.Domain, DomainLabel: m.DomainLabel, Range: m.Range, Currency: m.Currency, Total: m.Overall.Total, Items: []AllocationItem{}}
	for _, c := range m.Classes {
		a.Items = append(a.Items, AllocationItem{Class: c.Class, Label: c.Label, Total: c.Stats.Total, Percentage: c.Share})
	}
	return a
}

// ---- history ------------------------------------------------------------------

// HistoryEntry is one transaction within a domain.
type HistoryEntry struct {
	ID           string `json:"id"`
	Date         string `json:"date"`
	Class        string `json:"class"`
	ClassLabel   string `json:"class_label"`
	Amount       string `json:"amount"`
	Currency     string `json:"currency"`
	Direction    string `json:"direction"`
	Counterparty string `json:"counterparty"`
	Description  string `json:"description"`
	AccountName  string `json:"account_name"`
}

// HistoryPage is a page of history entries, newest first.
type HistoryPage struct {
	Domain     string         `json:"domain"`
	Range      RangeDTO       `json:"range"`
	Entries    []HistoryEntry `json:"entries"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// ErrBadCursor is returned for a malformed pagination cursor.
var ErrBadCursor = errors.New("invalid cursor")

// History lists the transactions of a domain in a date range, optionally
// filtered by class, with keyset pagination.
func (s *Service) History(ctx context.Context, userID string, d domain.MetricDomain, r domain.DateRange, class domain.MetricClass, cursor string, limit int) (HistoryPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args := []any{userID, string(d), r.From, r.To}
	where := `f.user_id = $1 AND f.metric_domain = $2 AND f.booked_on BETWEEN $3 AND $4`
	if class != "" {
		args = append(args, string(class))
		where += fmt.Sprintf(` AND f.metric_class = $%d`, len(args))
	}
	if cursor != "" {
		cDate, cID, err := decodeCursor(cursor)
		if err != nil {
			return HistoryPage{}, err
		}
		args = append(args, cDate, cID)
		where += fmt.Sprintf(` AND (f.booked_on, f.transaction_id) < ($%d, $%d::uuid)`, len(args)-1, len(args))
	}
	args = append(args, limit+1)
	rows, err := s.Pool.Query(ctx, `
		SELECT f.transaction_id::text, f.booked_on, f.metric_class, round(f.amount, 2)::text, f.currency, t.direction,
		       COALESCE(t.counterparty_name, ''), COALESCE(t.remittance, ''), COALESCE(a.name, '')
		FROM fact_transaction f
		JOIN bank_transaction t ON t.id = f.transaction_id AND t.booked_on = f.booked_on
		LEFT JOIN dim_account a ON a.id = f.account_id
		WHERE `+where+`
		ORDER BY f.booked_on DESC, f.transaction_id DESC
		LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return HistoryPage{}, err
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (HistoryEntry, error) {
		var e HistoryEntry
		var day time.Time
		err := row.Scan(&e.ID, &day, &e.Class, &e.Amount, &e.Currency, &e.Direction, &e.Counterparty, &e.Description, &e.AccountName)
		e.Date = day.Format(domain.DateLayout)
		e.ClassLabel = d.ClassLabel(domain.MetricClass(e.Class))
		return e, err
	})
	if err != nil {
		return HistoryPage{}, err
	}
	page := HistoryPage{Domain: string(d), Range: NewRangeDTO(r), Entries: entries}
	if len(entries) > limit {
		page.Entries = entries[:limit]
		last := page.Entries[limit-1]
		page.NextCursor = encodeCursor(last.Date, last.ID)
	}
	if page.Entries == nil {
		page.Entries = []HistoryEntry{}
	}
	return page, nil
}

func encodeCursor(date, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(date + "|" + id))
}

func decodeCursor(c string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", ErrBadCursor
	}
	date, id, ok := strings.Cut(string(raw), "|")
	if !ok || len(id) != 36 {
		return time.Time{}, "", ErrBadCursor
	}
	t, err := domain.ParseDate(date)
	if err != nil {
		return time.Time{}, "", ErrBadCursor
	}
	return t, id, nil
}

// ---- series -------------------------------------------------------------------

// Bucket sizes for time series.
const (
	BucketDay   = "day"
	BucketWeek  = "week"
	BucketMonth = "month"
)

// SeriesPoint is the total of one time bucket.
type SeriesPoint struct {
	Start string `json:"start"`
	Total string `json:"total"`
	Count int64  `json:"count"`
}

// Series is a gap-filled time series of a domain's totals.
type Series struct {
	Domain   string        `json:"domain"`
	Range    RangeDTO      `json:"range"`
	Currency string        `json:"currency"`
	Bucket   string        `json:"bucket"`
	Points   []SeriesPoint `json:"points"`
}

// AutoBucket picks a bucket size giving a readable number of points.
func AutoBucket(r domain.DateRange) string {
	switch n := r.Days(); {
	case n <= 31:
		return BucketDay
	case n <= 120:
		return BucketWeek
	default:
		return BucketMonth
	}
}

// BucketStart aligns a day to its bucket, matching TimescaleDB's time_bucket
// origins (weeks start on Monday, months on the 1st).
func BucketStart(day time.Time, bucket string) time.Time {
	switch bucket {
	case BucketWeek:
		offset := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -offset)
	case BucketMonth:
		return time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return day
}

func nextBucket(t time.Time, bucket string) time.Time {
	switch bucket {
	case BucketWeek:
		return t.AddDate(0, 0, 7)
	case BucketMonth:
		return t.AddDate(0, 1, 0)
	}
	return t.AddDate(0, 0, 1)
}

// FillSeries returns one point per bucket from the bucket of r.From to the
// bucket of r.To, using totals where present and zero elsewhere.
func FillSeries(r domain.DateRange, bucket string, totals map[string]SeriesPoint) []SeriesPoint {
	var out []SeriesPoint
	for b := BucketStart(r.From, bucket); !b.After(r.To); b = nextBucket(b, bucket) {
		key := b.Format(domain.DateLayout)
		p, ok := totals[key]
		if !ok {
			p = SeriesPoint{Start: key, Total: "0.00"}
		}
		out = append(out, p)
	}
	return out
}

// SeriesOf returns the totals of a domain per bucket over the range.
func (s *Service) SeriesOf(ctx context.Context, userID string, d domain.MetricDomain, r domain.DateRange, class domain.MetricClass, bucket string) (Series, error) {
	interval := map[string]string{BucketDay: "1 day", BucketWeek: "1 week", BucketMonth: "1 month"}[bucket]
	if interval == "" {
		return Series{}, fmt.Errorf("unknown bucket %q", bucket)
	}
	args := []any{interval, userID, string(d), r.From, r.To, s.Currency}
	where := `user_id = $2 AND metric_domain = $3 AND booked_on BETWEEN $4 AND $5 AND currency = $6`
	if class != "" {
		args = append(args, string(class))
		where += ` AND metric_class = $7`
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT time_bucket($1::interval, booked_on) AS b, round(sum(amount), 2)::text, count(*)
		FROM fact_transaction WHERE `+where+`
		GROUP BY b ORDER BY b`, args...)
	if err != nil {
		return Series{}, err
	}
	defer rows.Close()
	totals := map[string]SeriesPoint{}
	for rows.Next() {
		var b time.Time
		var p SeriesPoint
		if err := rows.Scan(&b, &p.Total, &p.Count); err != nil {
			return Series{}, err
		}
		p.Start = b.Format(domain.DateLayout)
		totals[p.Start] = p
	}
	if err := rows.Err(); err != nil {
		return Series{}, err
	}
	return Series{Domain: string(d), Range: NewRangeDTO(r), Currency: s.Currency, Bucket: bucket, Points: FillSeries(r, bucket, totals)}, nil
}

// ---- entry detail -------------------------------------------------------------

// Membership is a domain/class a transaction belongs to.
type Membership struct {
	Domain      string `json:"domain"`
	DomainLabel string `json:"domain_label"`
	Class       string `json:"class"`
	ClassLabel  string `json:"class_label"`
}

// EntryDetail is everything PFF knows about one transaction.
type EntryDetail struct {
	ID                string          `json:"id"`
	BookingDate       string          `json:"booking_date"`
	ValueDate         *string         `json:"value_date"`
	TransactionDate   *string         `json:"transaction_date"`
	Amount            string          `json:"amount"`
	Currency          string          `json:"currency"`
	Direction         string          `json:"direction"`
	Counterparty      string          `json:"counterparty"`
	CounterpartyIBAN  string          `json:"counterparty_iban"`
	Description       string          `json:"description"`
	EntryReference    string          `json:"entry_reference"`
	BankTxCode        string          `json:"bank_tx_code"`
	BankTxSubCode     string          `json:"bank_tx_sub_code"`
	BankTxDescription string          `json:"bank_tx_description"`
	MCC               string          `json:"mcc"`
	BalanceAfter      *string         `json:"balance_after"`
	AccountName       string          `json:"account_name"`
	AccountIBAN       string          `json:"account_iban"`
	FetchedAt         time.Time       `json:"fetched_at"`
	CategoryOverride  string          `json:"category_override"`
	Memberships       []Membership    `json:"memberships"`
	Raw               json.RawMessage `json:"raw"`
}

// ErrNotFound is returned for unknown transactions.
var ErrNotFound = errors.New("not found")

// Entry returns the full detail of one transaction of the user.
func (s *Service) Entry(ctx context.Context, userID, id string) (EntryDetail, error) {
	var e EntryDetail
	var booked time.Time
	var valueDate, txDate *time.Time
	var raw []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT t.id::text, t.booked_on, t.value_date, t.transaction_date, round(t.amount, 2)::text, t.currency, t.direction,
		       COALESCE(t.counterparty_name, ''), COALESCE(t.counterparty_iban, ''), COALESCE(t.remittance, ''),
		       COALESCE(t.entry_reference, ''), COALESCE(t.bank_tx_code, ''), COALESCE(t.bank_tx_sub_code, ''),
		       COALESCE(t.bank_tx_description, ''), COALESCE(t.mcc, ''), round(t.balance_after, 2)::text,
		       COALESCE(a.name, ''), COALESCE(a.iban, ''), t.fetched_at, COALESCE(o.metric_class, ''), t.raw
		FROM bank_transaction t
		LEFT JOIN dim_account a ON a.id = t.account_id
		LEFT JOIN transaction_category_override o
		       ON o.account_id = t.account_id AND o.booked_on = t.booked_on AND o.fingerprint = t.fingerprint
		WHERE t.id::text = $1 AND t.user_id = $2`, id, userID).Scan(
		&e.ID, &booked, &valueDate, &txDate, &e.Amount, &e.Currency, &e.Direction, &e.Counterparty, &e.CounterpartyIBAN,
		&e.Description, &e.EntryReference, &e.BankTxCode, &e.BankTxSubCode, &e.BankTxDescription, &e.MCC, &e.BalanceAfter,
		&e.AccountName, &e.AccountIBAN, &e.FetchedAt, &e.CategoryOverride, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	e.BookingDate = booked.Format(domain.DateLayout)
	if valueDate != nil {
		v := valueDate.Format(domain.DateLayout)
		e.ValueDate = &v
	}
	if txDate != nil {
		v := txDate.Format(domain.DateLayout)
		e.TransactionDate = &v
	}
	if len(raw) > 0 {
		e.Raw = raw
	}

	rows, err := s.Pool.Query(ctx, `
		SELECT metric_domain, metric_class FROM fact_transaction
		WHERE transaction_id::text = $1 AND booked_on = $2 AND user_id = $3`, id, booked, userID)
	if err != nil {
		return e, err
	}
	e.Memberships, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Membership, error) {
		var m Membership
		err := r.Scan(&m.Domain, &m.Class)
		d := domain.MetricDomain(m.Domain)
		m.DomainLabel = d.Info().Label
		m.ClassLabel = d.ClassLabel(domain.MetricClass(m.Class))
		return m, err
	})
	if e.Memberships == nil {
		e.Memberships = []Membership{}
	}
	return e, err
}
