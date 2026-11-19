// Package budget implements budgets with push notifications per metric domain
// and date range (FunctionalSpec feature 4). A budget caps the total of a
// metric domain (optionally one metric class) over a calendar-aligned period;
// when spending crosses the warning threshold or the limit, the user is
// notified once per period and level.
package budget

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/cecinuga/pff/ms/internal/analytics"
	"github.com/cecinuga/pff/ms/internal/domain"
	"github.com/cecinuga/pff/ms/internal/notify"
)

// Budget states.
const (
	StateOK       = "ok"
	StateWarning  = "warning"
	StateExceeded = "exceeded"
)

// Input is a budget as submitted by the client.
type Input struct {
	Name        string `json:"name"`
	Domain      string `json:"domain"`
	Class       string `json:"class"`
	Range       string `json:"range"`
	CustomFrom  string `json:"custom_from"`
	CustomTo    string `json:"custom_to"`
	Limit       string `json:"limit"`
	WarnPercent int    `json:"warn_percent"`
	Enabled     *bool  `json:"enabled"`
}

// Budget is a stored budget with its status in the current period.
type Budget struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Domain      string             `json:"domain"`
	DomainLabel string             `json:"domain_label"`
	Class       string             `json:"class"`
	ClassLabel  string             `json:"class_label"`
	Range       string             `json:"range"`
	CustomFrom  string             `json:"custom_from,omitempty"`
	CustomTo    string             `json:"custom_to,omitempty"`
	Limit       string             `json:"limit"`
	Currency    string             `json:"currency"`
	WarnPercent int                `json:"warn_percent"`
	Enabled     bool               `json:"enabled"`
	Period      analytics.RangeDTO `json:"period"`
	Spent       string             `json:"spent"`
	Remaining   string             `json:"remaining"`
	Percentage  string             `json:"percentage"`
	State       string             `json:"state"`

	customFrom, customTo *time.Time
	limit                decimal.Decimal
}

// ValidationError explains why a budget input was rejected.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// ErrNotFound is returned for unknown budgets.
var ErrNotFound = errors.New("budget not found")

// Service manages budgets.
type Service struct {
	Pool     *pgxpool.Pool
	Notifier *notify.Service
	Currency string
	Location *time.Location
	Log      *slog.Logger
	Now      func() time.Time

	evalMu sync.Map // userID -> *sync.Mutex, serialises evaluations per user
}

func (s *Service) today() time.Time { return domain.Civil(s.Now().In(s.Location)) }

type validated struct {
	name      string
	domain    domain.MetricDomain
	class     *string
	rangeCode domain.RangeCode
	from, to  *time.Time
	limit     decimal.Decimal
	warn      int
	enabled   bool
}

func validate(in Input) (validated, error) {
	var v validated
	v.name = strings.TrimSpace(in.Name)
	if v.name == "" || len(v.name) > 80 {
		return v, ValidationError{"name must be 1 to 80 characters"}
	}
	d, err := domain.ParseDomain(in.Domain)
	if err != nil {
		return v, ValidationError{err.Error()}
	}
	v.domain = d
	if in.Class != "" {
		if !d.HasClass(domain.MetricClass(in.Class)) {
			return v, ValidationError{fmt.Sprintf("class %q does not belong to domain %q", in.Class, in.Domain)}
		}
		c := in.Class
		v.class = &c
	}
	v.rangeCode = domain.RangeCode(in.Range)
	if v.rangeCode == domain.RangeCustom {
		f, err1 := domain.ParseDate(in.CustomFrom)
		t, err2 := domain.ParseDate(in.CustomTo)
		if err1 != nil || err2 != nil || f.After(t) {
			return v, ValidationError{"custom budgets need custom_from <= custom_to (YYYY-MM-DD)"}
		}
		v.from, v.to = &f, &t
	} else if _, err := domain.BudgetPeriod(v.rangeCode, time.Now(), nil, nil); err != nil {
		return v, ValidationError{"range must be one of 1d, 1w, 2w, 1m, 3m, custom"}
	}
	v.limit, err = decimal.NewFromString(strings.TrimSpace(in.Limit))
	if err != nil || !v.limit.IsPositive() || v.limit.Exponent() < -2 || v.limit.GreaterThan(decimal.NewFromInt(1_000_000_000)) {
		return v, ValidationError{"limit must be a positive amount with at most 2 decimals"}
	}
	v.warn = in.WarnPercent
	if v.warn == 0 {
		v.warn = 80
	}
	if v.warn < 1 || v.warn > 100 {
		return v, ValidationError{"warn_percent must be between 1 and 100"}
	}
	v.enabled = in.Enabled == nil || *in.Enabled
	return v, nil
}

