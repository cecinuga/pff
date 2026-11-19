package categorize

import (
	"testing"

	"github.com/cecinuga/pff/ms/internal/bank"
	"github.com/cecinuga/pff/ms/internal/domain"
)

func has(r Result, d domain.MetricDomain, c domain.MetricClass) bool {
	for _, m := range r.Memberships {
		if m.Domain == d && m.Class == c {
			return true
		}
	}
	return false
}

func TestClassify(t *testing.T) {
	own := map[string]bool{"IT02L1234512345123456789012": true}
	cases := []struct {
		name string
		in   Input
		want []Membership
	}{
		{
			name: "grocery card payment by MCC",
			in:   Input{Direction: bank.Debit, CounterpartyName: "ESSELUNGA MILANO", MCC: "5411", BankTxCode: "CCRD"},
			want: []Membership{{domain.DomainSpending, domain.ClassCard}, {domain.DomainSpendingCategory, domain.CatGroceries}},
		},
		{
			name: "rent paid by transfer is spending and an outgoing transfer",
			in:   Input{Direction: bank.Debit, CounterpartyName: "Mario Rossi", CounterpartyIBAN: "IT40S0542811101000000654321", Remittance: "Affitto appartamento", BankTxCode: "ICDT"},
			want: []Membership{{domain.DomainSpending, domain.ClassTransfer}, {domain.DomainSpendingCategory, domain.CatHousing}, {domain.DomainBankTransfer, domain.TrOutgoing}},
		},
		{
			name: "transfer to own savings account is internal only",
			in:   Input{Direction: bank.Debit, CounterpartyIBAN: "it02 l123 4512 3451 2345 6789 012", Remittance: "Giroconto", BankTxCode: "ICDT"},
			want: []Membership{{domain.DomainBankTransfer, domain.TrInternal}},
		},
		{
			name: "salary",
			in:   Input{Direction: bank.Credit, CounterpartyName: "ACME S.p.A.", Remittance: "Stipendio mese", BankTxCode: "RCDT"},
			want: []Membership{{domain.DomainIncome, domain.IncSalary}, {domain.DomainBankTransfer, domain.TrIncoming}},
		},
		{
			name: "card refund",
			in:   Input{Direction: bank.Credit, CounterpartyName: "AMAZON EU SARL", MCC: "5999"},
			want: []Membership{{domain.DomainIncome, domain.IncRefund}},
		},
		{
			name: "atm withdrawal",
			in:   Input{Direction: bank.Debit, CounterpartyName: "ATM BANCOMAT", MCC: "6011"},
			want: []Membership{{domain.DomainSpending, domain.ClassCashWithdrawal}, {domain.DomainSpendingCategory, domain.CatCash}},
		},
		{
			name: "bank fee",
			in:   Input{Direction: bank.Debit, Remittance: "Canone mensile conto", BankTxSubCode: "CHRG"},
			want: []Membership{{domain.DomainSpending, domain.ClassFee}, {domain.DomainSpendingCategory, domain.CatFees}},
		},
		{
			name: "utility direct debit",
			in:   Input{Direction: bank.Debit, CounterpartyName: "ENEL ENERGIA SPA", Remittance: "Bolletta luce e gas", BankTxCode: "IDDT"},
			want: []Membership{{domain.DomainSpending, domain.ClassDirectDebit}, {domain.DomainSpendingCategory, domain.CatUtilities}},
		},
		{
			name: "unknown outgoing transfer",
			in:   Input{Direction: bank.Debit, CounterpartyName: "Giulia", Remittance: "regalo", BankTxDescription: "Bonifico SEPA"},
			want: []Membership{{domain.DomainSpending, domain.ClassTransfer}, {domain.DomainSpendingCategory, domain.CatTransfers}, {domain.DomainBankTransfer, domain.TrOutgoing}},
		},
		{
			name: "user override wins over rules",
			in:   Input{Direction: bank.Debit, CounterpartyName: "AMAZON EU SARL", MCC: "5999", CategoryOverride: domain.CatEducation},
			want: []Membership{{domain.DomainSpending, domain.ClassCard}, {domain.DomainSpendingCategory, domain.CatEducation}},
		},
		{
			name: "interest",
			in:   Input{Direction: bank.Credit, Remittance: "Interessi creditori trimestre", BankTxSubCode: "INTR"},
			want: []Membership{{domain.DomainIncome, domain.IncInterestDividends}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.in, own)
			if len(got.Memberships) != len(c.want) {
				t.Fatalf("memberships = %+v, want %+v", got.Memberships, c.want)
			}
			for _, w := range c.want {
				if !has(got, w.Domain, w.Class) {
					t.Errorf("missing %s/%s in %+v", w.Domain, w.Class, got.Memberships)
				}
			}
		})
	}
}

func TestKeywordsMatchWholeWordsOnly(t *testing.T) {
	// "ip" (a fuel brand) must not match inside "Philips"; "bar" not inside "Barilla".
	got := Classify(Input{Direction: bank.Debit, CounterpartyName: "PHILIPS STORE BARILLA"}, nil)
	if got.Category == domain.CatTransport || got.Category == domain.CatRestaurants {
		t.Fatalf("substring matched as a whole word: %s", got.Category)
	}
}

func TestTransitCompanyIsNotCashWithdrawal(t *testing.T) {
	got := Classify(Input{Direction: bank.Debit, CounterpartyName: "ATM MILANO", Remittance: "Abbonamento mensile"}, nil)
	if got.Channel == domain.ClassCashWithdrawal {
		t.Fatalf("ATM Milano classified as cash withdrawal")
	}
}

func TestEveryClassIsInCatalog(t *testing.T) {
	inputs := []Input{
		{Direction: bank.Debit, MCC: "5411"}, {Direction: bank.Debit, MCC: "4111"}, {Direction: bank.Debit, MCC: "9311"},
		{Direction: bank.Debit, MCC: "3005"}, {Direction: bank.Debit, MCC: "5816"}, {Direction: bank.Debit, MCC: "8011"},
		{Direction: bank.Debit, Remittance: "netflix"}, {Direction: bank.Credit}, {Direction: bank.Debit},
	}
	for _, in := range inputs {
		for _, m := range Classify(in, nil).Memberships {
			if !m.Domain.HasClass(m.Class) {
				t.Errorf("class %s not in catalog of %s", m.Class, m.Domain)
			}
		}
	}
}
