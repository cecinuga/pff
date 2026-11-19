package budget

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestValidate(t *testing.T) {
	ok := Input{Name: "Groceries", Domain: "spending_category", Class: "groceries", Range: "1m", Limit: "400"}
	v, err := validate(ok)
	if err != nil {
		t.Fatal(err)
	}
	if v.warn != 80 || !v.enabled || v.class == nil || *v.class != "groceries" {
		t.Fatalf("defaults not applied: %+v", v)
	}

	bad := []Input{
		{Domain: "spending", Range: "1m", Limit: "10"},                              // no name
		{Name: "x", Domain: "savings", Range: "1m", Limit: "10"},                    // bad domain
		{Name: "x", Domain: "income", Class: "groceries", Range: "1m", Limit: "10"}, // class of another domain
		{Name: "x", Domain: "spending", Range: "1y", Limit: "10"},                   // bad range
		{Name: "x", Domain: "spending", Range: "custom", Limit: "10"},               // custom without dates
		{Name: "x", Domain: "spending", Range: "custom", CustomFrom: "2026-02-01", CustomTo: "2026-01-01", Limit: "10"},
		{Name: "x", Domain: "spending", Range: "1m", Limit: "0"},
		{Name: "x", Domain: "spending", Range: "1m", Limit: "-5"},
		{Name: "x", Domain: "spending", Range: "1m", Limit: "1.234"},
		{Name: "x", Domain: "spending", Range: "1m", Limit: "abc"},
		{Name: "x", Domain: "spending", Range: "1m", Limit: "10", WarnPercent: 150},
	}
	for _, in := range bad {
		var ve ValidationError
		if _, err := validate(in); !errors.As(err, &ve) {
			t.Errorf("%+v: expected ValidationError, got %v", in, err)
		}
	}
}

func TestStateFor(t *testing.T) {
	cases := map[string]string{"0": StateOK, "79.99": StateOK, "80": StateWarning, "99.99": StateWarning, "100": StateExceeded, "250": StateExceeded}
	for pct, want := range cases {
		if got := stateFor(decimal.RequireFromString(pct), 80); got != want {
			t.Errorf("%s%%: got %s, want %s", pct, got, want)
		}
	}
}

func TestMessage(t *testing.T) {
	b := Budget{Name: "Food", DomainLabel: "Spending by category", ClassLabel: "Groceries", State: StateWarning,
		Percentage: "85.50", Spent: "342.00", Limit: "400.00", Currency: "EUR"}
	b.Period.From, b.Period.To = "2026-11-01", "2026-11-30"
	n := message(b)
	if n.Kind != "budget_warning" || n.Title != "Budget at 85.5%: Food" || n.Link != "/budgets" {
		t.Fatalf("message = %+v", n)
	}
	b.State, b.Percentage = StateExceeded, "100.00"
	if n := message(b); n.Kind != "budget_exceeded" {
		t.Fatalf("exceeded kind = %s", n.Kind)
	}
}
