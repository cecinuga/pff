// Package connect manages the lifecycle of bank connections (PSD2 consents):
// listing banks, starting SCA at the bank, completing the redirect callback,
// listing and revoking connections.
package connect

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cecinuga/pff/ms/internal/bank"
	"github.com/cecinuga/pff/ms/internal/config"
	"github.com/cecinuga/pff/ms/internal/ingest"
	"github.com/cecinuga/pff/ms/internal/platform/secretbox"
	"github.com/cecinuga/pff/ms/internal/warehouse"
)

// Errors returned to the API layer.
var (
	ErrUnknownBank  = errors.New("unknown bank")
	ErrBadState     = errors.New("this bank authorisation link is invalid or was already used")
	ErrNotFound     = errors.New("connection not found")
	ErrBankRejected = errors.New("the bank did not grant access")
)

// Service implements the connection flows.
type Service struct {
	Store       *warehouse.Store
	Ingest      *ingest.Service
	Provider    bank.Provider
	Box         *secretbox.Box
	RedirectURL string
	Banks       []config.BankRef // the only banks users may connect (PFF_BANKS)
	PSUType     string
	ConsentDays int
	Log         *slog.Logger
	Now         func() time.Time

	cacheMu sync.Mutex
	cache   map[string]cachedBanks // provider bank list per country
}

type cachedBanks struct {
	at    time.Time
	banks []bank.ASPSP
}

const bankCacheTTL = time.Hour

// BankOption is a configured bank as offered to the user. Available is false
// when the provider does not list it (a typo in PFF_BANKS, or the bank left
// the provider).
type BankOption struct {
	bank.ASPSP
	Available bool `json:"available"`
}

// ConfiguredBanks resolves PFF_BANKS against the provider's bank list.
func (s *Service) ConfiguredBanks(ctx context.Context) ([]BankOption, error) {
	out := make([]BankOption, 0, len(s.Banks))
	for _, ref := range s.Banks {
		a, err := s.lookup(ctx, ref)
		if err != nil && !errors.Is(err, ErrUnknownBank) {
			return nil, err
		}
		if a == nil {
			s.Log.Warn("configured bank not offered by the provider", "country", ref.Country, "name", ref.Name)
			out = append(out, BankOption{ASPSP: bank.ASPSP{Name: ref.Name, Country: ref.Country, PSUTypes: []string{}}})
			continue
		}
		out = append(out, BankOption{ASPSP: *a, Available: true})
	}
	return out, nil
}

// lookup finds a configured bank in the provider's list for its country.
func (s *Service) lookup(ctx context.Context, ref config.BankRef) (*bank.ASPSP, error) {
	banks, err := s.providerBanks(ctx, ref.Country)
	if err != nil {
		return nil, err
	}
	for i := range banks {
		if strings.EqualFold(banks[i].Name, ref.Name) {
			return &banks[i], nil
		}
	}
	return nil, ErrUnknownBank
}

// providerBanks returns the provider's banks of a country, cached for an hour.
func (s *Service) providerBanks(ctx context.Context, country string) ([]bank.ASPSP, error) {
	s.cacheMu.Lock()
	c, ok := s.cache[country]
	s.cacheMu.Unlock()
	if ok && s.Now().Sub(c.at) < bankCacheTTL {
		return c.banks, nil
	}
	banks, err := s.Provider.ListASPSPs(ctx, country)
	if err != nil {
		return nil, err
	}
	s.cacheMu.Lock()
	if s.cache == nil {
		s.cache = map[string]cachedBanks{}
	}
	s.cache[country] = cachedBanks{at: s.Now(), banks: banks}
	s.cacheMu.Unlock()
	return banks, nil
}

// StartRequest selects one of the configured banks.
type StartRequest struct {
	ASPSPName    string
	ASPSPCountry string
	Language     string
}

