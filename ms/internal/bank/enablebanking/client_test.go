package enablebanking

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cecinuga/pff/ms/internal/bank"
)

var testKey = func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}()

func verifyJWT(t *testing.T, r *http.Request) {
	t.Helper()
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("missing bearer token")
	}
	parts := strings.Split(strings.TrimPrefix(auth, "Bearer "), ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	enc := base64.RawURLEncoding
	var header map[string]string
	hb, _ := enc.DecodeString(parts[0])
	_ = json.Unmarshal(hb, &header)
	if header["alg"] != "RS256" || header["typ"] != "JWT" || header["kid"] != "app-123" {
		t.Fatalf("bad jwt header %v", header)
	}
	var claims map[string]any
	cb, _ := enc.DecodeString(parts[1])
	_ = json.Unmarshal(cb, &claims)
	if claims["iss"] != "enablebanking.com" || claims["aud"] != "api.enablebanking.com" {
		t.Fatalf("bad jwt claims %v", claims)
	}
	if exp, iat := claims["exp"].(float64), claims["iat"].(float64); exp-iat > 86400 || exp <= iat {
		t.Fatalf("bad jwt lifetime iat=%v exp=%v", iat, exp)
	}
	sig, _ := enc.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&testKey.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("jwt signature does not verify: %v", err)
	}
}

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{
		BaseURL:    srv.URL,
		AppID:      "app-123",
		PrivateKey: testKey,
		Sleep:      func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTransactionsPaginatesFiltersAndSendsPSUHeaders(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		verifyJWT(t, r)
		n := atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/accounts/acc-1/transactions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Psu-Ip-Address") != "203.0.113.7" || r.Header.Get("Psu-User-Agent") != "test-agent" {
			t.Errorf("missing PSU headers: %v", r.Header)
		}
		q := r.URL.Query()
		if q.Get("date_from") != "2026-11-01" || q.Get("date_to") != "2026-11-10" {
			t.Errorf("date filter = %v", q)
		}
		switch n {
		case 1:
			if q.Get("continuation_key") != "" {
				t.Errorf("first page must not send a key")
			}
			_, _ = w.Write([]byte(`{"transactions":[
				{"entry_reference":"a","transaction_amount":{"currency":"EUR","amount":"12.50"},"credit_debit_indicator":"DBIT","status":"BOOK","booking_date":"2026-11-02","creditor":{"name":"Esselunga"},"merchant_category_code":"5411","remittance_information":["POS 123"]},
				{"entry_reference":"p","transaction_amount":{"currency":"EUR","amount":"3.00"},"credit_debit_indicator":"DBIT","status":"PDNG","booking_date":"2026-11-03"}
			],"continuation_key":"k1"}`))
		case 2:
			if q.Get("continuation_key") != "k1" {
				t.Errorf("second page key = %q", q.Get("continuation_key"))
			}
			// An empty page with a key is legal; keep paging.
			_, _ = w.Write([]byte(`{"transactions":[],"continuation_key":"k2"}`))
		case 3:
			_, _ = w.Write([]byte(`{"transactions":[
				{"transaction_amount":{"currency":"EUR","amount":"2000"},"credit_debit_indicator":"CRDT","status":"BOOK","booking_date":"2026-11-05","debtor":{"name":"ACME SpA"},"debtor_account":{"iban":"IT60X0542811101000000123456"},"balance_after_transaction":{"currency":"EUR","amount":"2500.10"}},
				{"transaction_amount":{"currency":"EUR","amount":"9"},"credit_debit_indicator":"DBIT","status":"BOOK","booking_date":"2026-12-01"}
			],"continuation_key":null}`))
		default:
			t.Errorf("unexpected call %d", n)
		}
	})

	from, _ := time.Parse("2006-01-02", "2026-11-01")
	to, _ := time.Parse("2006-01-02", "2026-11-10")
	txs, err := c.Transactions(context.Background(), "acc-1", bank.TxQuery{From: from, To: to},
		&bank.PSU{IPAddress: "203.0.113.7", UserAgent: "test-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if len(txs) != 2 {
		t.Fatalf("got %d transactions, want 2 (pending and out-of-range dropped)", len(txs))
	}
	if txs[0].CounterpartyName != "Esselunga" || txs[0].Direction != bank.Debit || txs[0].MCC != "5411" || txs[0].Amount.String() != "12.5" {
		t.Errorf("tx0 = %+v", txs[0])
	}
	if txs[1].CounterpartyName != "ACME SpA" || txs[1].CounterpartyIBAN != "IT60X0542811101000000123456" || txs[1].BalanceAfter == nil {
		t.Errorf("tx1 = %+v", txs[1])
	}
}

