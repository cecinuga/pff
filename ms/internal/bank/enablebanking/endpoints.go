package enablebanking

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/cecinuga/pff/ms/internal/bank"
)

// ---- wire types (subset of the Enable Banking schema PFF uses) -------------

type amountWire struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type accountIDWire struct {
	IBAN  string `json:"iban"`
	Other *struct {
		Identification string `json:"identification"`
	} `json:"other"`
}

type partyWire struct {
	Name string `json:"name"`
}

type accountWire struct {
	UID                string        `json:"uid"`
	AccountID          accountIDWire `json:"account_id"`
	IdentificationHash string        `json:"identification_hash"`
	Name               string        `json:"name"`
	Details            string        `json:"details"`
	Currency           string        `json:"currency"`
	CashAccountType    string        `json:"cash_account_type"`
	Product            string        `json:"product"`
}

type txWire struct {
	EntryReference       string         `json:"entry_reference"`
	MerchantCategoryCode string         `json:"merchant_category_code"`
	TransactionAmount    amountWire     `json:"transaction_amount"`
	Creditor             *partyWire     `json:"creditor"`
	CreditorAccount      *accountIDWire `json:"creditor_account"`
	Debtor               *partyWire     `json:"debtor"`
	DebtorAccount        *accountIDWire `json:"debtor_account"`
	BankTransactionCode  *struct {
		Description string `json:"description"`
		Code        string `json:"code"`
		SubCode     string `json:"sub_code"`
	} `json:"bank_transaction_code"`
	CreditDebitIndicator    string      `json:"credit_debit_indicator"`
	Status                  string      `json:"status"`
	BookingDate             string      `json:"booking_date"`
	ValueDate               string      `json:"value_date"`
	TransactionDate         string      `json:"transaction_date"`
	BalanceAfterTransaction *amountWire `json:"balance_after_transaction"`
	RemittanceInformation   []string    `json:"remittance_information"`
	Note                    string      `json:"note"`
}

// ---- endpoints --------------------------------------------------------------

// ListASPSPs implements bank.Provider (GET /aspsps).
func (c *Client) ListASPSPs(ctx context.Context, country string) ([]bank.ASPSP, error) {
	q := url.Values{"service": {"AIS"}}
	if country != "" {
		q.Set("country", strings.ToUpper(country))
	}
	var resp struct {
		ASPSPs []struct {
			Name                   string   `json:"name"`
			Country                string   `json:"country"`
			Logo                   string   `json:"logo"`
			PSUTypes               []string `json:"psu_types"`
			MaximumConsentValidity int64    `json:"maximum_consent_validity"`
			Beta                   bool     `json:"beta"`
			RequiredPSUHeaders     []string `json:"required_psu_headers"`
		} `json:"aspsps"`
	}
	if err := c.do(ctx, "GET", "/aspsps", q, nil, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]bank.ASPSP, 0, len(resp.ASPSPs))
	for _, a := range resp.ASPSPs {
		out = append(out, bank.ASPSP{
			Name:               a.Name,
			Country:            a.Country,
			Logo:               a.Logo,
			PSUTypes:           a.PSUTypes,
			MaxConsentValidity: time.Duration(a.MaximumConsentValidity) * time.Second,
			RequiredPSUHeaders: a.RequiredPSUHeaders,
			Beta:               a.Beta,
		})
	}
	return out, nil
}

// StartAuth implements bank.Provider (POST /auth).
func (c *Client) StartAuth(ctx context.Context, r bank.AuthRequest) (bank.AuthStart, error) {
	body := map[string]any{
		"access":       map[string]any{"valid_until": r.ValidUntil.UTC().Format(time.RFC3339)},
		"aspsp":        map[string]string{"name": r.ASPSPName, "country": r.ASPSPCountry},
		"state":        r.State,
		"redirect_url": r.RedirectURL,
	}
	if r.PSUType != "" {
		body["psu_type"] = r.PSUType
	}
	if r.Language != "" {
		body["language"] = r.Language
	}
	var resp struct {
		URL             string `json:"url"`
		AuthorizationID string `json:"authorization_id"`
	}
	if err := c.do(ctx, "POST", "/auth", nil, body, nil, &resp); err != nil {
		return bank.AuthStart{}, err
	}
	if resp.URL == "" {
		return bank.AuthStart{}, fmt.Errorf("enablebanking: /auth returned no url")
	}
	return bank.AuthStart{URL: resp.URL, AuthorizationID: resp.AuthorizationID}, nil
}

