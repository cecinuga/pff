// Package warehouse is the only writer of PFF's star schema. It stores bank
// data in the GetOrSet cache (bank_transaction), derives the atomic facts
// (fact_transaction) through the categoriser, records which days were fetched
// (sync_coverage) and bumps the user's data version so cached aggregate
// metrics (fact_metric) are invalidated.
package warehouse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/cecinuga/pff/ms/internal/bank"
	"github.com/cecinuga/pff/ms/internal/categorize"
	"github.com/cecinuga/pff/ms/internal/domain"
)

// Store wraps the connection pool.
type Store struct {
	Pool *pgxpool.Pool
}

// Account is a bank account with the state of its connection.
type Account struct {
	ID                 string
	UserID             string
	ProviderUID        string
	IBAN               string
	Name               string
	ConnectionID       string
	ConnectionStatus   string
	Provider           string
	ValidUntil         *time.Time
	RequiredPSUHeaders []string
}

// Window is an inclusive interval of civil dates.
type Window struct {
	From, To time.Time
}

func (w Window) String() string {
	return w.From.Format(domain.DateLayout) + ".." + w.To.Format(domain.DateLayout)
}

// ---- accounts -------------------------------------------------------------------

// Accounts returns the user's accounts and their connection state.
func (s *Store) Accounts(ctx context.Context, userID string) ([]Account, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT a.id::text, a.user_id::text, COALESCE(a.provider_uid, ''), COALESCE(a.iban, ''), COALESCE(a.name, ''),
		       COALESCE(c.id::text, ''), COALESCE(c.status, 'revoked'), COALESCE(c.provider, ''), c.valid_until,
		       COALESCE(c.required_psu_headers, '{}')
		FROM dim_account a LEFT JOIN bank_connection c ON c.id = a.connection_id
		WHERE a.user_id = $1
		ORDER BY a.name, a.id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Account, error) {
		var a Account
		err := r.Scan(&a.ID, &a.UserID, &a.ProviderUID, &a.IBAN, &a.Name, &a.ConnectionID, &a.ConnectionStatus,
			&a.Provider, &a.ValidUntil, &a.RequiredPSUHeaders)
		return a, err
	})
}

// UpsertAccounts stores the accounts of a new session (SCD type 1: attributes
// are overwritten, and an account seen in a new session moves to it). It
// returns the PFF ids of the accounts.
func (s *Store) UpsertAccounts(ctx context.Context, tx pgx.Tx, userID, connectionID string, accounts []bank.Account) ([]string, error) {
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO dim_account (user_id, connection_id, provider_uid, identification_hash, iban, name, currency, cash_account_type, product)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''))
			ON CONFLICT (user_id, identification_hash) DO UPDATE SET
				connection_id = EXCLUDED.connection_id,
				provider_uid = EXCLUDED.provider_uid,
				iban = COALESCE(EXCLUDED.iban, dim_account.iban),
				name = COALESCE(EXCLUDED.name, dim_account.name),
				currency = COALESCE(EXCLUDED.currency, dim_account.currency),
				cash_account_type = COALESCE(EXCLUDED.cash_account_type, dim_account.cash_account_type),
				product = COALESCE(EXCLUDED.product, dim_account.product),
				updated_at = now()
			RETURNING id::text`,
			userID, connectionID, a.UID, a.IdentificationHash, categorize.NormalizeIBAN(a.IBAN), a.Name, a.Currency, a.CashAccountType, a.Product,
		).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("upsert account: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// UpdateBalance overwrites the account's current balance (SCD type 1). It
// prefers booked/closing balances over intraday ones.
func (s *Store) UpdateBalance(ctx context.Context, accountID string, balances []bank.Balance) error {
	if len(balances) == 0 {
		return nil
	}
	rank := map[string]int{"CLBD": 0, "ITBD": 1, "XPCD": 2, "ITAV": 3, "CLAV": 4, "OPBD": 5}
	best := balances[0]
	bestRank := 99
	for _, b := range balances {
		r, ok := rank[b.Type]
		if !ok {
			r = 50
		}
		if r < bestRank {
			best, bestRank = b, r
		}
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE dim_account SET balance_amount = $2::numeric, balance_currency = $3, balance_type = $4,
		       balance_at = now(), updated_at = now()
		WHERE id = $1`, accountID, best.Amount.String(), best.Currency, best.Type)
	return err
}

// ---- coverage -------------------------------------------------------------------

