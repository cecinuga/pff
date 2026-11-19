// Package mock is a deterministic, offline bank.Provider for local development
// and demos. It needs no credentials: the consent redirect returns straight to
// PFF, and transactions are generated from a seed derived from (account, day),
// so fetching the same day twice always yields the same data, just like a real
// bank would.
package mock

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"net/url"
	"time"

	"github.com/shopspring/decimal"

	"github.com/cecinuga/pff/ms/internal/bank"
)

// ASPSPName is the single bank the mock exposes.
const ASPSPName = "PFF Demo Bank"

const (
	checkingIBAN = "IT60X0542811101000000123456"
	savingsIBAN  = "IT02L1234512345123456789012"
	checkingUID  = "demo-checking"
	savingsUID   = "demo-savings"
)

// Provider implements bank.Provider with generated data.
type Provider struct {
	now func() time.Time
	loc *time.Location
}

var _ bank.Provider = (*Provider)(nil)

// New returns a mock provider. loc is the time zone used to decide "today".
func New(loc *time.Location) *Provider {
	if loc == nil {
		loc = time.UTC
	}
	return &Provider{now: time.Now, loc: loc}
}

// Name implements bank.Provider.
func (p *Provider) Name() string { return "mock" }

// ListASPSPs implements bank.Provider.
func (p *Provider) ListASPSPs(_ context.Context, _ string) ([]bank.ASPSP, error) {
	return []bank.ASPSP{{
		Name:               ASPSPName,
		Country:            "IT",
		PSUTypes:           []string{"personal"},
		MaxConsentValidity: 180 * 24 * time.Hour,
	}}, nil
}

// StartAuth implements bank.Provider: "SCA" succeeds immediately and the user
// is sent straight back to PFF's redirect URL with a code.
func (p *Provider) StartAuth(_ context.Context, r bank.AuthRequest) (bank.AuthStart, error) {
	sum := sha256.Sum256([]byte(r.State))
	code := fmt.Sprintf("mock-%x", sum[:8])
	u, err := url.Parse(r.RedirectURL)
	if err != nil {
		return bank.AuthStart{}, fmt.Errorf("mock: bad redirect url: %w", err)
	}
	q := u.Query()
	q.Set("code", code)
	q.Set("state", r.State)
	u.RawQuery = q.Encode()
	return bank.AuthStart{URL: u.String(), AuthorizationID: code}, nil
}

// CreateSession implements bank.Provider.
func (p *Provider) CreateSession(_ context.Context, code string) (bank.Session, error) {
	if len(code) < 5 || code[:5] != "mock-" {
		return bank.Session{}, &bank.Error{Status: 400, Code: "WRONG_AUTHORIZATION_CODE", Message: "unknown code", Sentinel: bank.ErrInvalidCode}
	}
	return bank.Session{
		ID:           "session-" + code,
		ValidUntil:   p.now().Add(180 * 24 * time.Hour),
		ASPSPName:    ASPSPName,
		ASPSPCountry: "IT",
		Accounts: []bank.Account{
			{UID: checkingUID, IdentificationHash: "demo-hash-checking", IBAN: checkingIBAN, Name: "Conto Corrente", Currency: "EUR", CashAccountType: "CACC", Product: "Demo Current Account"},
			{UID: savingsUID, IdentificationHash: "demo-hash-savings", IBAN: savingsIBAN, Name: "Conto Deposito", Currency: "EUR", CashAccountType: "SVGS", Product: "Demo Savings"},
		},
	}, nil
}

// SessionStatus implements bank.Provider.
func (p *Provider) SessionStatus(context.Context, string) (string, error) {
	return bank.SessionAuthorized, nil
}

// DeleteSession implements bank.Provider.
func (p *Provider) DeleteSession(context.Context, string) error { return nil }