// CreateSession implements bank.Provider (POST /sessions).
func (c *Client) CreateSession(ctx context.Context, code string) (bank.Session, error) {
	var resp struct {
		SessionID string        `json:"session_id"`
		Accounts  []accountWire `json:"accounts"`
		ASPSP     struct {
			Name    string `json:"name"`
			Country string `json:"country"`
		} `json:"aspsp"`
		Access struct {
			ValidUntil time.Time `json:"valid_until"`
		} `json:"access"`
	}
	if err := c.do(ctx, "POST", "/sessions", nil, map[string]string{"code": code}, nil, &resp); err != nil {
		return bank.Session{}, err
	}
	s := bank.Session{
		ID:           resp.SessionID,
		ValidUntil:   resp.Access.ValidUntil,
		ASPSPName:    resp.ASPSP.Name,
		ASPSPCountry: resp.ASPSP.Country,
	}
	for _, a := range resp.Accounts {
		if a.UID == "" || a.IdentificationHash == "" {
			continue // not addressable in this session
		}
		s.Accounts = append(s.Accounts, mapAccount(a))
	}
	return s, nil
}

// SessionStatus implements bank.Provider (GET /sessions/{id}).
func (c *Client) SessionStatus(ctx context.Context, sessionID string) (string, error) {
	var resp struct {
		Status string `json:"status"`
	}
	if err := c.do(ctx, "GET", "/sessions/"+url.PathEscape(sessionID), nil, nil, nil, &resp); err != nil {
		return "", err
	}
	return resp.Status, nil
}

// DeleteSession implements bank.Provider (DELETE /sessions/{id}).
func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	return c.do(ctx, "DELETE", "/sessions/"+url.PathEscape(sessionID), nil, nil, nil, nil)
}

// Balances implements bank.Provider (GET /accounts/{uid}/balances).
func (c *Client) Balances(ctx context.Context, accountUID string, psu *bank.PSU) ([]bank.Balance, error) {
	var resp struct {
		Balances []struct {
			Name          string     `json:"name"`
			BalanceAmount amountWire `json:"balance_amount"`
			BalanceType   string     `json:"balance_type"`
			ReferenceDate string     `json:"reference_date"`
		} `json:"balances"`
	}
	if err := c.do(ctx, "GET", "/accounts/"+url.PathEscape(accountUID)+"/balances", nil, nil, psu, &resp); err != nil {
		return nil, err
	}
	out := make([]bank.Balance, 0, len(resp.Balances))
	for _, b := range resp.Balances {
		amt, err := decimal.NewFromString(b.BalanceAmount.Amount)
		if err != nil {
			continue
		}
		out = append(out, bank.Balance{
			Name:          b.Name,
			Type:          b.BalanceType,
			Amount:        amt,
			Currency:      b.BalanceAmount.Currency,
			ReferenceDate: parseDate(b.ReferenceDate),
		})
	}
	return out, nil
}

// Transactions implements bank.Provider (GET /accounts/{uid}/transactions),
// following continuation keys until the bank reports no more pages. Only
// booked transactions inside [q.From, q.To] are returned: some banks ignore
// the date filter, and pending items change identity once booked.
func (c *Client) Transactions(ctx context.Context, accountUID string, q bank.TxQuery, psu *bank.PSU) ([]bank.Transaction, error) {
	base := url.Values{}
	if !q.From.IsZero() {
		base.Set("date_from", q.From.Format("2006-01-02"))
	}
	if !q.To.IsZero() && q.Strategy != bank.StrategyLongest {
		base.Set("date_to", q.To.Format("2006-01-02"))
	}
	if q.Strategy != "" {
		base.Set("strategy", string(q.Strategy))
	}
	path := "/accounts/" + url.PathEscape(accountUID) + "/transactions"

	var out []bank.Transaction
	seenKeys := map[string]bool{}
	key := ""
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("enablebanking: more than %d transaction pages", maxPages)
		}
		params := url.Values{}
		for k, v := range base {
			params[k] = v
		}
		if key != "" {
			params.Set("continuation_key", key)
		}
		var resp struct {
			Transactions    []json.RawMessage `json:"transactions"`
			ContinuationKey *string           `json:"continuation_key"`
		}
		if err := c.do(ctx, "GET", path, params, nil, psu, &resp); err != nil {
			return nil, err
		}
		for _, raw := range resp.Transactions {
			tx, ok, err := mapTransaction(raw)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			d, _ := tx.EffectiveDate()
			if (!q.From.IsZero() && d.Before(q.From)) || (!q.To.IsZero() && d.After(q.To)) {
				continue
			}
			out = append(out, tx)
		}
		if resp.ContinuationKey == nil || *resp.ContinuationKey == "" {
			return out, nil
		}
		key = *resp.ContinuationKey
		if seenKeys[key] {
			return nil, fmt.Errorf("enablebanking: pagination loop on continuation key")
		}
		seenKeys[key] = true
	}
}

