// Package ingest implements the GetOrSet persistent cache of the
// TechnicalSpec: before any read, the backend checks which days of the
// requested period are already stored, fetches only the missing or stale days
// from the bank, stores them, and then serves everything from the database.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cecinuga/pff/ms/internal/bank"
	"github.com/cecinuga/pff/ms/internal/domain"
	"github.com/cecinuga/pff/ms/internal/warehouse"
)

// Freshness tells the client whether the data it got is complete.
type Freshness struct {
	Complete bool     `json:"complete"`
	Warnings []string `json:"warnings"`
}

func (f *Freshness) warn(format string, args ...any) {
	f.Complete = false
	f.Warnings = append(f.Warnings, fmt.Sprintf(format, args...))
}

// Service orchestrates bank fetches into the warehouse.
type Service struct {
	Store              *warehouse.Store
	Provider           bank.Provider
	Policy             GapPolicy
	BackgroundDailyMax int
	BackfillDays       int
	MaxHistoryDays     int
	LockWait           time.Duration
	Log                *slog.Logger
	Now                func() time.Time
	// OnChange is called (asynchronously) after a user's data changed.
	OnChange func(userID string)

	locks keyedLocks
}

// Today is the current civil date in the configured time zone.
func (s *Service) Today() time.Time {
	return domain.Civil(s.Now().In(s.Policy.Location))
}

// EnsureRange makes sure every account of the user has data for [from, to],
// fetching from the bank only what is missing or stale. psu is nil for
// background jobs (the user is not present). Bank failures never fail the
// call: cached data is served and the problem is reported in Freshness.
func (s *Service) EnsureRange(ctx context.Context, userID string, from, to time.Time, psu *bank.PSU) Freshness {
	fr := Freshness{Complete: true, Warnings: []string{}}
	today := s.Today()
	if to.After(today) {
		to = today
	}
	if earliest := today.AddDate(0, 0, -s.MaxHistoryDays); from.Before(earliest) {
		from = earliest
	}
	if from.After(to) {
		return fr
	}
	w := warehouse.Window{From: from, To: to}

	accounts, err := s.Store.Accounts(ctx, userID)
	if err != nil {
		s.Log.Error("load accounts", "err", err)
		fr.warn("could not check bank data, showing stored data")
		return fr
	}
	changed := false
	for _, acc := range accounts {
		ch, err := s.syncAccount(ctx, acc, w, psu)
		changed = changed || ch
		if err != nil {
			fr.warn("%s: %s", accountLabel(acc), err.Error())
		}
	}
	if changed && s.OnChange != nil {
		go s.OnChange(userID)
	}
	return fr
}

// userError is a sync problem worth showing to the user.
type userError string

func (e userError) Error() string { return string(e) }

func (s *Service) syncAccount(ctx context.Context, acc warehouse.Account, w warehouse.Window, psu *bank.PSU) (bool, error) {
	unlock, err := s.locks.lock(ctx, acc.ID, s.LockWait)
	if err != nil {
		return false, userError("synchronisation already in progress, showing stored data")
	}
	defer unlock()

	days, fetched, err := s.Store.Coverage(ctx, acc.ID, w)
	if err != nil {
		s.Log.Error("load coverage", "account", acc.ID, "err", err)
		return false, userError("could not check stored data")
	}
	now := s.Now()
	gaps := ComputeGaps(days, fetched, now, s.Policy)
	if len(gaps) == 0 {
		return false, nil
	}

	if acc.ConnectionStatus != "active" || acc.Provider != s.Provider.Name() || acc.ProviderUID == "" {
		return false, userError("bank consent is not active, reconnect the bank to update data")
	}
	if acc.ValidUntil != nil && now.After(*acc.ValidUntil) {
		return false, userError("bank consent expired, reconnect the bank to update data")
	}

	psu = effectivePSU(psu, acc.RequiredPSUHeaders)
	background := psu == nil
	today := s.Today()

	changed := false
	for _, g := range gaps {
		if background {
			n, err := s.Store.BackgroundFetches(ctx, acc.ID, today)
			if err != nil {
				return changed, userError("could not check fetch budget")
			}
			if n >= s.BackgroundDailyMax {
				return changed, userError("daily bank access limit reached, data will refresh when you open the app")
			}
		}
		ch, err := s.fetchWindow(ctx, acc, g, bank.StrategyDefault, psu)
		changed = changed || ch
		if err != nil {
			return changed, err
		}
	}

	// Refresh the balance alongside today's data when the user is present
	// (it costs one bank call, so background jobs skip it).
	if !background && gaps[len(gaps)-1].To.Equal(today) {
		if bals, err := s.Provider.Balances(ctx, acc.ProviderUID, psu); err == nil {
			if err := s.Store.UpdateBalance(ctx, acc.ID, bals); err != nil {
				s.Log.Error("update balance", "account", acc.ID, "err", err)
			}
		} else {
			s.Log.Warn("fetch balances", "account", acc.ID, "err", err)
		}
	}
	return changed, nil
}