// Create stores a new budget.
func (s *Service) Create(ctx context.Context, userID string, in Input) (Budget, error) {
	v, err := validate(in)
	if err != nil {
		return Budget{}, err
	}
	var id string
	err = s.Pool.QueryRow(ctx, `
		INSERT INTO budget (user_id, name, metric_domain, metric_class, range_code, custom_from, custom_to,
		                    limit_amount, currency, warn_percent, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9, $10, $11) RETURNING id::text`,
		userID, v.name, string(v.domain), v.class, string(v.rangeCode), v.from, v.to, v.limit.String(), s.Currency, v.warn, v.enabled).Scan(&id)
	if err != nil {
		return Budget{}, err
	}
	return s.Get(ctx, userID, id)
}

// Update replaces a budget's settings. Alerts already sent for the current
// period are cleared so the new thresholds take effect immediately.
func (s *Service) Update(ctx context.Context, userID, id string, in Input) (Budget, error) {
	v, err := validate(in)
	if err != nil {
		return Budget{}, err
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE budget SET name = $3, metric_domain = $4, metric_class = $5, range_code = $6, custom_from = $7,
			       custom_to = $8, limit_amount = $9::numeric, warn_percent = $10, enabled = $11, updated_at = now()
			WHERE id::text = $1 AND user_id = $2`,
			id, userID, v.name, string(v.domain), v.class, string(v.rangeCode), v.from, v.to, v.limit.String(), v.warn, v.enabled)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `DELETE FROM budget_alert WHERE budget_id::text = $1`, id)
		return err
	})
	if err != nil {
		return Budget{}, err
	}
	return s.Get(ctx, userID, id)
}

// Delete removes a budget.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM budget WHERE id::text = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns one budget with its current status.
func (s *Service) Get(ctx context.Context, userID, id string) (Budget, error) {
	list, err := s.load(ctx, userID, &id)
	if err != nil {
		return Budget{}, err
	}
	if len(list) == 0 {
		return Budget{}, ErrNotFound
	}
	return list[0], nil
}

// List returns all budgets of the user with their current status.
func (s *Service) List(ctx context.Context, userID string) ([]Budget, error) {
	return s.load(ctx, userID, nil)
}

// EarliestPeriodStart is the earliest day any of the user's budgets needs data
// for (zero if the user has no budgets). Callers use it to refresh the cache.
func (s *Service) EarliestPeriodStart(ctx context.Context, userID string) (time.Time, error) {
	list, err := s.budgetsWithPeriods(ctx, userID, nil)
	if err != nil || len(list) == 0 {
		return time.Time{}, err
	}
	earliest := s.today()
	for _, b := range list {
		if f, _ := domain.ParseDate(b.Period.From); f.Before(earliest) {
			earliest = f
		}
	}
	return earliest, nil
}

// load returns budgets with their status; spending for all of them comes from
// one aggregate query.
func (s *Service) load(ctx context.Context, userID string, id *string) ([]Budget, error) {
	list, err := s.budgetsWithPeriods(ctx, userID, id)
	if err != nil || len(list) == 0 {
		return list, err
	}

	// One row per budget: its domain/class filter and the elapsed part of its
	// period (from the start to today; future periods sum to zero).
	today := s.today()
	n := len(list)
	idx, doms, classes, curs := make([]int32, n), make([]string, n), make([]string, n), make([]string, n)
	froms, tos := make([]time.Time, n), make([]time.Time, n)
	for i, b := range list {
		idx[i], doms[i], classes[i], curs[i] = int32(i), b.Domain, b.Class, b.Currency
		froms[i], _ = domain.ParseDate(b.Period.From)
		tos[i], _ = domain.ParseDate(b.Period.To)
		if tos[i].After(today) {
			tos[i] = today
		}
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT b.idx, COALESCE(sum(f.amount), 0)::text
		FROM unnest($2::int[], $3::text[], $4::text[], $5::date[], $6::date[], $7::text[])
		     AS b(idx, metric_domain, metric_class, d_from, d_to, currency)
		LEFT JOIN fact_transaction f
		       ON f.user_id = $1 AND f.metric_domain = b.metric_domain
		      AND (b.metric_class = '' OR f.metric_class = b.metric_class)
		      AND f.booked_on BETWEEN b.d_from AND b.d_to AND f.currency = b.currency
		GROUP BY b.idx`, userID, idx, doms, classes, froms, tos, curs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i int32
		var total string
		if err := rows.Scan(&i, &total); err != nil {
			return nil, err
		}
		spent, err := decimal.NewFromString(total)
		if err != nil {
			return nil, err
		}
		b := &list[i]
		pct := domain.Percent(spent, b.limit)
		b.Spent = spent.StringFixed(2)
		b.Remaining = decimal.Max(b.limit.Sub(spent), decimal.Zero).StringFixed(2)
		b.Percentage = pct.StringFixed(2)
		b.State = stateFor(pct, b.WarnPercent)
	}
	return list, rows.Err()
}