// Coverage returns, for every day in w, the most recent time the day was fetched
// from the bank (zero time = never).
func (s *Store) Coverage(ctx context.Context, accountID string, w Window) ([]time.Time, []time.Time, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT d::date, max(c.fetched_at)
		FROM generate_series($2::date, $3::date, interval '1 day') AS d
		LEFT JOIN sync_coverage c
		       ON c.account_id = $1 AND d::date BETWEEN c.covered_from AND c.covered_to
		GROUP BY d ORDER BY d`, accountID, w.From, w.To)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var days, fetched []time.Time
	for rows.Next() {
		var d time.Time
		var f *time.Time
		if err := rows.Scan(&d, &f); err != nil {
			return nil, nil, err
		}
		days = append(days, d)
		if f == nil {
			fetched = append(fetched, time.Time{})
		} else {
			fetched = append(fetched, *f)
		}
	}
	return days, fetched, rows.Err()
}

// ---- fetch accounting ---------------------------------------------------------

// BackgroundFetches returns how many fetches without the user present were
// made today for the account.
func (s *Store) BackgroundFetches(ctx context.Context, accountID string, day time.Time) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx,
		`SELECT COALESCE((SELECT background_fetches FROM bank_fetch_log WHERE account_id = $1 AND day = $2), 0)`,
		accountID, day).Scan(&n)
	return n, err
}

// CountFetch records one bank fetch for the account.
func (s *Store) CountFetch(ctx context.Context, accountID string, day time.Time, background bool) error {
	col := "user_fetches"
	if background {
		col = "background_fetches"
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO bank_fetch_log (account_id, day, `+col+`) VALUES ($1, $2, 1)
		ON CONFLICT (account_id, day) DO UPDATE SET `+col+` = bank_fetch_log.`+col+` + 1`, accountID, day)
	return err
}

// ---- transactions -----------------------------------------------------------

