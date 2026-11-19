// Package categorize assigns every bank transaction to its metric domains and
// metric classes. PSD2 APIs do not return spending categories, so PFF derives
// them from the merchant category code (MCC), the ISO 20022 bank transaction
// code and keyword rules on the counterparty name and remittance text.
//
// The rules are deterministic and side-effect free: the same transaction and
// the same set of own IBANs always produce the same memberships.
package categorize

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/cecinuga/pff/ms/internal/bank"
	"github.com/cecinuga/pff/ms/internal/domain"
)

// Input is the subset of a transaction the rules look at.
type Input struct {
	Direction         bank.Direction
	CounterpartyName  string
	CounterpartyIBAN  string
	Remittance        string
	BankTxCode        string
	BankTxSubCode     string
	BankTxDescription string
	MCC               string
	// CategoryOverride is a user-chosen spending category that replaces the
	// rule-based one (empty = none).
	CategoryOverride domain.MetricClass
}

// Membership places a transaction in one metric class of one domain.
type Membership struct {
	Domain domain.MetricDomain
	Class  domain.MetricClass
}

// Result is the full classification of a transaction.
type Result struct {
	Channel     domain.MetricClass // spending class (payment channel)
	Category    domain.MetricClass // spending category (outflows only)
	Internal    bool               // transfer between the user's own accounts
	Memberships []Membership
}

// Classify computes the memberships of one transaction. ownIBANs holds the
// normalised IBANs of all the user's accounts (see NormalizeIBAN).
func Classify(in Input, ownIBANs map[string]bool) Result {
	text := strings.ToLower(strings.Join([]string{in.CounterpartyName, in.Remittance, in.BankTxDescription}, " "))
	channel := detectChannel(in, text)
	res := Result{Channel: channel}

	if iban := NormalizeIBAN(in.CounterpartyIBAN); iban != "" && ownIBANs[iban] {
		res.Internal = true
		res.Memberships = []Membership{{domain.DomainBankTransfer, domain.TrInternal}}
		return res
	}

	isTransfer := channel == domain.ClassTransfer
	if in.Direction == bank.Debit {
		res.Category = detectCategory(in, text, channel)
		if in.CategoryOverride != "" && domain.DomainSpendingCategory.HasClass(in.CategoryOverride) {
			res.Category = in.CategoryOverride
		}
		res.Memberships = append(res.Memberships,
			Membership{domain.DomainSpending, channel},
			Membership{domain.DomainSpendingCategory, res.Category},
		)
		if isTransfer {
			res.Memberships = append(res.Memberships, Membership{domain.DomainBankTransfer, domain.TrOutgoing})
		}
		return res
	}

	res.Memberships = append(res.Memberships, Membership{domain.DomainIncome, detectIncome(in, text, channel)})
	if isTransfer {
		res.Memberships = append(res.Memberships, Membership{domain.DomainBankTransfer, domain.TrIncoming})
	}
	return res
}

// NormalizeIBAN upper-cases an IBAN and strips spaces so IBANs from different
// sources compare equal.
func NormalizeIBAN(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
}

// ---- channel ----------------------------------------------------------------

func codeIs(in Input, codes ...string) bool {
	for _, c := range codes {
		if strings.EqualFold(in.BankTxCode, c) || strings.EqualFold(in.BankTxSubCode, c) {
			return true
		}
	}
	return false
}

var (
	// Bare "atm"/"bancomat" are deliberately absent: "ATM Milano" is a transit
	// company and "pagamento bancomat" is a POS payment. MCC 6011 and the CWDL
	// code catch real withdrawals.
	reCash        = words(`prelievo`, `prelevamento`, `prelievo bancomat`, `atm withdrawal`, `cash withdrawal`)
	reFee         = words(`commissione`, `commissioni`, `canone`, `spese tenuta conto`, `imposta di bollo`, `bank fee`, `fee`, `fees`)
	reDirectDebit = words(`addebito diretto`, `sdd`, `rid`, `direct debit`, `lastschrift`)
	reTransfer    = words(`bonifico`, `giroconto`, `sct`, `sepa credit transfer`, `credit transfer`, `transfer`, `instant transfer`)
	reCard        = words(`pagamento pos`, `pos`, `carta`, `card`, `pagamento carta`, `contactless`, `apple pay`, `google pay`)
)

