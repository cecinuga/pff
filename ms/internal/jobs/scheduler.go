// Package jobs runs PFF's periodic background work: consent expiry handling,
// background bank refreshes (within the PSD2 daily limit), budget evaluation
// and housekeeping.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cecinuga/pff/ms/internal/budget"
	"github.com/cecinuga/pff/ms/internal/ingest"
	"github.com/cecinuga/pff/ms/internal/notify"
)

// Scheduler runs Tick every Interval until its context ends.
type Scheduler struct {
	Pool     *pgxpool.Pool
	Ingest   *ingest.Service
	Budgets  *budget.Service
	Notifier *notify.Service
	Interval time.Duration
	Log      *slog.Logger
}

// Run blocks until ctx is cancelled. The first tick runs shortly after start.
func (s *Scheduler) Run(ctx context.Context) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.Tick(ctx)
			timer.Reset(s.Interval)
		}
	}
}

// Tick performs one round of background work.
func (s *Scheduler) Tick(ctx context.Context) {
	start := time.Now()
	s.expireConsents(ctx)
	s.refreshAndEvaluate(ctx)
	s.housekeeping(ctx)
	s.Log.Info("scheduler tick done", "took", time.Since(start).Round(time.Millisecond))
}

// expireConsents marks ended consents and warns users a week before expiry.
func (s *Scheduler) expireConsents(ctx context.Context) {
	rows, err := s.Pool.Query(ctx, `
		UPDATE bank_connection SET status = 'expired', updated_at = now()
		WHERE status = 'active' AND valid_until IS NOT NULL AND valid_until < now()
		RETURNING user_id::text, aspsp_name`)
	if err != nil {
		s.Log.Error("expire consents", "err", err)
		return
	}
	type ref struct{ user, bank string }
	expired, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ref, error) {
		var x ref
		return x, r.Scan(&x.user, &x.bank)
	})
	if err != nil {
		s.Log.Error("expire consents", "err", err)
	}
	for _, e := range expired {
		_ = s.Notifier.Notify(ctx, e.user, notify.Notification{
			Kind:  "consent_expired",
			Title: "Bank connection expired",
			Body:  fmt.Sprintf("Your consent for %s has expired. Reconnect it to keep your data up to date.", e.bank),
			Link:  "/connections",
		})
	}

	rows, err = s.Pool.Query(ctx, `
		UPDATE bank_connection SET expiry_notified = TRUE, updated_at = now()
		WHERE status = 'active' AND NOT expiry_notified AND valid_until < now() + interval '7 days'
		RETURNING user_id::text, aspsp_name`)
	if err != nil {
		s.Log.Error("consent expiry warnings", "err", err)
		return
	}
	expiring, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ref, error) {
		var x ref
		return x, r.Scan(&x.user, &x.bank)
	})
	if err != nil {
		s.Log.Error("consent expiry warnings", "err", err)
	}
	for _, e := range expiring {
		_ = s.Notifier.Notify(ctx, e.user, notify.Notification{
			Kind:  "consent_expiring",
			Title: "Bank connection expiring soon",
			Body:  fmt.Sprintf("Your consent for %s expires within 7 days. Reconnect it to avoid gaps in your data.", e.bank),
			Link:  "/connections",
		})
	}
}

// refreshAndEvaluate refreshes recent data without the user present (bounded
// by the daily background limit) and evaluates budgets.
func (s *Scheduler) refreshAndEvaluate(ctx context.Context) {
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT u.id::text FROM app_user u
		WHERE EXISTS (SELECT 1 FROM bank_connection c WHERE c.user_id = u.id AND c.status = 'active')
		   OR EXISTS (SELECT 1 FROM budget b WHERE b.user_id = u.id AND b.enabled)`)
	if err != nil {
		s.Log.Error("list users", "err", err)
		return
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.Log.Error("list users", "err", err)
		return
	}
	for _, userID := range users {
		if ctx.Err() != nil {
			return
		}
		today := s.Ingest.Today()
		from := today.AddDate(0, 0, -31)
		if earliest, err := s.Budgets.EarliestPeriodStart(ctx, userID); err == nil && !earliest.IsZero() && earliest.Before(from) {
			from = earliest
		}
		fr := s.Ingest.EnsureRange(ctx, userID, from, today, nil)
		if !fr.Complete {
			s.Log.Info("background refresh incomplete", "user", userID, "warnings", fr.Warnings)
		}
		s.Budgets.EvaluateUser(ctx, userID)
	}
}

func (s *Scheduler) housekeeping(ctx context.Context) {
	stmts := []string{
		// Aggregates computed at an outdated data version can never be served again.
		`DELETE FROM fact_metric m USING app_user u WHERE u.id = m.user_id AND m.data_version < u.data_version`,
		`DELETE FROM mfa_email_challenge WHERE created_at < now() - interval '1 day'`,
		`DELETE FROM mfa_session WHERE verified_at < now() - interval '60 days'`,
		`DELETE FROM bank_fetch_log WHERE day < current_date - 30`,
		`DELETE FROM bank_connection WHERE status IN ('pending', 'failed') AND created_at < now() - interval '7 days'
		   AND NOT EXISTS (SELECT 1 FROM dim_account a WHERE a.connection_id = bank_connection.id)`,
		`DELETE FROM notification WHERE created_at < now() - interval '180 days'`,
	}
	for _, q := range stmts {
		if _, err := s.Pool.Exec(ctx, q); err != nil {
			s.Log.Error("housekeeping", "err", err)
		}
	}
}