// Start creates a pending connection and returns the bank URL where the user
// must authorise PFF. Only banks listed in PFF_BANKS are accepted.
func (s *Service) Start(ctx context.Context, userID string, r StartRequest) (string, error) {
	ref := config.BankRef{Country: strings.ToUpper(r.ASPSPCountry), Name: r.ASPSPName}
	configured := false
	for _, b := range s.Banks {
		if b.Country == ref.Country && strings.EqualFold(b.Name, ref.Name) {
			configured = true
			break
		}
	}
	if !configured {
		return "", ErrUnknownBank
	}
	aspsp, err := s.lookup(ctx, ref)
	if err != nil {
		return "", err
	}

	validity := time.Duration(s.ConsentDays) * 24 * time.Hour
	if aspsp.MaxConsentValidity > 0 && aspsp.MaxConsentValidity < validity {
		validity = aspsp.MaxConsentValidity
	}
	state, err := secretbox.RandomToken(32)
	if err != nil {
		return "", err
	}
	stateHash := sha256.Sum256([]byte(state))
	headers := aspsp.RequiredPSUHeaders
	if headers == nil {
		headers = []string{}
	}

	var connID string
	if err := s.Store.Pool.QueryRow(ctx, `
		INSERT INTO bank_connection (user_id, provider, aspsp_name, aspsp_country, psu_type, status, auth_state_hash, required_psu_headers)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7) RETURNING id::text`,
		userID, s.Provider.Name(), aspsp.Name, aspsp.Country, s.PSUType, stateHash[:], headers).Scan(&connID); err != nil {
		return "", err
	}

	start, err := s.Provider.StartAuth(ctx, bank.AuthRequest{
		ASPSPName:    aspsp.Name,
		ASPSPCountry: aspsp.Country,
		PSUType:      s.PSUType,
		State:        state,
		RedirectURL:  s.RedirectURL,
		ValidUntil:   s.Now().Add(validity),
		Language:     r.Language,
	})
	if err != nil {
		s.fail(ctx, connID, err)
		return "", err
	}
	return start.URL, nil
}

// CompleteRequest carries the bank redirect parameters.
type CompleteRequest struct {
	Code             string
	State            string
	Error            string
	ErrorDescription string
}