func detectChannel(in Input, text string) domain.MetricClass {
	switch {
	case in.MCC == "6011" || codeIs(in, "CWDL") || reCash.MatchString(text):
		return domain.ClassCashWithdrawal
	case codeIs(in, "CHRG", "FEES", "COMM") || reFee.MatchString(text):
		return domain.ClassFee
	case in.MCC != "" || codeIs(in, "CCRD", "POSD", "POSC", "SMRT"):
		return domain.ClassCard
	case codeIs(in, "IDDT", "RDDT", "ESDD", "BBDD", "PMDD") || reDirectDebit.MatchString(text):
		return domain.ClassDirectDebit
	case codeIs(in, "ICDT", "RCDT", "ESCT", "DMCT", "XBCT", "SALA", "AUTT", "STDO") || reTransfer.MatchString(text):
		return domain.ClassTransfer
	case reCard.MatchString(text):
		return domain.ClassCard
	}
	return domain.ClassOther
}

// ---- spending category ------------------------------------------------------

// keywordRules are checked in order; the first match wins. More specific
// categories (subscriptions, travel) come before broad ones (shopping).
var keywordRules = []struct {
	class domain.MetricClass
	re    *regexp.Regexp
}{
	{domain.CatSubscriptions, words(`netflix`, `spotify`, `disney\+?`, `prime video`, `amazon prime`, `youtube premium`, `icloud`, `apple\.com/bill`, `dazn`, `now tv`, `abbonamento`, `subscription`)},
	{domain.CatHousing, words(`affitto`, `canone locazione`, `locazione`, `rent`, `condominio`, `mutuo`, `mortgage`)},
	{domain.CatUtilities, words(`enel`, `a2a`, `hera`, `iren`, `acea`, `edison`, `eni plenitude`, `sorgenia`, `tim`, `vodafone`, `windtre`, `wind tre`, `iliad`, `fastweb`, `ho\. mobile`, `bolletta`, `luce`, `gas`, `acqua`, `telecom`)},
	{domain.CatTaxes, words(`f24`, `agenzia delle entrate`, `agenzia entrate`, `tasse`, `tassa`, `imposta`, `imposte`, `inps`, `tari`, `imu`, `bollo auto`, `tax`)},
	{domain.CatTravel, words(`ryanair`, `easyjet`, `wizz air`, `ita airways`, `alitalia`, `booking\.com`, `airbnb`, `expedia`, `hotel`, `albergo`, `b&b`, `hertz`, `europcar`)},
	{domain.CatTransport, words(`trenitalia`, `italo`, `atm milano`, `atac`, `gtt`, `uber`, `free now`, `taxi`, `benzina`, `carburante`, `eni station`, `eni`, `q8`, `ip`, `tamoil`, `esso`, `shell`, `telepass`, `autostrade`, `parcheggio`, `parking`)},
	{domain.CatGroceries, words(`esselunga`, `coop`, `conad`, `carrefour`, `lidl`, `eurospin`, `aldi`, `pam`, `penny`, `iper`, `famila`, `despar`, `spar`, `bennet`, `supermercato`, `supermarket`, `alimentari`, `grocery`)},
	{domain.CatRestaurants, words(`ristorante`, `pizzeria`, `trattoria`, `osteria`, `bar`, `caffe`, `caffè`, `pasticceria`, `gelateria`, `sushi`, `mcdonald'?s?`, `burger king`, `deliveroo`, `just eat`, `glovo`, `restaurant`)},
	{domain.CatHealth, words(`farmacia`, `pharmacy`, `parafarmacia`, `medico`, `dentista`, `ospedale`, `clinica`, `poliambulatorio`, `ticket sanitario`, `analisi`)},
	{domain.CatEducation, words(`universita`, `università`, `scuola`, `school`, `corso`, `udemy`, `coursera`, `libreria`)},
	{domain.CatEntertainment, words(`cinema`, `teatro`, `ticketone`, `concerto`, `steam`, `playstation`, `xbox`, `nintendo`, `museo`)},
	{domain.CatShopping, words(`amazon`, `zalando`, `ikea`, `decathlon`, `mediaworld`, `unieuro`, `zara`, `h&m`, `ebay`, `shein`, `negozio`)},
}

