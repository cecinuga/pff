// Package bank defines the provider-neutral contract PFF uses to read bank data
// (PSD2 AIS). Concrete providers live in sub-packages (enablebanking, mock).
// Nothing outside this package tree knows which provider is configured.
package bank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Direction tells whether money entered (CRDT) or left (DBIT) the account.
type Direction string

const (
	Credit Direction = "CRDT"
	Debit  Direction = "DBIT"
)

// ASPSP is a bank the user can connect.
type ASPSP struct {
	Name               string        `json:"name"`
	Country            string        `json:"country"`
	Logo               string        `json:"logo,omitempty"`
	PSUTypes           []string      `json:"psu_types"`
	MaxConsentValidity time.Duration `json:"-"`
	RequiredPSUHeaders []string      `json:"-"`
	Beta               bool          `json:"beta"`
}

// AuthRequest starts the user's consent flow at the bank.
type AuthRequest struct {
	ASPSPName    string
	ASPSPCountry string
	PSUType      string // "personal" or "business"
	State        string // opaque CSRF token echoed back on the redirect
	RedirectURL  string
	ValidUntil   time.Time
	Language     string
}

// AuthStart is where the user must be sent to complete SCA at the bank.
type AuthStart struct {
	URL             string
	AuthorizationID string
}

// Session is an authorised consent with the accounts it covers.
type Session struct {
	ID           string
	ValidUntil   time.Time
	ASPSPName    string
	ASPSPCountry string
	Accounts     []Account
}

// SessionStatus values reported by providers, normalised.
const (
	SessionAuthorized = "AUTHORIZED"
	SessionExpired    = "EXPIRED"
	SessionRevoked    = "REVOKED"
	SessionClosed     = "CLOSED"
)

// Account is a bank account covered by a session.
type Account struct {
	// UID addresses the account in provider calls. It is only valid while the
	// session that produced it is authorised.
	UID string
	// IdentificationHash is stable across sessions and is PFF's durable key.
	IdentificationHash string
	IBAN               string
	Name               string
	Currency           string
	CashAccountType    string
	Product            string
}

// Balance is one balance figure of an account.
type Balance struct {
	Name          string
	Type          string
	Amount        decimal.Decimal
	Currency      string
	ReferenceDate *time.Time
}

// Transaction is a booked bank transaction, normalised across providers.
// Amount is always positive; Direction carries the sign.
type Transaction struct {
	EntryReference    string
	Amount            decimal.Decimal
	Currency          string
	Direction         Direction
	Status            string
	BookingDate       *time.Time
	ValueDate         *time.Time
	TransactionDate   *time.Time
	CounterpartyName  string
	CounterpartyIBAN  string
	Remittance        []string
	BankTxCode        string
	BankTxSubCode     string
	BankTxDescription string
	MCC               string
	BalanceAfter      *decimal.Decimal
	Raw               json.RawMessage
}

// EffectiveDate is the date PFF files the transaction under: the booking date,
// falling back to value date then transaction date.
func (t Transaction) EffectiveDate() (time.Time, bool) {
	for _, p := range []*time.Time{t.BookingDate, t.ValueDate, t.TransactionDate} {
		if p != nil && !p.IsZero() {
			return *p, true
		}
	}
	return time.Time{}, false
}

// PSU carries the end user's request headers. A nil *PSU means the user is not
// present (background sync), which banks rate-limit (typically 4 fetches/day).
type PSU struct {
	IPAddress      string
	UserAgent      string
	Referer        string
	Accept         string
	AcceptCharset  string
	AcceptEncoding string
	AcceptLanguage string
}

// TxStrategy selects how the provider handles periods the bank cannot serve.
type TxStrategy string

const (
	// StrategyDefault fails with ErrPeriodUnavailable if the range is not available.
	StrategyDefault TxStrategy = "default"
	// StrategyLongest returns the longest available history from From onward;
	// used for the initial backfill right after consent.
	StrategyLongest TxStrategy = "longest"
)

// TxQuery selects transactions by booking date (inclusive bounds).
type TxQuery struct {
	From     time.Time
	To       time.Time
	Strategy TxStrategy
}

// Provider is implemented by each banking integration.
type Provider interface {
	// Name identifies the provider in the database (bank_connection.provider).
	Name() string
	ListASPSPs(ctx context.Context, country string) ([]ASPSP, error)
	StartAuth(ctx context.Context, req AuthRequest) (AuthStart, error)
	CreateSession(ctx context.Context, code string) (Session, error)
	SessionStatus(ctx context.Context, sessionID string) (string, error)
	DeleteSession(ctx context.Context, sessionID string) error
	Balances(ctx context.Context, accountUID string, psu *PSU) ([]Balance, error)
	// Transactions returns every booked transaction in the query window,
	// following pagination to the end.
	Transactions(ctx context.Context, accountUID string, q TxQuery, psu *PSU) ([]Transaction, error)
}

// Sentinel errors providers map their failures to.
var (
	// ErrSessionExpired: the consent ended; the user must reconnect.
	ErrSessionExpired = errors.New("bank consent expired or revoked")
	// ErrRateLimited: the bank or provider refused due to rate limits.
	ErrRateLimited = errors.New("bank rate limit exceeded")
	// ErrPeriodUnavailable: the bank cannot serve the requested period.
	ErrPeriodUnavailable = errors.New("requested period not available at the bank")
	// ErrInvalidCode: the authorisation code is wrong, used or expired.
	ErrInvalidCode = errors.New("invalid or expired authorisation code")
	// ErrUnavailable: transient failure at the provider or bank.
	ErrUnavailable = errors.New("bank provider temporarily unavailable")
)

// Error wraps a provider failure with its HTTP status and provider error code,
// and unwraps to one of the sentinel errors when one applies.
type Error struct {
	Status   int
	Code     string
	Message  string
	Sentinel error
}

func (e *Error) Error() string {
	return fmt.Sprintf("bank provider error %d %s: %s", e.Status, e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Sentinel }
