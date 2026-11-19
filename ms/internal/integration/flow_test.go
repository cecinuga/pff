//go:build integration

// Package integration exercises PFF end to end against a real TimescaleDB:
// migrations, the GetOrSet cache, the star schema, analytics, budgets, e-mail
// MFA and the HTTP layer. Run with:
//
//	PFF_TEST_DATABASE_URL=postgres://pff:pff-dev-password@127.0.0.1:5432/pff?sslmode=disable \
//	  go test -tags integration -count=1 ./internal/integration/...
//
// Each run creates and drops its own database.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cecinuga/pff/ms/internal/analytics"
	"github.com/cecinuga/pff/ms/internal/auth"
	"github.com/cecinuga/pff/ms/internal/bank"
	"github.com/cecinuga/pff/ms/internal/bank/mock"
	"github.com/cecinuga/pff/ms/internal/budget"
	"github.com/cecinuga/pff/ms/internal/config"
	"github.com/cecinuga/pff/ms/internal/connect"
	"github.com/cecinuga/pff/ms/internal/domain"
	"github.com/cecinuga/pff/ms/internal/httpapi"
	"github.com/cecinuga/pff/ms/internal/ingest"
	"github.com/cecinuga/pff/ms/internal/notify"
	"github.com/cecinuga/pff/ms/internal/platform/db"
	"github.com/cecinuga/pff/ms/internal/platform/secretbox"
	"github.com/cecinuga/pff/ms/internal/warehouse"
)

// countingProvider records every Transactions call so tests can assert when
// the bank is (not) contacted.
type countingProvider struct {
	bank.Provider
	calls   atomic.Int32
	mu      sync.Mutex
	windows []string
}

func (c *countingProvider) Transactions(ctx context.Context, uid string, q bank.TxQuery, psu *bank.PSU) ([]bank.Transaction, error) {
	c.calls.Add(1)
	c.mu.Lock()
	c.windows = append(c.windows, fmt.Sprintf("%s %s..%s %s", uid, q.From.Format("2006-01-02"), q.To.Format("2006-01-02"), q.Strategy))
	c.mu.Unlock()
	return c.Provider.Transactions(ctx, uid, q, psu)
}

type captureMailer struct {
	mu   sync.Mutex
	last string
}

func (m *captureMailer) Send(_ context.Context, _, _, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last = body
	return nil
}

func freshDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("PFF_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("PFF_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	name := fmt.Sprintf("pff_it_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	u, _ := url.Parse(base)
	u.Path = "/" + name
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := db.Open(ctx, u.String(), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	if err := db.Migrate(ctx, pool, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Migrations are idempotent.
	if err := db.Migrate(ctx, pool, log); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	return pool
}

func TestEndToEnd(t *testing.T) {
	pool := freshDatabase(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	loc, _ := time.LoadLocation("Europe/Rome")
	store := &warehouse.Store{Pool: pool}
	if err := store.SyncCatalog(ctx); err != nil {
		t.Fatalf("sync catalog: %v", err)
	}
	if err := store.SyncCatalog(ctx); err != nil { // SCD1 upsert is repeatable
		t.Fatalf("re-sync catalog: %v", err)
	}

	// ---- whitelist -------------------------------------------------------------
	dir := t.TempDir()
	wl := filepath.Join(dir, "allowed.txt")
	_ = os.WriteFile(wl, []byte("alice@example.com\nbob@example.com\n"), 0o600)
	if n, err := auth.SyncWhitelistFile(ctx, pool, wl); err != nil || n != 2 {
		t.Fatalf("whitelist sync: %d %v", n, err)
	}
	_ = os.WriteFile(wl, []byte("alice@example.com\n"), 0o600)
	if _, err := auth.SyncWhitelistFile(ctx, pool, wl); err != nil {
		t.Fatal(err)
	}
	users := auth.PGStore{Pool: pool}
	if _, ok, _, err := users.Authorize(ctx, auth.Identity{UID: "uid-bob", Email: "bob@example.com"}); ok || err != nil {
		t.Fatalf("bob removed from the file must be disabled (err %v)", err)
	}
	id := auth.Identity{UID: "uid-alice", Email: "alice@example.com", EmailVerified: true, SignInProvider: "google.com", AuthTime: time.Now().UTC().Truncate(time.Second)}
	user, ok, _, err := users.Authorize(ctx, id) // first sign-in creates the user
	if err != nil || !ok || user.ID == "" {
		t.Fatalf("alice must be whitelisted and created: %+v %v %v", user, ok, err)
	}
	if again, _, _, _ := users.Authorize(ctx, id); again.ID != user.ID {
		t.Fatalf("second sign-in created another user: %s vs %s", again.ID, user.ID)
	}

	// ---- services ------------------------------------------------------------------
	box, _ := secretbox.New("ZGV2LW9ubHktc2VjcmV0LWtleS0zMi1ieXRlcyEhISE=")
	provider := &countingProvider{Provider: mock.New(loc)}
	notifier := &notify.Service{Pool: pool, Pusher: notify.LogPusher{Log: log}, Log: log}
	budgets := &budget.Service{Pool: pool, Notifier: notifier, Currency: "EUR", Location: loc, Log: log, Now: time.Now}
	now := time.Now()
	clock := func() time.Time { return now }
	ing := &ingest.Service{
		Store: store, Provider: provider,
		Policy:             ingest.GapPolicy{FreshTTL: time.Hour, SettlementDays: 3, MaxBridgeDays: 7, Location: loc},
		BackgroundDailyMax: 4, BackfillDays: 120, MaxHistoryDays: 1000, LockWait: 30 * time.Second,
		Log: log, Now: func() time.Time { return clock() },
	}
	conn := &connect.Service{Store: store, Ingest: ing, Provider: provider, Box: box,
		RedirectURL: "http://localhost:5173/connections/callback", Banks: []config.BankRef{{Country: "IT", Name: mock.ASPSPName}}, PSUType: "personal", ConsentDays: 90, Log: log, Now: time.Now}
	an := &analytics.Service{Pool: pool, Currency: "EUR"}
	psu := &bank.PSU{IPAddress: "203.0.113.9", UserAgent: "integration-test"}

	// ---- connect the bank ---------------------------------------------------------
	redirect, err := conn.Start(ctx, user.ID, connect.StartRequest{ASPSPName: mock.ASPSPName, ASPSPCountry: "IT"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	ru, _ := url.Parse(redirect)
	if _, err := conn.Complete(ctx, user.ID, connect.CompleteRequest{Code: ru.Query().Get("code"), State: "forged"}, psu); !errors.Is(err, connect.ErrBadState) {
		t.Fatalf("forged state accepted: %v", err)
	}
	connID, err := conn.Complete(ctx, user.ID, connect.CompleteRequest{Code: ru.Query().Get("code"), State: ru.Query().Get("state")}, psu)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := conn.Complete(ctx, user.ID, connect.CompleteRequest{Code: ru.Query().Get("code"), State: ru.Query().Get("state")}, psu); !errors.Is(err, connect.ErrBadState) {
		t.Fatalf("state reused: %v", err)
	}
	var sealed []byte
	_ = pool.QueryRow(ctx, `SELECT session_enc FROM bank_connection WHERE id = $1`, connID).Scan(&sealed)
	if strings.Contains(string(sealed), "session-") {
		t.Fatal("bank session id stored in clear text")
	}

	// The backfill runs in the background: wait for both accounts' coverage.
	waitFor(t, 20*time.Second, func() bool {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(DISTINCT account_id) FROM sync_coverage`).Scan(&n)
		return n == 2 && provider.calls.Load() >= 2
	})
	time.Sleep(200 * time.Millisecond) // let the backfill goroutine finish its last write
	afterBackfill := provider.calls.Load()
	if afterBackfill != 2 {
		t.Fatalf("backfill made %d bank calls, want 2 (one per account): %v", afterBackfill, provider.windows)
	}
	var txCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM bank_transaction WHERE user_id = $1`, user.ID).Scan(&txCount)
	if txCount < 50 {
		t.Fatalf("only %d transactions after a 120-day backfill", txCount)
	}

	// ---- GetOrSet: cached ranges do not hit the bank ------------------------------
	today := ing.Today()
	fr := ing.EnsureRange(ctx, user.ID, today.AddDate(0, 0, -60), today, psu)
	if !fr.Complete || provider.calls.Load() != afterBackfill {
		t.Fatalf("cached range refetched: calls %d -> %d, freshness %+v", afterBackfill, provider.calls.Load(), fr)
	}

	// Two hours later only the not-yet-final recent days are refetched.
	now = now.Add(2 * time.Hour)
	ing.EnsureRange(ctx, user.ID, today.AddDate(0, 0, -60), today, psu)
	if got := provider.calls.Load() - afterBackfill; got != 2 {
		t.Fatalf("stale refresh made %d calls, want 2: %v", got, provider.windows)
	}
	for _, w := range provider.windows[2:] {
		// SettlementDays = 3: days today-3..today are still open; older ones are final.
		if !strings.Contains(w, today.AddDate(0, 0, -3).Format("2006-01-02")+".."+today.Format("2006-01-02")) {
			t.Fatalf("stale refresh fetched the wrong window: %s", w)
		}
	}
	// Re-fetching the same days must not duplicate transactions.
	var txCount2 int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM bank_transaction WHERE user_id = $1`, user.ID).Scan(&txCount2)
	if txCount2 != txCount {
		t.Fatalf("refetch changed the transaction count %d -> %d", txCount, txCount2)
	}
	// Background fetches are limited per account and day.
	now = now.Add(2 * time.Hour)
	for i := 0; i < 6; i++ {
		now = now.Add(2 * time.Hour)
		ing.EnsureRange(ctx, user.ID, today.AddDate(0, 0, -2), today, nil)
	}
	var bg int
	_ = pool.QueryRow(ctx, `SELECT max(background_fetches) FROM bank_fetch_log`).Scan(&bg)
	if bg > 4 {
		t.Fatalf("background fetches per day = %d, limit is 4", bg)
	}

	// ---- analytics -----------------------------------------------------------------
	r, _ := domain.ResolveRange(domain.RangeRequest{Code: "3m"}, today)
	m, err := an.Metrics(ctx, user.ID, domain.DomainSpendingCategory, r)
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if m.Overall.Count == 0 || len(m.Classes) == 0 {
		t.Fatalf("empty metrics: %+v", m)
	}
	var sqlTotal string
	_ = pool.QueryRow(ctx, `SELECT round(sum(amount), 2)::text FROM fact_transaction
		WHERE user_id = $1 AND metric_domain = 'spending_category' AND booked_on BETWEEN $2 AND $3`, user.ID, r.From, r.To).Scan(&sqlTotal)
	if m.Overall.Total != sqlTotal {
		t.Fatalf("metrics total %s != SQL total %s", m.Overall.Total, sqlTotal)
	}
	var cached int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM fact_metric WHERE user_id = $1`, user.ID).Scan(&cached)
	if cached != len(m.Classes) {
		t.Fatalf("fact_metric rows = %d, classes = %d", cached, len(m.Classes))
	}
	m2, _ := an.Metrics(ctx, user.ID, domain.DomainSpendingCategory, r)
	if m2.Overall != m.Overall {
		t.Fatalf("cached metrics differ: %+v vs %+v", m2.Overall, m.Overall)
	}

	// Income over 3 months contains salaries; transfers to the savings account
	// are internal and never count as spending.
	inc, _ := an.Metrics(ctx, user.ID, domain.DomainIncome, r)
	if !hasClass(inc, "salary") {
		t.Fatalf("no salary in income: %+v", inc.Classes)
	}
	var leaked int
	_ = pool.QueryRow(ctx, `
		SELECT count(*) FROM fact_transaction f JOIN bank_transaction t ON t.id = f.transaction_id AND t.booked_on = f.booked_on
		WHERE f.metric_domain IN ('spending', 'income') AND t.remittance = 'Giroconto risparmio'`).Scan(&leaked)
	if leaked != 0 {
		t.Fatalf("%d internal transfers counted as spending/income", leaked)
	}
	tr, _ := an.Metrics(ctx, user.ID, domain.DomainBankTransfer, r)
	if !hasClass(tr, "internal") {
		t.Fatalf("internal transfers missing from bank_transfer: %+v", tr.Classes)
	}

	// History pagination is stable and newest first.
	p1, err := an.History(ctx, user.ID, domain.DomainSpending, r, "", "", 5)
	if err != nil || len(p1.Entries) != 5 || p1.NextCursor == "" {
		t.Fatalf("history page 1: %+v %v", p1, err)
	}
	p2, err := an.History(ctx, user.ID, domain.DomainSpending, r, "", p1.NextCursor, 5)
	if err != nil || len(p2.Entries) == 0 || p2.Entries[0].ID == p1.Entries[4].ID || p2.Entries[0].Date > p1.Entries[4].Date {
		t.Fatalf("history page 2: %+v %v", p2, err)
	}

	// Series: one point per week, totals add up to the metrics total.
	s, err := an.SeriesOf(ctx, user.ID, domain.DomainSpendingCategory, r, "", analytics.BucketWeek)
	if err != nil || len(s.Points) < 13 {
		t.Fatalf("series: %d points, %v", len(s.Points), err)
	}

	// Entry detail.
	e, err := an.Entry(ctx, user.ID, p1.Entries[0].ID)
	if err != nil || len(e.Memberships) == 0 || len(e.Raw) == 0 {
		t.Fatalf("entry: %+v %v", e, err)
	}
	if _, err := an.Entry(ctx, user.ID, "not-a-uuid"); !errors.Is(err, analytics.ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}

	// ---- category override invalidates cached metrics -------------------------------
	var groceryID string
	err = pool.QueryRow(ctx, `SELECT transaction_id::text FROM fact_transaction
		WHERE user_id = $1 AND metric_domain = 'spending_category' AND metric_class = 'groceries' AND booked_on BETWEEN $2 AND $3 LIMIT 1`,
		user.ID, r.From, r.To).Scan(&groceryID)
	if err != nil {
		t.Fatalf("find grocery: %v", err)
	}
	if err := store.SetCategory(ctx, user.ID, groceryID, domain.CatEducation); err != nil {
		t.Fatalf("set category: %v", err)
	}
	m3, _ := an.Metrics(ctx, user.ID, domain.DomainSpendingCategory, r)
	if !hasClass(m3, "education") || m3.Overall.Total != m.Overall.Total {
		t.Fatalf("override not reflected (or total changed): %+v", m3)
	}
	// The override survives a refetch of its day.
	now = now.Add(24 * time.Hour)
	ing.EnsureRange(ctx, user.ID, today.AddDate(0, 0, -3), today, psu)
	if err := store.SetCategory(ctx, user.ID, groceryID, ""); err != nil {
		t.Fatalf("clear category: %v", err)
	}

	// ---- budgets and notifications ---------------------------------------------------
	b, err := budgets.Create(ctx, user.ID, budget.Input{Name: "Food", Domain: "spending_category", Class: "groceries", Range: "3m", Limit: "1"})
	if err != nil {
		t.Fatalf("create budget: %v", err)
	}
	if b.State != budget.StateExceeded {
		t.Fatalf("budget state = %s (spent %s)", b.State, b.Spent)
	}
	budgets.EvaluateUser(ctx, user.ID)
	budgets.EvaluateUser(ctx, user.ID)
	items, unread, err := notifier.List(ctx, user.ID, 10)
	if err != nil || unread != 1 || items[0].Kind != "budget_exceeded" {
		t.Fatalf("notifications: %+v unread=%d %v", items, unread, err)
	}
	if _, err := budgets.Create(ctx, user.ID, budget.Input{Name: "x", Domain: "income", Class: "groceries", Range: "1m", Limit: "5"}); err == nil {
		t.Fatal("invalid budget accepted")
	}

	// ---- e-mail one-time codes ---------------------------------------------------------
	mailer := &captureMailer{}
	otp := &auth.EmailOTP{Pool: pool, Box: box, Mailer: mailer, Now: time.Now}
	p := &auth.Principal{User: user, Identity: id, MFAState: auth.MFAStateOK}
	chID, _, err := otp.Challenge(ctx, p, auth.PurposeEnroll)
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	code := regexp.MustCompile(`\d{6}`).FindString(mailer.last)
	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}
	if err := otp.Verify(ctx, p, chID, wrong); !errors.Is(err, auth.ErrOTPInvalid) {
		t.Fatalf("wrong code: %v", err)
	}
	var attempts int
	_ = pool.QueryRow(ctx, `SELECT attempts FROM mfa_email_challenge WHERE id = $1`, chID).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("failed attempt not persisted: attempts = %d", attempts)
	}
	if err := otp.Verify(ctx, p, chID, code); err != nil {
		t.Fatalf("right code: %v", err)
	}
	if err := otp.Verify(ctx, p, chID, code); !errors.Is(err, auth.ErrOTPInvalid) {
		t.Fatal("code reused")
	}
	if u, _, done, _ := users.Authorize(ctx, id); !done || !u.EmailMFAEnabled {
		t.Fatalf("session not marked as MFA-complete: %+v done=%v", u, done)
	}

	// ---- HTTP layer over the real database --------------------------------------------
	cfg := config.Config{Env: "development", Location: loc, ReportingCurrency: "EUR", Auth: config.AuthConfig{Mode: "dev"}}
	router, err := httpapi.NewRouter(httpapi.Deps{
		Cfg: cfg, Log: log, Pool: pool,
		Auth: &auth.Authenticator{Verifier: auth.DevVerifier{Started: id.AuthTime}, Store: users, Log: log},
		OTP:  otp, Ingest: ing, Connect: conn, Analytics: an, Warehouse: store, Budgets: budgets, Notify: notifier,
	})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path, token string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	if code, _ := get("/api/v1/metrics?range=1m", ""); code != 401 {
		t.Fatalf("anonymous metrics: %d", code)
	}
	if code, _ := get("/api/v1/metrics?range=1m", "dev:bob@example.com"); code != 403 {
		t.Fatalf("non-whitelisted metrics: %d", code)
	}
	// alice enabled e-mail MFA above, and the dev session time matches the
	// verified one, so data is reachable.
	status, body := get("/api/v1/metrics?range=1m", "dev:alice@example.com")
	if status != 200 || len(body["domains"].([]any)) != 4 {
		t.Fatalf("overview: %d %v", status, body)
	}
	for _, path := range []string{
		"/api/v1/meta",
		"/api/v1/metrics?domain=income&range=custom&from=" + today.AddDate(0, -2, 0).Format("2006-01-02") + "&to=" + today.Format("2006-01-02"),
		"/api/v1/allocation?domain=spending_category&range=3m",
		"/api/v1/history?domain=bank_transfer&range=3m&limit=3",
		"/api/v1/history/series?domain=spending&range=1w",
		"/api/v1/budgets",
		"/api/v1/connections",
		"/api/v1/banks",
		"/api/v1/notifications",
		"/api/v1/transactions/" + groceryID,
	} {
		if code, body := get(path, "dev:alice@example.com"); code != 200 {
			t.Fatalf("GET %s: %d %v", path, code, body)
		}
	}
	for path, want := range map[string]int{
		"/api/v1/metrics?domain=savings&range=1m":                   400,
		"/api/v1/metrics?domain=income&range=1y":                    400,
		"/api/v1/history?domain=income&range=1m&class=groceries":    400,
		"/api/v1/history?domain=income&range=1m&cursor=garbage":     400,
		"/api/v1/transactions/00000000-0000-0000-0000-000000000000": 404,
	} {
		if code, body := get(path, "dev:alice@example.com"); code != want {
			t.Fatalf("GET %s: %d %v, want %d", path, code, body, want)
		}
	}

	// ---- revoke -------------------------------------------------------------------------
	if err := conn.Revoke(ctx, user.ID, connID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	fr = ing.EnsureRange(ctx, user.ID, today.AddDate(0, 0, -1), today, psu)
	now = now.Add(48 * time.Hour)
	fr = ing.EnsureRange(ctx, user.ID, today.AddDate(0, 0, -1), today, psu)
	if fr.Complete {
		t.Fatal("revoked connection must report stale data")
	}
}

func hasClass(m analytics.MetricsResult, class string) bool {
	for _, c := range m.Classes {
		if c.Class == class {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