func detectCategory(in Input, text string, channel domain.MetricClass) domain.MetricClass {
	switch channel {
	case domain.ClassCashWithdrawal:
		return domain.CatCash
	case domain.ClassFee:
		return domain.CatFees
	}
	if c, ok := categoryFromMCC(in.MCC); ok {
		return c
	}
	for _, r := range keywordRules {
		if r.re.MatchString(text) {
			return r.class
		}
	}
	if channel == domain.ClassTransfer {
		return domain.CatTransfers
	}
	return domain.CatOther
}

// categoryFromMCC maps ISO 18245 merchant category codes to PFF categories.
func categoryFromMCC(mcc string) (domain.MetricClass, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(mcc))
	if err != nil || n <= 0 {
		return "", false
	}
	switch {
	case n == 6011 || n == 6010:
		return domain.CatCash, true
	case n == 5411 || n == 5412 || n == 5422 || n == 5441 || n == 5451 || n == 5462 || n == 5499:
		return domain.CatGroceries, true
	case n == 5811 || n == 5812 || n == 5813 || n == 5814:
		return domain.CatRestaurants, true
	case n >= 3000 && n <= 3299, n >= 3500 && n <= 3999, n == 4411, n == 4511, n == 4722, n == 7011, n == 7012, n == 7512:
		return domain.CatTravel, true
	case n == 4111 || n == 4112 || n == 4121 || n == 4131 || n == 4784 || n == 4789 || n == 5172 || n == 5541 || n == 5542 || n == 7523:
		return domain.CatTransport, true
	case n == 4814 || n == 4816 || n == 4900:
		return domain.CatUtilities, true
	case n == 4899 || n == 5815 || n == 5968:
		return domain.CatSubscriptions, true
	case n == 5912 || n == 5975 || n == 5976 || n == 8011 || n == 8021 || n == 8031 || n == 8041 || n == 8042 || n == 8043 || n == 8049 || n == 8050 || n == 8062 || n == 8071 || n == 8099:
		return domain.CatHealth, true
	case n == 8211 || n == 8220 || n == 8241 || n == 8244 || n == 8249 || n == 8299 || n == 5942:
		return domain.CatEducation, true
	case n == 9311 || n == 9222 || n == 9399 || n == 9211:
		return domain.CatTaxes, true
	case n == 5816 || n == 5817 || n == 5818 || n == 7832 || n == 7841 || n == 7922 || n == 7929 || n == 7991 || n == 7996 || n == 7997 || n == 7999 || n == 7994:
		return domain.CatEntertainment, true
	case n >= 5200 && n <= 5999:
		return domain.CatShopping, true
	}
	return "", false
}

// ---- income -------------------------------------------------------------------

var (
	reSalary   = words(`stipendio`, `stipendi`, `emolumenti`, `retribuzione`, `salario`, `salary`, `payroll`, `wages?`, `cedolino`, `pensione`)
	reRefund   = words(`rimborso`, `rimborsi`, `storno`, `refund`, `reversal`, `chargeback`, `cashback`)
	reInterest = words(`interessi`, `interesse`, `interest`, `dividendo`, `dividendi`, `dividend`, `cedola`, `cedole`)
)

func detectIncome(in Input, text string, channel domain.MetricClass) domain.MetricClass {
	switch {
	case codeIs(in, "SALA", "PENS") || reSalary.MatchString(text):
		return domain.IncSalary
	case codeIs(in, "INTR", "DVDE") || reInterest.MatchString(text):
		return domain.IncInterestDividends
	case codeIs(in, "RIMB", "RRTN") || reRefund.MatchString(text) || channel == domain.ClassCard:
		// A card credit is almost always a merchant refund.
		return domain.IncRefund
	case channel == domain.ClassTransfer:
		return domain.IncTransferIn
	}
	return domain.IncOther
}

// words compiles a case-insensitive regexp matching any of the given patterns
// as whole words.
func words(patterns ...string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}])(` + strings.Join(patterns, `|`) + `)($|[^\p{L}\p{N}])`)
}