// Complete exchanges the authorisation code for a session, stores the accounts
// and starts the initial history backfill in the background. It returns the
// connection id.
func (s *Service) Complete(ctx context.Context, userID string, r CompleteRequest, psu *bank.PSU) (string, error) {
	if r.State == "" {
		return "", ErrBadState
	}
	stateHash := sha256.Sum256([]byte(r.State))
	var connID string
	// Consume the state atomically: a state works once, for its own user only.
	err := s.Store.Pool.QueryRow(ctx, `
		UPDATE bank_connection SET auth_state_hash = NULL, updated_at = now()
		WHERE auth_state_hash = $1 AND user_id = $2 AND status = 'pending'
		RETURNING id::text`, stateHash[:], userID).Scan(&connID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrBadState
	}
	if err != nil {
		return "", err
	}
	if r.Error != "" || r.Code == "" {
		s.fail(ctx, connID, fmt.Errorf("%s %s", r.Error, r.ErrorDescription))
		return "", ErrBankRejected
	}

	sess, err := s.Provider.CreateSession(ctx, r.Code)
	if err != nil {
		s.fail(ctx, connID, err)
		if errors.Is(err, bank.ErrInvalidCode) {
			return "", ErrBadState
		}
		return "", err
	}
	sealed, err := s.Box.Seal([]byte(sess.ID))
	if err != nil {
		return "", err
	}

	var accountIDs []string
	err = pgx.BeginFunc(ctx, s.Store.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE bank_connection SET status = 'active', session_enc = $2, valid_until = $3,
			       last_error = NULL, expiry_notified = FALSE, updated_at = now()
			WHERE id = $1`, connID, sealed, sess.ValidUntil); err != nil {
			return err
		}
		ids, err := s.Store.UpsertAccounts(ctx, tx, userID, connID, sess.Accounts)
		if err != nil {
			return err
		}
		accountIDs = ids
		// Many banks keep a single consent per TPP and user: a previous
		// connection whose accounts all moved here is superseded.
		_, err = tx.Exec(ctx, `
			UPDATE bank_connection c SET status = 'revoked', session_enc = NULL, updated_at = now()
			WHERE c.user_id = $1 AND c.id <> $2 AND c.status IN ('active', 'expired')
			  AND NOT EXISTS (SELECT 1 FROM dim_account a WHERE a.connection_id = c.id)`, userID, connID)
		return err
	})
	if err != nil {
		return "", err
	}

	// New own accounts change which transfers are internal: rebuild facts,
	// then load history while the bank still exposes it in full.
	if err := s.Store.RebuildUser(ctx, userID); err != nil {
		s.Log.Error("rebuild facts after connect", "err", err)
	}
	go func() {
		bctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		s.Ingest.Backfill(bctx, userID, accountIDs, psu)
	}()
	return connID, nil
}

// Connection is a bank connection with its accounts.
type Connection struct {
	ID           string     `json:"id"`
	ASPSPName    string     `json:"aspsp_name"`
	ASPSPCountry string     `json:"aspsp_country"`
	Status       string     `json:"status"`
	ValidUntil   *time.Time `json:"valid_until"`
	CreatedAt    time.Time  `json:"created_at"`
	LastError    string     `json:"last_error,omitempty"`
	Accounts     []Account  `json:"accounts"`
}

// Account is an account as shown to the user.
type Account struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	IBAN            string     `json:"iban"`
	Currency        string     `json:"currency"`
	CashAccountType string     `json:"cash_account_type"`
	Balance         *string    `json:"balance"`
	BalanceCurrency string     `json:"balance_currency,omitempty"`
	BalanceAt       *time.Time `json:"balance_at"`
}

// List returns the user's connections (pending ones older than an hour are
// hidden: the user abandoned them).
func (s *Service) List(ctx context.Context, userID string) ([]Connection, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, aspsp_name, aspsp_country, status, valid_until, created_at, COALESCE(last_error, '')
		FROM bank_connection
		WHERE user_id = $1 AND NOT (status = 'pending' AND created_at < now() - interval '1 hour')
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	conns, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Connection, error) {
		var c Connection
		err := r.Scan(&c.ID, &c.ASPSPName, &c.ASPSPCountry, &c.Status, &c.ValidUntil, &c.CreatedAt, &c.LastError)
		c.Accounts = []Account{}
		return c, err
	})
	if err != nil {
		return nil, err
	}
	byID := map[string]*Connection{}
	for i := range conns {
		byID[conns[i].ID] = &conns[i]
	}
	rows, err = s.Store.Pool.Query(ctx, `
		SELECT id::text, connection_id::text, COALESCE(name, ''), COALESCE(iban, ''), COALESCE(currency, ''),
		       COALESCE(cash_account_type, ''), round(balance_amount, 2)::text, COALESCE(balance_currency, ''), balance_at
		FROM dim_account WHERE user_id = $1 AND connection_id IS NOT NULL ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Account
		var connID string
		if err := rows.Scan(&a.ID, &connID, &a.Name, &a.IBAN, &a.Currency, &a.CashAccountType, &a.Balance, &a.BalanceCurrency, &a.BalanceAt); err != nil {
			return nil, err
		}
		if c := byID[connID]; c != nil {
			c.Accounts = append(c.Accounts, a)
		}
	}
	return conns, rows.Err()
}

// Revoke ends the consent at the bank and marks the connection revoked. Data
// already stored is kept.
func (s *Service) Revoke(ctx context.Context, userID, connID string) error {
	var sealed []byte
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT session_enc FROM bank_connection WHERE id::text = $1 AND user_id = $2`, connID, userID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if len(sealed) > 0 {
		sid, err := s.Box.Open(sealed)
		if err != nil {
			return fmt.Errorf("decrypt session: %w", err)
		}
		if err := s.Provider.DeleteSession(ctx, string(sid)); err != nil && !errors.Is(err, bank.ErrSessionExpired) {
			s.Log.Warn("delete bank session", "err", err)
		}
	}
	_, err = s.Store.Pool.Exec(ctx, `
		UPDATE bank_connection SET status = 'revoked', session_enc = NULL, auth_state_hash = NULL, updated_at = now()
		WHERE id::text = $1 AND user_id = $2`, connID, userID)
	return err
}

func (s *Service) fail(ctx context.Context, connID string, cause error) {
	msg := cause.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	if _, err := s.Store.Pool.Exec(ctx, `
		UPDATE bank_connection SET status = 'failed', auth_state_hash = NULL, last_error = $2, updated_at = now()
		WHERE id = $1`, connID, msg); err != nil {
		s.Log.Error("mark connection failed", "err", err)
	}
}