// Balances implements bank.Provider.
func (p *Provider) Balances(_ context.Context, uid string, _ *bank.PSU) ([]bank.Balance, error) {
	today := p.today()
	amount := "3421.17"
	if uid == savingsUID {
		amount = "12873.40"
	}
	return []bank.Balance{{Name: "Saldo contabile", Type: "CLBD", Amount: decimal.RequireFromString(amount), Currency: "EUR", ReferenceDate: &today}}, nil
}

// Transactions implements bank.Provider.
func (p *Provider) Transactions(_ context.Context, uid string, q bank.TxQuery, _ *bank.PSU) ([]bank.Transaction, error) {
	if uid != checkingUID && uid != savingsUID {
		return nil, &bank.Error{Status: 404, Code: "ACCOUNT_DOES_NOT_EXIST", Message: "unknown account"}
	}
	today := p.today()
	to := q.To
	if to.IsZero() || to.After(today) {
		to = today
	}
	var out []bank.Transaction
	for d := q.From; !d.After(to); d = d.AddDate(0, 0, 1) {
		out = append(out, generateDay(uid, d)...)
	}
	return out, nil
}

func (p *Provider) today() time.Time {
	y, m, d := p.now().In(p.loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ---- generator ---------------------------------------------------------------

type merchant struct {
	name     string
	mcc      string
	min, max float64
}

var cardMerchants = []merchant{
	{"ESSELUNGA MILANO", "5411", 8, 95},
	{"CONAD CITY", "5411", 4, 45},
	{"LIDL ITALIA", "5411", 6, 60},
	{"PIZZERIA DA GINO", "5812", 14, 48},
	{"BAR CENTRALE", "5814", 1.2, 9},
	{"MCDONALD'S", "5814", 6, 18},
	{"TRENITALIA", "4111", 4.5, 39},
	{"ENI STATION", "5541", 30, 70},
	{"AMAZON EU SARL", "5999", 9, 120},
	{"ZALANDO", "5651", 25, 110},
	{"FARMACIA COMUNALE", "5912", 5, 35},
	{"CINEMA ODEON", "7832", 8, 22},
	{"STEAM GAMES", "5816", 5, 40},
}

func rngFor(uid string, d time.Time) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(uid))
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(d.Unix()))
	_, _ = h.Write(buf[:])
	return rand.New(rand.NewSource(int64(h.Sum64())))
}

func money(r *rand.Rand, min, max float64) decimal.Decimal {
	v := min + r.Float64()*(max-min)
	return decimal.NewFromFloat(v).Round(2)
}