// budgetsWithPeriods reads budgets and resolves their current period and
// labels, without computing spending.
func (s *Service) budgetsWithPeriods(ctx context.Context, userID string, id *string) ([]Budget, error) {
	q := `SELECT id::text, name, metric_domain, COALESCE(metric_class, ''), range_code, custom_from, custom_to,
	             limit_amount::text, currency, warn_percent, enabled
	      FROM budget WHERE user_id = $1`
	args := []any{userID}
	if id != nil {
		q += ` AND id::text = $2`
		args = append(args, *id)
	}
	rows, err := s.Pool.Query(ctx, q+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	today := s.today()
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Budget, error) {
		var b Budget
		var limit string
		err := r.Scan(&b.ID, &b.Name, &b.Domain, &b.Class, &b.Range, &b.customFrom, &b.customTo, &limit, &b.Currency, &b.WarnPercent, &b.Enabled)
		if err != nil {
			return b, err
		}
		if b.limit, err = decimal.NewFromString(limit); err != nil {
			return b, err
		}
		d := domain.MetricDomain(b.Domain)
		b.DomainLabel = d.Info().Label
		if b.Class != "" {
			b.ClassLabel = d.ClassLabel(domain.MetricClass(b.Class))
		}
		if b.customFrom != nil {
			b.CustomFrom = b.customFrom.Format(domain.DateLayout)
			b.CustomTo = b.customTo.Format(domain.DateLayout)
		}
		period, err := domain.BudgetPeriod(domain.RangeCode(b.Range), today, b.customFrom, b.customTo)
		if err != nil {
			return b, err
		}
		b.Period = analytics.NewRangeDTO(period)
		b.Limit = b.limit.StringFixed(2)
		b.Spent, b.Remaining, b.Percentage, b.State = "0.00", b.Limit, "0.00", StateOK
		return b, nil
	})
	if list == nil {
		list = []Budget{}
	}
	return list, err
}

func stateFor(pct decimal.Decimal, warn int) string {
	switch {
	case pct.GreaterThanOrEqual(decimal.NewFromInt(100)):
		return StateExceeded
	case pct.GreaterThanOrEqual(decimal.NewFromInt(int64(warn))):
		return StateWarning
	}
	return StateOK
}

// EvaluateUser checks every enabled budget of the user and sends at most one
// notification per budget, period and level.
func (s *Service) EvaluateUser(ctx context.Context, userID string) {
	m, _ := s.evalMu.LoadOrStore(userID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	list, err := s.List(ctx, userID)
	if err != nil {
		s.Log.Error("evaluate budgets", "user", userID, "err", err)
		return
	}
	for _, b := range list {
		if !b.Enabled || b.State == StateOK {
			continue
		}
		level := b.State // "warning" or "exceeded"
		periodFrom, err := domain.ParseDate(b.Period.From)
		if err != nil {
			continue
		}
		tag, err := s.Pool.Exec(ctx, `
			INSERT INTO budget_alert (budget_id, period_from, level) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, b.ID, periodFrom, level)
		if err != nil {
			s.Log.Error("record budget alert", "err", err)
			continue
		}
		if tag.RowsAffected() == 0 {
			continue // already notified for this period and level
		}
		if err := s.Notifier.Notify(ctx, userID, message(b)); err != nil {
			s.Log.Error("notify budget", "err", err)
		}
	}
}

func message(b Budget) notify.Notification {
	scope := b.DomainLabel
	if b.ClassLabel != "" {
		scope += " · " + b.ClassLabel
	}
	n := notify.Notification{Link: "/budgets"}
	if b.State == StateExceeded {
		n.Kind = "budget_exceeded"
		n.Title = fmt.Sprintf("Budget exceeded: %s", b.Name)
	} else {
		n.Kind = "budget_warning"
		n.Title = fmt.Sprintf("Budget at %s%%: %s", strings.TrimSuffix(strings.TrimSuffix(b.Percentage, "0"), ".0"), b.Name)
	}
	n.Body = fmt.Sprintf("%s: %s of %s %s spent (%s to %s).", scope, b.Spent, b.Limit, b.Currency, b.Period.From, b.Period.To)
	return n
}