// SaveFetch stores the result of fetching window w for an account, atomically:
//   - upserts the fetched transactions (ids stay stable across refetches);
//   - when replace is true, deletes cached transactions in w the bank no
//     longer reports (the bank is the source of truth for fetched days);
//   - rebuilds the facts of w;
//   - records w as covered at syncTime;
//   - bumps the user's data version.
//
// replace is false when the bank refused the period: the window is then only
// marked as covered, keeping whatever was cached before.
func (s *Store) SaveFetch(ctx context.Context, acc Account, w Window, txs []bank.Transaction, syncTime time.Time, replace bool) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// Serialise writers of the same account across backend instances.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, acc.ID); err != nil {
			return err
		}
		if replace {
			if err := upsertTransactions(ctx, tx, acc, txs, syncTime); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				DELETE FROM bank_transaction
				WHERE account_id = $1 AND booked_on BETWEEN $2 AND $3 AND fetched_at <> $4`,
				acc.ID, w.From, w.To, syncTime); err != nil {
				return fmt.Errorf("delete vanished transactions: %w", err)
			}
			if err := rebuildFacts(ctx, tx, acc.UserID, &acc.ID, &w); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM sync_coverage
			WHERE account_id = $1 AND covered_from >= $2 AND covered_to <= $3 AND fetched_at <= $4`,
			acc.ID, w.From, w.To, syncTime); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO sync_coverage (account_id, covered_from, covered_to, fetched_at) VALUES ($1, $2, $3, $4)`,
			acc.ID, w.From, w.To, syncTime); err != nil {
			return err
		}
		if replace {
			return bumpVersion(ctx, tx, acc.UserID)
		}
		return nil
	})
}

func upsertTransactions(ctx context.Context, tx pgx.Tx, acc Account, txs []bank.Transaction, syncTime time.Time) error {
	fps := Fingerprints(txs)
	batch := &pgx.Batch{}
	for i, t := range txs {
		day, ok := t.EffectiveDate()
		if !ok {
			continue
		}
		var balance *string
		if t.BalanceAfter != nil {
			s := t.BalanceAfter.String()
			balance = &s
		}
		var raw []byte
		if len(t.Raw) > 0 {
			raw = t.Raw
		}
		batch.Queue(`
			INSERT INTO bank_transaction (
				user_id, account_id, booked_on, fingerprint, entry_reference, value_date, transaction_date,
				amount, currency, direction, counterparty_name, counterparty_iban, remittance,
				bank_tx_code, bank_tx_sub_code, bank_tx_description, mcc, balance_after, raw, fetched_at)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8::numeric, $9, $10, NULLIF($11, ''), NULLIF($12, ''), NULLIF($13, ''),
			        NULLIF($14, ''), NULLIF($15, ''), NULLIF($16, ''), NULLIF($17, ''), $18::numeric, $19, $20)
			ON CONFLICT (account_id, booked_on, fingerprint) DO UPDATE SET
				entry_reference = EXCLUDED.entry_reference, value_date = EXCLUDED.value_date,
				transaction_date = EXCLUDED.transaction_date, amount = EXCLUDED.amount, currency = EXCLUDED.currency,
				direction = EXCLUDED.direction, counterparty_name = EXCLUDED.counterparty_name,
				counterparty_iban = EXCLUDED.counterparty_iban, remittance = EXCLUDED.remittance,
				bank_tx_code = EXCLUDED.bank_tx_code, bank_tx_sub_code = EXCLUDED.bank_tx_sub_code,
				bank_tx_description = EXCLUDED.bank_tx_description, mcc = EXCLUDED.mcc,
				balance_after = EXCLUDED.balance_after, raw = EXCLUDED.raw, fetched_at = EXCLUDED.fetched_at`,
			acc.UserID, acc.ID, day, fps[i], t.EntryReference, t.ValueDate, t.TransactionDate,
			t.Amount.String(), strings.ToUpper(t.Currency), string(t.Direction), t.CounterpartyName,
			categorize.NormalizeIBAN(t.CounterpartyIBAN), strings.Join(t.Remittance, " "),
			t.BankTxCode, t.BankTxSubCode, t.BankTxDescription, t.MCC, balance, raw, syncTime)
	}
	if batch.Len() == 0 {
		return nil
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("upsert transactions: %w", err)
	}
	return nil
}

// Fingerprints computes a de-duplication key per transaction that is stable
// across refetches of the same day: the bank's entry reference when present,
// otherwise the transaction's content. Identical items on the same day (two
// equal coffees) get an occurrence suffix, which is stable because PFF always
// refetches whole days.
func Fingerprints(txs []bank.Transaction) []string {
	out := make([]string, len(txs))
	seen := map[string]int{}
	for i, t := range txs {
		day, _ := t.EffectiveDate()
		var base string
		if t.EntryReference != "" {
			base = "ref|" + t.EntryReference
		} else {
			base = strings.Join([]string{
				"content", day.Format(domain.DateLayout), t.Amount.String(), strings.ToUpper(t.Currency), string(t.Direction),
				t.CounterpartyName, categorize.NormalizeIBAN(t.CounterpartyIBAN), strings.Join(t.Remittance, " "),
				t.BankTxCode, t.BankTxSubCode, t.MCC,
			}, "|")
		}
		sum := sha256.Sum256([]byte(base))
		key := hex.EncodeToString(sum[:16])
		n := seen[key]
		seen[key] = n + 1
		out[i] = fmt.Sprintf("%s-%d", key, n)
	}
	return out
}

// ---- facts ------------------------------------------------------------------

type rawRow struct {
	id, accountID, currency string
	bookedOn                time.Time
	amount                  decimal.Decimal
	in                      categorize.Input
}

// rebuildFacts recomputes fact_transaction for the user, optionally limited to
// one account and window.
func rebuildFacts(ctx context.Context, tx pgx.Tx, userID string, accountID *string, w *Window) error {
	own := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT iban FROM dim_account WHERE user_id = $1 AND iban IS NOT NULL`, userID)
	if err != nil {
		return err
	}
	ibans, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, iban := range ibans {
		own[categorize.NormalizeIBAN(iban)] = true
	}

	where := "t.user_id = $1"
	args := []any{userID}
	if accountID != nil {
		args = append(args, *accountID)
		where += fmt.Sprintf(" AND t.account_id = $%d", len(args))
	}
	if w != nil {
		args = append(args, w.From, w.To)
		where += fmt.Sprintf(" AND t.booked_on BETWEEN $%d AND $%d", len(args)-1, len(args))
	}

	delWhere := strings.ReplaceAll(where, "t.", "")
	if _, err := tx.Exec(ctx, "DELETE FROM fact_transaction WHERE "+delWhere, args...); err != nil {
		return fmt.Errorf("clear facts: %w", err)
	}

	rows, err = tx.Query(ctx, `
		SELECT t.id::text, t.account_id::text, t.booked_on, t.amount::text, t.currency, t.direction,
		       COALESCE(t.counterparty_name, ''), COALESCE(t.counterparty_iban, ''), COALESCE(t.remittance, ''),
		       COALESCE(t.bank_tx_code, ''), COALESCE(t.bank_tx_sub_code, ''), COALESCE(t.bank_tx_description, ''),
		       COALESCE(t.mcc, ''), COALESCE(o.metric_class, '')
		FROM bank_transaction t
		LEFT JOIN transaction_category_override o
		       ON o.account_id = t.account_id AND o.booked_on = t.booked_on AND o.fingerprint = t.fingerprint
		WHERE `+where, args...)
	if err != nil {
		return fmt.Errorf("load transactions: %w", err)
	}
	raws, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rawRow, error) {
		var x rawRow
		var amount, dir, override string
		err := r.Scan(&x.id, &x.accountID, &x.bookedOn, &amount, &x.currency, &dir,
			&x.in.CounterpartyName, &x.in.CounterpartyIBAN, &x.in.Remittance, &x.in.BankTxCode,
			&x.in.BankTxSubCode, &x.in.BankTxDescription, &x.in.MCC, &override)
		if err != nil {
			return x, err
		}
		x.amount, err = decimal.NewFromString(amount)
		x.in.Direction = bank.Direction(dir)
		x.in.CategoryOverride = domain.MetricClass(override)
		return x, err
	})
	if err != nil {
		return err
	}

	copyRows := make([][]any, 0, len(raws)*2)
	for _, x := range raws {
		for _, m := range categorize.Classify(x.in, own).Memberships {
			copyRows = append(copyRows, []any{userID, x.id, x.accountID, x.bookedOn, string(m.Domain), string(m.Class), x.amount.String(), x.currency})
		}
	}
	if len(copyRows) == 0 {
		return nil
	}
	// Stage through a temp table so CopyFrom can cast text to uuid/numeric.
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE IF NOT EXISTS fact_stage (
			user_id text, transaction_id text, account_id text, booked_on date,
			metric_domain text, metric_class text, amount text, currency text
		) ON COMMIT DELETE ROWS`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `TRUNCATE fact_stage`); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"fact_stage"},
		[]string{"user_id", "transaction_id", "account_id", "booked_on", "metric_domain", "metric_class", "amount", "currency"},
		pgx.CopyFromRows(copyRows)); err != nil {
		return fmt.Errorf("stage facts: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO fact_transaction (user_id, transaction_id, account_id, booked_on, metric_domain, metric_class, amount, currency)
		SELECT user_id::uuid, transaction_id::uuid, account_id::uuid, booked_on, metric_domain, metric_class, amount::numeric, currency
		FROM fact_stage`); err != nil {
		return fmt.Errorf("insert facts: %w", err)
	}
	return nil
}