func generateDay(uid string, d time.Time) []bank.Transaction {
	r := rngFor(uid, d)
	var out []bank.Transaction
	add := func(dir bank.Direction, amount decimal.Decimal, party, iban, mcc, code, sub, desc string, remittance ...string) {
		out = append(out, newTx(uid, d, len(out), dir, amount, party, iban, mcc, code, sub, desc, remittance))
	}
	day := d.Day()

	if uid == savingsUID {
		if day == 3 {
			add(bank.Credit, decimal.NewFromInt(300), "Conto Corrente", checkingIBAN, "", "RCDT", "ESCT", "Bonifico SEPA in entrata", "Giroconto risparmio")
		}
		if day == 1 && int(d.Month())%3 == 1 {
			add(bank.Credit, money(r, 8, 25), "PFF Demo Bank", "", "", "", "INTR", "Interessi creditori", "Interessi creditori trimestre")
		}
		return out
	}

	// Recurring checking-account items.
	switch day {
	case 1:
		add(bank.Debit, decimal.NewFromInt(750), "Mario Rossi", "IT40S0542811101000000654321", "", "ICDT", "ESCT", "Bonifico SEPA", "Affitto appartamento")
	case 3:
		add(bank.Debit, decimal.NewFromInt(300), "Conto Deposito", savingsIBAN, "", "ICDT", "ESCT", "Bonifico SEPA", "Giroconto risparmio")
	case 5:
		add(bank.Debit, money(r, 55, 95), "ENEL ENERGIA SPA", "IT11A0300203280000400012345", "", "IDDT", "ESDD", "Addebito diretto SDD", "Bolletta luce e gas")
	case 10:
		add(bank.Debit, decimal.RequireFromString("12.99"), "NETFLIX.COM", "", "4899", "CCRD", "POSD", "Pagamento carta", "NETFLIX.COM")
	case 15:
		add(bank.Debit, decimal.RequireFromString("10.99"), "SPOTIFY AB", "", "5815", "CCRD", "POSD", "Pagamento carta", "Spotify Premium")
	case 20:
		add(bank.Debit, decimal.RequireFromString("9.99"), "ILIAD ITALIA", "IT22B0300203280000400067890", "", "IDDT", "ESDD", "Addebito diretto SDD", "Abbonamento mobile")
	case 27:
		add(bank.Credit, decimal.RequireFromString("2350.00"), "ACME S.p.A.", "IT33C0306909606100000012345", "", "RCDT", "ESCT", "Bonifico SEPA in entrata", "Stipendio mese")
	case 28:
		add(bank.Debit, decimal.RequireFromString("2.00"), "PFF Demo Bank", "", "", "", "CHRG", "Commissioni", "Canone mensile conto")
	}

	// Day-to-day card spending: 0..3 purchases.
	for i, n := 0, r.Intn(4); i < n; i++ {
		m := cardMerchants[r.Intn(len(cardMerchants))]
		add(bank.Debit, money(r, m.min, m.max), m.name, "", m.mcc, "CCRD", "POSD", "Pagamento carta", "POS "+m.name)
	}
	if r.Intn(12) == 0 {
		add(bank.Debit, decimal.NewFromInt(int64(50*(1+r.Intn(3)))), "ATM BANCOMAT", "", "6011", "CCRD", "CWDL", "Prelievo ATM", "Prelievo contanti")
	}
	if r.Intn(25) == 0 {
		add(bank.Credit, money(r, 9, 60), "AMAZON EU SARL", "", "5999", "CCRD", "RIMB", "Storno carta", "Rimborso ordine")
	}
	if r.Intn(30) == 0 {
		add(bank.Credit, money(r, 10, 80), "Luca Bianchi", "IT55D0306909606100000099999", "", "RCDT", "ESCT", "Bonifico SEPA in entrata", "Cena di sabato")
	}
	return out
}

func newTx(uid string, d time.Time, seq int, dir bank.Direction, amount decimal.Decimal, party, iban, mcc, code, sub, desc string, remittance []string) bank.Transaction {
	ref := fmt.Sprintf("%s-%s-%02d", uid, d.Format("20060102"), seq)
	date := d
	tx := bank.Transaction{
		EntryReference:    ref,
		Amount:            amount,
		Currency:          "EUR",
		Direction:         dir,
		Status:            "BOOK",
		BookingDate:       &date,
		ValueDate:         &date,
		CounterpartyName:  party,
		CounterpartyIBAN:  iban,
		Remittance:        remittance,
		BankTxCode:        code,
		BankTxSubCode:     sub,
		BankTxDescription: desc,
		MCC:               mcc,
	}
	// Mirror the Enable Banking wire format so the detail view looks the same.
	partyKey, acctKey := "creditor", "creditor_account"
	if dir == bank.Credit {
		partyKey, acctKey = "debtor", "debtor_account"
	}
	raw := map[string]any{
		"entry_reference":        ref,
		"transaction_amount":     map[string]string{"currency": "EUR", "amount": amount.StringFixed(2)},
		"credit_debit_indicator": string(dir),
		"status":                 "BOOK",
		"booking_date":           d.Format("2006-01-02"),
		"value_date":             d.Format("2006-01-02"),
		"remittance_information": remittance,
		partyKey:                 map[string]string{"name": party},
		"bank_transaction_code":  map[string]string{"description": desc, "code": code, "sub_code": sub},
	}
	if iban != "" {
		raw[acctKey] = map[string]string{"iban": iban}
	}
	if mcc != "" {
		raw["merchant_category_code"] = mcc
	}
	tx.Raw, _ = json.Marshal(raw)
	return tx
}