// ---- mapping --------------------------------------------------------------

func mapAccount(a accountWire) bank.Account {
	name := a.Name
	if name == "" {
		name = a.Details
	}
	acc := bank.Account{
		UID:                a.UID,
		IdentificationHash: a.IdentificationHash,
		IBAN:               a.AccountID.IBAN,
		Name:               name,
		Currency:           a.Currency,
		CashAccountType:    a.CashAccountType,
		Product:            a.Product,
	}
	if acc.IBAN == "" && a.AccountID.Other != nil {
		acc.IBAN = a.AccountID.Other.Identification
	}
	return acc
}

// mapTransaction converts one wire transaction. ok is false for transactions
// PFF does not store (not booked, or without any date).
func mapTransaction(raw json.RawMessage) (bank.Transaction, bool, error) {
	var w txWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return bank.Transaction{}, false, fmt.Errorf("decode transaction: %w", err)
	}
	if w.Status != "" && w.Status != "BOOK" {
		return bank.Transaction{}, false, nil
	}
	amt, err := decimal.NewFromString(w.TransactionAmount.Amount)
	if err != nil {
		return bank.Transaction{}, false, fmt.Errorf("decode transaction amount %q: %w", w.TransactionAmount.Amount, err)
	}
	dir := bank.Direction(w.CreditDebitIndicator)
	if dir != bank.Credit && dir != bank.Debit {
		// Fall back to the sign of the amount when the indicator is missing.
		dir = bank.Credit
		if amt.IsNegative() {
			dir = bank.Debit
		}
	}
	tx := bank.Transaction{
		EntryReference:  w.EntryReference,
		Amount:          amt.Abs(),
		Currency:        w.TransactionAmount.Currency,
		Direction:       dir,
		Status:          "BOOK",
		BookingDate:     parseDate(w.BookingDate),
		ValueDate:       parseDate(w.ValueDate),
		TransactionDate: parseDate(w.TransactionDate),
		Remittance:      w.RemittanceInformation,
		MCC:             w.MerchantCategoryCode,
		Raw:             raw,
	}
	if w.Note != "" {
		tx.Remittance = append(tx.Remittance, w.Note)
	}
	// The counterparty is the creditor of an outflow and the debtor of an inflow.
	party, acct := w.Creditor, w.CreditorAccount
	if dir == bank.Credit {
		party, acct = w.Debtor, w.DebtorAccount
	}
	if party != nil {
		tx.CounterpartyName = party.Name
	}
	if acct != nil {
		tx.CounterpartyIBAN = acct.IBAN
		if tx.CounterpartyIBAN == "" && acct.Other != nil {
			tx.CounterpartyIBAN = acct.Other.Identification
		}
	}
	if w.BankTransactionCode != nil {
		tx.BankTxCode = w.BankTransactionCode.Code
		tx.BankTxSubCode = w.BankTransactionCode.SubCode
		tx.BankTxDescription = w.BankTransactionCode.Description
	}
	if w.BalanceAfterTransaction != nil {
		if b, err := decimal.NewFromString(w.BalanceAfterTransaction.Amount); err == nil {
			tx.BalanceAfter = &b
		}
	}
	if _, ok := tx.EffectiveDate(); !ok {
		return bank.Transaction{}, false, nil
	}
	return tx, true, nil
}

func parseDate(s string) *time.Time {
	if s == "" {
		return nil
	}
	if len(s) > 10 {
		s = s[:10] // tolerate full timestamps
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil
	}
	return &t
}
