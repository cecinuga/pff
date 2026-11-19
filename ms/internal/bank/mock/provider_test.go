package mock

import (
	"context"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/cecinuga/pff/ms/internal/bank"
)

func TestDeterministicTransactions(t *testing.T) {
	p := New(time.UTC)
	p.now = func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)

	a, err := p.Transactions(context.Background(), checkingUID, bank.TxQuery{From: from, To: to}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.Transactions(context.Background(), checkingUID, bank.TxQuery{From: from, To: to}, nil)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same query returned different data")
	}
	var salary bool
	for _, tx := range a {
		d, _ := tx.EffectiveDate()
		if d.Before(from) || d.After(to) {
			t.Fatalf("transaction outside window: %s", d)
		}
		if tx.Direction == bank.Credit && tx.Amount.String() == "2350" {
			salary = true
		}
	}
	if !salary {
		t.Error("expected the monthly salary in October")
	}
}

func TestNoFutureTransactions(t *testing.T) {
	p := New(time.UTC)
	p.now = func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	txs, _ := p.Transactions(context.Background(), checkingUID, bank.TxQuery{
		From: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
	}, nil)
	for _, tx := range txs {
		if d, _ := tx.EffectiveDate(); d.After(time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("future transaction %s", d)
		}
	}
}

func TestAuthRoundTrip(t *testing.T) {
	p := New(time.UTC)
	start, err := p.StartAuth(context.Background(), bank.AuthRequest{State: "abc", RedirectURL: "http://localhost:5173/connections/callback"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start.URL)
	if u.Query().Get("state") != "abc" {
		t.Fatalf("state not echoed: %s", start.URL)
	}
	s, err := p.CreateSession(context.Background(), u.Query().Get("code"))
	if err != nil || len(s.Accounts) != 2 {
		t.Fatalf("session = %+v, %v", s, err)
	}
	if _, err := p.CreateSession(context.Background(), "bogus"); err == nil {
		t.Fatal("bogus code accepted")
	}
}