func TestRetriesTransientErrorsButNotRateLimits(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if r.URL.Path == "/aspsps" {
			if n < 3 {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"message":"bad gateway","code":502,"error":"ASPSP_ERROR"}`))
				return
			}
			_, _ = w.Write([]byte(`{"aspsps":[{"name":"Mock ASPSP","country":"IT","psu_types":["personal"],"maximum_consent_validity":7776000}]}`))
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"limit","code":429,"error":"ASPSP_RATE_LIMIT_EXCEEDED"}`))
	})

	aspsps, err := c.ListASPSPs(context.Background(), "it")
	if err != nil {
		t.Fatal(err)
	}
	if len(aspsps) != 1 || aspsps[0].MaxConsentValidity != 90*24*time.Hour {
		t.Fatalf("aspsps = %+v", aspsps)
	}
	if calls != 3 {
		t.Fatalf("expected 2 retries, got %d calls", calls)
	}

	atomic.StoreInt32(&calls, 0)
	_, err = c.Balances(context.Background(), "acc", nil)
	if !errors.Is(err, bank.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if calls != 1 {
		t.Fatalf("rate limit must not be retried, got %d calls", calls)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := map[string]error{
		"EXPIRED_SESSION":           bank.ErrSessionExpired,
		"WRONG_TRANSACTIONS_PERIOD": bank.ErrPeriodUnavailable,
		"WRONG_AUTHORIZATION_CODE":  bank.ErrInvalidCode,
	}
	for code, want := range cases {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"x","code":400,"error":"` + code + `"}`))
		})
		_, err := c.CreateSession(context.Background(), "code")
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", code, err, want)
		}
		var be *bank.Error
		if !errors.As(err, &be) || be.Code != code {
			t.Errorf("%s: error not wrapped as *bank.Error: %v", code, err)
		}
	}
}

func TestCreateSessionAndStartAuth(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/auth":
			if body["state"] != "st" || body["redirect_url"] != "https://app/cb" {
				t.Errorf("auth body = %v", body)
			}
			access := body["access"].(map[string]any)
			if _, err := time.Parse(time.RFC3339, access["valid_until"].(string)); err != nil {
				t.Errorf("valid_until not RFC3339: %v", access["valid_until"])
			}
			_, _ = w.Write([]byte(`{"url":"https://auth.example/start","authorization_id":"auth-1"}`))
		case "/sessions":
			if body["code"] != "the-code" {
				t.Errorf("session body = %v", body)
			}
			_, _ = w.Write([]byte(`{"session_id":"s-1","aspsp":{"name":"Mock ASPSP","country":"IT"},"access":{"valid_until":"2027-02-16T10:00:00.000000+00:00"},
				"accounts":[{"uid":"u-1","identification_hash":"h-1","account_id":{"iban":"IT00X"},"name":"Main","currency":"EUR","cash_account_type":"CACC"},
				            {"identification_hash":"h-2","currency":"EUR","cash_account_type":"CARD"}]}`))
		}
	})
	start, err := c.StartAuth(context.Background(), bank.AuthRequest{
		ASPSPName: "Mock ASPSP", ASPSPCountry: "IT", State: "st", RedirectURL: "https://app/cb",
		ValidUntil: time.Now().Add(24 * time.Hour),
	})
	if err != nil || start.URL != "https://auth.example/start" {
		t.Fatalf("start = %+v, %v", start, err)
	}
	s, err := c.CreateSession(context.Background(), "the-code")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "s-1" || len(s.Accounts) != 1 || s.Accounts[0].IBAN != "IT00X" || s.ValidUntil.Year() != 2027 {
		t.Fatalf("session = %+v", s)
	}
}

func TestParsePrivateKeyRejectsGarbage(t *testing.T) {
	if _, err := ParsePrivateKey([]byte("not a pem")); err == nil {
		t.Fatal("expected error")
	}
}