func bumpVersion(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `UPDATE app_user SET data_version = data_version + 1 WHERE id = $1`, userID)
	return err
}

// RebuildUser recomputes every fact of the user. It is needed when the set of
// own accounts changes, because that changes which transfers are internal.
func (s *Store) RebuildUser(ctx context.Context, userID string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := rebuildFacts(ctx, tx, userID, nil, nil); err != nil {
			return err
		}
		return bumpVersion(ctx, tx, userID)
	})
}

// ErrNotFound is returned when a transaction does not exist for the user.
var ErrNotFound = errors.New("not found")

// SetCategory stores (or clears, when class is empty) the user's spending
// category for a transaction and rebuilds its facts.
func (s *Store) SetCategory(ctx context.Context, userID, transactionID string, class domain.MetricClass) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var accountID, fingerprint string
		var bookedOn time.Time
		err := tx.QueryRow(ctx, `
			SELECT account_id::text, booked_on, fingerprint FROM bank_transaction
			WHERE id::text = $1 AND user_id = $2`, transactionID, userID).Scan(&accountID, &bookedOn, &fingerprint)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if class == "" {
			_, err = tx.Exec(ctx, `DELETE FROM transaction_category_override
				WHERE account_id = $1 AND booked_on = $2 AND fingerprint = $3`, accountID, bookedOn, fingerprint)
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO transaction_category_override (account_id, booked_on, fingerprint, user_id, metric_class)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (account_id, booked_on, fingerprint) DO UPDATE SET metric_class = EXCLUDED.metric_class, updated_at = now()`,
				accountID, bookedOn, fingerprint, userID, string(class))
		}
		if err != nil {
			return err
		}
		day := Window{From: bookedOn, To: bookedOn}
		if err := rebuildFacts(ctx, tx, userID, &accountID, &day); err != nil {
			return err
		}
		return bumpVersion(ctx, tx, userID)
	})
}
