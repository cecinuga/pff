package warehouse

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/cecinuga/pff/ms/internal/bank"
)

func TestFingerprintsStableAndDistinct(t *testing.T) {
	d := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	coffee := bank.Transaction{Amount: decimal.RequireFromString("1.20"), Currency: "EUR", Direction: bank.Debit, BookingDate: &d, CounterpartyName: "BAR"}
	withRef := coffee
	withRef.EntryReference = "abc"

	a := Fingerprints([]bank.Transaction{coffee, coffee, withRef})
	b := Fingerprints([]bank.Transaction{coffee, coffee, withRef})
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("fingerprints not stable: %v vs %v", a, b)
		}
	}
	if a[0] == a[1] {
		t.Fatal("two identical coffees on the same day must get distinct fingerprints")
	}
	if a[2] == a[0] {
		t.Fatal("entry reference must change the fingerprint")
	}

	// The amount's textual form must not matter: 1.2 == 1.20.
	other := coffee
	other.Amount = decimal.RequireFromString("1.2")
	if Fingerprints([]bank.Transaction{other})[0] != a[0] {
		t.Fatal("equal amounts with different scale fingerprint differently")
	}
}
