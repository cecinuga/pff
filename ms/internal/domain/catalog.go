// Package domain holds PFF's pure business vocabulary: metric domains, metric
// classes, date ranges and the statistics computed over them. It has no I/O so
// every rule here is unit-testable in isolation.
package domain

import "fmt"

// MetricDomain is one of the four analytical domains defined by the
// FunctionalSpec ("spending", "spending for category", "income",
// "bank transfer").
type MetricDomain string

const (
	DomainSpending         MetricDomain = "spending"
	DomainSpendingCategory MetricDomain = "spending_category"
	DomainIncome           MetricDomain = "income"
	DomainBankTransfer     MetricDomain = "bank_transfer"
)

// MetricClass is a sub-classification inside a metric domain (e.g. the
// "groceries" category inside "spending_category"). Together with the domain it
// forms the primary key of the metric-class dimension.
type MetricClass string

// Spending classes: the payment channel of an outflow.
const (
	ClassCard           MetricClass = "card"
	ClassDirectDebit    MetricClass = "direct_debit"
	ClassTransfer       MetricClass = "transfer"
	ClassCashWithdrawal MetricClass = "cash_withdrawal"
	ClassFee            MetricClass = "fee"
	ClassOther          MetricClass = "other"
)

// Spending-for-category classes.
const (
	CatGroceries     MetricClass = "groceries"
	CatRestaurants   MetricClass = "restaurants"
	CatTransport     MetricClass = "transport"
	CatHousing       MetricClass = "housing"
	CatUtilities     MetricClass = "utilities"
	CatHealth        MetricClass = "health"
	CatShopping      MetricClass = "shopping"
	CatEntertainment MetricClass = "entertainment"
	CatTravel        MetricClass = "travel"
	CatSubscriptions MetricClass = "subscriptions"
	CatEducation     MetricClass = "education"
	CatTaxes         MetricClass = "taxes"
	CatFees          MetricClass = "fees"
	CatCash          MetricClass = "cash"
	CatTransfers     MetricClass = "transfers"
	CatOther         MetricClass = "other"
)

// Income classes: the source of an inflow.
const (
	IncSalary            MetricClass = "salary"
	IncRefund            MetricClass = "refund"
	IncInterestDividends MetricClass = "interest_dividends"
	IncTransferIn        MetricClass = "transfer_in"
	IncOther             MetricClass = "other"
)

// Bank-transfer classes: the direction of a transfer.
const (
	TrIncoming MetricClass = "incoming"
	TrOutgoing MetricClass = "outgoing"
	TrInternal MetricClass = "internal"
)

// DomainInfo describes a metric domain for the dimension table and the API.
type DomainInfo struct {
	Domain      MetricDomain `json:"domain"`
	Label       string       `json:"label"`
	Description string       `json:"description"`
	Classes     []ClassInfo  `json:"classes"`
}

// ClassInfo describes one metric class of a domain.
type ClassInfo struct {
	Class MetricClass `json:"class"`
	Label string      `json:"label"`
}

// Catalog is the single source of truth for domains and classes. At startup the
// backend upserts it into dim_metric_domain / dim_metric_class (SCD type 1:
// labels are overwritten, no history kept).
var Catalog = []DomainInfo{
	{
		Domain:      DomainSpending,
		Label:       "Spending",
		Description: "All outgoing money, excluding transfers between your own accounts, split by payment channel.",
		Classes: []ClassInfo{
			{ClassCard, "Card payments"},
			{ClassDirectDebit, "Direct debits"},
			{ClassTransfer, "Bank transfers"},
			{ClassCashWithdrawal, "Cash withdrawals"},
			{ClassFee, "Bank fees"},
			{ClassOther, "Other"},
		},
	},
	{
		Domain:      DomainSpendingCategory,
		Label:       "Spending by category",
		Description: "All outgoing money, excluding transfers between your own accounts, split by spending category.",
		Classes: []ClassInfo{
			{CatGroceries, "Groceries"},
			{CatRestaurants, "Restaurants & bars"},
			{CatTransport, "Transport"},
			{CatHousing, "Housing"},
			{CatUtilities, "Utilities & telecom"},
			{CatHealth, "Health"},
			{CatShopping, "Shopping"},
			{CatEntertainment, "Entertainment"},
			{CatTravel, "Travel"},
			{CatSubscriptions, "Subscriptions"},
			{CatEducation, "Education"},
			{CatTaxes, "Taxes"},
			{CatFees, "Fees"},
			{CatCash, "Cash"},
			{CatTransfers, "Transfers"},
			{CatOther, "Other"},
		},
	},
	{
		Domain:      DomainIncome,
		Label:       "Income",
		Description: "All incoming money, excluding transfers between your own accounts, split by source.",
		Classes: []ClassInfo{
			{IncSalary, "Salary"},
			{IncRefund, "Refunds"},
			{IncInterestDividends, "Interest & dividends"},
			{IncTransferIn, "Received transfers"},
			{IncOther, "Other"},
		},
	},
	{
		Domain:      DomainBankTransfer,
		Label:       "Bank transfers",
		Description: "All bank transfers, split by direction. Transfers between your own accounts are classed as internal.",
		Classes: []ClassInfo{
			{TrIncoming, "Incoming"},
			{TrOutgoing, "Outgoing"},
			{TrInternal, "Between own accounts"},
		},
	},
}

// ParseDomain validates a domain code coming from the API.
func ParseDomain(s string) (MetricDomain, error) {
	for _, d := range Catalog {
		if string(d.Domain) == s {
			return d.Domain, nil
		}
	}
	return "", fmt.Errorf("unknown metric domain %q", s)
}

// Info returns the catalog entry of a domain.
func (d MetricDomain) Info() DomainInfo {
	for _, info := range Catalog {
		if info.Domain == d {
			return info
		}
	}
	return DomainInfo{Domain: d, Label: string(d)}
}

// HasClass reports whether c is a valid class of domain d.
func (d MetricDomain) HasClass(c MetricClass) bool {
	for _, ci := range d.Info().Classes {
		if ci.Class == c {
			return true
		}
	}
	return false
}

// ClassLabel returns the human label of a class within domain d.
func (d MetricDomain) ClassLabel(c MetricClass) string {
	for _, ci := range d.Info().Classes {
		if ci.Class == c {
			return ci.Label
		}
	}
	return string(c)
}