// fetchWindow fetches one window from the bank and stores it.
func (s *Service) fetchWindow(ctx context.Context, acc warehouse.Account, g warehouse.Window, strategy bank.TxStrategy, psu *bank.PSU) (bool, error) {
	syncTime := s.Now()
	if err := s.Store.CountFetch(ctx, acc.ID, s.Today(), psu == nil); err != nil {
		s.Log.Error("count fetch", "err", err)
	}
	txs, err := s.Provider.Transactions(ctx, acc.ProviderUID, bank.TxQuery{From: g.From, To: g.To, Strategy: strategy}, psu)
	switch {
	case errors.Is(err, bank.ErrPeriodUnavailable):
		// The bank cannot serve this period (often: older than 90 days). Mark
		// it covered so PFF stops asking, but keep anything cached earlier.
		s.Log.Info("period unavailable at bank", "account", acc.ID, "window", g.String())
		if err := s.Store.SaveFetch(ctx, acc, g, nil, syncTime, false); err != nil {
			s.Log.Error("save coverage", "err", err)
		}
		return false, userError("the bank does not provide transactions for " + g.String())
	case errors.Is(err, bank.ErrSessionExpired):
		s.markConnection(ctx, acc, "expired")
		return false, userError("bank consent expired, reconnect the bank to update data")
	case errors.Is(err, bank.ErrRateLimited):
		return false, userError("the bank is rate-limiting requests, showing stored data")
	case err != nil:
		s.Log.Warn("fetch transactions", "account", acc.ID, "window", g.String(), "err", err)
		return false, userError("the bank is temporarily unavailable, showing stored data")
	}
	replaceFrom := g.From
	if strategy == bank.StrategyLongest {
		// "longest" may silently return less history than asked for. Only the
		// part the bank actually served is authoritative; older cached data
		// (e.g. from a previous consent) must survive a reconnect.
		replaceFrom = g.To.AddDate(0, 0, 1)
		for _, t := range txs {
			if d, ok := t.EffectiveDate(); ok && d.Before(replaceFrom) {
				replaceFrom = d
			}
		}
		if replaceFrom.After(g.From) {
			head := warehouse.Window{From: g.From, To: replaceFrom.AddDate(0, 0, -1)}
			if err := s.Store.SaveFetch(ctx, acc, head, nil, syncTime, false); err != nil {
				s.Log.Error("save coverage", "account", acc.ID, "err", err)
				return false, userError("could not store bank data")
			}
		}
		if replaceFrom.After(g.To) {
			return false, nil
		}
	}
	if err := s.Store.SaveFetch(ctx, acc, warehouse.Window{From: replaceFrom, To: g.To}, txs, syncTime, true); err != nil {
		s.Log.Error("save fetch", "account", acc.ID, "err", err)
		return false, userError("could not store bank data")
	}
	s.Log.Info("fetched transactions", "account", acc.ID, "window", g.String(), "count", len(txs), "background", psu == nil)
	return true, nil
}

// Backfill loads the longest available history for freshly connected accounts.
// Banks often expose full history only shortly after consent, so this runs
// right after the user returns from SCA.
func (s *Service) Backfill(ctx context.Context, userID string, accountIDs []string, psu *bank.PSU) {
	accounts, err := s.Store.Accounts(ctx, userID)
	if err != nil {
		s.Log.Error("backfill: load accounts", "err", err)
		return
	}
	wanted := map[string]bool{}
	for _, id := range accountIDs {
		wanted[id] = true
	}
	today := s.Today()
	w := warehouse.Window{From: today.AddDate(0, 0, -s.BackfillDays), To: today}
	changed := false
	for _, acc := range accounts {
		if !wanted[acc.ID] {
			continue
		}
		unlock, err := s.locks.lock(ctx, acc.ID, 10*time.Minute)
		if err != nil {
			continue
		}
		p := effectivePSU(psu, acc.RequiredPSUHeaders)
		ch, err := s.fetchWindow(ctx, acc, w, bank.StrategyLongest, p)
		if err == nil && p != nil {
			if bals, berr := s.Provider.Balances(ctx, acc.ProviderUID, p); berr == nil {
				_ = s.Store.UpdateBalance(ctx, acc.ID, bals)
			}
		}
		unlock()
		changed = changed || ch
		if err != nil {
			s.Log.Warn("backfill", "account", acc.ID, "err", err)
		}
	}
	if changed && s.OnChange != nil {
		s.OnChange(userID)
	}
}

func (s *Service) markConnection(ctx context.Context, acc warehouse.Account, status string) {
	if acc.ConnectionID == "" {
		return
	}
	if _, err := s.Store.Pool.Exec(ctx,
		`UPDATE bank_connection SET status = $2, updated_at = now() WHERE id = $1 AND status = 'active'`,
		acc.ConnectionID, status); err != nil {
		s.Log.Error("mark connection", "err", err)
	}
}

// effectivePSU returns psu if it can supply every header the bank requires,
// otherwise nil. Enable Banking rejects requests that send only some of an
// ASPSP's required PSU headers, so it is all or nothing.
func effectivePSU(psu *bank.PSU, required []string) *bank.PSU {
	if psu == nil {
		return nil
	}
	have := map[string]bool{
		"psu-ip-address":      psu.IPAddress != "",
		"psu-user-agent":      psu.UserAgent != "",
		"psu-referer":         psu.Referer != "",
		"psu-accept":          psu.Accept != "",
		"psu-accept-charset":  psu.AcceptCharset != "",
		"psu-accept-encoding": psu.AcceptEncoding != "",
		"psu-accept-language": psu.AcceptLanguage != "",
	}
	for _, h := range required {
		if !have[strings.ToLower(h)] {
			return nil
		}
	}
	return psu
}

func accountLabel(a warehouse.Account) string {
	if a.Name != "" {
		return a.Name
	}
	if n := len(a.IBAN); n > 4 {
		return "account …" + a.IBAN[n-4:]
	}
	return "account"
}

// ---- per-account locks with a bounded wait --------------------------------

type keyedLocks struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

func (k *keyedLocks) lock(ctx context.Context, key string, wait time.Duration) (func(), error) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]chan struct{}{}
	}
	ch, ok := k.m[key]
	if !ok {
		ch = make(chan struct{}, 1)
		k.m[key] = ch
	}
	k.mu.Unlock()

	if wait <= 0 {
		wait = 30 * time.Second
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		return nil, errors.New("lock wait timeout")
	}
}
